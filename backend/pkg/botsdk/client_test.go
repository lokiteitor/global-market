package botsdk

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// metaJSON es el fragmento meta fiel al contrato usado por los dobles.
const metaJSON = `{"sim_time":"360-045-12:30","sim_time_seconds":31104000,"server_time":"2026-07-15T10:00:00Z"}`

// sleepRecorder captura las esperas del cliente sin dormir de verdad.
type sleepRecorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
}

// record es el reemplazo inyectable de Client.sleep.
func (r *sleepRecorder) record(_ context.Context, d time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sleeps = append(r.sleeps, d)
	return nil
}

// durations devuelve una copia de las esperas registradas.
func (r *sleepRecorder) durations() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.sleeps...)
}

// newTestClient construye un Client contra el servidor doble, con esperas
// registradas en vez de reales.
func newTestClient(t *testing.T, srv *httptest.Server) (*Client, *sleepRecorder) {
	t.Helper()
	c, err := New(Options{BaseURL: srv.URL + "/api/v1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := &sleepRecorder{}
	c.sleep = rec.record
	return c, rec
}

func TestNewValidatesBaseURL(t *testing.T) {
	for _, base := range []string{"", "   ", "://sin-esquema", "sin-host"} {
		if _, err := New(Options{BaseURL: base}); err == nil {
			t.Errorf("New(%q): esperaba error", base)
		}
	}
	c, err := New(Options{BaseURL: "http://localhost:8080/api/v1/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.baseURL != "http://localhost:8080/api/v1" {
		t.Errorf("baseURL = %q, esperaba sin barra final", c.baseURL)
	}
}

func TestLoginGuardaTokenYMeta(t *testing.T) {
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") == "" {
			t.Error("Login sin Idempotency-Key")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"session_id":"01981c5e-0000-7000-8000-000000000001","token":"tok-secreto","expires_at":"2026-07-16T10:00:00Z","account":{"id":"01981c5e-0000-7000-8000-000000000002","kind":"bot","name":"carbonera","status":"active","bot_archetype":"primary_producer","created_at":"2026-07-01T00:00:00Z"}},"meta":`+metaJSON+`}`)
	})
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"data":{"id":"01981c5e-0000-7000-8000-000000000002","kind":"bot","name":"carbonera","status":"active","created_at":"2026-07-01T00:00:00Z"},"meta":`+metaJSON+`}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	sess, err := c.Login(context.Background(), "carbonera", "secreto")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if sess.Token != "tok-secreto" || c.Token() != "tok-secreto" {
		t.Errorf("token = %q / %q, esperaba tok-secreto", sess.Token, c.Token())
	}
	if sess.Account.Kind != AccountBot || sess.Account.BotArchetype != ArchetypePrimaryProducer {
		t.Errorf("account inesperada: %+v", sess.Account)
	}
	if got := c.SimTimeSeconds(); got != 31_104_000 {
		t.Errorf("SimTimeSeconds = %d, esperaba 31104000", got)
	}

	me, err := c.Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if gotAuth != "Bearer tok-secreto" {
		t.Errorf("Authorization = %q, esperaba Bearer tok-secreto", gotAuth)
	}
	if me.Name != "carbonera" {
		t.Errorf("me.Name = %q", me.Name)
	}
}

func TestLogoutBorraToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/auth/sessions/current", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-1" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)
	c.SetToken("tok-1")

	if err := c.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if c.Token() != "" {
		t.Errorf("token tras Logout = %q, esperaba vacío", c.Token())
	}
}

func TestErrorEnvelopeProduceAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/contracts/publications", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"error":{"code":"INSUFFICIENT_COLLATERAL","message":"La garantía disponible no cubre la publicación solicitada","details":{"required":"1000","available":"740"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	_, err := c.CreatePublication(context.Background(), PublicationCreate{Kind: PublicationSell})
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("esperaba *APIError, obtuve %T: %v", err, err)
	}
	if apiErr.Status != http.StatusUnprocessableEntity || apiErr.Code != "INSUFFICIENT_COLLATERAL" {
		t.Errorf("APIError = %+v", apiErr)
	}
	if apiErr.Details["required"] != "1000" || apiErr.Details["available"] != "740" {
		t.Errorf("Details = %v", apiErr.Details)
	}
	if !IsCode(err, "INSUFFICIENT_COLLATERAL") {
		t.Error("IsCode(INSUFFICIENT_COLLATERAL) = false")
	}
}

func TestErrorNoJSONProduceAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>bad gateway</html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	_, err := c.Me(context.Background())
	apiErr, ok := AsAPIError(err)
	if !ok {
		t.Fatalf("esperaba *APIError, obtuve %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadGateway || apiErr.Code != "" {
		t.Errorf("APIError = %+v", apiErr)
	}
}

func TestRateLimitReintentaRespetandoRetryAfterConMismaClave(t *testing.T) {
	var keys []string
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/contracts/publications", func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		attempts++
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if attempts == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"code":"RATE_LIMITED","message":"límite de comandos excedido"}}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"id":"01981c5e-0000-7000-8000-0000000000aa","kind":"sell","publisher_account_id":"01981c5e-0000-7000-8000-000000000002","channel":"board","product_id":"01981c5e-0000-7000-8000-00000000000p","quantity_total":"500","quantity_remaining":"500","unit_price":"120","min_lot":"50","origin_node_id":"01981c5e-0000-7000-8000-00000000000n","delivery_sim_seconds":172800,"status":"draw_window","published_at_sim":31104000},"meta":`+metaJSON+`}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, rec := newTestClient(t, srv)

	pub, err := c.CreatePublication(context.Background(), PublicationCreate{
		Kind:               PublicationSell,
		ProductID:          "01981c5e-0000-7000-8000-00000000000p",
		QuantityTotal:      "500",
		UnitPrice:          "120",
		MinLot:             "50",
		OriginNodeID:       "01981c5e-0000-7000-8000-00000000000n",
		DeliverySimSeconds: 172800,
	})
	if err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}
	if pub.Status != PublicationDrawWindow || pub.UnitPrice != "120" {
		t.Errorf("publicación inesperada: %+v", pub)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, esperaba 2", attempts)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("Idempotency-Key inestable entre reintentos: %v", keys)
	}
	sleeps := rec.durations()
	if len(sleeps) != 1 || sleeps[0] != 2*time.Second {
		t.Errorf("sleeps = %v, esperaba [2s] (Retry-After respetado)", sleeps)
	}
}

func TestRateLimitAgotaReintentos(t *testing.T) {
	attempts := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/contracts/board", func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"code":"RATE_LIMITED","message":"límite excedido"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, rec := newTestClient(t, srv)

	_, err := c.Board(context.Background(), BoardQuery{})
	if !IsCode(err, "RATE_LIMITED") {
		t.Fatalf("esperaba RATE_LIMITED, obtuve %v", err)
	}
	if attempts != 1+DefaultMaxRetries {
		t.Errorf("attempts = %d, esperaba %d", attempts, 1+DefaultMaxRetries)
	}
	// Sin Retry-After: backoff exponencial determinista base×2^n.
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}
	sleeps := rec.durations()
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %v, esperaba %v", sleeps, want)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Errorf("sleep[%d] = %v, esperaba %v", i, sleeps[i], want[i])
		}
	}
}

// fallaPrimero es un RoundTripper que falla la primera petición a nivel de
// red y delega las siguientes, registrando la Idempotency-Key de todas.
type fallaPrimero struct {
	mu    sync.Mutex
	calls int
	keys  []string
	next  http.RoundTripper
}

// RoundTrip implementa http.RoundTripper.
func (f *fallaPrimero) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.calls++
	f.keys = append(f.keys, req.Header.Get("Idempotency-Key"))
	fail := f.calls == 1
	f.mu.Unlock()
	if fail {
		return nil, errors.New("conexión reiniciada (simulada)")
	}
	return f.next.RoundTrip(req)
}

func TestErrorDeRedReintentaMutacionConMismaClave(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/world/shipments/s-1/dispatch", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"data":{"id":"s-1","owner_account_id":"a-1","product_id":"p-1","quantity":"100","contract_id":"c-1","vehicle_id":"v-1","status":"in_transit"},"meta":`+metaJSON+`}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := &fallaPrimero{next: http.DefaultTransport}
	c, err := New(Options{BaseURL: srv.URL + "/api/v1", HTTPClient: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rec := &sleepRecorder{}
	c.sleep = rec.record

	ship, err := c.Dispatch(context.Background(), "s-1", "v-1", "r-1")
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if ship.Status != ShipmentInTransit {
		t.Errorf("status = %q", ship.Status)
	}
	if rt.calls != 2 {
		t.Fatalf("calls = %d, esperaba 2", rt.calls)
	}
	if rt.keys[0] == "" || rt.keys[0] != rt.keys[1] {
		t.Errorf("Idempotency-Key inestable ante error de red: %v", rt.keys)
	}
	if len(rec.durations()) != 1 {
		t.Errorf("sleeps = %v, esperaba una espera de backoff", rec.durations())
	}
}

func TestIdempotencyKeyDistintaEntreMutaciones(t *testing.T) {
	var keys []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/world/concessions/c-1/renew", func(w http.ResponseWriter, r *http.Request) {
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, `{"data":{"id":"c-1","region_id":"r-1","holder_account_id":"a-1","parcel":{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]},"canon_amount":"1000","period_sim_days":90,"expires_at_sim":40000000,"status":"active","granted_at_sim":31104000},"meta":`+metaJSON+`}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	for range 2 {
		if _, err := c.RenewConcession(context.Background(), "c-1"); err != nil {
			t.Fatalf("RenewConcession: %v", err)
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Errorf("cada mutación debe estrenar clave: %v", keys)
	}
}
