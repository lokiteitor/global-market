package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// newTestMiddleware monta el middleware sobre un fake con reloj determinista.
func newTestMiddleware(t *testing.T, repo *fakeRepo, clock *fakeClock, opts Options) *Middleware {
	t.Helper()
	svc := newTestService(t, repo, clock)
	mw := NewMiddleware(svc, opts, nil, testLogger())
	if clock != nil {
		mw.now = clock.now
		mw.apiLimiter.now = clock.now
		mw.apiLimiter.lastSweep = clock.now()
	}
	return mw
}

// okHandler responde 200 y captura si la petición llegó con Principal.
func okHandler(sawPrincipal *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := FromContext(r.Context()); ok {
			*sawPrincipal = true
		}
		w.WriteHeader(http.StatusOK)
	})
}

// decodeErrorCode extrae error.code de un envelope de error.
func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decodificando el envelope de error: %v (cuerpo %q)", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestRequireAuthRejectsMissingOrMalformedHeader(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	var saw bool
	h := mw.RequireAuth(okHandler(&saw))

	cases := map[string]string{
		"sin header":      "",
		"esquema basic":   "Basic abc",
		"bearer vacío":    "Bearer ",
		"token desnudo":   "abcdef",
		"solo la palabra": "Bearer",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, esperado 401", rec.Code)
			}
			if code := decodeErrorCode(t, rec); code != CodeUnauthorized {
				t.Errorf("code = %q, esperado %q", code, CodeUnauthorized)
			}
		})
	}
	if saw {
		t.Error("el handler interno se ejecutó sin autenticación")
	}
}

func TestRequireAuthRejectsUnknownToken(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	var saw bool
	h := mw.RequireAuth(okHandler(&saw))

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer token-inexistente")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || saw {
		t.Fatalf("status = %d (handler ejecutado: %v), esperado 401 sin ejecución", rec.Code, saw)
	}
}

func TestRequireAuthRejectsExpiredSession(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	acc := repo.addAccount(t, Account{Kind: "human", Name: "Corp", Status: "active"}, "secreto")
	token, _ := NewToken()
	repo.addSession(acc, token, clock.now(), clock.now().Add(-time.Minute)) // ya expirada
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	var saw bool
	h := mw.RequireAuth(okHandler(&saw))

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || saw {
		t.Fatalf("sesión expirada: status = %d (handler ejecutado: %v), esperado 401", rec.Code, saw)
	}
}

func TestRequireAuthInjectsPrincipal(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	acc := repo.addAccount(t, Account{Kind: "bot", Name: "Bot-01", Status: "active", BotArchetype: "freighter"}, "secreto")
	token, _ := NewToken()
	sess := repo.addSession(acc, token, clock.now(), clock.now().Add(time.Hour))
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})

	var gotPrincipal Principal
	h := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("PrincipalFromContext vacío dentro de RequireAuth")
		}
		gotPrincipal = p
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", rec.Code)
	}
	if gotPrincipal.Account.ID != acc.ID || gotPrincipal.SessionID != sess.ID {
		t.Errorf("principal = %+v, esperado cuenta %s y sesión %s", gotPrincipal, acc.ID, sess.ID)
	}
	if gotPrincipal.Account.BotArchetype != "freighter" {
		t.Errorf("bot_archetype = %q, esperado freighter", gotPrincipal.Account.BotArchetype)
	}
}

func TestRequireAuthTouchThrottled(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	acc := repo.addAccount(t, Account{Kind: "human", Name: "Corp", Status: "active"}, "secreto")
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	h := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	do := func(token string) {
		req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, esperado 200", rec.Code)
		}
	}

	// last_seen_at reciente: no debe tocarse.
	freshToken, _ := NewToken()
	fresh := repo.addSession(acc, freshToken, clock.now(), clock.now().Add(time.Hour))
	do(freshToken)
	time.Sleep(50 * time.Millisecond) // margen para un goroutine espurio
	if got := repo.touchCount(fresh.ID); got != 0 {
		t.Errorf("sesión reciente tocada %d veces, esperado 0", got)
	}

	// last_seen_at antiguo (> touchInterval): debe tocarse en segundo plano.
	staleToken, _ := NewToken()
	stale := repo.addSession(acc, staleToken, clock.now().Add(-2*touchInterval), clock.now().Add(time.Hour))
	do(staleToken)
	deadline := time.Now().Add(2 * time.Second)
	for repo.touchCount(stale.ID) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := repo.touchCount(stale.ID); got != 1 {
		t.Errorf("sesión antigua tocada %d veces, esperado 1", got)
	}
}

func TestRateLimitAPIWithoutPrincipal(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	h := mw.RateLimitAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, esperado 401 sin principal", rec.Code)
	}
}

func TestRateLimitAPIPerAccount(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	accA := repo.addAccount(t, Account{Kind: "human", Name: "A", Status: "active"}, "s")
	accB := repo.addAccount(t, Account{Kind: "bot", Name: "B", Status: "active"}, "s")

	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	svc := newTestService(t, repo, clock)
	mw := NewMiddleware(svc, Options{APIRPS: 1, APIBurst: 2}, metrics, testLogger())
	mw.now = clock.now
	mw.apiLimiter.now = clock.now
	mw.apiLimiter.lastSweep = clock.now()

	h := mw.RateLimitAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(acc Account) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req = req.WithContext(ContextWithPrincipal(req.Context(), Principal{Account: acc}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Ráfaga de 2 para la cuenta A; la tercera se rechaza.
	for i := range 2 {
		if rec := do(accA); rec.Code != http.StatusOK {
			t.Fatalf("petición %d de A: status %d, esperado 200", i+1, rec.Code)
		}
	}
	rec := do(accA)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("tercera petición de A: status %d, esperado 429", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != CodeRateLimited {
		t.Errorf("code = %q, esperado %q", code, CodeRateLimited)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q, esperado segundos > 0", ra)
	}

	// La cuenta B no comparte bucket con A (misma política para bots).
	if rec := do(accB); rec.Code != http.StatusOK {
		t.Fatalf("primera petición de B: status %d, esperado 200", rec.Code)
	}

	// Métrica ii_rate_limited_total{scope="api"} == 1.
	if got := testutil.ToFloat64(metrics.rateLimited.WithLabelValues(ScopeAPI)); got != 1 {
		t.Errorf("ii_rate_limited_total{scope=api} = %v, esperado 1", got)
	}

	// Recarga: un segundo después A vuelve a pasar.
	clock.advance(time.Second)
	if rec := do(accA); rec.Code != http.StatusOK {
		t.Fatalf("petición de A tras recarga: status %d, esperado 200", rec.Code)
	}
}

// TestRequireAuthClienteAbortadoNoEs500 cubre el camino de MAYOR volumen del
// mismo fallo: RequireAuth está delante de todas las peticiones autenticadas,
// así que un cliente que se va mientras se resuelve su sesión generaba un 500 y
// una línea de ERROR por petición. Una desconexión no es un fallo del servicio.
func TestRequireAuthClienteAbortadoNoEs500(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.failWith = fmt.Errorf("auth: resolviendo sesión: %w", context.Canceled)
	mw := newTestMiddleware(t, repo, clock, Options{APIRPS: 20, APIBurst: 40})
	var saw bool
	h := mw.RequireAuth(okHandler(&saw))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // el cliente cerró la conexión mientras se autenticaba
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ledger/accounts", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer token-cualquiera")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code >= 500 {
		t.Fatalf("status = %d: la desconexión del cliente cuenta como 5xx del gateway", rec.Code)
	}
	if rec.Code != httpx.StatusClientClosedRequest {
		t.Fatalf("status = %d, esperado %d", rec.Code, httpx.StatusClientClosedRequest)
	}
	if saw {
		t.Fatal("la petición abortada alcanzó el handler")
	}
}
