package land_test

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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/land"
)

const testSimNow int64 = 7200

// TestLandIntegration ejercita los endpoints world/concessions* y
// world/concession-transfers contra una BD real con el esquema migrado y el seed
// del Incremento 1 (Demo y Norte Trading, cada una con caja de 1.000.000 y una
// concesión sembrada). Valida el otorgamiento con cobro de canon al sink, el
// solape (409), la autorización por titular (403), la renovación y el traspaso
// con su tasa, verificando los saldos del ledger.
//
// Se omite si II_TEST_DATABASE_URL no está definida (patrón existente): la URL
// solo identifica el servidor; el test crea una BD EFÍMERA propia (CREATEDB).
func TestLandIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	norte := accountID(t, ctx, pool, seed.DefaultTraderName)
	region := regionID(t, ctx, pool, seed.RegionName)
	seededDemoConcession := firstActiveConcession(t, ctx, pool, demo)

	svc, err := land.NewService(pool, fakeSim{testSimNow}, land.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, logger)
	norteMux := newMux(svc, norte, logger)

	var newConcessionID string

	// ── Otorgamiento feliz + cobro de canon al sink ───────────────────────────
	t.Run("otorgamiento cobra el canon al sink", func(t *testing.T) {
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		parcel := polygon(19500, 19500, 20500, 20500)
		rec := do(t, demoMux, http.MethodPost, "/world/concessions",
			fmt.Sprintf(`{"region_id":%q,"parcel":%s}`, region, parcel))
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST concession: status %d (body %s)", rec.Code, rec.Body.String())
		}
		c := dataOf[concessionDTO](t, rec)
		newConcessionID = c.ID
		if c.HolderAccountID != demo.String() || c.RegionID != region.String() {
			t.Fatalf("concesión mal formada: %+v", c)
		}
		if c.CanonAmount != "1000" { // canon_base de Askadia × 1
			t.Fatalf("canon inesperado: %q, esperado 1000", c.CanonAmount)
		}
		if c.Status != "active" || c.PeriodSimDays != 90 {
			t.Fatalf("estado/periodo inesperados: %+v", c)
		}
		if c.ExpiresAtSim <= c.GrantedAtSim || c.GrantedAtSim != testSimNow {
			t.Fatalf("vencimiento/otorgamiento inesperados: %+v", c)
		}
		assertGeo(t, c.Parcel, "Polygon")

		if got := cashBefore - cashBalance(t, ctx, pool, demo); got != 1000 {
			t.Fatalf("la caja de Demo cayó %d, esperado 1000 (canon)", got)
		}
		if got := sinkBalance(t, ctx, pool) - sinkBefore; got != 1000 {
			t.Fatalf("el sink subió %d, esperado 1000 (canon)", got)
		}
	})

	// ── Solape → 409 ──────────────────────────────────────────────────────────
	t.Run("parcela solapada devuelve 409", func(t *testing.T) {
		// Se solapa con la concesión sembrada de Demo (9500..10500).
		parcel := polygon(10000, 10000, 11000, 11000)
		rec := do(t, demoMux, http.MethodPost, "/world/concessions",
			fmt.Sprintf(`{"region_id":%q,"parcel":%s}`, region, parcel))
		if rec.Code != http.StatusConflict {
			t.Fatalf("POST concession solapada: status %d, esperado 409 (body %s)", rec.Code, rec.Body.String())
		}
		if code := errCodeOf(t, rec); code != "CONCESSION_OVERLAP" {
			t.Fatalf("code %q, esperado CONCESSION_OVERLAP", code)
		}
	})

	// ── Fuera de la región → 422 ──────────────────────────────────────────────
	t.Run("parcela fuera de la región devuelve 422", func(t *testing.T) {
		parcel := polygon(60000, 60000, 61000, 61000) // fuera de Askadia (0..50000)
		rec := do(t, demoMux, http.MethodPost, "/world/concessions",
			fmt.Sprintf(`{"region_id":%q,"parcel":%s}`, region, parcel))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("POST concession fuera de región: status %d, esperado 422 (body %s)", rec.Code, rec.Body.String())
		}
		if code := errCodeOf(t, rec); code != httpx.CodeValidationError {
			t.Fatalf("code %q, esperado VALIDATION_ERROR", code)
		}
	})

	// ── Listado y detalle: SOLO propias; ajena → 403 ──────────────────────────
	t.Run("listado propio y 403 sobre ajena", func(t *testing.T) {
		list, _ := listOf[concessionDTO](t, demoMux, "/world/concessions")
		if len(list) < 2 { // sembrada + nueva
			t.Fatalf("Demo debería ver >= 2 concesiones, vio %d", len(list))
		}
		for _, c := range list {
			if c.HolderAccountID != demo.String() {
				t.Fatalf("el listado incluyó una concesión ajena: %+v", c)
			}
		}
		// Detalle propio.
		rec := do(t, demoMux, http.MethodGet, "/world/concessions/"+newConcessionID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET concesión propia: status %d", rec.Code)
		}
		// Norte pide una concesión de Demo → 403.
		rec = do(t, norteMux, http.MethodGet, "/world/concessions/"+newConcessionID, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET concesión ajena: status %d, esperado 403 (body %s)", rec.Code, rec.Body.String())
		}
		if code := errCodeOf(t, rec); code != "NOT_RESOURCE_OWNER" {
			t.Fatalf("code %q, esperado NOT_RESOURCE_OWNER", code)
		}
	})

	// ── Renovación: extiende y cobra otro canon ───────────────────────────────
	t.Run("renovación extiende y cobra canon", func(t *testing.T) {
		before := dataOf[concessionDTO](t, do(t, demoMux, http.MethodGet, "/world/concessions/"+newConcessionID, ""))
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		rec := do(t, demoMux, http.MethodPost, "/world/concessions/"+newConcessionID+"/renew", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("renew: status %d (body %s)", rec.Code, rec.Body.String())
		}
		after := dataOf[concessionDTO](t, rec)
		wantExpiry := before.ExpiresAtSim + int64(before.PeriodSimDays)*simtime.SimDay
		if after.ExpiresAtSim != wantExpiry {
			t.Fatalf("expires_at_sim = %d, esperado %d", after.ExpiresAtSim, wantExpiry)
		}
		if got := cashBefore - cashBalance(t, ctx, pool, demo); got != 1000 {
			t.Fatalf("la caja de Demo cayó %d en la renovación, esperado 1000", got)
		}
		if got := sinkBalance(t, ctx, pool) - sinkBefore; got != 1000 {
			t.Fatalf("el sink subió %d en la renovación, esperado 1000", got)
		}
	})

	// ── Traspaso: precio comprador→vendedor + tasa comprador→sink ──────────────
	t.Run("traspaso mueve precio y tasa", func(t *testing.T) {
		const price int64 = 50000
		const fee int64 = price * 500 / 10000 // II_CONCESSION_TRANSFER_FEE_BP=500

		demoCashBefore := cashBalance(t, ctx, pool, demo)
		norteCashBefore := cashBalance(t, ctx, pool, norte)
		sinkBefore := sinkBalance(t, ctx, pool)

		rec := do(t, demoMux, http.MethodPost, "/world/concession-transfers",
			fmt.Sprintf(`{"concession_id":%q,"to_account_id":%q,"price":"%d"}`, newConcessionID, norte, price))
		if rec.Code != http.StatusCreated {
			t.Fatalf("transfer: status %d (body %s)", rec.Code, rec.Body.String())
		}
		tr := dataOf[transferDTO](t, rec)
		if tr.FromAccountID != demo.String() || tr.ToAccountID != norte.String() {
			t.Fatalf("partes del traspaso inesperadas: %+v", tr)
		}
		if tr.Price != fmt.Sprint(price) || tr.SystemFee != fmt.Sprint(fee) {
			t.Fatalf("precio/tasa inesperados: %+v", tr)
		}
		// El titular cambió a Norte.
		got := dataOf[concessionDTO](t, do(t, norteMux, http.MethodGet, "/world/concessions/"+newConcessionID, ""))
		if got.HolderAccountID != norte.String() {
			t.Fatalf("el titular no cambió a Norte: %+v", got)
		}
		// Saldos: comprador -price-fee, vendedor +price, sink +fee.
		if d := norteCashBefore - cashBalance(t, ctx, pool, norte); d != price+fee {
			t.Fatalf("la caja del comprador cayó %d, esperado %d", d, price+fee)
		}
		if d := cashBalance(t, ctx, pool, demo) - demoCashBefore; d != price {
			t.Fatalf("la caja del vendedor subió %d, esperado %d", d, price)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != fee {
			t.Fatalf("el sink subió %d, esperado %d (tasa)", d, fee)
		}
	})

	// ── Traspaso sin fondos del comprador → 422 INSUFFICIENT_FUNDS ────────────
	t.Run("traspaso sin fondos devuelve 422", func(t *testing.T) {
		// Demo traspasa su concesión sembrada a Norte por un precio > caja de Norte.
		rec := do(t, demoMux, http.MethodPost, "/world/concession-transfers",
			fmt.Sprintf(`{"concession_id":%q,"to_account_id":%q,"price":"%d"}`, seededDemoConcession, norte, int64(50_000_000)))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("transfer sin fondos: status %d, esperado 422 (body %s)", rec.Code, rec.Body.String())
		}
		if code := errCodeOf(t, rec); code != "INSUFFICIENT_FUNDS" {
			t.Fatalf("code %q, esperado INSUFFICIENT_FUNDS", code)
		}
		// La concesión sigue siendo de Demo (todo-o-nada).
		var holder uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT holder_account_id FROM world.land_concessions WHERE id=$1`, seededDemoConcession).Scan(&holder); err != nil {
			t.Fatalf("releyendo la concesión: %v", err)
		}
		if holder != demo {
			t.Fatalf("el traspaso fallido cambió el titular a %s", holder)
		}
	})
}

// ─── DTOs del contrato para las aserciones ───────────────────────────────────

type concessionDTO struct {
	ID              string          `json:"id"`
	RegionID        string          `json:"region_id"`
	HolderAccountID string          `json:"holder_account_id"`
	Parcel          json.RawMessage `json:"parcel"`
	CanonAmount     string          `json:"canon_amount"`
	PeriodSimDays   int32           `json:"period_sim_days"`
	ExpiresAtSim    int64           `json:"expires_at_sim"`
	Status          string          `json:"status"`
	GrantedAtSim    int64           `json:"granted_at_sim"`
}

type transferDTO struct {
	ID            string `json:"id"`
	ConcessionID  string `json:"concession_id"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	Price         string `json:"price"`
	SystemFee     string `json:"system_fee"`
	OccurredAtSim int64  `json:"occurred_at_sim"`
}

// ─── Infraestructura del test (identidad, reloj, HTTP, BD) ────────────────────

type fakeSim struct{ now int64 }

func (f fakeSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(f.now) }

type fakeMeta struct{}

func (fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(simtime.SimTime(testSimNow)), SimTimeSeconds: testSimNow, ServerTime: time.Now().UTC()}
}

// fakeIdentity fija la cuenta autenticada de un mux (una por corporación).
type fakeIdentity struct{ acc uuid.UUID }

func (i fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return i.acc, true }

func newMux(svc *land.Service, acc uuid.UUID, logger *slog.Logger) *http.ServeMux {
	h := land.NewHandlers(svc, fakeIdentity{acc}, fakeMeta{}, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

// polygon construye el GeoJSON de un rectángulo cerrado (coordenadas planas).
func polygon(minX, minY, maxX, maxY int) string {
	return fmt.Sprintf(`{"type":"Polygon","coordinates":[[[%d,%d],[%d,%d],[%d,%d],[%d,%d],[%d,%d]]]}`,
		minX, minY, maxX, minY, maxX, maxY, minX, maxY, minX, minY)
}

func do(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func dataOf[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decodificando data: %v (body %s)", err, rec.Body.String())
	}
	return resp.Data
}

func listOf[T any](t *testing.T, mux *http.ServeMux, target string) ([]T, string) {
	t.Helper()
	rec := do(t, mux, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body %s)", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []T `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: json inválido: %v", target, err)
	}
	return resp.Data, resp.Meta.NextCursor
}

func errCodeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("envelope de error inválido: %v (body %s)", err, rec.Body.String())
	}
	return resp.Error.Code
}

func assertGeo(t *testing.T, raw json.RawMessage, wantType string) {
	t.Helper()
	var geo struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &geo); err != nil {
		t.Fatalf("geometría no es objeto GeoJSON: %v (%s)", err, raw)
	}
	if geo.Type != wantType || len(geo.Coordinates) == 0 {
		t.Fatalf("geometría inesperada type=%q (%s)", geo.Type, raw)
	}
}

func cashBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, acc uuid.UUID) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, acc).Scan(&bal); err != nil {
		t.Fatalf("saldo de caja de %s: %v", acc, err)
	}
	return bal
}

func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`).Scan(&bal); err != nil {
		t.Fatalf("saldo del sink: %v", err)
	}
	return bal
}

func accountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("cuenta %q: %v", name, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func firstActiveConcession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.land_concessions WHERE holder_account_id=$1 AND status='active' ORDER BY granted_at_sim, id LIMIT 1`, holder).Scan(&id); err != nil {
		t.Fatalf("concesión sembrada de %s: %v", holder, err)
	}
	return id
}

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldlandtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
