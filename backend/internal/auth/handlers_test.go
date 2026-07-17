package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// testHandlers monta los Handlers sobre un fake con reloj determinista.
func testHandlers(t *testing.T, repo *fakeRepo, clock *fakeClock, opts Options, metrics *Metrics) (*Handlers, *Service) {
	t.Helper()
	if opts.LoginPerMin == 0 {
		opts.LoginPerMin = DefaultRateLoginPerMin
	}
	if opts.APIRPS == 0 {
		opts.APIRPS = DefaultRateAPIRPS
	}
	if opts.APIBurst == 0 {
		opts.APIBurst = DefaultRateAPIBurst
	}
	svc := newTestService(t, repo, clock)
	h := NewHandlers(svc, stubMeta{}, opts, metrics, testLogger())
	if clock != nil {
		h.loginLimiter.now = clock.now
		h.loginLimiter.lastSweep = clock.now()
	}
	return h, svc
}

// dataEnvelope es la forma {data,meta} de las respuestas exitosas.
type testEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta struct {
		SimTime        string `json:"sim_time"`
		SimTimeSeconds int64  `json:"sim_time_seconds"`
		ServerTime     string `json:"server_time"`
	} `json:"meta"`
}

// sessionCreatedBody es la proyección de SessionCreated para asserts.
type sessionCreatedBody struct {
	SessionID string          `json:"session_id"`
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
	Account   json.RawMessage `json:"account"`
}

func postLogin(t *testing.T, h *Handlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/sessions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.CreateSession().ServeHTTP(rec, req)
	return rec
}

func TestCreateSessionOK(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.addAccount(t, Account{Kind: "human", Name: "Ferro SA", Status: "active"}, "un-secreto")
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	rec := postLogin(t, h, `{"account_name":"ferro sa","secret":"un-secreto","client_info":{"app":"test"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, esperado 201 (cuerpo %s)", rec.Code, rec.Body.String())
	}
	var env testEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decodificando envelope: %v", err)
	}
	if env.Meta.SimTime == "" || env.Meta.ServerTime == "" {
		t.Errorf("meta incompleta: %+v", env.Meta)
	}
	var data sessionCreatedBody
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decodificando data: %v", err)
	}
	if data.SessionID == "" {
		t.Error("session_id vacío")
	}
	if len(data.Token) != 43 {
		t.Errorf("token de %d caracteres, esperado 43 (32B base64url sin padding)", len(data.Token))
	}
	if want := clock.now().Add(SessionTTL); !data.ExpiresAt.Equal(want) {
		t.Errorf("expires_at = %v, esperado %v (now+24h)", data.ExpiresAt, want)
	}
	var acc map[string]any
	if err := json.Unmarshal(data.Account, &acc); err != nil {
		t.Fatalf("decodificando account: %v", err)
	}
	if acc["kind"] != "human" || acc["name"] != "Ferro SA" || acc["status"] != "active" {
		t.Errorf("account inesperada: %v", acc)
	}
	if _, present := acc["bot_archetype"]; present {
		t.Error("bot_archetype presente en una cuenta humana")
	}
	if _, present := acc["created_at"]; !present {
		t.Error("created_at ausente en account")
	}
	// El token en claro nunca se guarda: el repo solo conoce su hash.
	if _, _, err := repo.FindSessionByTokenHash(t.Context(), HashToken(data.Token)); err != nil {
		t.Errorf("la sesión no quedó persistida por hash: %v", err)
	}
}

func TestCreateSessionBotIncludesArchetype(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.addAccount(t, Account{Kind: "bot", Name: "Bot-7", Status: "active", BotArchetype: "arbitrageur"}, "s3")
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	rec := postLogin(t, h, `{"account_name":"Bot-7","secret":"s3"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, esperado 201", rec.Code)
	}
	var env testEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decodificando envelope: %v", err)
	}
	var data sessionCreatedBody
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decodificando data: %v", err)
	}
	var acc map[string]any
	if err := json.Unmarshal(data.Account, &acc); err != nil {
		t.Fatalf("decodificando account: %v", err)
	}
	if acc["bot_archetype"] != "arbitrageur" {
		t.Errorf("bot_archetype = %v, esperado arbitrageur", acc["bot_archetype"])
	}
}

func TestCreateSessionGenericUnauthorized(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.addAccount(t, Account{Kind: "human", Name: "Real", Status: "active"}, "secreto-real")
	repo.addAccount(t, Account{Kind: "human", Name: "Suspendida", Status: "suspended"}, "secreto-susp")
	h, _ := testHandlers(t, repo, clock, Options{LoginPerMin: 100}, nil)

	cases := map[string]string{
		"cuenta inexistente": `{"account_name":"NoExiste","secret":"lo-que-sea"}`,
		"secreto inválido":   `{"account_name":"Real","secret":"secreto-malo"}`,
		"cuenta suspendida":  `{"account_name":"Suspendida","secret":"secreto-susp"}`,
	}
	var bodies []string
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postLogin(t, h, body)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, esperado 401", rec.Code)
			}
			if code := decodeErrorCode(t, rec); code != CodeUnauthorized {
				t.Errorf("code = %q, esperado %q", code, CodeUnauthorized)
			}
			bodies = append(bodies, rec.Body.String())
		})
	}
	// La respuesta es idéntica en todos los casos: no filtra la causa.
	for i := 1; i < len(bodies); i++ {
		if bodies[i] != bodies[0] {
			t.Errorf("respuestas 401 distintas entre causas: %q vs %q", bodies[0], bodies[i])
		}
	}
}

func TestCreateSessionValidation(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	longName := strings.Repeat("a", maxAccountNameLen+1)
	longSecret := strings.Repeat("s", maxSecretLen+1)
	cases := map[string]string{
		"cuerpo vacío":           ``,
		"json inválido":          `{`,
		"sin account_name":       `{"secret":"x"}`,
		"sin secret":             `{"account_name":"x"}`,
		"nombre en blanco":       `{"account_name":"   ","secret":"x"}`,
		"nombre demasiado largo": fmt.Sprintf(`{"account_name":%q,"secret":"x"}`, longName),
		"secret demasiado largo": fmt.Sprintf(`{"account_name":"x","secret":%q}`, longSecret),
		"tipo incorrecto":        `{"account_name":42,"secret":"x"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			rec := postLogin(t, h, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, esperado 400 (cuerpo %s)", rec.Code, rec.Body.String())
			}
			if code := decodeErrorCode(t, rec); code != "VALIDATION_ERROR" {
				t.Errorf("code = %q, esperado VALIDATION_ERROR", code)
			}
		})
	}
}

func TestCreateSessionRateLimited(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.addAccount(t, Account{Kind: "human", Name: "Corp", Status: "active"}, "secreto")
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	h, _ := testHandlers(t, repo, clock, Options{LoginPerMin: 2}, metrics)

	body := `{"account_name":"Corp","secret":"secreto-malo"}`
	for i := range 2 {
		if rec := postLogin(t, h, body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("intento %d: status %d, esperado 401", i+1, rec.Code)
		}
	}
	rec := postLogin(t, h, body)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("tercer intento: status %d, esperado 429", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != CodeRateLimited {
		t.Errorf("code = %q, esperado %q", code, CodeRateLimited)
	}
	if ra := rec.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q, esperado segundos > 0", ra)
	}
	if got := testutil.ToFloat64(metrics.rateLimited.WithLabelValues(ScopeLogin)); got != 1 {
		t.Errorf("ii_rate_limited_total{scope=login} = %v, esperado 1", got)
	}

	// La clave es IP+nombre: otro nombre desde la misma IP no está limitado.
	rec = postLogin(t, h, `{"account_name":"Otra","secret":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("nombre distinto: status %d, esperado 401 (no 429)", rec.Code)
	}

	// Recarga con el paso del tiempo: 30s a 2/min = 1 token.
	clock.advance(30 * time.Second)
	rec = postLogin(t, h, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("tras recarga: status %d, esperado 401 (no 429)", rec.Code)
	}
}

func TestDeleteCurrentSession(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	acc := repo.addAccount(t, Account{Kind: "human", Name: "Corp", Status: "active"}, "secreto")
	token, _ := NewToken()
	sess := repo.addSession(acc, token, clock.now(), clock.now().Add(time.Hour))
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	req := httptest.NewRequest(http.MethodDelete, "/auth/sessions/current", nil)
	req = req.WithContext(ContextWithPrincipal(req.Context(), Principal{Account: acc, SessionID: sess.ID}))
	rec := httptest.NewRecorder()
	h.DeleteCurrentSession().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, esperado 204", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("cuerpo no vacío en 204: %q", rec.Body.String())
	}
	if repo.hasSession(sess.ID) {
		t.Error("la sesión sigue viva tras el logout")
	}

	// Sin principal (sin RequireAuth): 401.
	rec = httptest.NewRecorder()
	h.DeleteCurrentSession().ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/auth/sessions/current", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin principal: status = %d, esperado 401", rec.Code)
	}
}

func TestMe(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	acc := repo.addAccount(t, Account{Kind: "bot", Name: "Bot-9", Status: "active", BotArchetype: "primary_producer"}, "s")
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req = req.WithContext(ContextWithPrincipal(req.Context(), Principal{Account: acc}))
	rec := httptest.NewRecorder()
	h.Me().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", rec.Code)
	}
	var env testEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decodificando envelope: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("decodificando data: %v", err)
	}
	if got["id"] != acc.ID.String() || got["kind"] != "bot" || got["name"] != "Bot-9" ||
		got["status"] != "active" || got["bot_archetype"] != "primary_producer" {
		t.Errorf("account = %v", got)
	}

	// Sin principal: 401.
	rec = httptest.NewRecorder()
	h.Me().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("sin principal: status = %d, esperado 401", rec.Code)
	}
}

func TestCreateSessionRepoFailureIs500(t *testing.T) {
	clock := newFakeClock()
	repo := newFakeRepo(clock.now)
	repo.failWith = fmt.Errorf("bd caída")
	h, _ := testHandlers(t, repo, clock, Options{}, nil)

	rec := postLogin(t, h, `{"account_name":"Corp","secret":"secreto"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, esperado 500", rec.Code)
	}
	if code := decodeErrorCode(t, rec); code != "INTERNAL" {
		t.Errorf("code = %q, esperado INTERNAL", code)
	}
}
