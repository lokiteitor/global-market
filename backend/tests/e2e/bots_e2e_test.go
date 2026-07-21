// Economía de bots del Incremento 4 de proceso a proceso contra una BD real:
// el gateway COMPLETO (internal/gateway.BuildServer: el mismo árbol de rutas
// que cmd/gateway MÁS el router del Notification/Event Gateway WS, ADR-023)
// servido con httptest, TODOS los motores del engine corriendo como goroutines
// (worker CCRI, motor de producción, motor de tránsito, shipment_creator,
// delivery_confirmer y el agregador OHLC) y el Bot Orchestration Service REAL
// (internal/bots.Orchestrator, ADR-024) ejecutando los bucles de decisión de
// 1 coal_producer + 1 iron_producer + 1 trader con tick corto, donde todo el
// gameplay pasa por pkg/botsdk contra la API pública. Ningún mock.
//
// Fases (el reloj de simulación se CONGELA y se avanza por SQL, patrón del
// resto de la suite; el test solo AVANZA EL RELOJ y OBSERVA — las decisiones
// son de los bots):
//
//	(a) Ambos productores completan su SETUP por la API (concesión → mina →
//	    operational → receta → lotes); el carbonero PRODUCE y mantiene su
//	    venta; el hierro publica su solicitud de compra de coal.
//	(b) El carbonero acepta la compra, el sorteo confirma el contrato
//	    (cross-node), el shipment_creator materializa el cargamento y el bot
//	    DESPACHA (camión + plan de ruta + ruta + dispatch); el motor de
//	    tránsito entrega avanzando el reloj y el delivery_confirmer LIQUIDA.
//	    Con el combustible entregado, el hierro PUEDE SEGUIR PRODUCIENDO.
//	(c) Norte publica una ganga bajo el umbral del trader y el trader la
//	    COMPRA (liquidación in situ del sorteo).
//	(d) El agregador OHLC registra las velas de los trades de bots (el
//	    volumen de coal de las velas coincide con lo liquidado bot↔bot).
//	(e) Un cliente WS del SDK conectado a la room corp del iron_producer
//	    recibió al menos un frame event contract.* enrutado por el gateway.
//
// Asserts contables finales sobre la BD ya QUIESCENTE (bots y motores
// detenidos): para cada bot, caja == capital − gastos (canon, build_cost,
// camión, salarios) + comercio liquidado (ventas − compras, del dominio) −
// dinero aún bloqueado en espejos (escrow/garantías), con la lista CERRADA de
// kinds que pueden tocar su caja; el ledger cuadra a cero por activo y la
// reconciliación física↔contable es 0.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// Parámetros del ciclo de bots que el test reproduce en sus aserciones.
const (
	botsSimBase    int64 = 1_000_000
	botsCapital    int64 = 500_000
	botsSecretSeed       = "e2e-bots-seed"

	// botsBatchStep rebasa la duración de un lote (3600 s de sim) por barrido.
	botsBatchStep int64 = 4_000
	// botsDrawStep avanza poco durante la fase de despacho: los sorteos son
	// wall-clock, pero el carbonero puede necesitar completar lotes para tener
	// stock libre que aceptar, sin consumir el plazo del contrato.
	botsDrawStep int64 = 2_000
	// botsTransitStep rebasa el tiempo de viaje de cualquier segmento del test.
	botsTransitStep int64 = 5_000

	// Nombres de la población (claves de idempotencia del provisioning).
	botsCoalName   = "Bot Carbonera 01"
	botsIronName   = "Bot Minera 01"
	botsTraderName = "Bot Mercader 01"

	// Ganga de Norte para el trader: 90 = 90% del base_price 100 de iron_ore,
	// bajo el umbral de compra del arquetipo (95%).
	botsGangaQty   int64 = 200
	botsGangaPrice int64 = 90
)

func TestBotsEconomyE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado (incluye la cadena del carbón del Incremento 4) ───────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, botsSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'coal'`)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	junctionID := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE kind = 'junction' AND region_id = $1`, regionID)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	norteNode, norteWH := warehouseNodeOf(t, ctx, pool, norteID)

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

	// Motores del engine con el MISMO lector congelado (CacheTTL 0).
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
	deliveryConfirmer, err := contracts.NewDeliveryConfirmer(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewDeliveryConfirmer: %v", err)
	}
	dcConsumer := deliveryConfirmer.NewConsumer(pool, outbox.WithLogger(logger))
	aggregator, err := market.NewAggregator(market.DefaultOptions(), market.NewMetrics(prometheus.NewRegistry()), logger)
	if err != nil {
		t.Fatalf("market.NewAggregator: %v", err)
	}
	ohlcConsumer := aggregator.NewConsumer(pool, outbox.WithLogger(logger))

	startRunner("ccri_worker", ccriWorker.Run)
	startRunner("production_worker", prodWorker.Run)
	startRunner("transit_worker", transitWorker.Run)
	startRunner("shipment_creator", func(ctx context.Context) error {
		return scConsumer.Run(ctx, 50*time.Millisecond, shipmentCreator.Handle)
	})
	startRunner("delivery_confirmer", func(ctx context.Context) error {
		return dcConsumer.Run(ctx, 50*time.Millisecond, deliveryConfirmer.Handle)
	})
	startRunner("ohlc_aggregator", func(ctx context.Context) error {
		return ohlcConsumer.Run(ctx, 50*time.Millisecond, aggregator.Handle)
	})

	// ── Provisioning del Bot Orchestration Service (1+1+1) ───────────────────
	botsOpts := bots.Options{
		CoalProducers: 1, IronProducers: 1, Traders: 1,
		SecretSeed: botsSecretSeed, Capital: botsCapital,
		Tick: 150 * time.Millisecond, Addr: ":0", APIURL: apiURL,
	}
	orch, err := bots.NewOrchestrator(pool, botsOpts, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("bots.NewOrchestrator: %v", err)
	}
	provisioned, err := orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(provisioned) != 3 {
		t.Fatalf("bots aprovisionados: %d, esperados 3", len(provisioned))
	}
	byName := map[string]bots.ProvisionedBot{}
	for _, b := range provisioned {
		byName[b.Name] = b
		if got := cashOf(t, ctx, pool, b.AccountID); got != botsCapital {
			t.Fatalf("caja de %s tras capitalizar: %d, esperado %d", b.Name, got, botsCapital)
		}
	}
	for _, name := range []string{botsCoalName, botsIronName, botsTraderName} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("falta el bot %q en la población aprovisionada", name)
		}
	}
	coalBotID := byName[botsCoalName].AccountID
	ironBotID := byName[botsIronName].AccountID
	traderBotID := byName[botsTraderName].AccountID

	// ── Cliente WS del SDK: observador de la corp del iron_producer, suscrito
	//    ANTES de arrancar los bucles para presenciar los contract.* ──────────
	ironBot := byName[botsIronName]
	wsClient, err := botsdk.New(botsdk.Options{BaseURL: apiURL})
	if err != nil {
		t.Fatalf("botsdk.New: %v", err)
	}
	if _, err := wsClient.Login(ctx, ironBot.Name, ironBot.Secret); err != nil {
		t.Fatalf("login del observador WS (%s): %v", ironBot.Name, err)
	}
	wsConn, err := wsClient.Connect(ctx, botsdk.WSOptions{})
	if err != nil {
		t.Fatalf("Connect WS: %v", err)
	}
	defer func() { _ = wsConn.Close() }()
	if _, err := wsConn.JoinCorp(ctx); err != nil {
		t.Fatalf("JoinCorp: %v", err)
	}
	var evMu sync.Mutex
	var wsEvents []botsdk.Event
	go func() {
		for ev := range wsConn.Events() {
			evMu.Lock()
			wsEvents = append(wsEvents, ev)
			evMu.Unlock()
		}
	}()

	// ── Bucles reales de los bots (una goroutine por bot, tick corto) ────────
	startRunner("bots_orchestrator", orch.Run)

	// ── (a) Setup de ambos productores; el carbonero produce y vende, el
	//        hierro pide su combustible al mercado ────────────────────────────
	const sqlMineOf = `
		SELECT b.id FROM world.buildings b
		  JOIN world.building_types bt ON bt.id = b.building_type_id
		 WHERE b.owner_account_id = $1 AND bt.code = $2`
	var coalMineID, ironMineID uuid.UUID
	pollPhase(t, 150*time.Second, "fase (a): setup y producción de los productores",
		func() { advanceSim(t, ctx, pool, botsBatchStep) },
		func() (bool, string) {
			if coalMineID == uuid.Nil {
				coalMineID = optionalUUID(t, ctx, pool, sqlMineOf, coalBotID, "coal_mine")
			}
			if ironMineID == uuid.Nil {
				ironMineID = optionalUUID(t, ctx, pool, sqlMineOf, ironBotID, "iron_mine")
			}
			if coalMineID == uuid.Nil || ironMineID == uuid.Nil {
				return false, fmt.Sprintf("minas construidas: coal=%v iron=%v", coalMineID != uuid.Nil, ironMineID != uuid.Nil)
			}
			coalStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.buildings WHERE id = $1`, coalMineID)
			ironStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.buildings WHERE id = $1`, ironMineID)
			coalSells := countRows(t, ctx, pool,
				`SELECT count(*) FROM ledger.publications WHERE publisher_account_id = $1 AND kind = 'sell'`, coalBotID)
			ironBuys := countRows(t, ctx, pool,
				`SELECT count(*) FROM ledger.publications WHERE publisher_account_id = $1 AND kind = 'buy'`, ironBotID)
			coalPhys := inventoryQtyOrZero(t, ctx, pool, coalMineID, coalID)
			ok := coalStatus == "operational" && ironStatus == "operational" &&
				(coalSells >= 1 || coalPhys >= 50) && ironBuys >= 1
			return ok, fmt.Sprintf("coal[%s sells=%d phys=%d] iron[%s buys=%d]",
				coalStatus, coalSells, coalPhys, ironStatus, ironBuys)
		})

	// Red vial hasta las minas de los bots: los bots construyen minas, pero
	// las carreteras son infraestructura del mundo (mismo fixture que la
	// integración de internal/bots).
	coalMineNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, coalMineID)
	ironMineNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, ironMineID)
	linkNodesBothWays(t, ctx, pool, regionID, coalMineNode, junctionID)
	linkNodesBothWays(t, ctx, pool, regionID, ironMineNode, junctionID)

	// ── (b1) El carbonero acepta la compra del hierro y DESPACHA ─────────────
	var fuelContractID uuid.UUID
	pollPhase(t, 120*time.Second, "fase (b1): aceptación, contrato de combustible y despacho",
		func() { advanceSim(t, ctx, pool, botsDrawStep) },
		func() (bool, string) {
			if fuelContractID == uuid.Nil {
				fuelContractID = optionalUUID(t, ctx, pool, `
					SELECT id FROM ledger.contracts
					 WHERE buyer_account_id = $1 AND seller_account_id = $2 AND product_id = $3
					 ORDER BY created_at LIMIT 1`, ironBotID, coalBotID, coalID)
			}
			if fuelContractID == uuid.Nil {
				return false, "sin contrato de combustible aún"
			}
			cStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM ledger.contracts WHERE id = $1`, fuelContractID)
			shStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.shipments WHERE contract_id = $1`, fuelContractID)
			ok := cStatus == "settled" || shStatus == "in_transit" || shStatus == "delivered"
			return ok, fmt.Sprintf("contrato=%s cargamento=%s", cStatus, shStatus)
		})
	// El despacho es del bot: compró su camión (flota acotada a 1).
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, coalBotID); n != 1 {
		t.Fatalf("flota del coal_producer: %d camiones, esperado 1", n)
	}

	// ── (b2) Tránsito real hasta la mina del hierro y LIQUIDACIÓN ────────────
	pollPhase(t, 120*time.Second, "fase (b2): tránsito, entrega y liquidación del combustible",
		func() { advanceSim(t, ctx, pool, botsTransitStep) },
		func() (bool, string) {
			cStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM ledger.contracts WHERE id = $1`, fuelContractID)
			return cStatus == "settled", "contrato=" + cStatus
		})
	var fillBP int
	var fuelAgreed, fuelDelivered, fuelUnitPrice int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(fill_bp, 0), quantity_agreed, quantity_delivered, unit_price
		  FROM ledger.contracts WHERE id = $1`, fuelContractID).
		Scan(&fillBP, &fuelAgreed, &fuelDelivered, &fuelUnitPrice); err != nil {
		t.Fatalf("contrato de combustible liquidado: %v", err)
	}
	if fillBP != 10_000 || fuelDelivered <= 0 || fuelDelivered != fuelAgreed {
		t.Fatalf("liquidación del combustible inesperada: fill=%d entregado=%d acordado=%d",
			fillBP, fuelDelivered, fuelAgreed)
	}
	if fuelUnitPrice != 66 {
		t.Fatalf("precio del combustible: %d, esperado 66 (110%% del base_price 60 de coal)", fuelUnitPrice)
	}

	// ── (b3) Con el coal entregado, el hierro PUEDE SEGUIR PRODUCIENDO ───────
	pollPhase(t, 120*time.Second, "fase (b3): el iron_producer produce con el combustible entregado",
		func() { advanceSim(t, ctx, pool, botsBatchStep) },
		func() (bool, string) {
			phys := inventoryQtyOrZero(t, ctx, pool, ironMineID, ironOreID)
			return phys >= 50, fmt.Sprintf("iron_ore físico en la mina del hierro: %d", phys)
		})

	// ── (c) El trader compra una venta del tablón (ganga bajo su umbral) ─────
	norteToken := login(t, srv, traderName, traderSecret)
	r := call(t, srv, http.MethodPost, "/api/v1/contracts/publications", norteToken, map[string]any{
		"kind":                 "sell",
		"product_id":           ironOreID.String(),
		"quantity_total":       itoa(botsGangaQty),
		"unit_price":           itoa(botsGangaPrice),
		"min_lot":              "50",
		"origin_node_id":       norteNode.String(),
		"delivery_sim_seconds": 86_400,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar la ganga de Norte: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	pollPhase(t, 60*time.Second, "fase (c): el trader compra la ganga y liquida in situ",
		nil,
		func() (bool, string) {
			id := optionalUUID(t, ctx, pool, `
				SELECT id FROM ledger.contracts
				 WHERE buyer_account_id = $1 AND seller_account_id = $2 AND product_id = $3 AND status = 'settled'`,
				traderBotID, norteID, ironOreID)
			return id != uuid.Nil, "sin contrato liquidado del trader aún"
		})
	// La venta era in situ: el stock del trader reposa en el almacén de Norte
	// (libre o ya congelado por su re-listado con margen).
	if got := stockTotalOf(t, ctx, pool, traderBotID, ironOreID, norteWH); got != botsGangaQty {
		t.Fatalf("stock de iron_ore del trader en el almacén de Norte: %d, esperado %d", got, botsGangaQty)
	}

	// ── (d) OHLC: las velas de coal reflejan exactamente los trades bot↔bot ──
	pollPhase(t, 60*time.Second, "fase (d): velas OHLC de los trades de bots",
		nil,
		func() (bool, string) {
			var wantVol int64
			if err := pool.QueryRow(ctx, `
				SELECT COALESCE(SUM(quantity_delivered), 0) FROM ledger.contracts
				 WHERE product_id = $1 AND status = 'settled'`, coalID).Scan(&wantVol); err != nil {
				t.Fatalf("volumen liquidado de coal: %v", err)
			}
			r := call(t, srv, http.MethodGet,
				"/api/v1/market/ohlc?product_id="+coalID.String()+"&region_id="+regionID.String(), norteToken, nil)
			if r.status != http.StatusOK {
				return false, fmt.Sprintf("market/ohlc status %d", r.status)
			}
			candles, ok := r.body["data"].([]any)
			if !ok || len(candles) == 0 {
				return false, "sin velas de coal aún"
			}
			var vol, cc int64
			for i, item := range candles {
				c := asMap(t, item, fmt.Sprintf("candle[%d]", i))
				vol += int64Str(t, c["volume"], "volume")
				cc += int64Num(t, c["contract_count"], "contract_count")
			}
			return wantVol > 0 && vol == wantVol && cc >= 1,
				fmt.Sprintf("velas=%d volumen=%d esperado=%d contratos=%d", len(candles), vol, wantVol, cc)
		})

	// ── (e) El cliente WS del SDK recibió los contract.* de la corp ──────────
	pollPhase(t, 30*time.Second, "fase (e): frames event contract.* por el WS del SDK",
		nil,
		func() (bool, string) {
			evMu.Lock()
			defer evMu.Unlock()
			contractEvents := 0
			for _, ev := range wsEvents {
				if strings.HasPrefix(ev.EventType, "contract.") {
					contractEvents++
				}
			}
			return contractEvents >= 1,
				fmt.Sprintf("frames event recibidos: %d (contract.*: %d)", len(wsEvents), contractEvents)
		})
	evMu.Lock()
	var sample *botsdk.Event
	for i := range wsEvents {
		if strings.HasPrefix(wsEvents[i].EventType, "contract.") {
			sample = &wsEvents[i]
			break
		}
	}
	evMu.Unlock()
	if sample == nil || sample.Seq <= 0 || sample.EventID == "" || len(sample.Payload) == 0 {
		t.Fatalf("frame event contract.* incompleto: %+v", sample)
	}

	// ── Apagado ordenado: bots y motores se detienen antes de auditar ────────
	stopRun()
	wg.Wait()
	rmu.Lock()
	errsCopy := append([]string(nil), runnerErrs...)
	rmu.Unlock()
	if len(errsCopy) > 0 {
		t.Fatalf("procesos de fondo terminaron con error: %v", errsCopy)
	}

	// ── Asserts contables sobre la BD quiescente ─────────────────────────────
	assertBalancedLedger(t, ctx, pool)
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable final: %d divergencias (err %v), esperado 0", disc, err)
	}
	for _, b := range provisioned {
		auditBotCash(t, ctx, pool, b.Name, b.AccountID, botsCapital)
	}
}

// ─── Auditoría contable de la caja de un bot ─────────────────────────────────

// botCashKinds es la lista CERRADA de kinds de transacción que pueden tocar la
// caja de un bot en este incremento (capitalización; canon del suelo;
// maintenance = build_cost del edificio y compra del camión, el kind del
// contexto world; salarios; y el ciclo CCRI completo). Cualquier otro
// movimiento delata un flujo no contemplado y el test falla.
var botCashKinds = map[string]bool{
	"bot_capitalization":    true,
	"canon":                 true,
	"maintenance":           true,
	"wage":                  true,
	"publication_lock":      true,
	"publication_release":   true,
	"acceptance_lock":       true,
	"contract_confirmation": true,
	"delivery_settlement":   true,
}

// auditBotCash verifica la coherencia de la caja de un bot contra el DOMINIO:
// caja == capital − gastos (canon + maintenance + wage, leídos por kind del
// extracto) + comercio liquidado (ventas − compras de ledger.contracts) −
// dinero aún bloqueado en sus cuentas espejo (escrow/guarantee). Exige además
// que ningún kind fuera de la lista cerrada haya movido la caja y que todos
// sus contratos estén activos o liquidados al 100% (sin forfeits que romperían
// la identidad).
func auditBotCash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string, botID uuid.UUID, capital int64) {
	t.Helper()

	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.contracts
		 WHERE (buyer_account_id = $1 OR seller_account_id = $1)
		   AND (status NOT IN ('active', 'settled')
		        OR (status = 'settled' AND (fill_bp IS NULL OR fill_bp <> 10000)))`, botID); n != 0 {
		t.Fatalf("bot %s: %d contratos fallidos o liquidados con fill parcial", name, n)
	}

	rows, err := pool.Query(ctx, `
		SELECT t.kind::text, COALESCE(SUM(e.amount), 0)
		  FROM ledger.entries e
		  JOIN ledger.transactions t ON t.id = e.transaction_id
		  JOIN ledger.accounts a ON a.id = e.account_id
		 WHERE a.kind = 'cash' AND a.owner_account_id = $1
		 GROUP BY t.kind`, botID)
	if err != nil {
		t.Fatalf("extracto de caja de %s: %v", name, err)
	}
	defer rows.Close()
	sums := map[string]int64{}
	for rows.Next() {
		var kind string
		var total int64
		if err := rows.Scan(&kind, &total); err != nil {
			t.Fatalf("extracto de caja de %s: %v", name, err)
		}
		if !botCashKinds[kind] {
			t.Fatalf("bot %s: la caja se movió con un kind inesperado %q (neto %d)", name, kind, total)
		}
		sums[kind] = total
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("extracto de caja de %s: %v", name, err)
	}
	if sums["bot_capitalization"] != capital {
		t.Fatalf("bot %s: capitalización asentada %d, esperado %d (única)", name, sums["bot_capitalization"], capital)
	}
	var gastos int64
	for _, kind := range []string{"canon", "maintenance", "wage"} {
		if sums[kind] > 0 {
			t.Fatalf("bot %s: el kind de gasto %s suma positivo en caja (%d)", name, kind, sums[kind])
		}
		gastos += -sums[kind]
	}

	// Comercio liquidado según el DOMINIO: ventas cobradas − compras pagadas.
	var tradeNet int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(CASE WHEN seller_account_id = $1 THEN  quantity_delivered * unit_price
		                         ELSE                            -(quantity_delivered * unit_price) END), 0)
		  FROM ledger.contracts
		 WHERE status = 'settled' AND (seller_account_id = $1 OR buyer_account_id = $1)`, botID).Scan(&tradeNet); err != nil {
		t.Fatalf("comercio liquidado de %s: %v", name, err)
	}

	// Dinero aún bloqueado en cuentas espejo propias (escrow de compras y
	// garantías de publicaciones/aceptaciones/contratos vivos).
	var locked int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE owner_account_id = $1 AND kind IN ('escrow', 'guarantee')`, botID).Scan(&locked); err != nil {
		t.Fatalf("bloqueos en espejo de %s: %v", name, err)
	}
	if locked < 0 {
		t.Fatalf("bot %s: bloqueos en espejo negativos (%d)", name, locked)
	}

	cash := cashOf(t, ctx, pool, botID)
	want := capital - gastos + tradeNet - locked
	if cash != want {
		t.Fatalf("caja de %s incoherente: %d, esperado %d = capital %d − gastos %d + comercio %d − bloqueado %d",
			name, cash, want, capital, gastos, tradeNet, locked)
	}
}

// ─── Polling por fases ───────────────────────────────────────────────────────

// pollPhase espera a que cond se cumpla, ejecutando step (p. ej. avanzar el
// reloj congelado por SQL) antes de cada comprobación. step puede ser nil
// (fases puramente wall-clock). Falla con timeout describiendo el último
// estado observado.
func pollPhase(t *testing.T, timeout time.Duration, desc string, step func(), cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if step != nil {
			step()
		}
		ok, state := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout en %s (último estado: %s)", desc, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ─── Lecturas auxiliares tolerantes a ausencia de fila ───────────────────────

// optionalUUID devuelve uuid.Nil si la consulta no encuentra fila.
func optionalUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, sql, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil
	}
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

// stringOrEmpty devuelve "" si la consulta no encuentra fila.
func stringOrEmpty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var s string
	err := pool.QueryRow(ctx, sql, args...).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return s
}

// inventoryQtyOrZero devuelve el inventario físico de un producto en un
// edificio, o 0 si aún no hay fila.
func inventoryQtyOrZero(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	t.Helper()
	var q int64
	err := pool.QueryRow(ctx,
		`SELECT quantity FROM world.building_inventories WHERE building_id = $1 AND product_id = $2`,
		building, product).Scan(&q)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatalf("inventario físico (%s, %s): %v", building, product, err)
	}
	return q
}

// stockTotalOf suma el stock (libre + reservado) de un dueño para un producto
// en un almacén concreto.
func stockTotalOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind IN ('stock_free', 'stock_reserved')
		   AND owner_account_id = $1 AND product_id = $2 AND warehouse_building_id = $3`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock total de %s: %v", owner, err)
	}
	return b
}

// ─── Fixture de red vial (misma forma que la red del seed) ───────────────────

// linkNodesBothWays crea enlaces road dirigidos en ambos sentidos entre dos
// nodos, con su segmento único y congestión fluida: los bots construyen minas,
// pero las carreteras son infraestructura del mundo.
func linkNodesBothWays(t *testing.T, ctx context.Context, pool *pgxpool.Pool, regionID, a, b uuid.UUID) {
	t.Helper()
	ax, ay := nodeXY(t, ctx, pool, a)
	bx, by := nodeXY(t, ctx, pool, b)
	length := int64(math.Round(math.Hypot(bx-ax, by-ay)))
	if length < 1_000 {
		length = 1_000 // nodos casi coincidentes: longitud mínima de trazado
	}
	insertRoadLink(t, ctx, pool, regionID, a, ax, ay, b, bx, by, length)
	insertRoadLink(t, ctx, pool, regionID, b, bx, by, a, ax, ay, length)
}

func insertRoadLink(t *testing.T, ctx context.Context, pool *pgxpool.Pool, regionID, from uuid.UUID, fx, fy float64, to uuid.UUID, tx, ty float64, lengthM int64) {
	t.Helper()
	path := fmt.Sprintf("LINESTRING(%f %f, %f %f)", fx, fy, tx, ty)
	linkID := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_links
		       (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1, 'road', $2, $3, ST_GeomFromText($4, 0), $5, 60, 80)`,
		linkID, from, to, path, lengthM); err != nil {
		t.Fatalf("creando el enlace road %s→%s: %v", from, to, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema)
		VALUES ($1, $2, $3, 1, ST_GeomFromText($4, 0), $5, 1.0)`,
		uuid.Must(uuid.NewV7()), linkID, regionID, path, lengthM); err != nil {
		t.Fatalf("creando el segmento del enlace %s: %v", linkID, err)
	}
}

func nodeXY(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nodeID uuid.UUID) (float64, float64) {
	t.Helper()
	var x, y float64
	if err := pool.QueryRow(ctx,
		`SELECT ST_X(location), ST_Y(location) FROM world.network_nodes WHERE id = $1`, nodeID).Scan(&x, &y); err != nil {
		t.Fatalf("ubicación del nodo %s: %v", nodeID, err)
	}
	return x, y
}
