package idempotency

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
)

// TestIdempotencyIntegration ejercita el middleware contra el almacén real
// (public.idempotency_keys, migración 0008): miss que ejecuta y persiste,
// hit que reproduce sin re-ejecutar, aislamiento por clave y por cuenta,
// carrera perdida en el INSERT, 5xx no persistido y clave inválida.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales de db/migrations y la
// destruye al terminar (mismo patrón que internal/ledger).
func TestIdempotencyIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	accountA := mustAuthAccount(t, ctx, pool, "Idem Corp A")
	accountB := mustAuthAccount(t, ctx, pool, "Idem Corp B")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolver := &switchableResolver{id: accountA}
	m := NewMiddleware(pool, resolver, prometheus.NewRegistry(), logger)

	executed := 0
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed++
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"data":{"ejecucion":%d},"meta":{}}`, executed)
	}))

	// ── Miss: ejecuta, entrega y persiste la respuesta completa ─────────────
	key := uuid.New()
	rec := post(h, key.String())
	if rec.Code != http.StatusCreated || executed != 1 {
		t.Fatalf("miss: status %d, ejecuciones %d; esperado 201 y 1", rec.Code, executed)
	}
	if rec.Header().Get(HeaderReplayed) != "" {
		t.Fatal("miss: no debe marcar Idempotency-Replayed")
	}
	var (
		gotMethod, gotPath, gotCT string
		gotStatus                 int
		gotBody                   []byte
	)
	err := pool.QueryRow(ctx,
		`SELECT method, path, response_status, content_type, response_body
           FROM public.idempotency_keys WHERE key = $1 AND account_id = $2`,
		key, accountA).Scan(&gotMethod, &gotPath, &gotStatus, &gotCT, &gotBody)
	if err != nil {
		t.Fatalf("miss: fila no persistida: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/ledger/publications" ||
		gotStatus != http.StatusCreated || gotCT != "application/json; charset=utf-8" ||
		string(gotBody) != `{"data":{"ejecucion":1},"meta":{}}` {
		t.Fatalf("miss: fila inesperada: %s %s %d %q %q", gotMethod, gotPath, gotStatus, gotCT, gotBody)
	}

	// ── Hit: reproduce sin re-ejecutar ──────────────────────────────────────
	rec = post(h, key.String())
	if executed != 1 {
		t.Fatalf("hit: el handler se re-ejecutó (%d veces)", executed)
	}
	if rec.Code != http.StatusCreated ||
		rec.Body.String() != `{"data":{"ejecucion":1},"meta":{}}` ||
		rec.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		rec.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("hit: replay inesperado: status %d, cuerpo %q, cabeceras %v",
			rec.Code, rec.Body.String(), rec.Header())
	}
	if got := testutil.ToFloat64(m.hits); got != 1 {
		t.Fatalf("ii_idempotency_hits_total = %v, esperado 1", got)
	}

	// ── Clave distinta: ejecuta de nuevo ────────────────────────────────────
	if rec = post(h, uuid.NewString()); executed != 2 {
		t.Fatalf("clave nueva: ejecuciones %d, esperado 2", executed)
	}

	// ── Misma clave, otra cuenta: el ámbito es (key, account) ──────────────
	resolver.id = accountB
	if rec = post(h, key.String()); executed != 3 {
		t.Fatalf("otra cuenta con la misma clave debe ejecutar: %d, esperado 3", executed)
	}
	if rec.Body.String() != `{"data":{"ejecucion":3},"meta":{}}` {
		t.Fatalf("otra cuenta: cuerpo %q", rec.Body.String())
	}
	resolver.id = accountA

	// ── Carrera: el ganador concurrente persiste durante la ejecución ──────
	raceKey := uuid.New()
	winnerBody := `{"data":{"ganador":true},"meta":{}}`
	raced := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simula a la petición concurrente que gana la carrera: su fila ya
		// está cuando este intento llegue al INSERT ... ON CONFLICT.
		if _, err := pool.Exec(r.Context(),
			`INSERT INTO public.idempotency_keys
                 (key, account_id, method, path, response_status, content_type, response_body)
             VALUES ($1, $2, 'POST', '/ledger/publications', 201, 'application/json; charset=utf-8', $3)`,
			raceKey, accountA, []byte(winnerBody)); err != nil {
			t.Errorf("insertando la fila del ganador: %v", err)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"perdedor":true},"meta":{}}`))
	}))
	rec = post(raced, raceKey.String())
	if rec.Body.String() != winnerBody || rec.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("carrera: el perdedor debe devolver la respuesta del ganador: %q (replayed %q)",
			rec.Body.String(), rec.Header().Get(HeaderReplayed))
	}

	// ── 5xx: no se persiste y el reintento ejecuta de verdad ────────────────
	failKey := uuid.New()
	failures := 0
	flaky := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failures++
		if failures == 1 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"reintento":true},"meta":{}}`))
	}))
	if rec = post(flaky, failKey.String()); rec.Code != http.StatusInternalServerError {
		t.Fatalf("primer intento: status %d, esperado 500", rec.Code)
	}
	if n := countKeys(t, ctx, pool, failKey); n != 0 {
		t.Fatalf("un 5xx no debe persistirse: %d fila(s)", n)
	}
	rec = post(flaky, failKey.String())
	if rec.Code != http.StatusCreated || failures != 2 || rec.Header().Get(HeaderReplayed) != "" {
		t.Fatalf("reintento tras 5xx: status %d, ejecuciones %d, replayed %q",
			rec.Code, failures, rec.Header().Get(HeaderReplayed))
	}
	if n := countKeys(t, ctx, pool, failKey); n != 1 {
		t.Fatalf("el reintento exitoso debe persistirse: %d fila(s)", n)
	}

	// ── Clave inválida contra el middleware real ────────────────────────────
	before := executed
	if rec = post(h, "esto-no-es-uuid"); rec.Code != http.StatusBadRequest || executed != before {
		t.Fatalf("clave inválida: status %d (esperado 400), ejecuciones %d (esperado %d)",
			rec.Code, executed, before)
	}
}

// switchableResolver permite cambiar la cuenta autenticada entre peticiones.
type switchableResolver struct{ id uuid.UUID }

func (s *switchableResolver) AccountID(context.Context) (uuid.UUID, bool) { return s.id, true }

func post(h http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ledger/publications", nil)
	if key != "" {
		req.Header.Set(Header, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func countKeys(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.idempotency_keys WHERE key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("contando claves: %v", err)
	}
	return n
}

// ─── Infraestructura del test (mismo patrón que internal/ledger) ────────────

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella. Todo se destruye al terminar el test.
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("idemtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	if _, err := migrate.New(conn, "../../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
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

// mustAuthAccount inserta una corporación de prueba en auth.accounts (FK de
// idempotency_keys.account_id).
func mustAuthAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generando UUIDv7: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.accounts (id, kind, name) VALUES ($1, 'human', $2)`, id, name); err != nil {
		t.Fatalf("creando la cuenta %q: %v", name, err)
	}
	return id
}
