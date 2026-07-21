// DENSIDAD DINÁMICA de bots (GDD §13.4 modo 2, §19: la válvula de carga
// principal) de PROCESO A PROCESO: el Bot Orchestration Service REAL con su
// población real jugando por pkg/botsdk contra el gateway REAL servido con
// httptest, y el DensityController corriendo en paralelo como en cmd/bots
// —mismo cableado: `orch.Run` y `density.Run` en goroutines hermanas sobre el
// mismo pool y el mismo registry de métricas—. Ningún mock: ni de la población
// (el supervisor arranca y para goroutines de bots de verdad, con su sesión HTTP
// y su bucle de Decide) ni de las señales (se leen de la BD).
//
// A diferencia de la integración de internal/bots —que ejercita la FÓRMULA
// contra una población en memoria—, aquí se comprueba el EFECTO OBSERVABLE que
// operaciones verá en producción:
//
//	(1) ARRANQUE: el orquestador aprovisiona y arranca la población; el gauge
//	    ii_bots_active llega al techo aprovisionado.
//	(2) CARGA ALTA SINTÉTICA: con el outbox por encima del umbral (eventos
//	    inyectados y un cursor de consumidor retrasado) el controlador RECORTA
//	    la población activa: ii_bots_active baja, ii_bots_density_target cae y
//	    ii_bots_density_adjustments_total{direction="down"} crece. Los bots
//	    parados NO se retiran: conservan cuenta, capital y estado.
//	(3) NORMALIZACIÓN: cuando el cursor alcanza la cabecera del outbox, el
//	    controlador REANUDA la población hasta el techo.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
)

const (
	densitySimBase    int64 = 1_000_000
	densityCapital    int64 = 500_000
	densitySecretSeed       = "e2e-density-seed"

	// densityTraders es la población aprovisionada del test. El comerciante es
	// el arquetipo que menos mundo necesita para vivir (no ocupa suelo ni
	// construye), así que aísla la densidad de la economía: lo que se mide es
	// cuántos bots ESTÁN CORRIENDO, no lo que consiguen hacer.
	densityTraders = 4
	// densityCycle es la cadencia del lazo de control durante el test.
	densityCycle = 200 * time.Millisecond
	// densityLagEvents son los eventos sintéticos del outbox que saturan la
	// señal de carga (LagHigh = 10 en la configuración del test).
	densityLagEvents = 40
	// densityProbeConsumer es el consumidor lógico cuyo cursor se retrasa para
	// fabricar el lag (no lo consume nadie: es una sonda del test), y
	// densityProbeEvent el tipo que declara consumir — el retraso se mide
	// contra SUS eventos, no contra la cabecera del outbox.
	densityProbeConsumer = "e2e_density_probe"
	densityProbeEvent    = "density.test"
)

func TestBotsDensityE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, densitySimBase)

	// ── Gateway real: los bots juegan de verdad mientras se les gobierna ─────
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth:        auth.Options{LoginPerMin: 600, APIRPS: 1_000, APIBurst: 2_000},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		}),
	})
	if err != nil {
		t.Fatalf("BuildHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── Orquestador + densidad: el MISMO cableado de cmd/bots ───────────────
	reg := prometheus.NewRegistry()
	orch, err := bots.NewOrchestrator(pool, bots.Options{
		Traders:             densityTraders,
		TransformerMarginBP: bots.DefaultTransformerMarginBP,
		FreighterMarginBP:   bots.DefaultFreighterMarginBP,
		SecretSeed:          densitySecretSeed, Capital: densityCapital,
		Tick: 200 * time.Millisecond, Addr: ":0", APIURL: srv.URL + gateway.APIPrefix,
	}, ledger.DefaultOptions(), logger, reg)
	if err != nil {
		t.Fatalf("bots.NewOrchestrator: %v", err)
	}

	// Configuración del lazo: base = toda la población aprovisionada y sin
	// bonos de actividad ni cobertura, para AISLAR la señal de carga; rampa de
	// lag corta (un puñado de eventos satura) y paso amplio para que el efecto
	// se vea en pocos ciclos.
	densityOpts := bots.DefaultDensityOptions()
	densityOpts.Interval = densityCycle
	densityOpts.BaseBP = 10_000
	densityOpts.ActivityGainBP = 0
	densityOpts.CoverageMin = 0
	densityOpts.LagLow = 1
	densityOpts.LagHigh = 10
	densityOpts.LoadFloorBP = 0
	densityOpts.MaxStep = densityTraders
	densityOpts.Hysteresis = 0
	if err := densityOpts.Validate(); err != nil {
		t.Fatalf("configuración de densidad inválida: %v", err)
	}
	density, err := bots.NewDensityController(pool, densityOpts, orch, logger, reg)
	if err != nil {
		t.Fatalf("bots.NewDensityController: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var wg sync.WaitGroup
	var rmu sync.Mutex
	var runnerErrs []string
	startRunner := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(runCtx); err != nil {
				rmu.Lock()
				runnerErrs = append(runnerErrs, fmt.Sprintf("%s: %v", name, err))
				rmu.Unlock()
			}
		}()
	}
	startRunner("bots_orchestrator", orch.Run)
	startRunner("density_controller", density.Run)

	// ── (1) Arranque: la población activa llega al techo aprovisionado ───────
	densityWaitActive(t, reg, densityTraders, 60*time.Second,
		"fase (1): el orquestador arranca la población completa")
	provisionedAccounts := countRows(t, ctx, pool, `
		SELECT count(*) FROM auth.bot_profiles bp
		  JOIN auth.accounts a ON a.id = bp.account_id
		 WHERE bp.archetype::text = 'arbitrageur' AND a.status::text = 'active'`)
	if provisionedAccounts != densityTraders {
		t.Fatalf("cuentas de bot aprovisionadas: %d, esperadas %d", provisionedAccounts, densityTraders)
	}
	// Las plazas activas son bots que JUEGAN de verdad: cada una abre su sesión
	// por la API pública (POST /auth/sessions del SDK), no una goroutine vacía.
	pollPhase(t, 60*time.Second, "fase (1): los bots activos abren sesión por la API", nil,
		func() (bool, string) {
			n := countRows(t, ctx, pool, `
				SELECT count(*) FROM auth.sessions s
				  JOIN auth.accounts a ON a.id = s.account_id
				 WHERE a.kind::text = 'bot' AND s.expires_at > now()`)
			return n >= densityTraders, fmt.Sprintf("sesiones vivas de bots: %d", n)
		})

	// ── (2) Carga alta sintética: el controlador RECORTA la población ────────
	// El lag del outbox es la señal de saturación del SAD §13: eventos DE SUS
	// TIPOS SUSCRITOS pendientes del consumidor más retrasado. Con LagHigh=10 y 40
	// eventos sin consumir, el factor de carga cae a su suelo (0) y el objetivo
	// se va a cero: ante saturación se reduce la población de bots ANTES que
	// degradar la experiencia humana (GDD §19).
	densityInsertOutboxEvents(t, ctx, pool, densityLagEvents)
	densitySetConsumerCursor(t, ctx, pool, densityProbeConsumer, 0)

	densityWaitActive(t, reg, 0, 60*time.Second,
		"fase (2): con el outbox saturado la densidad para a los bots")
	target := popScrapeByLabel(t, reg, "ii_bots_density_target", "archetype")
	if target[bots.ArchetypeTrader] != 0 {
		t.Fatalf("ii_bots_density_target{archetype=trader} con el sistema saturado: %v, esperado 0",
			target[bots.ArchetypeTrader])
	}
	adjustments := popScrapeByLabel(t, reg, "ii_bots_density_adjustments_total", "direction")
	if adjustments["down"] < densityTraders {
		t.Fatalf("ii_bots_density_adjustments_total{direction=down}: %v, esperado al menos %d",
			adjustments["down"], densityTraders)
	}
	// El recorte es una PAUSA, no un retiro (ADR-024): las cuentas siguen
	// activas y conservan su capital para cuando se las reanude.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM auth.bot_profiles bp
		  JOIN auth.accounts a ON a.id = bp.account_id
		 WHERE bp.archetype::text = 'arbitrageur' AND a.status::text = 'active'`); n != densityTraders {
		t.Fatalf("cuentas de bot activas tras el recorte: %d, esperadas %d (la densidad pausa, no retira)",
			n, densityTraders)
	}
	if lag := popScrapeByLabel(t, reg, "ii_outbox_lag_observed", ""); lag[""] < float64(densityLagEvents) {
		t.Fatalf("ii_outbox_lag_observed: %v, esperado al menos %d", lag[""], densityLagEvents)
	}

	// ── (3) Normalización: el cursor alcanza la cabecera y se reanuda ────────
	densityAdvanceConsumerCursor(t, ctx, pool, densityProbeConsumer)
	densityWaitActive(t, reg, densityTraders, 60*time.Second,
		"fase (3): normalizada la carga, la densidad restaura la población")
	// El arranque inicial lo hace el propio orquestador y NO cuenta como ajuste:
	// los arranques contados son las plazas que REANUDÓ la densidad.
	adjustments = popScrapeByLabel(t, reg, "ii_bots_density_adjustments_total", "direction")
	if adjustments["up"] < densityTraders {
		t.Fatalf("ii_bots_density_adjustments_total{direction=up}: %v, esperado al menos %d (reanudación)",
			adjustments["up"], densityTraders)
	}

	// ── Apagado ordenado ─────────────────────────────────────────────────────
	stopRun()
	wg.Wait()
	rmu.Lock()
	errsCopy := append([]string(nil), runnerErrs...)
	rmu.Unlock()
	if len(errsCopy) > 0 {
		t.Fatalf("procesos de fondo terminaron con error: %v", errsCopy)
	}
	if got := popScrapeByLabel(t, reg, "ii_bots_active", "archetype")[bots.ArchetypeTrader]; got != 0 {
		t.Fatalf("ii_bots_active{archetype=trader} tras el apagado: %v, esperado 0", got)
	}
	assertBalancedLedger(t, ctx, pool)
}

// ─── Auxiliares del test de densidad ─────────────────────────────────────────

// densityWaitActive espera a que el gauge ii_bots_active{archetype=trader}
// alcance el valor esperado, describiendo el último estado observado si vence
// el plazo.
func densityWaitActive(t *testing.T, reg *prometheus.Registry, want int, timeout time.Duration, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last float64
	for {
		last = popScrapeByLabel(t, reg, "ii_bots_active", "archetype")[bots.ArchetypeTrader]
		if last == float64(want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout en %s (ii_bots_active{archetype=trader}=%v, esperado %d)", desc, last, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// densityInsertOutboxEvents inyecta eventos sintéticos en el outbox (cabecera
// de seq): la carga que el sistema aún no ha digerido.
func densityInsertOutboxEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, n int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox.events (aggregate_type, aggregate_id, event_type, payload, sim_time_at)
		SELECT 'e2e_density', uuidv7(), $3, '{}'::jsonb, $2
		FROM generate_series(1, $1)`, n, densitySimBase, densityProbeEvent); err != nil {
		t.Fatalf("insertando eventos de outbox: %v", err)
	}
}

// densitySetConsumerCursor fija el cursor de un consumidor lógico SUSCRITO a
// densityProbeEvent (lag sintético: el consumidor con más eventos de SUS tipos
// pendientes manda).
func densitySetConsumerCursor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, seq int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox.consumer_cursors (consumer_name, last_seq, event_types)
		VALUES ($1, $2, ARRAY[$3])
		ON CONFLICT (consumer_name) DO UPDATE
		   SET last_seq = EXCLUDED.last_seq, event_types = EXCLUDED.event_types`,
		name, seq, densityProbeEvent); err != nil {
		t.Fatalf("fijando el cursor del consumidor: %v", err)
	}
}

// densityAdvanceConsumerCursor lleva el cursor a la cabecera del outbox
// (lag 0: el sistema se ha puesto al día).
func densityAdvanceConsumerCursor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE outbox.consumer_cursors
		   SET last_seq = (SELECT COALESCE(max(seq), 0) FROM outbox.events)
		 WHERE consumer_name = $1`, name); err != nil {
		t.Fatalf("avanzando el cursor del consumidor: %v", err)
	}
}
