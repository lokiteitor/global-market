package worldgen_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/worldgen"
)

// TestWorldgenIntegration ejercita el generador procedural sobre una BD real con
// Askadia ya sembrada: estructura del mundo (8 regiones nuevas + Askadia, ciudades
// y yacimientos por región terrestre, enlaces rail/sea con segmentos partidos por
// la frontera, terminales intermodales), idempotencia (2ª corrida no duplica) e
// integridad de Askadia (sus nodos, edificios y red road no cambian).
//
// Se omite si II_TEST_DATABASE_URL no está definida. Crea una BD efímera propia
// (el rol debe tener CREATEDB), aplica las migraciones reales y la destruye al
// terminar.
func TestWorldgenIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	runSeed(t, ctx, pool)

	// Instantánea de Askadia ANTES del generador (para probar que queda intacta).
	askadia := askadiaSnapshot(t, ctx, pool)

	opts := worldgen.DefaultOptions()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	summary, err := worldgen.Generate(ctx, pool, opts, logger)
	if err != nil {
		t.Fatalf("worldgen (1ª corrida): %v", err)
	}

	assertStructure(t, ctx, pool, summary)
	assertInterRegionSegments(t, ctx, pool)
	assertGeneratedCityIsLive(t, ctx, pool)

	// Idempotencia: la 2ª corrida no debe crear ni una fila.
	before := worldCounts(t, ctx, pool)
	summary2, err := worldgen.Generate(ctx, pool, opts, logger)
	if err != nil {
		t.Fatalf("worldgen (2ª corrida): %v", err)
	}
	after := worldCounts(t, ctx, pool)
	for table, n := range before {
		if after[table] != n {
			t.Fatalf("idempotencia rota en %s: antes %d, después %d", table, n, after[table])
		}
	}
	if summary2.RegionsCreated != 0 || summary2.CitiesCreated != 0 || summary2.DepositsCreated != 0 ||
		summary2.RailLinks != 0 || summary2.SeaLinks != 0 || summary2.TerminalsCreated != 0 {
		t.Fatalf("la 2ª corrida reporta creaciones: %+v", summary2)
	}

	// Askadia intacta tras dos corridas.
	assertAskadiaIntact(t, ctx, pool, askadia)
}

// TestWorldgenDeterminism: dos generaciones con la MISMA semilla en dos BD
// independientes producen el MISMO mundo (mismos biomas por celda y mismas
// ciudades). Requisito duro: mismo II_WORLD_SEED ⇒ mismo mundo.
func TestWorldgenDeterminism(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := worldgen.DefaultOptions()

	world := func() (map[string]string, []string) {
		pool := newEphemeralDB(t, ctx, adminURL)
		runSeed(t, ctx, pool)
		if _, err := worldgen.Generate(ctx, pool, opts, logger); err != nil {
			t.Fatalf("worldgen: %v", err)
		}
		return regionBiomes(t, ctx, pool), cityNames(t, ctx, pool)
	}

	biomesA, citiesA := world()
	biomesB, citiesB := world()

	if len(biomesA) != len(biomesB) {
		t.Fatalf("número de regiones distinto: %d vs %d", len(biomesA), len(biomesB))
	}
	for cell, biome := range biomesA {
		if biomesB[cell] != biome {
			t.Fatalf("bioma no determinista en %s: %q vs %q", cell, biome, biomesB[cell])
		}
	}
	if len(citiesA) != len(citiesB) {
		t.Fatalf("número de ciudades distinto: %d vs %d", len(citiesA), len(citiesB))
	}
	for i := range citiesA {
		if citiesA[i] != citiesB[i] {
			t.Fatalf("ciudad no determinista en la posición %d: %q vs %q", i, citiesA[i], citiesB[i])
		}
	}
}

// ─── Aserciones ───────────────────────────────────────────────────────────────

// assertStructure comprueba la estructura del mundo generado: 9 regiones (8
// nuevas + Askadia), cada región terrestre con ciudad, yacimientos, junction y
// red vial, y la presencia de enlaces rail y sea y de terminales.
func assertStructure(t *testing.T, ctx context.Context, pool *pgxpool.Pool, summary worldgen.Summary) {
	t.Helper()

	if got := count(t, ctx, pool, `SELECT count(*) FROM world.regions`); got != 9 {
		t.Fatalf("regiones totales = %d, esperado 9 (8 nuevas + Askadia)", got)
	}
	if summary.RegionsCreated != 8 {
		t.Fatalf("summary.RegionsCreated = %d, esperado 8", summary.RegionsCreated)
	}

	// Cada región terrestre (salvo Askadia, que valida su propio test) tiene
	// ciudad, ≥2 yacimientos, junction central y ≥1 enlace road.
	rows, err := pool.Query(ctx, `
		SELECT id, grid_x, grid_y, biome::text FROM world.regions
		 WHERE NOT (grid_x = 0 AND grid_y = 0)`)
	if err != nil {
		t.Fatalf("listando regiones: %v", err)
	}
	defer rows.Close()
	type reg struct {
		id     string
		gx, gy int
		biome  string
	}
	var regs []reg
	for rows.Next() {
		var r reg
		if err := rows.Scan(&r.id, &r.gx, &r.gy, &r.biome); err != nil {
			t.Fatalf("scan región: %v", err)
		}
		regs = append(regs, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando regiones: %v", err)
	}

	landRegions := 0
	for _, r := range regs {
		junctions := count(t, ctx, pool, `SELECT count(*) FROM world.network_nodes WHERE region_id=$1 AND kind='junction'`, r.id)
		if junctions < 1 {
			t.Fatalf("región (%d,%d) %s sin junction", r.gx, r.gy, r.biome)
		}
		if r.biome == "ocean" {
			// Región de agua: sin ciudad ni yacimientos (solo su junction/waypoint).
			if c := count(t, ctx, pool, `SELECT count(*) FROM world.cities WHERE region_id=$1`, r.id); c != 0 {
				t.Fatalf("región oceánica (%d,%d) con %d ciudades", r.gx, r.gy, c)
			}
			continue
		}
		landRegions++
		if c := count(t, ctx, pool, `SELECT count(*) FROM world.cities WHERE region_id=$1`, r.id); c < 1 {
			t.Fatalf("región terrestre (%d,%d) %s sin ciudad", r.gx, r.gy, r.biome)
		}
		if d := count(t, ctx, pool, `SELECT count(*) FROM world.resource_deposits WHERE region_id=$1`, r.id); d < 2 {
			t.Fatalf("región terrestre (%d,%d) %s con %d yacimientos (<2)", r.gx, r.gy, r.biome, d)
		}
		roads := count(t, ctx, pool, `
			SELECT count(*) FROM world.network_links l
			  JOIN world.network_nodes n ON n.id = l.from_node_id
			 WHERE n.region_id=$1 AND l.mode='road'`, r.id)
		if roads < 1 {
			t.Fatalf("región terrestre (%d,%d) %s sin enlaces road", r.gx, r.gy, r.biome)
		}
	}
	if landRegions == 0 {
		t.Fatal("ninguna región terrestre generada")
	}

	// Enlaces inter-región: existen rail y sea (mezcla del mundo por defecto).
	if rail := count(t, ctx, pool, `SELECT count(*) FROM world.network_links WHERE mode='rail'`); rail == 0 {
		t.Fatal("no hay enlaces rail inter-región")
	}
	if sea := count(t, ctx, pool, `SELECT count(*) FROM world.network_links WHERE mode='sea'`); sea == 0 {
		t.Fatal("no hay enlaces sea inter-región")
	}
	if summary.RailLinks == 0 || summary.SeaLinks == 0 {
		t.Fatalf("summary sin rail o sea: rail=%d sea=%d", summary.RailLinks, summary.SeaLinks)
	}

	// Terminales intermodales creadas.
	term := count(t, ctx, pool, `SELECT count(*) FROM world.terminals`)
	if term == 0 {
		t.Fatal("no se crearon terminales intermodales")
	}
	// Slots de prioridad a la venta: cada terminal ofrece varios (GDD 7.3).
	if summary.SlotsCreated == 0 {
		t.Fatal("no se crearon slots de prioridad de terminal")
	}
	slots := count(t, ctx, pool, `SELECT count(*) FROM world.terminal_slots`)
	if slots != term*int64(3) {
		t.Fatalf("slots de terminal = %d, esperado %d (3 por terminal)", slots, term*3)
	}
	// Todos a la venta (sin titular) al generarse.
	if held := count(t, ctx, pool, `SELECT count(*) FROM world.terminal_slots WHERE holder_account_id IS NOT NULL`); held != 0 {
		t.Fatalf("slots con titular al generarse = %d, esperado 0 (todos a la venta)", held)
	}
	// Catálogo aditivo de vehículos rail/sea.
	if v := count(t, ctx, pool, `SELECT count(*) FROM world.vehicle_types WHERE mode IN ('rail','sea')`); v < 2 {
		t.Fatalf("faltan tipos de vehículo rail/sea: %d", v)
	}
}

// assertInterRegionSegments comprueba que TODO enlace inter-región (rail/sea) está
// partido en exactamente 2 segmentos con region_id distinto (uno por lado de la
// frontera, GDD 15.1).
func assertInterRegionSegments(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT l.id, count(s.id) AS n, count(DISTINCT s.region_id) AS regions
		  FROM world.network_links l
		  JOIN world.link_segments s ON s.link_id = l.id
		 WHERE l.mode IN ('rail','sea')
		 GROUP BY l.id`)
	if err != nil {
		t.Fatalf("consultando segmentos inter-región: %v", err)
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var id string
		var n, regions int
		if err := rows.Scan(&id, &n, &regions); err != nil {
			t.Fatalf("scan segmentos: %v", err)
		}
		if n != 2 {
			t.Fatalf("enlace inter-región %s tiene %d segmentos, esperado 2", id, n)
		}
		if regions != 2 {
			t.Fatalf("enlace inter-región %s tiene segmentos en %d regiones, esperado 2 (uno por lado)", id, regions)
		}
		checked++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando segmentos: %v", err)
	}
	if checked == 0 {
		t.Fatal("no hay enlaces inter-región con segmentos")
	}
}

// assertGeneratedCityIsLive comprueba que una ciudad generada es un sumidero real:
// tiene caja prefondeada (CityInitialCapital), demanda de iron_ore y centro de
// distribución con nodo en el grafo.
func assertGeneratedCityIsLive(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var cityID, accountID, name string
	err := pool.QueryRow(ctx, `
		SELECT c.id, c.account_id, c.name
		  FROM world.cities c
		  JOIN world.regions r ON r.id = c.region_id
		 WHERE NOT (r.grid_x = 0 AND r.grid_y = 0)
		 ORDER BY c.id LIMIT 1`).Scan(&cityID, &accountID, &name)
	if err != nil {
		t.Fatalf("localizando una ciudad generada: %v", err)
	}
	balance := count(t, ctx, pool, `
		SELECT COALESCE(balance,0) FROM ledger.accounts WHERE owner_account_id=$1 AND kind='cash'`, accountID)
	if balance != worldgen.CityInitialCapital {
		t.Fatalf("ciudad %q: caja = %d, esperado %d", name, balance, worldgen.CityInitialCapital)
	}
	if d := count(t, ctx, pool, `SELECT count(*) FROM world.city_demand WHERE city_id=$1`, cityID); d < 1 {
		t.Fatalf("ciudad %q sin demanda", name)
	}
	if n := count(t, ctx, pool, `SELECT count(*) FROM world.network_nodes WHERE city_id=$1 AND kind='distribution_center'`, cityID); n != 1 {
		t.Fatalf("ciudad %q: nodos de centro de distribución = %d, esperado 1", name, n)
	}
}

// assertAskadiaIntact comprueba que el generador no tocó los nodos, edificios ni
// la red road de Askadia.
func assertAskadiaIntact(t *testing.T, ctx context.Context, pool *pgxpool.Pool, before askadiaState) {
	t.Helper()
	now := askadiaSnapshot(t, ctx, pool)
	if now.regionID != before.regionID {
		t.Fatalf("el id de la región Askadia cambió: %s → %s", before.regionID, now.regionID)
	}
	if now.nodes != before.nodes {
		t.Fatalf("los nodos de Askadia cambiaron: %d → %d", before.nodes, now.nodes)
	}
	if now.buildings != before.buildings {
		t.Fatalf("los edificios de Askadia cambiaron: %d → %d", before.buildings, now.buildings)
	}
	if now.roadLinks != before.roadLinks {
		t.Fatalf("la red road de Askadia cambió: %d → %d", before.roadLinks, now.roadLinks)
	}
	if now.junctionID != before.junctionID {
		t.Fatalf("el junction de Askadia cambió: %s → %s", before.junctionID, now.junctionID)
	}
}

// ─── Consultas de apoyo ───────────────────────────────────────────────────────

type askadiaState struct {
	regionID   string
	junctionID string
	nodes      int64
	buildings  int64
	roadLinks  int64
}

func askadiaSnapshot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) askadiaState {
	t.Helper()
	var s askadiaState
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE grid_x=0 AND grid_y=0`).Scan(&s.regionID); err != nil {
		t.Fatalf("región Askadia: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes WHERE region_id=$1 AND kind='junction' ORDER BY id LIMIT 1`,
		s.regionID).Scan(&s.junctionID); err != nil {
		t.Fatalf("junction de Askadia: %v", err)
	}
	s.nodes = count(t, ctx, pool, `SELECT count(*) FROM world.network_nodes WHERE region_id=$1`, s.regionID)
	s.buildings = count(t, ctx, pool, `SELECT count(*) FROM world.buildings WHERE region_id=$1`, s.regionID)
	s.roadLinks = count(t, ctx, pool, `
		SELECT count(*) FROM world.network_links l
		  JOIN world.network_nodes n ON n.id = l.from_node_id
		 WHERE n.region_id=$1 AND l.mode='road'`, s.regionID)
	return s
}

// worldCounts devuelve el conteo de las tablas que el generador escribe, para la
// prueba de idempotencia.
func worldCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]int64 {
	t.Helper()
	tables := []string{
		"world.regions", "world.cities", "world.city_demand", "world.resource_deposits",
		"world.network_nodes", "world.network_links", "world.link_segments", "world.terminals",
		"world.buildings", "world.land_concessions", "world.vehicle_types",
		"ledger.accounts", "ledger.entries",
	}
	out := make(map[string]int64, len(tables))
	for _, tbl := range tables {
		out[tbl] = count(t, ctx, pool, fmt.Sprintf("SELECT count(*) FROM %s", tbl)) //nolint:gosec // nombres de tabla constantes del test
	}
	return out
}

func regionBiomes(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT grid_x, grid_y, biome::text FROM world.regions ORDER BY grid_x, grid_y`)
	if err != nil {
		t.Fatalf("biomas por región: %v", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var gx, gy int
		var biome string
		if err := rows.Scan(&gx, &gy, &biome); err != nil {
			t.Fatalf("scan bioma: %v", err)
		}
		out[fmt.Sprintf("%d,%d", gx, gy)] = biome
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando biomas: %v", err)
	}
	return out
}

func cityNames(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT name FROM world.cities ORDER BY name`)
	if err != nil {
		t.Fatalf("ciudades: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan ciudad: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando ciudades: %v", err)
	}
	return out
}

// count ejecuta una consulta escalar que devuelve un entero.
func count(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("consulta %q: %v", query, err)
	}
	return n
}

// ─── Infraestructura de BD efímera ────────────────────────────────────────────

func runSeed(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	opts := seed.Options{
		DemoName:     seed.DefaultDemoName,
		DemoSecret:   "demo-secret-test",
		TraderName:   seed.DefaultTraderName,
		TraderSecret: "norte-secret-test",
		Ledger:       ledger.DefaultOptions(),
	}
	if err := seed.Run(ctx, pool, opts, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve el
// pool (mismo patrón que el test del seed).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldgentest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
