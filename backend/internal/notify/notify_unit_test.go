package notify

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

// ─── ParseClientFrame ───────────────────────────────────────────────────────

func TestParseClientFrame(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
		want    ClientFrame
	}{
		{"auth válido", `{"type":"auth","token":"tok-123"}`, false,
			ClientFrame{Type: FrameTypeAuth, Token: "tok-123"}},
		{"join corp", `{"type":"join","room":"corp"}`, false,
			ClientFrame{Type: FrameTypeJoin, Room: "corp"}},
		{"leave corp", `{"type":"leave","room":"corp"}`, false,
			ClientFrame{Type: FrameTypeLeave, Room: "corp"}},
		{"ping con nonce", `{"type":"ping","nonce":"n1"}`, false,
			ClientFrame{Type: FrameTypePing, Nonce: "n1"}},
		{"ping sin nonce", `{"type":"ping"}`, false,
			ClientFrame{Type: FrameTypePing}},
		{"auth sin token", `{"type":"auth"}`, true, ClientFrame{}},
		{"join sin room", `{"type":"join"}`, true, ClientFrame{}},
		{"leave sin room", `{"type":"leave"}`, true, ClientFrame{}},
		{"type desconocido", `{"type":"subscribe","room":"corp"}`, true, ClientFrame{}},
		{"sin type", `{"room":"corp"}`, true, ClientFrame{}},
		{"JSON malformado", `{"type":"auth"`, true, ClientFrame{}},
		{"JSON no objeto", `[1,2,3]`, true, ClientFrame{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseClientFrame([]byte(tc.in))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseClientFrame(%s): se esperaba error, se obtuvo %+v", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseClientFrame(%s): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseClientFrame(%s) = %+v, esperado %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestServerFramesJSON fija el JSON exacto de los frames salientes (ADR-023).
func TestServerFramesJSON(t *testing.T) {
	accountID := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8057")
	eventID := uuid.MustParse("01890a5d-ac96-774b-bcce-b302099a8058")

	cases := []struct {
		name  string
		frame any
		want  string
	}{
		{"auth_ok", NewAuthOK(accountID, 4321),
			`{"type":"auth_ok","account_id":"01890a5d-ac96-774b-bcce-b302099a8057","sim_time_seconds":4321}`},
		{"joined", NewJoined(RoomCorp, 99),
			`{"type":"joined","room":"corp","watermark":99}`},
		{"pong", NewPong("n1"),
			`{"type":"pong","nonce":"n1"}`},
		{"error", NewError(ErrCodeBadFrame, "frame inválido"),
			`{"type":"error","code":"BAD_FRAME","message":"frame inválido"}`},
		{"event", EventFrame{
			Type: FrameTypeEvent, Room: RoomCorp, Seq: 7, EventID: eventID.String(),
			EventType: "contract.settled", SimTime: 1000, AggregateType: "contract",
			AggregateID: accountID.String(), Payload: json.RawMessage(`{"unit_price":"1500"}`),
		}, `{"type":"event","room":"corp","seq":7,"event_id":"01890a5d-ac96-774b-bcce-b302099a8058","event_type":"contract.settled","sim_time":1000,"aggregate_type":"contract","aggregate_id":"01890a5d-ac96-774b-bcce-b302099a8057","payload":{"unit_price":"1500"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.frame)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Fatalf("JSON del frame %s:\n  got  %s\n  want %s", tc.name, b, tc.want)
			}
		})
	}
}

// ─── Hub ────────────────────────────────────────────────────────────────────

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testHubOptions(buffer int) Options {
	opts := DefaultOptions()
	opts.SendBuffer = buffer
	opts.MaxConnsPerAccount = 2
	return opts
}

func TestHubBroadcastPorRoomYCuenta(t *testing.T) {
	reg := prometheus.NewRegistry()
	hub := NewHub(testHubOptions(4), NewMetrics(reg), discardLogger())
	corpA := uuid.Must(uuid.NewV7())
	corpB := uuid.Must(uuid.NewV7())

	connA, err := hub.Register(corpA)
	if err != nil {
		t.Fatalf("Register(A): %v", err)
	}
	connB, err := hub.Register(corpB)
	if err != nil {
		t.Fatalf("Register(B): %v", err)
	}
	connBNoJoin, err := hub.Register(corpB)
	if err != nil {
		t.Fatalf("Register(B2): %v", err)
	}
	hub.Join(connA, RoomCorp)
	hub.Join(connB, RoomCorp)
	// connBNoJoin no se suscribe: no debe recibir nada.

	// Solo corpA: llega a connA, no a connB ni a connBNoJoin.
	if got := hub.Broadcast([]uuid.UUID{corpA}, RoomCorp, NewPong("x")); got != 1 {
		t.Fatalf("Broadcast(corpA) entregó %d, esperado 1", got)
	}
	select {
	case b := <-connA.Outbound():
		var f PongFrame
		if err := json.Unmarshal(b, &f); err != nil || f.Type != FrameTypePong || f.Nonce != "x" {
			t.Fatalf("frame inesperado en connA: %s (err %v)", b, err)
		}
	default:
		t.Fatal("connA no recibió el frame")
	}
	select {
	case b := <-connB.Outbound():
		t.Fatalf("connB recibió un frame de otra corp: %s", b)
	default:
	}

	// Ambas corps con cuenta repetida: un frame por conexión suscrita, sin
	// duplicados por la repetición de corpA.
	got := hub.Broadcast([]uuid.UUID{corpA, corpB, corpA}, RoomCorp, NewPong("y"))
	if got != 2 {
		t.Fatalf("Broadcast(A,B,A) entregó %d, esperado 2", got)
	}
	if len(connA.Outbound()) != 1 || len(connB.Outbound()) != 1 {
		t.Fatalf("buffers tras broadcast: A=%d B=%d, esperado 1 y 1",
			len(connA.Outbound()), len(connB.Outbound()))
	}
	select {
	case b := <-connBNoJoin.Outbound():
		t.Fatalf("conexión sin join recibió un frame: %s", b)
	default:
	}

	// Leave: deja de recibir.
	hub.Leave(connB, RoomCorp)
	<-connB.Outbound() // drena el pendiente
	if got := hub.Broadcast([]uuid.UUID{corpB}, RoomCorp, NewPong("z")); got != 0 {
		t.Fatalf("Broadcast tras leave entregó %d, esperado 0", got)
	}
}

func TestHubLimiteConexionesPorCuenta(t *testing.T) {
	hub := NewHub(testHubOptions(4), NewMetrics(prometheus.NewRegistry()), discardLogger())
	corp := uuid.Must(uuid.NewV7())

	c1, err := hub.Register(corp)
	if err != nil {
		t.Fatalf("Register 1: %v", err)
	}
	if _, err := hub.Register(corp); err != nil {
		t.Fatalf("Register 2: %v", err)
	}
	if _, err := hub.Register(corp); !errors.Is(err, ErrTooManyConns) {
		t.Fatalf("Register 3: err = %v, esperado ErrTooManyConns", err)
	}
	// Tras liberar una, vuelve a haber hueco. Unregister es idempotente.
	hub.Unregister(c1)
	hub.Unregister(c1)
	if _, err := hub.Register(corp); err != nil {
		t.Fatalf("Register tras Unregister: %v", err)
	}
	if got := hub.Connections(corp); got != 2 {
		t.Fatalf("Connections = %d, esperado 2", got)
	}
}

func TestHubBufferLlenoCierraClienteLento(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	hub := NewHub(testHubOptions(2), metrics, discardLogger())
	corp := uuid.Must(uuid.NewV7())

	conn, err := hub.Register(corp)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	hub.Join(conn, RoomCorp)

	// Nadie drena Outbound: el tercer frame desborda el buffer (cap 2).
	for i := range 2 {
		if got := hub.Broadcast([]uuid.UUID{corp}, RoomCorp, NewPong("n")); got != 1 {
			t.Fatalf("Broadcast %d entregó %d, esperado 1", i, got)
		}
	}
	select {
	case <-conn.SlowClose():
		t.Fatal("SlowClose cerrado antes de desbordar el buffer")
	default:
	}
	if got := hub.Broadcast([]uuid.UUID{corp}, RoomCorp, NewPong("overflow")); got != 0 {
		t.Fatalf("Broadcast desbordante entregó %d, esperado 0", got)
	}
	select {
	case <-conn.SlowClose():
		// marcada para cierre 1013
	default:
		t.Fatal("SlowClose no se cerró con el buffer lleno")
	}
	// La conexión marcada no recibe más frames.
	if got := hub.Broadcast([]uuid.UUID{corp}, RoomCorp, NewPong("post")); got != 0 {
		t.Fatalf("Broadcast tras el cierre lento entregó %d, esperado 0", got)
	}
	if got := gaugeValue(t, reg, "ii_ws_slow_client_closes_total"); got != 1 {
		t.Fatalf("ii_ws_slow_client_closes_total = %v, esperado 1", got)
	}
	if got := gaugeValue(t, reg, "ii_ws_frames_sent_total"); got != 2 {
		t.Fatalf("ii_ws_frames_sent_total = %v, esperado 2", got)
	}
}

func TestHubGaugeConexiones(t *testing.T) {
	reg := prometheus.NewRegistry()
	hub := NewHub(testHubOptions(2), NewMetrics(reg), discardLogger())
	corp := uuid.Must(uuid.NewV7())

	c1, _ := hub.Register(corp)
	c2, _ := hub.Register(corp)
	if got := gaugeValue(t, reg, "ii_ws_connections"); got != 2 {
		t.Fatalf("ii_ws_connections = %v, esperado 2", got)
	}
	hub.Unregister(c1)
	hub.Unregister(c1) // idempotente: no decrementa dos veces
	hub.Unregister(c2)
	if got := gaugeValue(t, reg, "ii_ws_connections"); got != 0 {
		t.Fatalf("ii_ws_connections = %v, esperado 0", got)
	}
}

// gaugeValue lee el valor de una métrica sin etiquetas (gauge o counter).
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetGauge().GetValue() + m.GetCounter().GetValue()
		}
	}
	return 0
}
