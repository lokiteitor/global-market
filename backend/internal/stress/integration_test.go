// Integración LIGERA del cluster de stress test (GDD §13.4 modo 3, §15.4)
// contra una BD real y el gateway REAL (internal/gateway.BuildHandler, el mismo
// árbol de rutas que cmd/gateway) servido con httptest y envuelto en el
// middleware de métricas de la plataforma. Ningún mock.
//
// Una corrida corta (5 bots, ~5 s) demuestra el ciclo completo del harness:
//
//	(1) PROVISIONING por BD de las cuentas del run con prefijo reconocible
//	    "stress-<run_id>-…" (admin del entorno de pruebas: el contrato no expone
//	    endpoint de registro), capitalizadas por EMISIÓN del banco central.
//	(2) CARGA real por pkg/botsdk contra la API pública: lecturas del tablón con
//	    filtros, catálogo del mundo, red logística, ledger y contratos; y
//	    escrituras reales —publicación de compra con escrow bloqueado,
//	    cancelación respetando el cooldown y planificación de rutas—.
//	(3) MEDICIÓN: ops>0, percentiles por operación y CERO 5xx / cero errores
//	    inesperados (los 429 del rate limit se cuentan aparte como benignos).
//	(4) INFORME en JSON y consola, con las métricas RASPADAS del propio sistema
//	    bajo prueba (el gateway midió sus propias latencias del tablón) y el
//	    sondeo de BD (outbox, publicaciones vivas, contratos).
//	(5) LIMPIEZA: las cuentas del run quedan RETIRADAS (y no pueden volver a
//	    entrar) sin borrar un solo asiento: el ledger es append-only y sigue
//	    cuadrando a cero por activo.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package stress_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/market"
	platformdb "github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/stress"
	"github.com/lokiteitor/global-market/backend/internal/world/buildings"
	"github.com/lokiteitor/global-market/backend/internal/world/catalog"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/land"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

const (
	itDemoName    = "Demo"
	itDemoSecret  = "stress-demo-secret"
	itNorteName   = "Norte Trading"
	itNorteSecret = "stress-norte-secret"
	itRunID       = "it01"
	itSecretSeed  = "it-stress-seed"
	itCapital     = 500_000
	itSimBase     = 1_000_000
	itBots        = 5
	// itStockEndowment es la dotación de stock por bot: habilita el lado
	// VENDEDOR del harness (publicar sell), sin el cual la operación de
	// aceptación dependería de una oferta ajena y podría no ejercitarse.
	itStockEndowment = 500
	itSellShare      = 0.5
)

func TestStressHarnessShortRunIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool, dbURL := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado + reloj congelado ─────────────────────────────────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: itDemoName, DemoSecret: itDemoSecret,
		TraderName: itNorteName, TraderSecret: itNorteSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, itSimBase)

	// ── Gateway REAL con el middleware de métricas de la plataforma, para que
	//    el harness pueda RASPAR las métricas del propio sistema bajo prueba ──
	m := metrics.New(metrics.ServiceGateway)
	// Mismo registro de métricas que cmd/gateway: sin esto el proceso bajo prueba
	// no publicaría la familia ii_tx_serialization_* y el informe no podría medir
	// la contención SERIALIZABLE (disparador del SAD §13).
	platformdb.RegisterTxMetrics(m.Registry())
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	contractsOpts.CancelCooldownSeconds = 1 // la corrida es corta: el cooldown debe vencer dentro
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: m.Registry(),
		Options: gateway.Options{
			Auth:        auth.Options{LoginPerMin: 600, APIRPS: 2_000, APIBurst: 4_000},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			Catalog:     catalog.DefaultOptions(),
			Land:        land.DefaultOptions(),
			Buildings:   buildings.DefaultOptions(),
			Production:  production.DefaultOptions(),
			Fleet:       fleet.DefaultOptions(),
			Logistics:   logistics.DefaultOptions(),
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		},
	})
	if err != nil {
		t.Fatalf("BuildHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", handler)
	mux.Handle("GET /metrics", m.Handler())
	srv := httptest.NewServer(m.Middleware(mux))
	defer srv.Close()

	// ── Configuración de la corrida ──────────────────────────────────────────
	reportPath := filepath.Join(t.TempDir(), "stress-report.json")
	opts := stress.Options{
		APIURL:         srv.URL + gateway.APIPrefix,
		Env:            "dev",
		AllowHosts:     allowHostsFor(t, adminURL),
		RunID:          itRunID,
		Bots:           itBots,
		Ramp:           time.Second,
		Duration:       5 * time.Second,
		Tick:           200 * time.Millisecond,
		Mix:            mustMix(t, stress.DefaultMixSpec),
		WriteRatio:     0.5,
		ReportPath:     reportPath,
		Addr:           ":0",
		SellShare:      itSellShare,
		Cleanup:        true,
		Capital:        itCapital,
		StockEndowment: itStockEndowment,
		SecretSeed:     itSecretSeed,
		DatabaseURL:    dbURL,
		TargetMetrics:  []string{srv.URL + "/metrics"},
		LogInterval:    time.Second,
		MaxSamples:     stress.DefaultMaxSamples,
	}

	emission0 := emissionBalance(t, ctx, pool)
	worldSource0 := worldSourceEmitted(t, ctx, pool)
	physical0 := physicalInventory(t, ctx, pool)
	runner, err := stress.NewRunner(pool, opts, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	report, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// ── (1) Provisioning: cuentas con prefijo reconocible y capitalizadas ────
	prefix := opts.RunAccountPrefix()
	if !strings.HasPrefix(prefix, stress.AccountPrefix) {
		t.Fatalf("prefijo de cuentas %q, esperado que empiece por %q", prefix, stress.AccountPrefix)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM auth.accounts WHERE name LIKE $1`, prefix+"%"); n != itBots {
		t.Fatalf("cuentas del run: %d, esperadas %d", n, itBots)
	}
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM auth.bot_profiles bp
		  JOIN auth.accounts a ON a.id = bp.account_id
		 WHERE a.name LIKE $1 AND bp.behavior->>'stress_run_id' = $2`, prefix+"%", itRunID); n != itBots {
		t.Fatalf("perfiles marcados con el run_id: %d, esperados %d", n, itBots)
	}
	if got, want := emission0-emissionBalance(t, ctx, pool), int64(itBots*itCapital); got != want {
		t.Fatalf("emisión consumida por la capitalización: %d, esperada %d", got, want)
	}
	// El provisioning también DOTA STOCK: sin mercancía propia el harness solo
	// puede publicar buy y la aceptación queda colgada de una oferta ajena.
	// La dotación es contabilidad visible: −N world_source / +N stock_free, con
	// el plano físico movido a la vez.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.accounts la
		  JOIN auth.accounts a ON a.id = la.owner_account_id
		 WHERE la.kind = 'stock_free' AND a.name LIKE $1 AND la.balance > 0`, prefix+"%"); n != itBots {
		t.Fatalf("cuentas stock_free dotadas: %d, esperadas %d", n, itBots)
	}
	if got, want := worldSourceEmitted(t, ctx, pool)-worldSource0, int64(itBots*itStockEndowment); got != want {
		t.Fatalf("stock emitido por la dotación: %d, esperado %d", got, want)
	}
	if got, want := physicalInventory(t, ctx, pool)-physical0, int64(itBots*itStockEndowment); got != want {
		t.Fatalf("inventario físico añadido por la dotación: %d, esperado %d (los dos planos deben moverse juntos)", got, want)
	}

	// ── (2)(3) Carga medida: ops > 0 y ningún error inesperado ───────────────
	if report.Totals.Requests == 0 {
		t.Fatal("la corrida no emitió ninguna petición")
	}
	if got := report.Totals.ErrorsBySt["5xx"]; got != 0 {
		t.Fatalf("respuestas 5xx: %d, esperadas 0 (errores: %v)", got, report.Totals.ErrorsByCode)
	}
	if report.Totals.UnexpectedError != 0 {
		t.Fatalf("errores inesperados: %d (por status %v, por código %v)",
			report.Totals.UnexpectedError, report.Totals.ErrorsBySt, report.Totals.ErrorsByCode)
	}
	if !report.Verdict.OK {
		t.Fatalf("veredicto negativo: %s / %v", report.Verdict.Summary, report.Verdict.Lines)
	}
	if report.Totals.OpsPerSecond <= 0 {
		t.Fatalf("throughput = %v, esperado > 0", report.Totals.OpsPerSecond)
	}

	byOp := map[stress.Op]int{}
	for _, op := range report.Operations {
		byOp[op.Op] = int(op.Requests)
		if op.Requests > 0 && op.Latency.P95Ms <= 0 {
			t.Errorf("la operación %s no reportó percentiles (p95 %v)", op.Op, op.Latency.P95Ms)
		}
	}
	if byOp[stress.OpLogin] != itBots {
		t.Errorf("logins medidos: %d, esperados %d", byOp[stress.OpLogin], itBots)
	}
	if byOp[stress.OpBoardRead] == 0 {
		t.Error("la corrida no consultó el tablón: la carga de lectura no se ejercitó")
	}
	if byOp[stress.OpPublish] == 0 {
		t.Error("la corrida no publicó nada: la carga de ESCRITURA no se ejercitó")
	}
	publish := findOpReport(report, stress.OpPublish)
	if publish == nil || publish.OK == 0 {
		t.Fatalf("ninguna publicación tuvo éxito: %+v", publish)
	}
	// Las escrituras llegaron de verdad al dominio: hay publicaciones cuyo
	// publicador es una cuenta del run.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.publications p
		  JOIN auth.accounts a ON a.id = p.publisher_account_id
		 WHERE a.name LIKE $1`, prefix+"%"); n == 0 {
		t.Fatal("ninguna publicación del run llegó a la BD")
	}
	// …y las hay de los DOS lados del tablón: con solo buy, el harness no tiene
	// contraparte propia y su tasa de aceptación no puede escalar con la
	// población (el defecto que la dotación de stock corrige).
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.publications p
		  JOIN auth.accounts a ON a.id = p.publisher_account_id
		 WHERE a.name LIKE $1 AND p.kind = 'sell'`, prefix+"%"); n == 0 {
		t.Error("el run no publicó ninguna oferta sell: sin lado vendedor la aceptación no se ejercita")
	}
	// El informe debe DECLARAR los caminos que no se ejercitaron: un informe sin
	// esa línea puede leerse como sano habiendo medido solo el camino corto.
	for _, path := range report.Verdict.Unexercised {
		if path == string(stress.OpPublish) || path == string(stress.OpBoardRead) {
			t.Errorf("el camino %q quedó sin ejercitar en una corrida de escritura: %v", path, report.Verdict.Lines)
		}
	}

	// ── (4) Informe: JSON en disco, consola y métricas del sistema bajo prueba
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("leyendo el informe: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("el informe no es JSON válido: %v", err)
	}
	for _, key := range []string{"run_id", "config", "totals", "operations", "system", "cleanup", "verdict", "duration_seconds"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("el informe JSON no incluye %q", key)
		}
	}
	if cfg, ok := decoded["config"].(map[string]any); ok {
		if got := cfg["account_prefix"]; got != prefix {
			t.Errorf("el informe declara el prefijo %v, esperado %q", got, prefix)
		}
		for _, secret := range []string{itSecretSeed, dbURL} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("el informe filtra un secreto (%q): la configuración solo debe exponer el host de BD", secret)
			}
		}
	} else {
		t.Error("el informe no incluye un bloque config legible")
	}
	if console := report.Console(); !strings.Contains(console, itRunID) || !strings.Contains(console, "VEREDICTO") {
		t.Errorf("la salida de consola no es el informe esperado:\n%s", console)
	}

	if len(report.System.Targets) != 1 {
		t.Fatalf("targets raspados: %d, esperado 1", len(report.System.Targets))
	}
	target := report.System.Targets[0]
	if !target.Reachable {
		t.Fatalf("las métricas del sistema bajo prueba no fueron accesibles: %s", target.Error)
	}
	if target.Service != metrics.ServiceGateway {
		t.Errorf("servicio raspado = %q, esperado %q", target.Service, metrics.ServiceGateway)
	}
	if target.HTTPRequests <= 0 {
		t.Error("el target no reportó peticiones servidas")
	}
	if target.HTTPErrors5xx != 0 {
		t.Errorf("el propio gateway contabilizó %v respuestas 5xx", target.HTTPErrors5xx)
	}
	if target.BoardRequests <= 0 || target.BoardRoute == "" {
		t.Errorf("el target no reportó la latencia servida del tablón (route %q, peticiones %v)", target.BoardRoute, target.BoardRequests)
	}
	// Contención SERIALIZABLE (SAD §13): el informe la mide del propio sistema.
	// Una corrida de 5 bots no tiene por qué agotar ningún presupuesto, pero la
	// LECTURA tiene que existir y quedar por escrito en el veredicto: enterarse
	// de que un trabajo de fondo se cayó por contención no puede depender de leer
	// el log del engine a mano.
	if !target.TxMetricsPublished {
		t.Error("el target no publicó ii_tx_serialization_*: el informe se queda sin el disparador de contención")
	}
	if !target.BaselineTaken {
		t.Error("sin línea base la contención medida no es atribuible a la corrida")
	}
	if target.TxExhaustedDelta != 0 {
		t.Errorf("la corrida agotó %v presupuestos de reintentos serializables", target.TxExhaustedDelta)
	}
	if report.Verdict.TargetTxExhausted != 0 {
		t.Errorf("el veredicto contabiliza %d presupuestos agotados", report.Verdict.TargetTxExhausted)
	}
	if !strings.Contains(strings.Join(report.Verdict.Lines, "\n"), "contención SERIALIZABLE en gateway") {
		t.Errorf("el veredicto no informa de la contención medida: %v", report.Verdict.Lines)
	}
	if !report.System.Database.Reachable {
		t.Fatalf("el sondeo de BD falló: %s", report.System.Database.Error)
	}
	db := report.System.Database
	if db.StressAccounts != itBots {
		t.Errorf("cuentas del run en el sondeo: %d, esperadas %d", db.StressAccounts, itBots)
	}
	if db.PublicationsCreatedDuringRun <= 0 {
		t.Error("el sondeo no vio publicaciones creadas durante la corrida")
	}
	if db.OutboxEmittedDuringRun <= 0 {
		t.Error("el sondeo no vio eventos de outbox emitidos durante la corrida")
	}

	// ── (5) Limpieza: cuentas retiradas, ledger intacto ──────────────────────
	if report.Cleanup.Skipped {
		t.Fatal("la limpieza estaba activada y el informe la declara omitida")
	}
	if report.Cleanup.Retired != itBots {
		t.Fatalf("cuentas retiradas: %d, esperadas %d (fallidas %d)", report.Cleanup.Retired, itBots, report.Cleanup.Failed)
	}
	if n := countRows(t, ctx, pool,
		`SELECT count(*) FROM auth.accounts WHERE name LIKE $1 AND status = 'retired'`, prefix+"%"); n != itBots {
		t.Fatalf("cuentas en estado retired: %d, esperadas %d", n, itBots)
	}
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM auth.bot_profiles bp
		  JOIN auth.accounts a ON a.id = bp.account_id
		 WHERE a.name LIKE $1 AND bp.active`, prefix+"%"); n != 0 {
		t.Fatalf("perfiles aún activos tras la limpieza: %d, esperados 0", n)
	}
	if db.StressAccountsActive != 0 {
		t.Errorf("el informe declara %d cuentas activas tras la limpieza, esperadas 0", db.StressAccountsActive)
	}
	// Una cuenta retirada NO puede volver a entrar (el harness no deja bots
	// jugando en el entorno tras la corrida).
	retiredName := fmt.Sprintf("%sproducer-0001", prefix)
	client, err := botsdk.New(botsdk.Options{BaseURL: opts.APIURL, MaxRetries: -1})
	if err != nil {
		t.Fatalf("botsdk.New: %v", err)
	}
	if _, err := client.Login(ctx, retiredName, stress.DeriveSecret(itSecretSeed, retiredName)); err == nil {
		t.Fatal("una cuenta retirada por la limpieza no debería poder iniciar sesión")
	}
	// El ledger es append-only: la limpieza no borra valor y sigue cuadrando.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.entries e
		  JOIN ledger.accounts la ON la.id = e.account_id
		  JOIN auth.accounts a ON a.id = la.owner_account_id
		 WHERE a.name LIKE $1`, prefix+"%"); n == 0 {
		t.Fatal("los asientos del run desaparecieron: el ledger debe ser append-only")
	}
	assertBalancedLedger(t, ctx, pool)
}

// findOpReport localiza el resumen de una operación en el informe.
func findOpReport(r *stress.Report, op stress.Op) *stress.OpReport {
	for i := range r.Operations {
		if r.Operations[i].Op == op {
			return &r.Operations[i]
		}
	}
	return nil
}

// mustMix interpreta una mezcla de arquetipos o falla el test.
func mustMix(t *testing.T, spec string) stress.Mix {
	t.Helper()
	m, err := stress.ParseMix(spec)
	if err != nil {
		t.Fatalf("ParseMix(%q): %v", spec, err)
	}
	return m
}

// allowHostsFor amplía la allowlist por defecto con el host de la BD de test
// (en CI puede no ser localhost). La salvaguarda sigue vigente: el host de la
// API es el de httptest (127.0.0.1) y II_ENV nunca es prod.
func allowHostsFor(t *testing.T, adminURL string) []string {
	t.Helper()
	hosts := append([]string{}, stress.DefaultAllowHosts...)
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	if h := u.Hostname(); h != "" {
		hosts = append(hosts, h)
	}
	return hosts
}

// ─── Ayudas de BD ────────────────────────────────────────────────────────────

func freezeSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, at int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE world.sim_clock SET frozen = true, sim_time_at = $1, wall_anchor = now(), updated_at = now() WHERE id = 1`, at); err != nil {
		t.Fatalf("congelando el reloj: %v", err)
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// worldSourceEmitted es el stock NETO emitido al mundo: el saldo de las cuentas
// world_source es negativo por construcción (ADR-022), así que se devuelve con
// el signo cambiado para poder compararlo con lo dotado.
func worldSourceEmitted(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(-SUM(balance), 0) FROM ledger.accounts WHERE kind = 'world_source'`).Scan(&b); err != nil {
		t.Fatalf("saldo world_source: %v", err)
	}
	return b
}

// physicalInventory es el inventario FÍSICO total del mundo (plano que debe
// moverse a la vez que el contable).
func physicalInventory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var q int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(quantity), 0) FROM world.building_inventories`).Scan(&q); err != nil {
		t.Fatalf("inventario físico: %v", err)
	}
	return q
}

func emissionBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind = 'emission'`).Scan(&b); err != nil {
		t.Fatalf("saldo de emisión: %v", err)
	}
	return b
}

func assertBalancedLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(product_id::text, 'MONEY') AS asset, SUM(balance) AS total
		  FROM ledger.accounts
		 GROUP BY COALESCE(product_id::text, 'MONEY')`)
	if err != nil {
		t.Fatalf("sumando el ledger: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var asset string
		var total int64
		if err := rows.Scan(&asset, &total); err != nil {
			t.Fatalf("leyendo la suma del ledger: %v", err)
		}
		if total != 0 {
			t.Fatalf("el ledger no cuadra a cero para el activo %s: suma %d", asset, total)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando las sumas del ledger: %v", err)
	}
}

// ─── BD efímera ──────────────────────────────────────────────────────────────

// newEphemeralDB crea una BD efímera con las migraciones reales y devuelve el
// pool y su cadena de conexión (que el harness usa para su provisioner).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) (*pgxpool.Pool, string) {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("stresstest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	return pool, ephemeralURL(t, adminURL, dbName)
}

// ephemeralURL reescribe la URL de administración para apuntar a la BD efímera.
func ephemeralURL(t *testing.T, adminURL, dbName string) string {
	t.Helper()
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + dbName
	return u.String()
}
