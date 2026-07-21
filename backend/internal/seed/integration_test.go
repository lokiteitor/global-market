package seed_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
)

// TestSeedIntegration ejercita el seed completo contra una BD real con el
// esquema migrado: dos ejecuciones (la segunda 100% no-op: mismas filas y
// mismos IDs por clave natural), contenido del mundo mínimo del Incremento 1
// y coherencia física↔contable del stock inicial (ADR-022): inventario físico
// == saldo stock_free por (corp, producto) y world_source negativo == suma
// emitida.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales de db/migrations y la
// destruye al terminar (mismo patrón que platform/migrate y ledger).
func TestSeedIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := seed.Options{
		DemoName:     seed.DefaultDemoName,
		DemoSecret:   "demo-secret-test",
		TraderName:   seed.DefaultTraderName,
		TraderSecret: "norte-secret-test",
		Ledger:       ledger.DefaultOptions(),
	}

	if err := seed.Run(ctx, pool, opts, logger); err != nil {
		t.Fatalf("seed (1ª ejecución): %v", err)
	}

	first := snapshot(t, ctx, pool)
	assertWorldContent(t, ctx, pool)
	assertCoalChain(t, ctx, pool)
	assertCoherence(t, ctx, pool)

	if err := seed.Run(ctx, pool, opts, logger); err != nil {
		t.Fatalf("seed (2ª ejecución): %v", err)
	}
	second := snapshot(t, ctx, pool)

	if !maps.Equal(first.counts, second.counts) {
		t.Fatalf("la segunda ejecución no fue no-op: filas antes %v, después %v",
			first.counts, second.counts)
	}
	if !maps.Equal(first.ids, second.ids) {
		t.Fatalf("IDs inestables entre ejecuciones: antes %v, después %v",
			first.ids, second.ids)
	}
	assertCoalChain(t, ctx, pool)
	assertCoherence(t, ctx, pool)
}

// assertCoalChain valida la cadena del carbón del Incremento 4 (los bots deben
// poder producir su propio combustible): el tipo coal_mine con su regla
// near_resource, la receta mine_coal SIN combustible (decisión de arranque v1:
// extraer carbón no consume carbón) con salida coal 60, y el yacimiento de coal
// en suelo libre de Askadia, dentro de la región y lejos de toda concesión
// sembrada (un bot puede tomar una parcela libre a <8000 m).
func assertCoalChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// Tipo coal_mine: catálogo, placement_rules y curva de nivel de iron_mine.
	var (
		footprintCells, maxLevel            int
		baseStorage, buildCost, maintenance int64
		nearResource                        string
		maxDistanceM                        float64
		sameCurveAsIronMine                 bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT footprint_cells, max_level, base_storage, build_cost, maintenance_cost,
		       placement_rules->>'near_resource',
		       (placement_rules->>'max_distance_m')::float8,
		       level_curve = (SELECT level_curve FROM world.building_types WHERE code = 'iron_mine')
		  FROM world.building_types WHERE code = 'coal_mine'`).
		Scan(&footprintCells, &maxLevel, &baseStorage, &buildCost, &maintenance,
			&nearResource, &maxDistanceM, &sameCurveAsIronMine); err != nil {
		t.Fatalf("building_type coal_mine: %v", err)
	}
	if footprintCells != 4 || maxLevel != 4 || baseStorage != 50_000 ||
		buildCost != 60_000 || maintenance != 80 {
		t.Fatalf("coal_mine inesperada: cells=%d maxLevel=%d storage=%d build=%d maint=%d",
			footprintCells, maxLevel, baseStorage, buildCost, maintenance)
	}
	if nearResource != "coal" || maxDistanceM != 8_000 || !sameCurveAsIronMine {
		t.Fatalf("reglas de coal_mine inesperadas: near_resource=%s max_distance_m=%f curvaComoIronMine=%v",
			nearResource, maxDistanceM, sameCurveAsIronMine)
	}

	// Receta mine_coal: sin combustible, 3 trabajadores, salida coal 60 y sin
	// más ingredientes.
	var (
		buildingCode                            string
		batchSeconds, fuelPerBatch, outputQty   int64
		workers, minCityLevel, ingredientsTotal int
		fuelIsNull, outputIsCoal                bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT bt.code, r.batch_sim_seconds, r.fuel_product_id IS NULL,
		       r.fuel_per_batch, r.workers_required, r.min_city_level,
		       (SELECT count(*) FROM world.recipe_ingredients ri WHERE ri.recipe_id = r.id),
		       ri.quantity, p.code = 'coal'
		  FROM world.recipes r
		  JOIN world.building_types bt ON bt.id = r.building_type_id
		  JOIN world.recipe_ingredients ri ON ri.recipe_id = r.id AND ri.role = 'output'
		  JOIN world.products p ON p.id = ri.product_id
		 WHERE r.code = 'mine_coal'`).
		Scan(&buildingCode, &batchSeconds, &fuelIsNull, &fuelPerBatch, &workers,
			&minCityLevel, &ingredientsTotal, &outputQty, &outputIsCoal); err != nil {
		t.Fatalf("receta mine_coal: %v", err)
	}
	if buildingCode != "coal_mine" || batchSeconds != 3_600 || workers != 3 || minCityLevel != 1 {
		t.Fatalf("mine_coal inesperada: building=%s batch=%d workers=%d minCityLevel=%d",
			buildingCode, batchSeconds, workers, minCityLevel)
	}
	if !fuelIsNull || fuelPerBatch != 0 {
		t.Fatalf("mine_coal debe extraer SIN combustible (arranque v1): fuelNull=%v fuelPerBatch=%d",
			fuelIsNull, fuelPerBatch)
	}
	if ingredientsTotal != 1 || !outputIsCoal || outputQty != 60 {
		t.Fatalf("salida de mine_coal inesperada: ingredientes=%d salidaEsCoal=%v qty=%d",
			ingredientsTotal, outputIsCoal, outputQty)
	}

	// Yacimiento de coal: finito, intacto, en (40000,15000) dentro de la región
	// y en suelo libre (ninguna concesión sembrada a menos de 8000 m: hay sitio
	// para que un bot tome una parcela libre que cumpla near_resource).
	var (
		initial, remaining   int64
		renewable, inRegion  bool
		depositX, depositY   float64
		concessionsWithin8km int
	)
	if err := pool.QueryRow(ctx, `
		SELECT d.initial_amount, d.remaining_amount, d.renewable,
		       ST_X(d.location), ST_Y(d.location),
		       ST_Contains(r.bounds, d.location),
		       (SELECT count(*) FROM world.land_concessions c
		         WHERE ST_DWithin(c.parcel, d.location, 8000))
		  FROM world.resource_deposits d
		  JOIN world.products p ON p.id = d.product_id
		  JOIN world.regions r ON r.id = d.region_id
		 WHERE p.code = 'coal'`).
		Scan(&initial, &remaining, &renewable, &depositX, &depositY,
			&inRegion, &concessionsWithin8km); err != nil {
		t.Fatalf("yacimiento de coal: %v", err)
	}
	if initial != 2_000_000 || remaining != initial || renewable {
		t.Fatalf("yacimiento de coal inesperado: initial=%d remaining=%d renewable=%v",
			initial, remaining, renewable)
	}
	if depositX != 40_000 || depositY != 15_000 || !inRegion {
		t.Fatalf("ubicación del yacimiento de coal inesperada: (%f,%f) enRegión=%v",
			depositX, depositY, inRegion)
	}
	if concessionsWithin8km != 0 {
		t.Fatalf("el yacimiento de coal no está en suelo libre: %d concesiones a <8000 m",
			concessionsWithin8km)
	}
}

// dbSnapshot captura filas por tabla e IDs por clave natural para comparar
// dos ejecuciones del seed.
type dbSnapshot struct {
	counts map[string]int
	ids    map[string]string
}

// snapshot recoge el número de filas de cada tabla que el seed escribe y los
// IDs de todo lo sembrado indexados por su clave natural.
func snapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) dbSnapshot {
	t.Helper()
	tables := []string{
		"auth.accounts", "auth.account_credentials",
		"ledger.accounts", "ledger.transactions", "ledger.entries",
		"world.regions", "world.products", "world.building_types",
		"world.land_concessions", "world.buildings", "world.network_nodes",
		"world.building_inventories", "world.sim_clock",
		"world.recipes", "world.recipe_ingredients", "world.resource_deposits",
		"world.cities", "world.city_demand",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("contando %s: %v", table, err)
		}
		counts[table] = n
	}

	rows, err := pool.Query(ctx, `
		          SELECT 'region:'||name, id::text FROM world.regions
		UNION ALL SELECT 'product:'||code, id::text FROM world.products
		UNION ALL SELECT 'building_type:'||code, id::text FROM world.building_types
		UNION ALL SELECT 'auth:'||name, id::text FROM auth.accounts
		UNION ALL SELECT 'concession:'||a.name, c.id::text
		            FROM world.land_concessions c
		            JOIN auth.accounts a ON a.id = c.holder_account_id
		UNION ALL SELECT 'building:'||a.name, b.id::text
		            FROM world.buildings b
		            JOIN auth.accounts a ON a.id = b.owner_account_id
		UNION ALL SELECT 'node:'||a.name, n.id::text
		            FROM world.network_nodes n
		            JOIN world.buildings b ON b.id = n.building_id
		            JOIN auth.accounts a ON a.id = b.owner_account_id
		UNION ALL SELECT 'recipe:'||code, id::text FROM world.recipes
		UNION ALL SELECT 'deposit:'||p.code, d.id::text
		            FROM world.resource_deposits d
		            JOIN world.products p ON p.id = d.product_id
		UNION ALL SELECT 'city:'||name, id::text FROM world.cities
		UNION ALL SELECT 'ledger:'||la.kind||':'||COALESCE(a.name,'-')||':'||COALESCE(p.code,'-'), la.id::text
		            FROM ledger.accounts la
		            LEFT JOIN auth.accounts a ON a.id = la.owner_account_id
		            LEFT JOIN world.products p ON p.id = la.product_id`)
	if err != nil {
		t.Fatalf("consultando los IDs por clave natural: %v", err)
	}
	defer rows.Close()
	ids := map[string]string{}
	total := 0
	for rows.Next() {
		var key, id string
		if err := rows.Scan(&key, &id); err != nil {
			t.Fatalf("leyendo un ID por clave natural: %v", err)
		}
		ids[key] = id
		total++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando los IDs por clave natural: %v", err)
	}
	if total != len(ids) {
		t.Fatalf("claves naturales duplicadas: %d filas para %d claves (el seed duplicó algo)", total, len(ids))
	}
	return dbSnapshot{counts: counts, ids: ids}
}

// assertWorldContent valida el contenido del mundo mínimo del Incremento 1:
// región, catálogos y la implantación completa de cada corporación.
func assertWorldContent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// Región Askadia: valores administrados y bounds de 50 km × 50 km.
	var (
		gridX, gridY, taxBP, customsBP int
		biome, shardKey                string
		canonBase                      int64
		area                           float64
	)
	if err := pool.QueryRow(ctx, `
		SELECT grid_x, grid_y, biome::text, shard_key, tax_rate_bp,
		       customs_rate_bp, canon_base, ST_Area(bounds)
		  FROM world.regions WHERE name = $1`, seed.RegionName).
		Scan(&gridX, &gridY, &biome, &shardKey, &taxBP, &customsBP, &canonBase, &area); err != nil {
		t.Fatalf("región %q: %v", seed.RegionName, err)
	}
	if gridX != 0 || gridY != 0 || biome != "plains" || shardKey != "shard-0" ||
		taxBP != 500 || customsBP != 200 || canonBase != 1000 || area != 50_000*50_000 {
		t.Fatalf("región inesperada: grid=(%d,%d) biome=%s shard=%s tax=%d customs=%d canon=%d area=%f",
			gridX, gridY, biome, shardKey, taxBP, customsBP, canonBase, area)
	}

	// Productos del catálogo.
	products := map[string]struct {
		class                            string
		unitVolume, base, floor, ceiling int64
		isFuel                           bool
	}{
		"iron_ore": {class: "basic", unitVolume: 2, base: 100, floor: 20, ceiling: 400},
		"coal":     {class: "basic", unitVolume: 1, base: 60, floor: 12, ceiling: 240, isFuel: true},
	}
	for code, want := range products {
		var (
			class                            string
			unitVolume, base, floor, ceiling int64
			isFuel                           bool
		)
		if err := pool.QueryRow(ctx, `
			SELECT class::text, unit_volume, base_price, price_floor, price_ceiling, is_fuel
			  FROM world.products WHERE code = $1`, code).
			Scan(&class, &unitVolume, &base, &floor, &ceiling, &isFuel); err != nil {
			t.Fatalf("producto %q: %v", code, err)
		}
		if class != want.class || unitVolume != want.unitVolume || base != want.base ||
			floor != want.floor || ceiling != want.ceiling || isFuel != want.isFuel {
			t.Fatalf("producto %q inesperado: class=%s vol=%d base=%d floor=%d ceiling=%d fuel=%v",
				code, class, unitVolume, base, floor, ceiling, isFuel)
		}
	}

	// Tipo de edificación warehouse.
	var (
		footprintCells, maxLevel            int
		baseStorage, buildCost, maintenance int64
	)
	if err := pool.QueryRow(ctx, `
		SELECT footprint_cells, max_level, base_storage, build_cost, maintenance_cost
		  FROM world.building_types WHERE code = 'warehouse'`).
		Scan(&footprintCells, &maxLevel, &baseStorage, &buildCost, &maintenance); err != nil {
		t.Fatalf("building_type warehouse: %v", err)
	}
	if footprintCells != 4 || maxLevel != 4 || baseStorage != 100_000 ||
		buildCost != 50_000 || maintenance != 50 {
		t.Fatalf("warehouse inesperado: cells=%d maxLevel=%d storage=%d build=%d maint=%d",
			footprintCells, maxLevel, baseStorage, buildCost, maintenance)
	}

	// Implantación por corporación: concesión activa dentro de la región,
	// almacén operativo dentro de la parcela, nodo warehouse en su ubicación.
	locations := map[string][2]float64{
		seed.DefaultDemoName:   {10_000, 10_000},
		seed.DefaultTraderName: {30_000, 30_000},
	}
	for name, loc := range locations {
		var (
			status, buildingStatus, nodeKind  string
			canon, expires, granted           int64
			periodDays, level                 int
			parcelInRegion, footprintInParcel bool
			nodeX, nodeY                      float64
		)
		if err := pool.QueryRow(ctx, `
			SELECT c.status::text, c.canon_amount, c.period_sim_days,
			       c.expires_at_sim, c.granted_at_sim,
			       b.status::text, b.level,
			       ST_Contains(r.bounds, c.parcel), ST_Contains(c.parcel, b.footprint),
			       n.kind::text, ST_X(n.location), ST_Y(n.location)
			  FROM auth.accounts a
			  JOIN world.land_concessions c ON c.holder_account_id = a.id
			  JOIN world.regions r ON r.id = c.region_id
			  JOIN world.buildings b ON b.concession_id = c.id
			  JOIN world.network_nodes n ON n.building_id = b.id
			 WHERE a.name = $1`, name).
			Scan(&status, &canon, &periodDays, &expires, &granted,
				&buildingStatus, &level, &parcelInRegion, &footprintInParcel,
				&nodeKind, &nodeX, &nodeY); err != nil {
			t.Fatalf("implantación de %s: %v", name, err)
		}
		if status != "active" || canon != 1000 || periodDays != 90 ||
			expires != 90*86_400 || granted != 0 {
			t.Fatalf("concesión de %s inesperada: status=%s canon=%d period=%d expires=%d granted=%d",
				name, status, canon, periodDays, expires, granted)
		}
		if buildingStatus != "operational" || level != 1 {
			t.Fatalf("almacén de %s inesperado: status=%s level=%d", name, buildingStatus, level)
		}
		if !parcelInRegion || !footprintInParcel {
			t.Fatalf("geometría de %s incoherente: parcelaEnRegión=%v footprintEnParcela=%v",
				name, parcelInRegion, footprintInParcel)
		}
		if nodeKind != "warehouse" || nodeX != loc[0] || nodeY != loc[1] {
			t.Fatalf("nodo de %s inesperado: kind=%s en (%f,%f), esperado warehouse en (%f,%f)",
				name, nodeKind, nodeX, nodeY, loc[0], loc[1])
		}

		// Credencial y caja con el capital semilla intacto.
		var hasCred bool
		var cashBalance int64
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM auth.account_credentials cr
			                JOIN auth.accounts a ON a.id = cr.account_id
			               WHERE a.name = $1),
			       (SELECT la.balance FROM ledger.accounts la
			          JOIN auth.accounts a ON a.id = la.owner_account_id
			         WHERE a.name = $1 AND la.kind = 'cash')`, name).
			Scan(&hasCred, &cashBalance); err != nil {
			t.Fatalf("credencial/caja de %s: %v", name, err)
		}
		if !hasCred || cashBalance != seed.CorpSeedCapital {
			t.Fatalf("cuenta de %s incompleta: credencial=%v caja=%d (esperado %d)",
				name, hasCred, cashBalance, seed.CorpSeedCapital)
		}
	}
}

// assertCoherence valida la coherencia física↔contable del stock (ADR-022):
// cada fila de inventario físico coincide con el saldo stock_free de su
// (corp, producto, almacén); el saldo negativo de world_source es la suma
// emitida; y el ledger cierra a cero por producto.
func assertCoherence(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// Inventario físico == saldo stock_free, por (corp, producto, almacén).
	var totalRows, mismatches int
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE la.balance IS DISTINCT FROM bi.quantity)
		  FROM world.building_inventories bi
		  JOIN world.buildings b ON b.id = bi.building_id
		  LEFT JOIN ledger.accounts la
		         ON la.kind = 'stock_free'
		        AND la.owner_account_id = b.owner_account_id
		        AND la.product_id = bi.product_id
		        AND la.warehouse_building_id = bi.building_id`).
		Scan(&totalRows, &mismatches); err != nil {
		t.Fatalf("comparando inventario físico y contable: %v", err)
	}
	if totalRows != 4 || mismatches != 0 {
		t.Fatalf("coherencia física↔contable rota: %d filas de inventario (esperadas 4), %d descuadres",
			totalRows, mismatches)
	}

	// Cantidades concretas por (corp, producto).
	quantities := map[string]int64{"iron_ore": 5_000, "coal": 3_000}
	for _, corp := range []string{seed.DefaultDemoName, seed.DefaultTraderName} {
		for code, want := range quantities {
			var qty int64
			if err := pool.QueryRow(ctx, `
				SELECT bi.quantity
				  FROM world.building_inventories bi
				  JOIN world.buildings b ON b.id = bi.building_id
				  JOIN auth.accounts a ON a.id = b.owner_account_id
				  JOIN world.products p ON p.id = bi.product_id
				 WHERE a.name = $1 AND p.code = $2`, corp, code).Scan(&qty); err != nil {
				t.Fatalf("inventario de %s (%s): %v", corp, code, err)
			}
			if qty != want {
				t.Fatalf("inventario de %s (%s): %d, esperado %d", corp, code, qty, want)
			}
		}
	}

	// world_source por producto == -(stock total emitido a las 2 corps).
	worldSource := map[string]int64{"iron_ore": -10_000, "coal": -6_000}
	for code, want := range worldSource {
		var balance int64
		var ownerIsBank bool
		if err := pool.QueryRow(ctx, `
			SELECT la.balance, a.name = $2
			  FROM ledger.accounts la
			  JOIN world.products p ON p.id = la.product_id
			  JOIN auth.accounts a ON a.id = la.owner_account_id
			 WHERE la.kind = 'world_source' AND p.code = $1`, code, seed.CentralBankName).
			Scan(&balance, &ownerIsBank); err != nil {
			t.Fatalf("cuenta world_source de %s: %v", code, err)
		}
		if balance != want || !ownerIsBank {
			t.Fatalf("world_source de %s: balance=%d (esperado %d), titularBanco=%v",
				code, balance, want, ownerIsBank)
		}
	}

	// El ledger cierra a cero por activo: cada producto y el dinero.
	var unbalancedAssets int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT COALESCE(product_id::text, 'MONEY') AS asset, SUM(balance) AS total
			  FROM ledger.accounts
			 GROUP BY COALESCE(product_id::text, 'MONEY')
		) sums WHERE sums.total <> 0`).Scan(&unbalancedAssets); err != nil {
		t.Fatalf("verificando el cierre por activo: %v", err)
	}
	if unbalancedAssets != 0 {
		t.Fatalf("el ledger no cierra a cero en %d activos", unbalancedAssets)
	}

	// La emisión monetaria es exactamente el capital de las dos corporaciones, la
	// tesorería de garantía del banco central (emitida para sí, Incremento 6a) y el
	// capital inicial de la única ciudad seedada (faucet urbano, Incremento 6b).
	var emission int64
	if err := pool.QueryRow(ctx,
		`SELECT balance FROM ledger.accounts WHERE kind = 'emission'`).Scan(&emission); err != nil {
		t.Fatalf("cuenta emission: %v", err)
	}
	if want := -(2*seed.CorpSeedCapital + seed.CentralBankTreasury + seed.CityInitialCapital); emission != want {
		t.Fatalf("emission: %d, esperado %d", emission, want)
	}
}

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella. Todo se destruye al terminar el test.
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("seedtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
