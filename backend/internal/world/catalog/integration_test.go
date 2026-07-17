package catalog_test

import (
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/catalog"
)

// TestCatalogIntegration ejercita los endpoints world/* de catálogo contra una
// BD real con el esquema migrado y el seed del Incremento 1 (región Askadia,
// productos iron_ore/coal, tipo warehouse). Sobre ese mundo mínimo el test
// siembra las piezas que el seed no crea (ciudades, demanda, yacimientos,
// tipos de edificio y recetas con ingredientes) y valida cada endpoint contra
// el servicio real vía httptest: filtros, cursores, geometrías GeoJSON planas y
// los 404 de recurso inexistente.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica el
// servidor: el test crea una BD EFÍMERA propia (el rol debe tener CREATEDB),
// le aplica las migraciones reales y la destruye al terminar.
func TestCatalogIntegration(t *testing.T) {
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
		// El test aporta sus propios fixtures de catálogo (iron_mine, recetas,
		// yacimiento, ciudad) con las mismas claves naturales que el mundo
		// industrial del seed: se omite este para no colisionar.
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	iron := productID(t, ctx, pool, "iron_ore")
	coal := productID(t, ctx, pool, "coal")
	region := regionID(t, ctx, pool, seed.RegionName)
	warehouseType := buildingTypeID(t, ctx, pool, "warehouse")

	fx := seedFixtures(t, ctx, pool, region, iron, coal)

	svc := catalog.NewService(pool, catalog.DefaultOptions())
	h := catalog.NewHandlers(svc, fakeMeta{}, logger)
	mux := http.NewServeMux()
	h.Register(mux)

	// ── Regiones ────────────────────────────────────────────────────────────
	t.Run("regiones: listado, filtro por bioma, detalle y geometría", func(t *testing.T) {
		regions, next := listOf[regionDTO](t, mux, "/world/regions")
		if len(regions) != 1 || regions[0].ID != region.String() || regions[0].Name != seed.RegionName {
			t.Fatalf("regiones sembradas inesperadas: %+v", regions)
		}
		if next != "" {
			t.Fatalf("next_cursor inesperado en una sola página: %q", next)
		}
		if regions[0].Biome != "plains" || regions[0].CanonBase == "" {
			t.Fatalf("región mal serializada: %+v", regions[0])
		}
		assertGeo(t, regions[0].Bounds, "Polygon")

		// Filtro por bioma: plains devuelve Askadia; desert, vacío.
		if got, _ := listOf[regionDTO](t, mux, "/world/regions?biome=plains"); len(got) != 1 {
			t.Fatalf("biome=plains: %d regiones, esperado 1", len(got))
		}
		if got, _ := listOf[regionDTO](t, mux, "/world/regions?biome=desert"); len(got) != 0 {
			t.Fatalf("biome=desert: %d regiones, esperado 0", len(got))
		}
		// Bioma inválido → 400.
		if code := errorCodeOf(t, mux, "/world/regions?biome=jungle", http.StatusBadRequest); code != httpx.CodeValidationError {
			t.Fatalf("biome inválido: code=%s", code)
		}

		// Detalle por id + geometría; id desconocido → 404.
		reg := objectOf[regionDTO](t, mux, "/world/regions/"+region.String())
		if reg.ID != region.String() {
			t.Fatalf("GetRegion devolvió %+v", reg)
		}
		assertGeo(t, reg.Bounds, "Polygon")
		errorCodeOf(t, mux, "/world/regions/"+uuid.Must(uuid.NewV7()).String(), http.StatusNotFound)
		// Path no-UUID → 404 (no resuelve a ninguna entidad).
		errorCodeOf(t, mux, "/world/regions/not-a-uuid", http.StatusNotFound)
	})

	// ── Productos ───────────────────────────────────────────────────────────
	t.Run("productos: listado, filtros class/is_fuel y paginación por cursor", func(t *testing.T) {
		products, _ := listOf[productDTO](t, mux, "/world/products")
		if len(products) != 2 {
			t.Fatalf("productos: %d, esperado 2", len(products))
		}
		// class=basic devuelve ambos; is_fuel=true, solo coal.
		if got, _ := listOf[productDTO](t, mux, "/world/products?class=basic"); len(got) != 2 {
			t.Fatalf("class=basic: %d, esperado 2", len(got))
		}
		fuels, _ := listOf[productDTO](t, mux, "/world/products?is_fuel=true")
		if len(fuels) != 1 || fuels[0].ID != coal.String() || !fuels[0].IsFuel {
			t.Fatalf("is_fuel=true inesperado: %+v", fuels)
		}
		if fuels[0].BasePrice == "" || fuels[0].PriceFloor == "" {
			t.Fatalf("dinero no serializado como string: %+v", fuels[0])
		}
		// class inválida → 400.
		errorCodeOf(t, mux, "/world/products?class=mythic", http.StatusBadRequest)

		// Paginación keyset: dos páginas de 1 cubren el conjunto sin repetir.
		seen := map[string]bool{}
		page, cursor := listOf[productDTO](t, mux, "/world/products?limit=1")
		if len(page) != 1 || cursor == "" {
			t.Fatalf("primera página limit=1 inesperada: len=%d cursor=%q", len(page), cursor)
		}
		seen[page[0].ID] = true
		page2, cursor2 := listOf[productDTO](t, mux, "/world/products?limit=1&cursor="+cursor)
		if len(page2) != 1 {
			t.Fatalf("segunda página: len=%d", len(page2))
		}
		seen[page2[0].ID] = true
		if cursor2 != "" {
			// Tercera página vacía y sin cursor.
			if last, c3 := listOf[productDTO](t, mux, "/world/products?limit=1&cursor="+cursor2); len(last) != 0 || c3 != "" {
				t.Fatalf("tercera página inesperada: len=%d cursor=%q", len(last), c3)
			}
		}
		if !seen[iron.String()] || !seen[coal.String()] {
			t.Fatalf("la paginación no cubrió ambos productos: %v", seen)
		}
		// Cursor de otra entidad/ilegible → 400.
		errorCodeOf(t, mux, "/world/products?cursor=not-a-cursor", http.StatusBadRequest)
	})

	// ── Tipos de edificio ─────────────────────────────────────────────────────
	t.Run("building-types: catálogo con JSONB de reglas/curva", func(t *testing.T) {
		types, _ := listOf[buildingTypeDTO](t, mux, "/world/building-types")
		// warehouse (seed) + iron_mine + blast_furnace (fixtures) = 3.
		if len(types) != 3 {
			t.Fatalf("building-types: %d, esperado 3", len(types))
		}
		byID := map[string]buildingTypeDTO{}
		for _, ty := range types {
			byID[ty.ID] = ty
		}
		mine := byID[fx.mineType.String()]
		if mine.Code != "iron_mine" || mine.BaseStorage == "" || mine.BuildCost == "" {
			t.Fatalf("iron_mine mal serializado: %+v", mine)
		}
		// placement_rules es un objeto JSON, no un string escapado.
		var rules map[string]any
		if err := json.Unmarshal(mine.PlacementRules, &rules); err != nil {
			t.Fatalf("placement_rules no es objeto JSON: %v (%s)", err, mine.PlacementRules)
		}
		if rules["near_resource"] != "iron_ore" {
			t.Fatalf("placement_rules perdió datos: %v", rules)
		}
		// warehouse: placement_rules por defecto '{}'.
		wh := byID[warehouseType.String()]
		if string(wh.PlacementRules) != "{}" {
			t.Fatalf("warehouse placement_rules esperado {}: %s", wh.PlacementRules)
		}
	})

	// ── Recetas ───────────────────────────────────────────────────────────────
	t.Run("recetas: filtros e ingredientes con role/quantity", func(t *testing.T) {
		recipes, _ := listOf[recipeDTO](t, mux, "/world/recipes")
		if len(recipes) != 2 {
			t.Fatalf("recetas: %d, esperado 2", len(recipes))
		}
		// Filtro por tipo de edificio: iron_mine → solo extract_iron_ore.
		mineRecipes, _ := listOf[recipeDTO](t, mux, "/world/recipes?building_type_id="+fx.mineType.String())
		if len(mineRecipes) != 1 || mineRecipes[0].Code != "extract_iron_ore" {
			t.Fatalf("recetas de iron_mine inesperadas: %+v", mineRecipes)
		}
		extract := mineRecipes[0]
		if extract.FuelProductID != coal.String() || extract.FuelPerBatch == "" {
			t.Fatalf("combustible de la receta mal serializado: %+v", extract)
		}
		if len(extract.Ingredients) != 1 || extract.Ingredients[0].Role != "output" ||
			extract.Ingredients[0].ProductID != iron.String() || extract.Ingredients[0].Quantity != "500" {
			t.Fatalf("ingredientes de extract_iron_ore inesperados: %+v", extract.Ingredients)
		}

		// Filtro por producto: coal aparece como INGREDIENTE solo en burn_coal
		// (en extract_iron_ore es combustible, no ingrediente).
		coalRecipes, _ := listOf[recipeDTO](t, mux, "/world/recipes?product_id="+coal.String())
		if len(coalRecipes) != 1 || coalRecipes[0].Code != "burn_coal" {
			t.Fatalf("recetas con coal como ingrediente inesperadas: %+v", coalRecipes)
		}
		// iron_ore aparece en ambas (output de una, output de la otra).
		if got, _ := listOf[recipeDTO](t, mux, "/world/recipes?product_id="+iron.String()); len(got) != 2 {
			t.Fatalf("recetas con iron_ore: %d, esperado 2", len(got))
		}
	})

	// ── Yacimientos ────────────────────────────────────────────────────────────
	t.Run("resource-deposits: only_available, filtros y geometría", func(t *testing.T) {
		// Default only_available=true → solo el yacimiento con remaining > 0.
		avail, _ := listOf[depositDTO](t, mux, "/world/resource-deposits")
		if len(avail) != 1 || avail[0].ID != fx.depositAvail.String() {
			t.Fatalf("yacimientos disponibles inesperados: %+v", avail)
		}
		assertGeo(t, avail[0].Location, "Point")
		if avail[0].RemainingAmount == "" || avail[0].InitialAmount == "" {
			t.Fatalf("stock del yacimiento no serializado como string: %+v", avail[0])
		}
		// only_available=false incluye el agotado.
		if all, _ := listOf[depositDTO](t, mux, "/world/resource-deposits?only_available=false"); len(all) != 2 {
			t.Fatalf("only_available=false: %d, esperado 2", len(all))
		}
		// Filtro por producto: coal solo tiene el agotado.
		if got, _ := listOf[depositDTO](t, mux, "/world/resource-deposits?product_id="+coal.String()+"&only_available=false"); len(got) != 1 {
			t.Fatalf("depósitos de coal: %d, esperado 1", len(got))
		}
		// Filtro por región (Askadia) + disponibles.
		if got, _ := listOf[depositDTO](t, mux, "/world/resource-deposits?region_id="+region.String()); len(got) != 1 {
			t.Fatalf("depósitos disponibles de Askadia: %d, esperado 1", len(got))
		}
	})

	// ── Ciudades ────────────────────────────────────────────────────────────────
	t.Run("cities: listado, filtros, detalle y demanda", func(t *testing.T) {
		cities, _ := listOf[cityDTO](t, mux, "/world/cities")
		if len(cities) != 1 || cities[0].ID != fx.city.String() {
			t.Fatalf("ciudades inesperadas: %+v", cities)
		}
		assertGeo(t, cities[0].Location, "Point")
		if cities[0].BaseSalary == "" || cities[0].SupplyIndex <= 0 {
			t.Fatalf("ciudad mal serializada: %+v", cities[0])
		}
		// Filtro por región y por nivel mínimo.
		if got, _ := listOf[cityDTO](t, mux, "/world/cities?region_id="+region.String()); len(got) != 1 {
			t.Fatalf("cities?region_id: %d, esperado 1", len(got))
		}
		if got, _ := listOf[cityDTO](t, mux, "/world/cities?min_level=5"); len(got) != 0 {
			t.Fatalf("min_level=5: %d, esperado 0 (la ciudad es nivel 3)", len(got))
		}
		if got, _ := listOf[cityDTO](t, mux, "/world/cities?min_level=3"); len(got) != 1 {
			t.Fatalf("min_level=3: %d, esperado 1", len(got))
		}

		// Detalle por id; id desconocido → 404.
		c := objectOf[cityDTO](t, mux, "/world/cities/"+fx.city.String())
		if c.ID != fx.city.String() || c.Level != 3 {
			t.Fatalf("GetCity devolvió %+v", c)
		}
		errorCodeOf(t, mux, "/world/cities/"+uuid.Must(uuid.NewV7()).String(), http.StatusNotFound)

		// Demanda: dos filas; filtro por producto → una; ciudad inexistente → 404.
		demand, _ := listOf[cityDemandDTO](t, mux, "/world/cities/"+fx.city.String()+"/demand")
		if len(demand) != 2 {
			t.Fatalf("demanda: %d filas, esperado 2", len(demand))
		}
		for _, d := range demand {
			if d.D0PerSimDay == "" || d.CurrentPrice == "" || d.SaturationFactor <= 0 {
				t.Fatalf("fila de demanda mal serializada: %+v", d)
			}
		}
		one, _ := listOf[cityDemandDTO](t, mux, "/world/cities/"+fx.city.String()+"/demand?product_id="+iron.String())
		if len(one) != 1 || one[0].ProductID != iron.String() {
			t.Fatalf("demanda filtrada por producto inesperada: %+v", one)
		}
		errorCodeOf(t, mux, "/world/cities/"+uuid.Must(uuid.NewV7()).String()+"/demand", http.StatusNotFound)
	})
}

// ─── Fixtures del test (lo que el seed del Incremento 1 no crea) ──────────────

type fixtures struct {
	city         uuid.UUID
	mineType     uuid.UUID
	furnaceType  uuid.UUID
	depositAvail uuid.UUID
	depositEmpty uuid.UUID
}

func seedFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, iron, coal uuid.UUID) fixtures {
	t.Helper()
	fx := fixtures{
		city:         uuid.Must(uuid.NewV7()),
		mineType:     uuid.Must(uuid.NewV7()),
		furnaceType:  uuid.Must(uuid.NewV7()),
		depositAvail: uuid.Must(uuid.NewV7()),
		depositEmpty: uuid.Must(uuid.NewV7()),
	}

	// Ciudad: cuenta de sistema kind city + fila de ciudad + dos filas de demanda.
	cityAccount := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO auth.accounts (id, kind, name) VALUES ($1, 'city'::auth.account_kind, $2)`,
		cityAccount, "Ciudad Prueba")
	exec(t, ctx, pool, `
		INSERT INTO world.cities
		       (id, region_id, account_id, name, location, level, population,
		        supply_index, influence_radius_m, base_salary)
		VALUES ($1, $2, $3, $4, ST_GeomFromText('POINT(12000 12000)', 0), 3, 25000,
		        1.5, 8000, 120)`,
		fx.city, region, cityAccount, "Ciudad Prueba")
	exec(t, ctx, pool, `
		INSERT INTO world.city_demand
		       (city_id, product_id, d0_per_sim_day, supply_ema, saturation_factor,
		        current_price, unlocked_at_level, updated_at_sim)
		VALUES ($1, $2, 400, 100, 1.2, 110, 1, 7200),
		       ($1, $3, 250, 80, 0.9, 55, 1, 7200)`,
		fx.city, iron, coal)

	// Tipos de edificio: mina (con placement_rules JSON) y horno.
	exec(t, ctx, pool, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, 'iron_mine', 'Mina de hierro', 4, 4, 50000,
		        '{"near_resource":"iron_ore","max_distance_m":5000}'::jsonb,
		        '{"lines":[1,2,4,8]}'::jsonb, 80000, 100)`,
		fx.mineType)
	exec(t, ctx, pool, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, 'blast_furnace', 'Alto horno', 6, 4, 40000,
		        '{}'::jsonb, '{}'::jsonb, 120000, 200)`,
		fx.furnaceType)

	// Recetas: extract_iron_ore (mina, combustible=coal, output iron_ore) y
	// burn_coal (horno, input coal, output iron_ore).
	extract := uuid.Must(uuid.NewV7())
	burn := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `
		INSERT INTO world.recipes
		       (id, building_type_id, code, name, batch_sim_seconds,
		        fuel_product_id, fuel_per_batch, workers_required, min_city_level, changeover_seconds)
		VALUES ($1, $2, 'extract_iron_ore', 'Extracción de mineral de hierro', 3600,
		        $3, 2, 5, 1, 0)`,
		extract, fx.mineType, coal)
	exec(t, ctx, pool, `
		INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity)
		VALUES ($1, $2, 'output'::world.ingredient_role, 500)`,
		extract, iron)
	exec(t, ctx, pool, `
		INSERT INTO world.recipes
		       (id, building_type_id, code, name, batch_sim_seconds,
		        fuel_product_id, fuel_per_batch, workers_required, min_city_level, changeover_seconds)
		VALUES ($1, $2, 'burn_coal', 'Fundición con carbón', 1800,
		        $3, 1, 8, 1, 0)`,
		burn, fx.furnaceType, coal)
	exec(t, ctx, pool, `
		INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity)
		VALUES ($1, $2, 'input'::world.ingredient_role, 10),
		       ($1, $3, 'output'::world.ingredient_role, 5)`,
		burn, coal, iron)

	// Yacimientos: uno de iron_ore disponible, uno de coal agotado.
	exec(t, ctx, pool, `
		INSERT INTO world.resource_deposits
		       (id, region_id, product_id, location, initial_amount, remaining_amount,
		        renewable, regen_per_sim_day)
		VALUES ($1, $2, $3, ST_GeomFromText('POINT(11000 11000)', 0), 1000000, 1000000, false, 0)`,
		fx.depositAvail, region, iron)
	exec(t, ctx, pool, `
		INSERT INTO world.resource_deposits
		       (id, region_id, product_id, location, initial_amount, remaining_amount,
		        renewable, regen_per_sim_day)
		VALUES ($1, $2, $3, ST_GeomFromText('POINT(13000 13000)', 0), 500000, 0, false, 0)`,
		fx.depositEmpty, region, coal)

	return fx
}

// ─── DTOs del contrato para las aserciones ───────────────────────────────────

type regionDTO struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	GridX         int32           `json:"grid_x"`
	GridY         int32           `json:"grid_y"`
	Bounds        json.RawMessage `json:"bounds"`
	Biome         string          `json:"biome"`
	CanonBase     string          `json:"canon_base"`
	TaxRateBp     int32           `json:"tax_rate_bp"`
	CustomsRateBp int32           `json:"customs_rate_bp"`
	OpenedAtSim   int64           `json:"opened_at_sim"`
}

type productDTO struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	Class      string `json:"class"`
	BasePrice  string `json:"base_price"`
	PriceFloor string `json:"price_floor"`
	IsFuel     bool   `json:"is_fuel"`
}

type buildingTypeDTO struct {
	ID             string          `json:"id"`
	Code           string          `json:"code"`
	BaseStorage    string          `json:"base_storage"`
	BuildCost      string          `json:"build_cost"`
	PlacementRules json.RawMessage `json:"placement_rules"`
	LevelCurve     json.RawMessage `json:"level_curve"`
}

type recipeIngredientDTO struct {
	ProductID string `json:"product_id"`
	Role      string `json:"role"`
	Quantity  string `json:"quantity"`
}

type recipeDTO struct {
	ID             string                `json:"id"`
	BuildingTypeID string                `json:"building_type_id"`
	Code           string                `json:"code"`
	FuelProductID  string                `json:"fuel_product_id"`
	FuelPerBatch   string                `json:"fuel_per_batch"`
	Ingredients    []recipeIngredientDTO `json:"ingredients"`
}

type depositDTO struct {
	ID              string          `json:"id"`
	RegionID        string          `json:"region_id"`
	ProductID       string          `json:"product_id"`
	Location        json.RawMessage `json:"location"`
	InitialAmount   string          `json:"initial_amount"`
	RemainingAmount string          `json:"remaining_amount"`
}

type cityDTO struct {
	ID          string          `json:"id"`
	RegionID    string          `json:"region_id"`
	Location    json.RawMessage `json:"location"`
	Level       int32           `json:"level"`
	Population  int64           `json:"population"`
	SupplyIndex float64         `json:"supply_index"`
	BaseSalary  string          `json:"base_salary"`
}

type cityDemandDTO struct {
	CityID           string  `json:"city_id"`
	ProductID        string  `json:"product_id"`
	D0PerSimDay      string  `json:"d0_per_sim_day"`
	SaturationFactor float64 `json:"saturation_factor"`
	CurrentPrice     string  `json:"current_price"`
}

// ─── Helpers HTTP del test ───────────────────────────────────────────────────

type fakeMeta struct{}

func (fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(7200), SimTimeSeconds: 7200, ServerTime: time.Now().UTC()}
}

func doGet(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// listOf decodifica el sobre {data:[...], meta:{next_cursor}} en []T.
func listOf[T any](t *testing.T, mux *http.ServeMux, target string) ([]T, string) {
	t.Helper()
	rec := doGet(t, mux, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body: %s)", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []T `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: json inválido: %v (body: %s)", target, err, rec.Body.String())
	}
	return resp.Data, resp.Meta.NextCursor
}

// objectOf decodifica el sobre {data:{...}} en T.
func objectOf[T any](t *testing.T, mux *http.ServeMux, target string) T {
	t.Helper()
	rec := doGet(t, mux, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body: %s)", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: json inválido: %v (body: %s)", target, err, rec.Body.String())
	}
	return resp.Data
}

// errorCodeOf comprueba el status esperado y devuelve el code del envelope de
// error.
func errorCodeOf(t *testing.T, mux *http.ServeMux, target string, wantStatus int) string {
	t.Helper()
	rec := doGet(t, mux, target)
	if rec.Code != wantStatus {
		t.Fatalf("GET %s: status %d, esperado %d (body: %s)", target, rec.Code, wantStatus, rec.Body.String())
	}
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: envelope de error inválido: %v (body: %s)", target, err, rec.Body.String())
	}
	return resp.Error.Code
}

// assertGeo valida que raw es un objeto GeoJSON del tipo esperado con
// coordenadas planas presentes.
func assertGeo(t *testing.T, raw json.RawMessage, wantType string) {
	t.Helper()
	var geo struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &geo); err != nil {
		t.Fatalf("geometría no es objeto GeoJSON: %v (%s)", err, raw)
	}
	if geo.Type != wantType {
		t.Fatalf("geometría type=%q, esperado %q (%s)", geo.Type, wantType, raw)
	}
	if len(geo.Coordinates) == 0 {
		t.Fatalf("geometría sin coordinates: %s", raw)
	}
}

// ─── Infraestructura del test (mismo patrón que ledger/contracts/market) ─────

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldcatalogtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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

func exec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func productID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("producto %q: %v", code, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func buildingTypeID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("tipo de edificio %q: %v", code, err)
	}
	return id
}
