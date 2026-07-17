package buildings_test

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
	"github.com/lokiteitor/global-market/backend/internal/world/buildings"
)

const testSimNow int64 = 7200

// TestBuildingsIntegration ejercita world/buildings* contra una BD real con el
// esquema migrado y el seed del Incremento 1, más las piezas que el seed no crea
// (tipos mina/horno con placement_rules y level_curve, recetas, un yacimiento,
// una ciudad y concesiones libres de Demo). Valida la construcción feliz (nodo
// creado, coste al sink), PLACEMENT_INVALID por cada regla, la mejora con coste
// no lineal, el cambio de receta inválido y el inventario.
func TestBuildingsIntegration(t *testing.T) {
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
		// El test aporta sus propios fixtures industriales (iron_mine, recetas,
		// yacimiento, ciudad) con las mismas claves naturales que el mundo
		// industrial del seed: se omite este para no colisionar.
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	norte := accountID(t, ctx, pool, seed.DefaultTraderName)
	region := regionID(t, ctx, pool, seed.RegionName)
	iron := productID(t, ctx, pool, "iron_ore")
	fx := seedFixtures(t, ctx, pool, region, demo, iron)

	svc, err := buildings.NewService(pool, fakeSim{testSimNow}, buildings.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, logger)
	norteMux := newMux(svc, norte, logger)

	var buildingA string

	// ── Construcción feliz: nodo mine + coste al sink ─────────────────────────
	t.Run("construcción feliz crea nodo y cobra al sink", func(t *testing.T) {
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		footprint := polygon(20000, 20000, 20200, 20200) // dentro de Demo1, junto al yacimiento
		rec := do(t, demoMux, http.MethodPost, "/world/buildings",
			fmt.Sprintf(`{"building_type_id":%q,"concession_id":%q,"footprint":%s}`, fx.mineType, fx.concession1, footprint))
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST building: status %d (body %s)", rec.Code, rec.Body.String())
		}
		b := dataOf[buildingDTO](t, rec)
		buildingA = b.ID
		if b.Status != "under_construction" || b.Level != 1 || b.ConditionPct != 100 {
			t.Fatalf("edificio recién creado inesperado: %+v", b)
		}
		if b.OwnerAccountID != demo.String() || b.ConcessionID != fx.concession1.String() {
			t.Fatalf("propiedad/concesión inesperadas: %+v", b)
		}
		assertGeo(t, b.Footprint, "Polygon")

		// Nodo del grafo ligado, kind mine, en el centroide.
		var kind string
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM world.network_nodes WHERE building_id=$1`, uuid.MustParse(b.ID)).Scan(&count); err != nil {
			t.Fatalf("contando nodos: %v", err)
		}
		if count != 1 {
			t.Fatalf("se esperaba 1 nodo ligado, hay %d", count)
		}
		if err := pool.QueryRow(ctx, `SELECT kind::text FROM world.network_nodes WHERE building_id=$1`, uuid.MustParse(b.ID)).Scan(&kind); err != nil {
			t.Fatalf("kind del nodo: %v", err)
		}
		if kind != "mine" {
			t.Fatalf("kind del nodo = %q, esperado mine", kind)
		}
		// Coste al sink (build_cost 80000).
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != 80000 {
			t.Fatalf("la caja de Demo cayó %d, esperado 80000 (build_cost)", d)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != 80000 {
			t.Fatalf("el sink subió %d, esperado 80000", d)
		}
	})

	// ── PLACEMENT_INVALID: footprint fuera de la parcela ──────────────────────
	t.Run("placement: fuera de la parcela", func(t *testing.T) {
		footprint := polygon(25000, 25000, 25100, 25100) // fuera de Demo1 (18000..22000)
		rec := do(t, demoMux, http.MethodPost, "/world/buildings",
			fmt.Sprintf(`{"building_type_id":%q,"concession_id":%q,"footprint":%s}`, fx.mineType, fx.concession1, footprint))
		assertPlacement(t, rec, "footprint_within_parcel")
	})

	// ── PLACEMENT_INVALID: solape con edificio existente ──────────────────────
	t.Run("placement: solape con edificio", func(t *testing.T) {
		footprint := polygon(20100, 20100, 20300, 20300) // solapa con el edificio A, dentro de Demo1
		rec := do(t, demoMux, http.MethodPost, "/world/buildings",
			fmt.Sprintf(`{"building_type_id":%q,"concession_id":%q,"footprint":%s}`, fx.mineType, fx.concession1, footprint))
		assertPlacement(t, rec, "footprint_overlap")
	})

	// ── PLACEMENT_INVALID: mina sin yacimiento cercano ────────────────────────
	t.Run("placement: sin recurso cercano para la mina", func(t *testing.T) {
		footprint := polygon(40000, 40000, 40100, 40100) // dentro de Demo2, lejos del yacimiento
		rec := do(t, demoMux, http.MethodPost, "/world/buildings",
			fmt.Sprintf(`{"building_type_id":%q,"concession_id":%q,"footprint":%s}`, fx.mineType, fx.concession2, footprint))
		assertPlacement(t, rec, "near_resource")
	})

	// ── PLACEMENT_INVALID: concesión ajena → 403 ──────────────────────────────
	t.Run("construir sobre concesión ajena devuelve 403", func(t *testing.T) {
		footprint := polygon(20400, 20400, 20500, 20500)
		rec := do(t, norteMux, http.MethodPost, "/world/buildings",
			fmt.Sprintf(`{"building_type_id":%q,"concession_id":%q,"footprint":%s}`, fx.mineType, fx.concession1, footprint))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("construir sobre concesión ajena: status %d, esperado 403 (body %s)", rec.Code, rec.Body.String())
		}
	})

	// ── Detalle/propiedad: propio 200, ajeno 403 ──────────────────────────────
	t.Run("detalle propio y 403 ajeno", func(t *testing.T) {
		if rec := do(t, demoMux, http.MethodGet, "/world/buildings/"+buildingA, ""); rec.Code != http.StatusOK {
			t.Fatalf("GET propio: status %d", rec.Code)
		}
		rec := do(t, norteMux, http.MethodGet, "/world/buildings/"+buildingA, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("GET ajeno: status %d, esperado 403", rec.Code)
		}
		list, _ := listOf[buildingDTO](t, demoMux, "/world/buildings")
		for _, b := range list {
			if b.OwnerAccountID != demo.String() {
				t.Fatalf("el listado incluyó un edificio ajeno: %+v", b)
			}
		}
	})

	// ── Mejora con coste no lineal ────────────────────────────────────────────
	t.Run("mejora sube nivel con coste no lineal", func(t *testing.T) {
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		rec := do(t, demoMux, http.MethodPost, "/world/buildings/"+buildingA+"/upgrade", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("upgrade: status %d (body %s)", rec.Code, rec.Body.String())
		}
		b := dataOf[buildingDTO](t, rec)
		if b.Level != 2 {
			t.Fatalf("nivel = %d, esperado 2", b.Level)
		}
		// cost = build_cost(80000) * upgrade_cost_factor[nivel destino 2] = 80000*2.
		const wantCost int64 = 160000
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != wantCost {
			t.Fatalf("la caja cayó %d en la mejora, esperado %d", d, wantCost)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != wantCost {
			t.Fatalf("el sink subió %d en la mejora, esperado %d", d, wantCost)
		}
	})

	// ── Cambio de receta: inválida (otro tipo) → 422; válida → 200 ────────────
	t.Run("cambiar receta valida el tipo y el nivel de ciudad", func(t *testing.T) {
		// Receta de un tipo distinto (horno) sobre una mina → 422.
		rec := do(t, demoMux, http.MethodPatch, "/world/buildings/"+buildingA,
			fmt.Sprintf(`{"active_recipe_id":%q}`, fx.burnRecipe))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("receta de otro tipo: status %d, esperado 422 (body %s)", rec.Code, rec.Body.String())
		}
		if code := errCodeOf(t, rec); code != httpx.CodeValidationError {
			t.Fatalf("code %q, esperado VALIDATION_ERROR", code)
		}
		// Receta que exige nivel de ciudad 5 (la más cercana es nivel 3) → 422.
		rec = do(t, demoMux, http.MethodPatch, "/world/buildings/"+buildingA,
			fmt.Sprintf(`{"active_recipe_id":%q}`, fx.deepRecipe))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("receta con min_city_level alto: status %d, esperado 422 (body %s)", rec.Code, rec.Body.String())
		}
		// Receta válida del tipo mina, min_city_level alcanzado → 200.
		rec = do(t, demoMux, http.MethodPatch, "/world/buildings/"+buildingA,
			fmt.Sprintf(`{"active_recipe_id":%q}`, fx.extractRecipe))
		if rec.Code != http.StatusOK {
			t.Fatalf("receta válida: status %d (body %s)", rec.Code, rec.Body.String())
		}
		if b := dataOf[buildingDTO](t, rec); b.ActiveRecipeID != fx.extractRecipe.String() {
			t.Fatalf("active_recipe_id no fijado: %+v", b)
		}
		// null detiene la línea.
		rec = do(t, demoMux, http.MethodPatch, "/world/buildings/"+buildingA, `{"active_recipe_id":null}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("detener línea: status %d (body %s)", rec.Code, rec.Body.String())
		}
		if b := dataOf[buildingDTO](t, rec); b.ActiveRecipeID != "" {
			t.Fatalf("la línea no se detuvo: %+v", b)
		}
	})

	// ── Mantenimiento: start_maintenance → in_maintenance ─────────────────────
	t.Run("start_maintenance pasa a in_maintenance", func(t *testing.T) {
		rec := do(t, demoMux, http.MethodPatch, "/world/buildings/"+buildingA, `{"start_maintenance":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("start_maintenance: status %d (body %s)", rec.Code, rec.Body.String())
		}
		if b := dataOf[buildingDTO](t, rec); b.Status != "in_maintenance" {
			t.Fatalf("estado = %q, esperado in_maintenance", b.Status)
		}
	})

	// ── Inventario físico ─────────────────────────────────────────────────────
	t.Run("inventario físico del edificio", func(t *testing.T) {
		// Vacío al principio.
		items, _ := listOf[inventoryDTO](t, demoMux, "/world/buildings/"+buildingA+"/inventory")
		if len(items) != 0 {
			t.Fatalf("inventario inicial no vacío: %+v", items)
		}
		// Se siembra una fila física y aparece.
		if _, err := pool.Exec(ctx, `INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim) VALUES ($1,$2,$3,$4)`,
			uuid.MustParse(buildingA), iron, int64(500), testSimNow); err != nil {
			t.Fatalf("sembrando inventario: %v", err)
		}
		items, _ = listOf[inventoryDTO](t, demoMux, "/world/buildings/"+buildingA+"/inventory")
		if len(items) != 1 || items[0].ProductID != iron.String() || items[0].Quantity != "500" {
			t.Fatalf("inventario inesperado: %+v", items)
		}
		// Inventario ajeno → 403.
		if rec := do(t, norteMux, http.MethodGet, "/world/buildings/"+buildingA+"/inventory", ""); rec.Code != http.StatusForbidden {
			t.Fatalf("inventario ajeno: status %d, esperado 403", rec.Code)
		}
	})
}

// ─── Fixtures (lo que el seed no crea) ────────────────────────────────────────

type fixtures struct {
	mineType      uuid.UUID
	furnaceType   uuid.UUID
	extractRecipe uuid.UUID
	burnRecipe    uuid.UUID
	deepRecipe    uuid.UUID
	concession1   uuid.UUID // libre, junto al yacimiento (18000..22000)
	concession2   uuid.UUID // libre, lejos del yacimiento (39000..41000)
}

func seedFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, demo, iron uuid.UUID) fixtures {
	t.Helper()
	fx := fixtures{
		mineType:      uuid.Must(uuid.NewV7()),
		furnaceType:   uuid.Must(uuid.NewV7()),
		extractRecipe: uuid.Must(uuid.NewV7()),
		burnRecipe:    uuid.Must(uuid.NewV7()),
		deepRecipe:    uuid.Must(uuid.NewV7()),
		concession1:   uuid.Must(uuid.NewV7()),
		concession2:   uuid.Must(uuid.NewV7()),
	}
	exec(t, ctx, pool, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, 'iron_mine', 'Mina de hierro', 4, 4, 50000,
		        '{"near_resource":"iron_ore","max_distance_m":5000}'::jsonb,
		        '{"upgrade_cost_factor":[1,2,4,8]}'::jsonb, 80000, 100)`, fx.mineType)
	exec(t, ctx, pool, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, 'blast_furnace', 'Alto horno', 6, 4, 40000,
		        '{}'::jsonb, '{}'::jsonb, 120000, 200)`, fx.furnaceType)

	// Recetas: extract_iron_ore (mina, nivel 1), deep (mina, nivel 5),
	// burn_coal (horno, nivel 1).
	exec(t, ctx, pool, `
		INSERT INTO world.recipes (id, building_type_id, code, name, batch_sim_seconds, workers_required, min_city_level)
		VALUES ($1, $2, 'extract_iron_ore', 'Extracción de hierro', 3600, 5, 1)`, fx.extractRecipe, fx.mineType)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1, $2, 'output', 500)`, fx.extractRecipe, iron)
	exec(t, ctx, pool, `
		INSERT INTO world.recipes (id, building_type_id, code, name, batch_sim_seconds, workers_required, min_city_level)
		VALUES ($1, $2, 'deep_extract', 'Extracción profunda', 3600, 8, 5)`, fx.deepRecipe, fx.mineType)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1, $2, 'output', 800)`, fx.deepRecipe, iron)
	exec(t, ctx, pool, `
		INSERT INTO world.recipes (id, building_type_id, code, name, batch_sim_seconds, workers_required, min_city_level)
		VALUES ($1, $2, 'burn_coal', 'Fundición', 1800, 8, 1)`, fx.burnRecipe, fx.furnaceType)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1, $2, 'output', 5)`, fx.burnRecipe, iron)

	// Yacimiento de iron_ore junto a la concesión 1.
	exec(t, ctx, pool, `
		INSERT INTO world.resource_deposits (id, region_id, product_id, location, initial_amount, remaining_amount, renewable, regen_per_sim_day)
		VALUES ($1, $2, $3, ST_GeomFromText('POINT(20000 20000)', 0), 1000000, 1000000, false, 0)`,
		uuid.Must(uuid.NewV7()), region, iron)

	// Ciudad nivel 3 (cualificación laboral de recetas).
	cityAccount := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO auth.accounts (id, kind, name) VALUES ($1, 'city'::auth.account_kind, $2)`, cityAccount, "Ciudad Prueba")
	exec(t, ctx, pool, `
		INSERT INTO world.cities (id, region_id, account_id, name, location, level, population, supply_index, influence_radius_m, base_salary)
		VALUES ($1, $2, $3, 'Ciudad Prueba', ST_GeomFromText('POINT(20000 20000)', 0), 3, 25000, 1.5, 8000, 120)`,
		uuid.Must(uuid.NewV7()), region, cityAccount)

	// Concesiones libres de Demo (parcelas amplias, sin edificios).
	insertConcession(t, ctx, pool, fx.concession1, region, demo, 18000, 18000, 22000, 22000)
	insertConcession(t, ctx, pool, fx.concession2, region, demo, 39000, 39000, 41000, 41000)
	return fx
}

func insertConcession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id, region, holder uuid.UUID, minX, minY, maxX, maxY int) {
	t.Helper()
	wkt := fmt.Sprintf("POLYGON((%d %d,%d %d,%d %d,%d %d,%d %d))", minX, minY, maxX, minY, maxX, maxY, minX, maxY, minX, minY)
	exec(t, ctx, pool, `
		INSERT INTO world.land_concessions (id, region_id, holder_account_id, parcel, canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), 1000, 90, $5, 'active', 0)`,
		id, region, holder, wkt, 90*simtime.SimDay)
}

// ─── DTOs ─────────────────────────────────────────────────────────────────────

type buildingDTO struct {
	ID             string          `json:"id"`
	OwnerAccountID string          `json:"owner_account_id"`
	RegionID       string          `json:"region_id"`
	ConcessionID   string          `json:"concession_id"`
	BuildingTypeID string          `json:"building_type_id"`
	Footprint      json.RawMessage `json:"footprint"`
	Level          int32           `json:"level"`
	Status         string          `json:"status"`
	ActiveRecipeID string          `json:"active_recipe_id"`
	ConditionPct   int32           `json:"condition_pct"`
	FuelStock      string          `json:"fuel_stock"`
}

type inventoryDTO struct {
	BuildingID string `json:"building_id"`
	ProductID  string `json:"product_id"`
	Quantity   string `json:"quantity"`
}

// ─── Infraestructura del test ─────────────────────────────────────────────────

type fakeSim struct{ now int64 }

func (f fakeSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(f.now) }

type fakeMeta struct{}

func (fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(simtime.SimTime(testSimNow)), SimTimeSeconds: testSimNow, ServerTime: time.Now().UTC()}
}

type fakeIdentity struct{ acc uuid.UUID }

func (i fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return i.acc, true }

func newMux(svc *buildings.Service, acc uuid.UUID, logger *slog.Logger) *http.ServeMux {
	h := buildings.NewHandlers(svc, fakeIdentity{acc}, fakeMeta{}, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

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

// assertPlacement comprueba 422 PLACEMENT_INVALID con la regla esperada en
// details.rule.
func assertPlacement(t *testing.T, rec *httptest.ResponseRecorder, wantRule string) {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, esperado 422 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("envelope inválido: %v", err)
	}
	if resp.Error.Code != "PLACEMENT_INVALID" {
		t.Fatalf("code %q, esperado PLACEMENT_INVALID (body %s)", resp.Error.Code, rec.Body.String())
	}
	if got, _ := resp.Error.Details["rule"].(string); got != wantRule {
		t.Fatalf("rule %q, esperado %q (body %s)", got, wantRule, rec.Body.String())
	}
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

func productID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatalf("producto %q: %v", code, err)
	}
	return id
}

func exec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldbuildingstest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
