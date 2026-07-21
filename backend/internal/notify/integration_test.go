// Integración del Notification/Event Gateway (ADR-023) contra una BD real y
// un servidor HTTP real: el handler del upgrade con el protocolo completo
// (auth en banda ok/timeout/token inválido, join con watermark, ping/pong) y
// el router del outbox (consumidor notification_gateway) haciendo fan-out
// SOLO a las corporaciones interesadas, incluida la resolución de cuentas por
// SQL cuando el payload no las trae. La validación de tokens usa el módulo
// auth REAL (sesión por hash), igual que el composition root.
//
// Se omite si II_TEST_DATABASE_URL no está definida (mismo patrón que el
// resto de tests de integración: BD efímera propia, migraciones reales,
// destruida al terminar).
package notify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/notify"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

const (
	demoName     = "Demo"
	demoSecret   = "notify-demo-secret"
	traderName   = "Norte Trading"
	traderSecret = "notify-norte-secret"

	// fixedSimTime es el sim-time del SimSource estampado en auth_ok.
	fixedSimTime = 7777

	// frameTimeout acota la espera de cada frame esperado.
	frameTimeout = 10 * time.Second
)

// fixedSim implementa notify.SimSource con un sim-time fijo (determinismo del
// assert de auth_ok; el resto del test no depende del reloj).
type fixedSim struct{}

func (fixedSim) Now(context.Context) simtime.SimTime { return fixedSimTime }

// authValidator implementa notify.TokenValidator con el servicio auth real:
// la misma resolución de sesión por hash que usa el composition root.
type authValidator struct{ svc *auth.Service }

func (v authValidator) Validate(ctx context.Context, token string) (uuid.UUID, error) {
	_, acc, err := v.svc.Authenticate(ctx, token)
	if err != nil {
		return uuid.Nil, err
	}
	return acc.ID, nil
}

func TestNotifyGatewayIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado: dos corporaciones con credenciales ──────────────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)

	authSvc, err := auth.NewService(auth.NewPGRepository(pool), logger)
	if err != nil {
		t.Fatalf("auth.NewService: %v", err)
	}
	demoLogin, err := authSvc.Login(ctx, demoName, demoSecret, nil)
	if err != nil {
		t.Fatalf("login Demo: %v", err)
	}
	norteLogin, err := authSvc.Login(ctx, traderName, traderSecret, nil)
	if err != nil {
		t.Fatalf("login Norte: %v", err)
	}

	// ── Gateway de notificaciones real con ventanas cortas ──────────────────
	opts := notify.DefaultOptions()
	opts.AuthTimeout = 500 * time.Millisecond
	opts.PingInterval = 500 * time.Millisecond
	opts.RouterInterval = 10 * time.Millisecond
	opts.SendBuffer = 64

	reg := prometheus.NewRegistry()
	outbox.RegisterMetrics(reg)
	metrics := notify.NewMetrics(reg)
	hub := notify.NewHub(opts, metrics, logger)
	router := notify.NewRouter(pool, hub, opts, metrics, logger)
	handler := notify.NewHandler(hub, authValidator{authSvc}, fixedSim{}, router, opts, logger)

	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/ws", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	routerCtx, stopRouter := context.WithCancel(ctx)
	routerDone := make(chan error, 1)
	go func() { routerDone <- router.Run(routerCtx) }()
	defer func() {
		stopRouter()
		if err := <-routerDone; err != nil {
			t.Errorf("router.Run devolvió error en el apagado: %v", err)
		}
	}()

	// ── Fallos de autenticación en banda ────────────────────────────────────
	t.Run("token inválido: frame error y cierre 4401", func(t *testing.T) {
		ws := dialWS(t, ctx, srv)
		defer ws.CloseNow() //nolint:errcheck
		sendFrame(t, ctx, ws, notify.ClientFrame{Type: "auth", Token: "token-falso"})
		f := readFrame(t, ctx, ws)
		if f["type"] != "error" || f["code"] != notify.ErrCodeUnauthorized {
			t.Fatalf("frame inesperado: %v", f)
		}
		expectClose(t, ctx, ws, notify.StatusAuthRequired)
	})

	t.Run("sin frame auth dentro del plazo: cierre 4401", func(t *testing.T) {
		ws := dialWS(t, ctx, srv)
		defer ws.CloseNow()                                //nolint:errcheck
		expectClose(t, ctx, ws, notify.StatusAuthRequired) // sin enviar nada
	})

	t.Run("primer frame distinto de auth: cierre 4401", func(t *testing.T) {
		ws := dialWS(t, ctx, srv)
		defer ws.CloseNow() //nolint:errcheck
		sendFrame(t, ctx, ws, notify.ClientFrame{Type: "join", Room: "corp"})
		f := readFrame(t, ctx, ws)
		if f["type"] != "error" || f["code"] != notify.ErrCodeBadFrame {
			t.Fatalf("frame inesperado: %v", f)
		}
		expectClose(t, ctx, ws, notify.StatusAuthRequired)
	})

	// ── Dos clientes autenticados de corps distintas ────────────────────────
	demoWS := dialWS(t, ctx, srv)
	defer demoWS.CloseNow() //nolint:errcheck
	authOK := authenticate(t, ctx, demoWS, demoLogin.Token)
	if authOK["account_id"] != demoID.String() {
		t.Fatalf("auth_ok.account_id = %v, esperado %s", authOK["account_id"], demoID)
	}
	if got := int64(authOK["sim_time_seconds"].(float64)); got != fixedSimTime {
		t.Fatalf("auth_ok.sim_time_seconds = %d, esperado %d", got, fixedSimTime)
	}

	norteWS := dialWS(t, ctx, srv)
	defer norteWS.CloseNow() //nolint:errcheck
	authOK = authenticate(t, ctx, norteWS, norteLogin.Token)
	if authOK["account_id"] != norteID.String() {
		t.Fatalf("auth_ok.account_id = %v, esperado %s", authOK["account_id"], norteID)
	}

	var wm0 int64
	t.Run("join corp responde joined con watermark", func(t *testing.T) {
		maxSeq := queryInt64(t, ctx, pool, `SELECT COALESCE(max(seq), 0) FROM outbox.events`)
		sendFrame(t, ctx, demoWS, notify.ClientFrame{Type: "join", Room: "corp"})
		f := readFrame(t, ctx, demoWS)
		if f["type"] != "joined" || f["room"] != "corp" {
			t.Fatalf("frame inesperado: %v", f)
		}
		wm0 = int64(f["watermark"].(float64))
		if wm0 != maxSeq {
			t.Fatalf("watermark = %d, esperado max(seq) = %d (router sin despachos)", wm0, maxSeq)
		}

		sendFrame(t, ctx, norteWS, notify.ClientFrame{Type: "join", Room: "corp"})
		f = readFrame(t, ctx, norteWS)
		if f["type"] != "joined" || f["room"] != "corp" {
			t.Fatalf("frame inesperado en Norte: %v", f)
		}
	})

	t.Run("join a room desconocida: error UNSUPPORTED_ROOM", func(t *testing.T) {
		sendFrame(t, ctx, demoWS, notify.ClientFrame{Type: "join", Room: "viewport:0,0,1,1"})
		f := readFrame(t, ctx, demoWS)
		if f["type"] != "error" || f["code"] != notify.ErrCodeUnsupportedRoom {
			t.Fatalf("frame inesperado: %v", f)
		}
	})

	t.Run("ping/pong de aplicación", func(t *testing.T) {
		sendFrame(t, ctx, demoWS, notify.ClientFrame{Type: "ping", Nonce: "n-42"})
		f := readFrame(t, ctx, demoWS)
		if f["type"] != "pong" || f["nonce"] != "n-42" {
			t.Fatalf("frame inesperado: %v", f)
		}
	})

	t.Run("evento del dueño SOLO a su corp; contract.confirmed a buyer Y seller", func(t *testing.T) {
		// building.created es solo de Demo (dueño). El contract.confirmed
		// posterior llega a ambas corps: que Norte lo reciba como su PRIMER
		// frame demuestra que el evento del edificio nunca se le entregó (los
		// frames por conexión conservan el orden de seq).
		buildingID := uuid.Must(uuid.NewV7())
		mustEmit(t, ctx, pool, 1000, "building", buildingID, "building.created", map[string]any{
			"building_id":      buildingID.String(),
			"owner_account_id": demoID.String(),
			"build_cost":       "2500",
			"created_at_sim":   1000,
		})
		contractID := uuid.Must(uuid.NewV7())
		mustEmit(t, ctx, pool, 1100, "contract", contractID, "contract.confirmed", map[string]any{
			"contract_id":       contractID.String(),
			"kind":              "sell",
			"buyer_account_id":  demoID.String(),
			"seller_account_id": norteID.String(),
			"quantity":          "100",
			"unit_price":        "1500",
		})

		// Demo recibe ambos, en orden de seq.
		f := readFrame(t, ctx, demoWS)
		if f["type"] != "event" || f["room"] != "corp" || f["event_type"] != "building.created" {
			t.Fatalf("frame inesperado en Demo: %v", f)
		}
		buildingSeq := int64(f["seq"].(float64))
		if buildingSeq <= wm0 {
			t.Fatalf("seq %d no supera el watermark %d", buildingSeq, wm0)
		}
		if f["aggregate_type"] != "building" || f["aggregate_id"] != buildingID.String() {
			t.Fatalf("agregado inesperado: %v", f)
		}
		if _, err := uuid.Parse(f["event_id"].(string)); err != nil {
			t.Fatalf("event_id inválido: %v", f["event_id"])
		}
		payload := f["payload"].(map[string]any)
		if payload["build_cost"] != "2500" || payload["owner_account_id"] != demoID.String() {
			t.Fatalf("payload no es el del outbox tal cual: %v", payload)
		}
		f = readFrame(t, ctx, demoWS)
		if f["event_type"] != "contract.confirmed" || int64(f["seq"].(float64)) <= buildingSeq {
			t.Fatalf("segundo frame inesperado en Demo: %v", f)
		}

		// Norte SOLO recibe el contract.confirmed (nunca vio building.created).
		f = readFrame(t, ctx, norteWS)
		if f["type"] != "event" || f["event_type"] != "contract.confirmed" ||
			f["aggregate_id"] != contractID.String() {
			t.Fatalf("primer frame inesperado en Norte: %v", f)
		}
		if p := f["payload"].(map[string]any); p["unit_price"] != "1500" {
			t.Fatalf("payload inesperado en Norte: %v", p)
		}
	})

	t.Run("contract.settled sin cuentas en el payload: lookup SQL a ambas corps", func(t *testing.T) {
		contractID := insertContract(t, ctx, pool, demoID, norteID)
		mustEmit(t, ctx, pool, 1200, "contract", contractID, "contract.settled", map[string]any{
			"contract_id":        contractID.String(),
			"unit_price":         "1500",
			"quantity_agreed":    "100",
			"quantity_delivered": "100",
			"fill_bp":            10000,
			"settled_at_sim":     1200,
			"status":             "settled",
		})
		for name, ws := range map[string]*websocket.Conn{"Demo": demoWS, "Norte": norteWS} {
			f := readFrame(t, ctx, ws)
			if f["type"] != "event" || f["event_type"] != "contract.settled" ||
				f["aggregate_id"] != contractID.String() {
				t.Fatalf("frame inesperado en %s: %v", name, f)
			}
		}
	})

	t.Run("leave corp deja de entregar", func(t *testing.T) {
		sendFrame(t, ctx, norteWS, notify.ClientFrame{Type: "leave", Room: "corp"})
		// El leave no tiene frame de confirmación. Se emite un evento para
		// ambas corps (missedID) que solo debe llegar a Demo; después Norte
		// re-entra a la room y se emite un segundo evento (afterID): que el
		// primer frame de Norte tras el joined sea afterID demuestra que
		// missedID no se le entregó durante la baja.
		missedID := uuid.Must(uuid.NewV7())
		mustEmit(t, ctx, pool, 1300, "contract", missedID, "contract.confirmed", map[string]any{
			"contract_id":       missedID.String(),
			"buyer_account_id":  demoID.String(),
			"seller_account_id": norteID.String(),
			"unit_price":        "900",
			"quantity":          "10",
		})
		f := readFrame(t, ctx, demoWS)
		if f["type"] != "event" || f["aggregate_id"] != missedID.String() {
			t.Fatalf("frame inesperado en Demo: %v", f)
		}

		sendFrame(t, ctx, norteWS, notify.ClientFrame{Type: "join", Room: "corp"})
		f = readFrame(t, ctx, norteWS)
		if f["type"] != "joined" {
			t.Fatalf("se esperaba joined en Norte, llegó %v", f)
		}
		afterID := uuid.Must(uuid.NewV7())
		mustEmit(t, ctx, pool, 1400, "contract", afterID, "contract.confirmed", map[string]any{
			"contract_id":       afterID.String(),
			"buyer_account_id":  demoID.String(),
			"seller_account_id": norteID.String(),
			"unit_price":        "901",
			"quantity":          "11",
		})
		f = readFrame(t, ctx, norteWS)
		if f["type"] != "event" || f["aggregate_id"] != afterID.String() {
			t.Fatalf("Norte debía recibir %s como primer evento tras el re-join, llegó %v", afterID, f)
		}
		f = readFrame(t, ctx, demoWS)
		if f["type"] != "event" || f["aggregate_id"] != afterID.String() {
			t.Fatalf("frame inesperado en Demo: %v", f)
		}
	})

	t.Run("métricas del enrutado", func(t *testing.T) {
		if got := counterValue(t, reg, "ii_ws_events_routed_total", "event_type", "contract.confirmed"); got < 3 {
			t.Errorf("ii_ws_events_routed_total{contract.confirmed} = %v, esperado >= 3", got)
		}
		if got := counterValue(t, reg, "ii_ws_events_routed_total", "event_type", "building.created"); got < 1 {
			t.Errorf("ii_ws_events_routed_total{building.created} = %v, esperado >= 1", got)
		}
	})
}

// ─── Helpers WS ─────────────────────────────────────────────────────────────

// dialWS abre una conexión WS contra el endpoint del servidor de test.
func dialWS(t *testing.T, ctx context.Context, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/ws"
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("Dial(%s): %v", url, err)
	}
	return ws
}

// authenticate ejecuta el auth en banda y devuelve el frame auth_ok decodificado.
func authenticate(t *testing.T, ctx context.Context, ws *websocket.Conn, token string) map[string]any {
	t.Helper()
	sendFrame(t, ctx, ws, notify.ClientFrame{Type: "auth", Token: token})
	f := readFrame(t, ctx, ws)
	if f["type"] != "auth_ok" {
		t.Fatalf("se esperaba auth_ok, llegó %v", f)
	}
	return f
}

func sendFrame(t *testing.T, ctx context.Context, ws *websocket.Conn, frame any) {
	t.Helper()
	b, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("serializando frame: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	if err := ws.Write(wctx, websocket.MessageText, b); err != nil {
		t.Fatalf("escribiendo frame %s: %v", b, err)
	}
}

func readFrame(t *testing.T, ctx context.Context, ws *websocket.Conn) map[string]any {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	_, data, err := ws.Read(rctx)
	if err != nil {
		t.Fatalf("leyendo frame: %v", err)
	}
	var f map[string]any
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("decodificando frame %s: %v", data, err)
	}
	return f
}

// expectClose espera que el servidor cierre la conexión con el código dado.
func expectClose(t *testing.T, ctx context.Context, ws *websocket.Conn, want websocket.StatusCode) {
	t.Helper()
	rctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	_, data, err := ws.Read(rctx)
	if err == nil {
		t.Fatalf("se esperaba el cierre %d, llegó el frame %s", want, data)
	}
	if got := websocket.CloseStatus(err); got != want {
		t.Fatalf("código de cierre = %d (%v), esperado %d", got, err, want)
	}
}

// ─── Helpers BD ─────────────────────────────────────────────────────────────

// insertContract crea un contrato mínimo Demo(buyer)↔Norte(seller) para
// ejercitar el lookup SQL del enrutado (las cuentas espejo reutilizan la
// primera cuenta del ledger: el FK solo exige que exista).
func insertContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool, buyer, seller uuid.UUID) uuid.UUID {
	t.Helper()
	productID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	nodeID := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes LIMIT 1`)
	mirrorID := queryUUID(t, ctx, pool, `SELECT id FROM ledger.accounts LIMIT 1`)
	contractID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO ledger.contracts (
		    id, channel, buyer_account_id, seller_account_id, product_id,
		    quantity_agreed, unit_price, origin_node_id, destination_node_id,
		    deadline_sim, stock_reserve_account_id, seller_guarantee_account_id,
		    escrow_account_id, confirmed_at_sim)
		VALUES ($1, 'board', $2, $3, $4, 100, 1500, $5, $5, 100000, $6, $6, $6, 1)`,
		contractID, buyer, seller, productID, nodeID, mirrorID); err != nil {
		t.Fatalf("insertando el contrato: %v", err)
	}
	return contractID
}

// mustEmit emite un evento del outbox en una transacción propia y confirma.
func mustEmit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, simTime int64, aggType string, aggID uuid.UUID, eventType string, payload any) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := outbox.Emit(ctx, tx, simTime, aggType, aggID, eventType, payload); err != nil {
		t.Fatalf("Emit(%s): %v", eventType, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", eventType, err)
	}
}

func queryUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func queryInt64(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// counterValue suma las series de un counter cuyo par etiqueta=valor coincide.
func counterValue(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	var sum float64
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella (mismo patrón que el resto de módulos).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("notifytest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(conn, "../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
		t.Fatalf("aplicando las migraciones: %v", err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("cerrando la conexión de migraciones: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando la URL del pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
