package botsdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// wsTestServer es un Notification Gateway de prueba que habla el protocolo v1
// del ADR-023: auth en banda (token esperado o error UNAUTHORIZED + cierre
// 4401), join → joined con watermark, ping → pong, y un guion por conexión.
type wsTestServer struct {
	t         *testing.T
	token     string
	accountID string
	// watermarks entrega el watermark del joined de cada conexión (por orden).
	watermarks []int64
	// script corre tras el join de cada conexión (índice = nº de conexión).
	script []func(ctx context.Context, conn *websocket.Conn)
	conns  atomic.Int64
}

func (s *wsTestServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow() //nolint:errcheck
		ctx := r.Context()
		n := int(s.conns.Add(1)) - 1

		// ── auth en banda ──
		var f wsClientFrame
		if err := readJSON(ctx, conn, &f); err != nil {
			return
		}
		if f.Type != "auth" || f.Token != s.token {
			writeJSON(ctx, conn, map[string]any{"type": "error", "code": "UNAUTHORIZED", "message": "credenciales o sesión inválidas"})
			conn.Close(websocket.StatusCode(4401), "token inválido") //nolint:errcheck
			return
		}
		writeJSON(ctx, conn, map[string]any{"type": "auth_ok", "account_id": s.accountID, "sim_time_seconds": 4242})

		// ── bucle de frames: join/ping ──
		joined := make(chan struct{}, 1)
		go func() {
			for {
				var cf wsClientFrame
				if err := readJSON(ctx, conn, &cf); err != nil {
					return
				}
				switch cf.Type {
				case "join":
					wm := int64(0)
					if n < len(s.watermarks) {
						wm = s.watermarks[n]
					}
					writeJSON(ctx, conn, map[string]any{"type": "joined", "room": cf.Room, "watermark": wm})
					select {
					case joined <- struct{}{}:
					default:
					}
				case "ping":
					writeJSON(ctx, conn, map[string]any{"type": "pong", "nonce": cf.Nonce})
				}
			}
		}()

		if n < len(s.script) && s.script[n] != nil {
			select {
			case <-joined:
			case <-ctx.Done():
				return
			}
			s.script[n](ctx, conn)
		}
		<-ctx.Done()
	})
}

func readJSON(ctx context.Context, conn *websocket.Conn, out any) error {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) {
	b, _ := json.Marshal(v)
	_ = conn.Write(ctx, websocket.MessageText, b)
}

func sendEvent(ctx context.Context, conn *websocket.Conn, seq int64, eventType string, payload map[string]any) {
	writeJSON(ctx, conn, map[string]any{
		"type": "event", "room": "corp", "seq": seq,
		"event_id":   "0197b2f0-0000-7000-8000-000000000001",
		"event_type": eventType, "sim_time": 1000,
		"aggregate_type": "contract", "aggregate_id": "0197b2f0-0000-7000-8000-000000000002",
		"payload": payload,
	})
}

// newWSTestClient construye un Client apuntando al servidor de prueba con el
// token ya fijado (como tras un Login).
func newWSTestClient(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	c, err := New(Options{BaseURL: srv.URL + "/api/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.SetToken(token)
	return c
}

func TestWSConnectJoinEventsPing(t *testing.T) {
	ts := &wsTestServer{
		t: t, token: "tok-1", accountID: "acc-1",
		watermarks: []int64{42},
		script: []func(ctx context.Context, conn *websocket.Conn){
			func(ctx context.Context, conn *websocket.Conn) {
				sendEvent(ctx, conn, 43, "contract.settled", map[string]any{"unit_price": "1500"})
				sendEvent(ctx, conn, 44, "shipment.arrived", map[string]any{"quantity": "200"})
			},
		},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/ws", ts.handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := newWSTestClient(t, srv, "tok-1")
	ws, err := c.Connect(ctx, WSOptions{})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	if got := ws.AccountID(); got != "acc-1" {
		t.Fatalf("AccountID = %q, esperado acc-1", got)
	}
	if got := ws.SimTimeSeconds(); got != 4242 {
		t.Fatalf("SimTimeSeconds = %d, esperado 4242", got)
	}

	wm, err := ws.JoinCorp(ctx)
	if err != nil {
		t.Fatalf("JoinCorp: %v", err)
	}
	if wm != 42 {
		t.Fatalf("watermark = %d, esperado 42", wm)
	}
	if got := ws.Watermark(); got != 42 {
		t.Fatalf("Watermark() = %d, esperado 42", got)
	}

	ev := mustEvent(t, ctx, ws)
	if ev.Seq != 43 || ev.EventType != "contract.settled" {
		t.Fatalf("evento 1 inesperado: %+v", ev)
	}
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil || payload["unit_price"] != "1500" {
		t.Fatalf("payload no llegó tal cual (strings): %s (%v)", ev.Payload, err)
	}
	ev = mustEvent(t, ctx, ws)
	if ev.Seq != 44 || ev.EventType != "shipment.arrived" {
		t.Fatalf("evento 2 inesperado: %+v", ev)
	}

	if err := ws.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if err := ws.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// El canal de eventos queda cerrado tras Close.
	if _, ok := <-ws.Events(); ok {
		t.Fatal("Events() debía estar cerrado tras Close")
	}
}

func TestWSReconnectRejoinAndWatermark(t *testing.T) {
	dropped := make(chan struct{})
	ts := &wsTestServer{
		t: t, token: "tok-2", accountID: "acc-2",
		watermarks: []int64{10, 20},
		script: []func(ctx context.Context, conn *websocket.Conn){
			// Conexión 1: un evento y caída abrupta del servidor.
			func(ctx context.Context, conn *websocket.Conn) {
				sendEvent(ctx, conn, 11, "publication.created", map[string]any{"qty": "1"})
				time.Sleep(50 * time.Millisecond) // deja salir el frame
				conn.CloseNow()                   //nolint:errcheck
				close(dropped)
			},
			// Conexión 2 (tras la reconexión + re-join automático): otro evento.
			func(ctx context.Context, conn *websocket.Conn) {
				sendEvent(ctx, conn, 21, "contract.confirmed", map[string]any{"qty": "2"})
			},
		},
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/ws", ts.handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := newWSTestClient(t, srv, "tok-2")
	ws, err := c.Connect(ctx, WSOptions{ReconnectBackoffBase: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	wm, err := ws.JoinCorp(ctx)
	if err != nil {
		t.Fatalf("JoinCorp: %v", err)
	}
	if wm != 10 {
		t.Fatalf("watermark inicial = %d, esperado 10", wm)
	}
	if ev := mustEvent(t, ctx, ws); ev.Seq != 11 {
		t.Fatalf("evento previo a la caída: seq %d, esperado 11", ev.Seq)
	}
	<-dropped

	// Reconexión automática: re-join y watermark nuevo por Reconnected().
	select {
	case wm := <-ws.Reconnected():
		if wm != 20 {
			t.Fatalf("watermark tras reconectar = %d, esperado 20", wm)
		}
	case <-ctx.Done():
		t.Fatal("timeout esperando Reconnected()")
	}
	if got := ws.Watermark(); got != 20 {
		t.Fatalf("Watermark() tras reconectar = %d, esperado 20", got)
	}
	if ev := mustEvent(t, ctx, ws); ev.Seq != 21 || ev.EventType != "contract.confirmed" {
		t.Fatalf("evento tras reconectar inesperado: %+v", ev)
	}
	if got := ts.conns.Load(); got != 2 {
		t.Fatalf("conexiones al servidor = %d, esperadas 2", got)
	}
}

func TestWSConnectRejectsBadToken(t *testing.T) {
	ts := &wsTestServer{t: t, token: "tok-buena", accountID: "acc-3"}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/ws", ts.handler())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := newWSTestClient(t, srv, "tok-mala")
	_, err := c.Connect(ctx, WSOptions{})
	if err == nil {
		t.Fatal("Connect debía fallar con token inválido")
	}
	var wsErr *WSError
	if !errors.As(err, &wsErr) || wsErr.Code != "UNAUTHORIZED" {
		t.Fatalf("error inesperado: %v", err)
	}
}

func TestWSConnectRequiresToken(t *testing.T) {
	c, err := New(Options{BaseURL: "http://localhost:1/api/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Connect(context.Background(), WSOptions{}); err == nil {
		t.Fatal("Connect sin token debía fallar")
	}
}

func TestDeriveWSURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"http://localhost:8080/api/v1", "ws://localhost:8080/api/v1/ws"},
		{"https://api.imperio.dev/api/v1", "wss://api.imperio.dev/api/v1/ws"},
	}
	for _, tc := range cases {
		got, err := deriveWSURL(tc.in)
		if err != nil {
			t.Fatalf("deriveWSURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("deriveWSURL(%q) = %q, esperado %q", tc.in, got, tc.want)
		}
	}
}

// mustEvent espera el siguiente evento del canal o falla por timeout.
func mustEvent(t *testing.T, ctx context.Context, ws *WSConn) Event {
	t.Helper()
	select {
	case ev, ok := <-ws.Events():
		if !ok {
			t.Fatal("el canal de eventos se cerró inesperadamente")
		}
		return ev
	case <-ctx.Done():
		t.Fatal("timeout esperando un evento")
		return Event{}
	}
}
