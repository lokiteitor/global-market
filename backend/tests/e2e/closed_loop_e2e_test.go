// CADENA INDUSTRIAL CERRADA ENTRE BOTS, SIN NINGUNA PARTICIPACIÓN HUMANA
// (GDD §13.4 «mundo vivo»: la población es contraparte y backstop de liquidez
// de los DOS lados del tablón). Mismo montaje de proceso a proceso que
// population_e2e_test.go —gateway COMPLETO, motores del engine reales y el Bot
// Orchestration Service con los cinco arquetipos jugando por pkg/botsdk— con
// UNA diferencia que es toda la prueba: NINGUNA corporación humana abre sesión
// ni toca la API. Demo y Norte existen en el mundo sembrado y se quedan
// mirando.
//
// Lo que demuestra, y que population_e2e_test.go NO puede demostrar porque
// Norte sirve allí las dos compras del transformador:
//
//	(a) COMBUSTIBLE: el minero de hierro publica su solicitud de coal y el
//	    CARBONERO la atiende, deposita garantía, despacha con camión propio y
//	    la entrega física liquida el contrato. La mina de hierro arranca.
//	(b) INSUMO ANCLA: el transformador publica su solicitud de iron_ore y el
//	    MINERO DE HIERRO la atiende — el eslabón que faltaba: hasta ahora el
//	    hierro solo publicaba ventas y jamás cruzaba una `buy`, así que el
//	    horno se quedaba sin insumo indefinidamente aunque el comprador pagase
//	    MÁS (110) que el precio al que el propio minero vendía (100).
//	(c) SUMINISTRO SOSTENIDO: la solicitud del horno se sirve por LOTES —cada
//	    aceptación está acotada por el stock libre del momento—, así que el
//	    minero vuelve a atenderla en cuanto su aceptación anterior sale del
//	    sorteo. Se exigen DOS entregas: una sola probaría el cruce, no el
//	    abastecimiento continuo.
//	(d) MANUFACTURA: con insumo y combustible propios de la población, el
//	    transformador funde acero y lo publica con margen sobre el coste.
//
// SECUENCIA DEL RELOJ: el reloj de simulación se CONGELA y solo lo avanza el
// test, a pasos pequeños (clSimStep) para que el tiempo de reacción de los bots
// —wall-clock: sorteo del CCRI, compra de camión, plan de ruta y despacho— sea
// despreciable frente a los plazos de entrega en sim-time de los contratos.
//
// El agregador OHLC NO corre a propósito (igual que en la población completa):
// sin velas, el precio de referencia sale del catálogo y el margen del
// transformador es reproducible.
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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/notify"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

const (
	clSimBase    int64 = 2_000_000
	clSecretSeed       = "e2e-closed-loop-seed"
	clTick             = 100 * time.Millisecond

	// clSimStep es el paso de reloj por vuelta del poll. Es pequeño a
	// propósito: marca la relación sim-time/wall-clock del test, y con los
	// plazos de entrega de las solicitudes (2 días de sim = 172 800 s) deja a
	// los bots decenas de segundos de reloj real para reaccionar a cada
	// contrato. Un paso grande haría que los contratos venciesen antes de que
	// el vendedor pudiera comprar camión y despachar.
	clSimStep int64 = 1_000

	// clIronDeliveries es el número de entregas de iron_ore del minero al
	// horno que exige la fase (c): DOS, para distinguir el cruce puntual del
	// abastecimiento sostenido.
	clIronDeliveries = 2
)

func TestBotsClosedLoopWithoutHumansE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado (cadena completa: carbón, hierro y alto horno) ────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, clSimBase)

	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'coal'`)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	steelID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'steel_ingot'`)
	// Las DOS corporaciones humanas del seed: existen y NO juegan. Sus ids solo
	// se usan para demostrar al final que no tocaron nada.
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)

	// ── Gateway COMPLETO: rutas del contrato + router de notificaciones WS ───
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	notifyOpts := notify.DefaultOptions()
	notifyOpts.RouterInterval = 50 * time.Millisecond
	server, err := gateway.BuildServer(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth:        auth.Options{LoginPerMin: 60, APIRPS: 1_000, APIBurst: 2_000},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			Notify:      notifyOpts,
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		}),
	})
	if err != nil {
		t.Fatalf("BuildServer: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", server.Handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	apiURL := srv.URL + gateway.APIPrefix

	// ── Procesos de fondo bajo runCtx: router WS, motores del engine y bots ──
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
	startRunner("notify_router", server.Run)

	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	prodWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: 100 * time.Millisecond, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{
		SweepInterval: 100 * time.Millisecond, BatchSize: 100,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	transitOpts := fleet.DefaultWorkerOptions()
	transitOpts.SweepInterval = 100 * time.Millisecond
	transitOpts.Roll = func() float64 { return 1.0 } // sin averías: ruta determinista
	transitWorker, err := fleet.NewTransitWorker(pool, reader, transitOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("fleet.NewTransitWorker: %v", err)
	}
	shipmentCreator := fleet.NewShipmentCreator(logger, prometheus.NewRegistry())
	scConsumer := shipmentCreator.NewConsumer(pool, outbox.WithLogger(logger))
	freightCreator := fleet.NewFreightShipmentCreator(logger, prometheus.NewRegistry())
	fcConsumer := freightCreator.NewConsumer(pool, outbox.WithLogger(logger))
	deliveryConfirmer, err := contracts.NewDeliveryConfirmer(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewDeliveryConfirmer: %v", err)
	}
	dcConsumer := deliveryConfirmer.NewConsumer(pool, outbox.WithLogger(logger))
	freightSettler, err := contracts.NewFreightSettler(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewFreightSettler: %v", err)
	}
	fsConsumer := freightSettler.NewConsumer(pool, outbox.WithLogger(logger))

	startRunner("ccri_worker", ccriWorker.Run)
	startRunner("production_worker", prodWorker.Run)
	startRunner("transit_worker", transitWorker.Run)
	startRunner("shipment_creator", func(ctx context.Context) error {
		return scConsumer.Run(ctx, 50*time.Millisecond, shipmentCreator.Handle)
	})
	startRunner("freight_shipment_creator", func(ctx context.Context) error {
		return fcConsumer.Run(ctx, 50*time.Millisecond, freightCreator.Handle)
	})
	startRunner("delivery_confirmer", func(ctx context.Context) error {
		return dcConsumer.Run(ctx, 50*time.Millisecond, deliveryConfirmer.Handle)
	})
	startRunner("freight_settler", func(ctx context.Context) error {
		return fsConsumer.Run(ctx, 50*time.Millisecond, freightSettler.Handle)
	})

	// ── Bot Orchestration Service: LOS CINCO ARQUETIPOS, uno de cada ─────────
	botsReg := prometheus.NewRegistry()
	orch, err := bots.NewOrchestrator(pool, bots.Options{
		CoalProducers: 1, IronProducers: 1, Traders: 1, Transformers: 1, Freighters: 1,
		TransformerMarginBP: bots.DefaultTransformerMarginBP,
		FreighterMarginBP:   bots.DefaultFreighterMarginBP,
		SecretSeed:          clSecretSeed, Capital: popCapital,
		Tick: clTick, Addr: ":0", APIURL: apiURL,
	}, ledger.DefaultOptions(), logger, botsReg)
	if err != nil {
		t.Fatalf("bots.NewOrchestrator: %v", err)
	}
	provisioned, err := orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(provisioned) != 5 {
		t.Fatalf("bots aprovisionados: %d, esperados 5 (los cinco arquetipos del GDD 13.2)", len(provisioned))
	}
	byName := map[string]bots.ProvisionedBot{}
	for _, b := range provisioned {
		byName[b.Name] = b
	}
	coalBotID := byName[popCoalName].AccountID
	ironBotID := byName[popIronName].AccountID
	transformerBotID := byName[popTransformerName].AccountID

	startRunner("bots_orchestrator", orch.Run)

	// ── (a) Implantación: las dos minas y el alto horno, por la API ──────────
	const sqlBuildingOf = `
		SELECT b.id FROM world.buildings b
		  JOIN world.building_types bt ON bt.id = b.building_type_id
		 WHERE b.owner_account_id = $1 AND bt.code = $2`
	var coalMineID, ironMineID, plantID uuid.UUID
	pollPhase(t, 180*time.Second, "fase (a): implantación de la cadena (dos minas + alto horno)",
		func() {
			advanceSim(t, ctx, pool, clSimStep)
			coalMineID = orZero(coalMineID, optionalUUID(t, ctx, pool, sqlBuildingOf, coalBotID, "coal_mine"))
			ironMineID = orZero(ironMineID, optionalUUID(t, ctx, pool, sqlBuildingOf, ironBotID, "iron_mine"))
			plantID = orZero(plantID, optionalUUID(t, ctx, pool, sqlBuildingOf, transformerBotID, "blast_furnace"))
		},
		func() (bool, string) {
			if coalMineID == uuid.Nil || ironMineID == uuid.Nil || plantID == uuid.Nil {
				return false, fmt.Sprintf("edificios: coal=%v iron=%v horno=%v",
					coalMineID != uuid.Nil, ironMineID != uuid.Nil, plantID != uuid.Nil)
			}
			plantStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.buildings WHERE id = $1`, plantID)
			recipe := stringOrEmpty(t, ctx, pool, `
				SELECT COALESCE(r.code, '') FROM world.buildings b
				  LEFT JOIN world.recipes r ON r.id = b.active_recipe_id
				 WHERE b.id = $1`, plantID)
			return plantStatus == "operational" && recipe == "smelt_steel",
				fmt.Sprintf("horno[%s receta=%s]", plantStatus, recipe)
		})
	plantNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, plantID)
	for _, n := range []uuid.UUID{
		queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, coalMineID),
		queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, ironMineID),
		plantNode,
	} {
		requireRoadSpur(t, ctx, pool, n)
	}

	// ── (b)+(c)+(d) La cadena se cierra SOLA: combustible → mineral → acero ──
	pollPhase(t, 600*time.Second, "fases (b)-(d): combustible al minero, mineral al horno y acero fundido",
		func() { advanceSim(t, ctx, pool, clSimStep) },
		func() (bool, string) {
			fuelN, fuelQty := clSupply(t, ctx, pool, coalBotID, ironBotID, coalID)
			plantCoalN, plantCoalQty := clSupply(t, ctx, pool, coalBotID, transformerBotID, coalID)
			ironN, ironQty := clSupply(t, ctx, pool, ironBotID, transformerBotID, ironOreID)
			steel := popStockFreeOrZero(t, ctx, pool, transformerBotID, steelID, plantID)
			steelSells := countRows(t, ctx, pool, sqlOpenSellOf, transformerBotID, steelID)
			ok := fuelN >= 1 && plantCoalN >= 1 && ironN >= clIronDeliveries &&
				steel >= popSteelPerBatch && steelSells >= 1
			return ok, fmt.Sprintf(
				"carbón→minero[%d entregas, %d u] carbón→horno[%d entregas, %d u] "+
					"hierro→horno[%d entregas, %d u] acero=%d ventas=%d",
				fuelN, fuelQty, plantCoalN, plantCoalQty, ironN, ironQty, steel, steelSells)
		})

	// ── Apagado ordenado: bots y motores se detienen antes de auditar ────────
	stopRun()
	wg.Wait()
	rmu.Lock()
	errsCopy := append([]string(nil), runnerErrs...)
	rmu.Unlock()
	if len(errsCopy) > 0 {
		t.Fatalf("procesos de fondo terminaron con error: %v", errsCopy)
	}

	// ── LA PRUEBA: NINGÚN humano tocó nada ───────────────────────────────────
	// Ni publicaciones, ni aceptaciones, ni contratos, ni vehículos: si Demo o
	// Norte hubieran participado, la cadena no sería de los bots.
	for _, human := range []struct {
		id   uuid.UUID
		name string
	}{{demoID, demoName}, {norteID, traderName}} {
		if n := countRows(t, ctx, pool,
			`SELECT count(*) FROM ledger.publications WHERE publisher_account_id = $1`, human.id); n != 0 {
			t.Fatalf("la corporación humana %s publicó %d veces: la cadena debe cerrarse SOLO entre bots", human.name, n)
		}
		if n := countRows(t, ctx, pool,
			`SELECT count(*) FROM ledger.contracts WHERE buyer_account_id = $1 OR seller_account_id = $1`, human.id); n != 0 {
			t.Fatalf("la corporación humana %s firmó %d contratos: la cadena debe cerrarse SOLO entre bots", human.name, n)
		}
		if n := countRows(t, ctx, pool,
			`SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, human.id); n != 0 {
			t.Fatalf("la corporación humana %s movió %d vehículos: la cadena debe cerrarse SOLO entre bots", human.name, n)
		}
	}

	// ── (b) COMBUSTIBLE: el carbonero abasteció la mina de hierro ────────────
	if n, qty := clSupply(t, ctx, pool, coalBotID, ironBotID, coalID); n < 1 || qty <= 0 {
		t.Fatalf("suministro de carbón del carbonero a la mina de hierro: %d contratos, %d unidades", n, qty)
	}

	// ── (c) INSUMO ANCLA SOSTENIDO: el minero sirvió el horno más de una vez ─
	ironN, ironQty := clSupply(t, ctx, pool, ironBotID, transformerBotID, ironOreID)
	if ironN < clIronDeliveries {
		t.Fatalf("entregas de iron_ore del minero al horno: %d, esperadas al menos %d "+
			"(una sola prueba el cruce, no el abastecimiento continuo)", ironN, clIronDeliveries)
	}
	if got := popStockInflow(t, ctx, pool, transformerBotID, ironOreID, plantID); got != ironQty {
		t.Fatalf("iron_ore que entró en el horno: %d, esperado %d (todo lo entregado por el minero)", got, ironQty)
	}

	// ── (d) MANUFACTURA: acero fundido y publicado con margen sobre el coste ─
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.production_batches
		 WHERE building_id = $1 AND status IN ('running', 'completed')`, plantID); n < 1 {
		t.Fatalf("lotes de fundición del horno: %d, esperado al menos 1", n)
	}
	var steelPrice, steelQty int64
	var steelOrigin uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT unit_price, quantity_total, origin_node_id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, transformerBotID, steelID).
		Scan(&steelPrice, &steelQty, &steelOrigin); err != nil {
		t.Fatalf("venta de acero del transformador: %v", err)
	}
	// Sin velas OHLC el coste unitario sale del catálogo —(20×100 + 10×60)/8 =
	// 325— y el precio es coste × 1,25 = 407.
	if steelPrice != 407 || steelOrigin != plantNode {
		t.Fatalf("venta de acero: precio=%d origen=%s, esperado 407 (coste 325 × 1,25) desde el horno %s",
			steelPrice, steelOrigin, plantNode)
	}
	if steelQty < popSteelPerBatch || steelQty%popSteelPerBatch != 0 {
		t.Fatalf("cantidad publicada de acero: %d, esperados lotes enteros de %d", steelQty, popSteelPerBatch)
	}

	// ── Los cinco arquetipos vivos y auditables durante toda la partida ──────
	decisions := popScrapeByLabel(t, botsReg, "ii_bot_decisions_total", "bot")
	for _, name := range []string{popCoalName, popIronName, popTraderName, popTransformerName, popFreighterName} {
		if decisions[name] < 1 {
			t.Fatalf("el bot %q no registró ninguna decisión auditable (ii_bot_decisions_total=%v)", name, decisions[name])
		}
	}

	// ── Coherencia final sobre la BD quiescente ──────────────────────────────
	assertBalancedLedger(t, ctx, pool)
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable final: %d divergencias (err %v), esperado 0", disc, err)
	}
}

// clSupply resume el suministro liquidado de un producto de un vendedor a un
// comprador: número de contratos y unidades entregadas. Es la medida de que la
// contraparte fue REAL (contrato liquidado con entrega física) y no una
// publicación colgada del tablón.
func clSupply(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seller, buyer, product uuid.UUID) (int, int64) {
	t.Helper()
	var n int
	var qty int64
	if err := pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(SUM(quantity_delivered), 0) FROM ledger.contracts
		 WHERE seller_account_id = $1 AND buyer_account_id = $2 AND product_id = $3
		   AND status = 'settled'`, seller, buyer, product).Scan(&n, &qty); err != nil {
		t.Fatalf("suministro liquidado %s→%s (%s): %v", seller, buyer, product, err)
	}
	return n, qty
}
