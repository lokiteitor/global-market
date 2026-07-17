// Package e2e ejercita el gateway de proceso completo contra una BD real:
// migraciones + seed programático + el árbol de rutas REAL del contrato
// (internal/gateway.BuildHandler, el mismo que monta cmd/gateway) servido con
// httptest. Ningún mock: el flujo login → me → cuentas → extracto → logout
// recorre auth, ledger y el reloj de simulación de punta a punta.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales de db/migrations y la
// destruye al terminar (mismo patrón que platform/migrate y ledger).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

const (
	demoName   = "Demo"
	demoSecret = "demo-secret-dev"
)

func TestGatewayE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Seed programático, dos veces: la segunda debe ser un no-op (nunca
	//    re-emite capital ni duplica cuentas) ────────────────────────────────
	seedOpts := seed.Options{DemoName: demoName, DemoSecret: demoSecret, Ledger: ledger.DefaultOptions()}
	if err := seed.Run(ctx, pool, seedOpts, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := seed.Run(ctx, pool, seedOpts, logger); err != nil {
		t.Fatalf("seed (2ª ejecución, idempotencia): %v", err)
	}

	// ── El mux real del gateway: exactamente el mismo árbol que cmd/gateway ─
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: gateway.Options{
			Auth: auth.Options{
				LoginPerMin: auth.DefaultRateLoginPerMin,
				APIRPS:      auth.DefaultRateAPIRPS,
				APIBurst:    auth.DefaultRateAPIBurst,
			},
			Ledger:      ledger.DefaultOptions(),
			ClockReader: clock.ReaderOptions{CacheTTL: 0}, // relectura del ancla por petición
		},
	})
	if err != nil {
		t.Fatalf("BuildHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── Login demo → 201 con token y meta coherente ─────────────────────────
	r := call(t, srv, http.MethodPost, "/api/v1/auth/sessions", "",
		map[string]any{"account_name": demoName, "secret": demoSecret})
	if r.status != http.StatusCreated {
		t.Fatalf("login: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	data := asMap(t, r.body["data"], "data")
	token, _ := data["token"].(string)
	if token == "" {
		t.Fatal("login: data.token ausente o vacío")
	}
	account := asMap(t, data["account"], "data.account")
	if account["name"] != demoName || account["kind"] != "human" {
		t.Fatalf("login: cuenta inesperada: %v", account)
	}
	loginSimSecs := assertMeta(t, r.body, "login")

	// ── GET /auth/me → 200 con la cuenta demo ───────────────────────────────
	r = call(t, srv, http.MethodGet, "/api/v1/auth/me", token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("me: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	me := asMap(t, r.body["data"], "data")
	if me["name"] != demoName || me["kind"] != "human" {
		t.Fatalf("me: cuenta inesperada: %v", me)
	}
	meSimSecs := assertMeta(t, r.body, "me")
	if meSimSecs < loginSimSecs {
		t.Fatalf("sim_time_seconds retrocedió entre respuestas: %d < %d", meSimSecs, loginSimSecs)
	}

	// ── GET /ledger/accounts → caja con el capital semilla (string) ─────────
	r = call(t, srv, http.MethodGet, "/api/v1/ledger/accounts", token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("ledger/accounts: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	accounts, ok := r.body["data"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("ledger/accounts: data inesperada (esperada solo la caja): %s", r.raw)
	}
	cash := asMap(t, accounts[0], "data[0]")
	if cash["kind"] != "cash" || cash["balance"] != "1000000" {
		t.Fatalf("caja demo inesperada (balance debe ser \"1000000\" como string): %v", cash)
	}
	cashID, _ := cash["id"].(string)
	if cashID == "" {
		t.Fatal("ledger/accounts: id de la caja ausente")
	}
	assertMeta(t, r.body, "ledger/accounts")

	// ── Extracto de la caja → una única partida seed_capital de +1000000 ────
	r = call(t, srv, http.MethodGet, "/api/v1/ledger/accounts/"+cashID+"/entries", token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("entries: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	entries, ok := r.body["data"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries: %d partidas, esperada exactamente 1 (idempotencia del seed): %s",
			len(entries), r.raw)
	}
	entry := asMap(t, entries[0], "data[0]")
	if entry["amount"] != "1000000" || entry["transaction_kind"] != "seed_capital" {
		t.Fatalf("partida de capital semilla inesperada: %v", entry)
	}
	if _, ok := entry["sim_time_at"].(float64); !ok {
		t.Fatalf("entries: sim_time_at ausente o no numérico: %v", entry)
	}

	// ── 404/405 y rutas protegidas: envelopes del contrato ──────────────────
	r = call(t, srv, http.MethodGet, "/api/v1/no-such-route", token, nil)
	assertErrorEnvelope(t, r, http.StatusNotFound, "NOT_FOUND", "ruta inexistente")

	r = call(t, srv, http.MethodPut, "/api/v1/auth/me", token, nil)
	assertErrorEnvelope(t, r, http.StatusMethodNotAllowed, "VALIDATION_ERROR", "método no permitido")

	r = call(t, srv, http.MethodGet, "/api/v1/ledger/accounts", "", nil)
	assertErrorEnvelope(t, r, http.StatusUnauthorized, "UNAUTHORIZED", "ledger sin sesión")

	// ── Logout → 204 sin cuerpo; el token queda invalidado ──────────────────
	r = call(t, srv, http.MethodDelete, "/api/v1/auth/sessions/current", token, nil)
	if r.status != http.StatusNoContent {
		t.Fatalf("logout: status %d, esperado 204 (cuerpo: %s)", r.status, r.raw)
	}
	if len(r.raw) != 0 {
		t.Fatalf("logout: cuerpo no vacío en un 204: %s", r.raw)
	}

	r = call(t, srv, http.MethodGet, "/api/v1/auth/me", token, nil)
	assertErrorEnvelope(t, r, http.StatusUnauthorized, "UNAUTHORIZED", "me con token invalidado")

	// ── Login con secreto erróneo → 401 UNAUTHORIZED genérico ───────────────
	r = call(t, srv, http.MethodPost, "/api/v1/auth/sessions", "",
		map[string]any{"account_name": demoName, "secret": "secreto-incorrecto"})
	assertErrorEnvelope(t, r, http.StatusUnauthorized, "UNAUTHORIZED", "login con secreto erróneo")
}

// ─── Aserciones ─────────────────────────────────────────────────────────────

// assertMeta valida el schema Meta del contrato en una respuesta exitosa y
// devuelve sim_time_seconds: presente, no negativo, coherente con el formato
// legible sim_time y acompañado de un server_time RFC 3339 válido.
func assertMeta(t *testing.T, body map[string]any, where string) int64 {
	t.Helper()
	meta := asMap(t, body["meta"], where+".meta")
	secs, ok := meta["sim_time_seconds"].(float64)
	if !ok {
		t.Fatalf("%s: meta.sim_time_seconds ausente o no numérico: %v", where, meta)
	}
	if secs < 0 {
		t.Fatalf("%s: meta.sim_time_seconds negativo: %v", where, secs)
	}
	simStr, _ := meta["sim_time"].(string)
	if want := simtime.Format(simtime.SimTime(int64(secs))); simStr != want {
		t.Fatalf("%s: meta.sim_time %q incoherente con sim_time_seconds %d (esperado %q)",
			where, simStr, int64(secs), want)
	}
	serverTime, _ := meta["server_time"].(string)
	if _, err := time.Parse(time.RFC3339Nano, serverTime); err != nil {
		t.Fatalf("%s: meta.server_time %q no es RFC 3339: %v", where, serverTime, err)
	}
	return int64(secs)
}

// assertErrorEnvelope valida un envelope de error del contrato: status y
// code exactos con Content-Type JSON.
func assertErrorEnvelope(t *testing.T, r response, wantStatus int, wantCode, where string) {
	t.Helper()
	if r.status != wantStatus {
		t.Fatalf("%s: status %d, esperado %d (cuerpo: %s)", where, r.status, wantStatus, r.raw)
	}
	if ct := r.header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("%s: Content-Type %q, esperado JSON del contrato", where, ct)
	}
	errBody := asMap(t, r.body["error"], where+".error")
	if errBody["code"] != wantCode {
		t.Fatalf("%s: code %v, esperado %s (cuerpo: %s)", where, errBody["code"], wantCode, r.raw)
	}
	if msg, _ := errBody["message"].(string); msg == "" {
		t.Fatalf("%s: error.message ausente: %s", where, r.raw)
	}
}

func asMap(t *testing.T, v any, where string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: no es un objeto JSON: %T (%v)", where, v, v)
	}
	return m
}

// ─── Cliente HTTP del test ──────────────────────────────────────────────────

type response struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

// call ejecuta una petición contra el servidor de test. payload no nil se
// serializa como JSON; token no vacío viaja como bearer. El cuerpo de la
// respuesta se decodifica si no está vacío.
func call(t *testing.T, srv *httptest.Server, method, path, token string, payload any) response {
	t.Helper()
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("%s %s: serializando el cuerpo: %v", method, path, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatalf("%s %s: creando la petición: %v", method, path, err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: leyendo la respuesta: %v", method, path, err)
	}
	r := response{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &r.body); err != nil {
			t.Fatalf("%s %s: la respuesta no es JSON: %v (cuerpo: %s)", method, path, err, raw)
		}
	}
	return r
}

// ─── BD efímera con las migraciones reales ──────────────────────────────────

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella. Todo se destruye al terminar el test (mismo patrón que
// platform/migrate y ledger).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("e2etest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
