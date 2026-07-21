// POBLACIÓN COMPLETA de bots (GDD §13.2: los CINCO arquetipos) de proceso a
// proceso contra una BD real: el gateway COMPLETO (internal/gateway.BuildServer:
// el mismo árbol de rutas que cmd/gateway MÁS el router del Notification/Event
// Gateway WS), los motores del engine corriendo como goroutines (worker CCRI,
// motor de producción, motor de tránsito, shipment_creator,
// freight_shipment_creator, delivery_confirmer y freight_settler) y el Bot
// Orchestration Service REAL ejecutando 1 coal_producer + 1 iron_producer +
// 1 trader + 1 industrial_transformer + 1 freighter, donde TODO el gameplay de
// los bots pasa por pkg/botsdk contra la API pública (ADR-010). Ningún mock.
//
// Lo que este test añade sobre bots_e2e_test.go (que cubre la economía de los
// tres arquetipos v1) son los DOS ROLES NUEVOS del GDD §13.2 en la misma
// economía viva:
//
//	(a) SETUP DE LOS CINCO: los cinco bots completan su implantación por la API
//	    —minas de carbón y hierro, alto horno con receta smelt_steel— y el
//	    TRANSFORMADOR publica sus solicitudes de compra de insumos derivadas de
//	    la RECETA (200 de iron_ore y 100 de coal, con destino su horno).
//	(b) ABASTECIMIENTO: Norte (corporación HUMANA del seed) sirve esas dos
//	    compras con camión y ruta reales; la llegada física al nodo del horno
//	    liquida los contratos y deja el stock en la planta.
//	(c) MANUFACTURA: con los insumos dentro, el transformador FUNDE acero
//	    (3 lotes × 8 lingotes) y publica su venta con margen sobre el coste.
//	(d) FLETE (CCRI-Flete, GDD §5.3.2): Norte publica una solicitud de flete y
//	    el TRANSPORTISTA la acepta (garantía inmovilizada), compra su camión en
//	    el origen, despacha el cargamento AJENO y, al llegar físicamente, el
//	    settler liquida: la carga aparece en destino a nombre del cargador y el
//	    transportista cobra la tarifa y recupera la garantía.
//	(e) ARBITRAJE: el comerciante compra la ganga que Norte publica bajo su
//	    umbral, cerrando los cinco roles en la misma partida.
//
// SECUENCIA DEL RELOJ (por qué el orden importa): el reloj de simulación se
// CONGELA y solo lo avanza el test. Durante la fase (a) NO se avanza, así que
// ningún productor tiene existencias y las compras del transformador solo puede
// servirlas Norte; en cuanto el reloj corre, el carbonero produce y atiende la
// solicitud de combustible del hierro por su cuenta. Así la economía de bots
// sigue siendo libre, pero el test es determinista.
//
// El agregador OHLC NO corre a propósito: sin velas, el precio de referencia de
// los bots cae en el base_price del catálogo y el margen del transformador es
// reproducible (coste (20×100 + 10×60)/8 = 325 frente a 400 del acero). Las
// velas se cubren en bots_e2e_test.go.
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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

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

// Parámetros de la población completa que el test reproduce en sus aserciones.
const (
	popSimBase    int64 = 1_000_000
	popCapital    int64 = 500_000
	popSecretSeed       = "e2e-population-seed"
	popTick             = 150 * time.Millisecond
	popSimDay     int64 = 86_400

	// popSmeltStep rebasa la duración de un lote de smelt_steel (7200 s de sim)
	// y también el tiempo de viaje de cualquier segmento del test.
	popSmeltStep int64 = 8_000
	// popTransitStep rebasa el tiempo de viaje de un segmento (3600 s de sim).
	popTransitStep int64 = 5_000

	// Nombres de la población (claves de idempotencia del provisioning).
	popCoalName        = "Bot Carbonera 01"
	popIronName        = "Bot Minera 01"
	popTraderName      = "Bot Mercader 01"
	popTransformerName = "Bot Siderúrgica 01"
	popFreighterName   = "Bot Transportista 01"

	// Insumos que el transformador deriva de la receta smelt_steel (20 de
	// iron_ore + 10 de coal por lote × InputBuyBatches = 10).
	popIronBuyQty int64 = 200
	popCoalBuyQty int64 = 100
	// popSteelPerBatch es la salida de un lote de smelt_steel (el transformador
	// encola 3 de una vez, así que su venta es siempre múltiplo de esta cifra).
	popSteelPerBatch int64 = 8

	// Solicitud de flete de Norte: 200 de iron_ore de su almacén al de Demo,
	// tarifa 20/unidad y valor declarado 20 000 (base de la garantía del
	// transportista: el 10% por defecto). El plazo es holgado porque el test
	// avanza el reloj a saltos grandes para fundir acero en paralelo.
	popFreightQty       int64 = 200
	popFreightTariff    int64 = 20
	popFreightDeclared  int64 = 20_000
	popFreightGuarantee       = popFreightDeclared / 10
	popFreightWindowSim       = 30 * popSimDay
	// popTruckPrice es el precio de catálogo del truck_small sembrado.
	popTruckPrice int64 = 40_000

	// Ganga de Norte para el comerciante: 90 = 90% del base_price 100 de
	// iron_ore, bajo su umbral de compra (95%).
	popBargainQty   int64 = 200
	popBargainPrice int64 = 90
)

func TestBotsFullPopulationE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
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
	freezeSim(t, ctx, pool, popSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'coal'`)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	steelID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'steel_ingot'`)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteNode, _ := warehouseNodeOf(t, ctx, pool, norteID)
	demoNode, demoWH := warehouseNodeOf(t, ctx, pool, demoID)

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
		SecretSeed:          popSecretSeed, Capital: popCapital,
		Tick: popTick, Addr: ":0", APIURL: apiURL,
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
		if got := cashOf(t, ctx, pool, b.AccountID); got != popCapital {
			t.Fatalf("caja de %s tras capitalizar: %d, esperado %d", b.Name, got, popCapital)
		}
	}
	wantArchetypes := map[string]string{
		popCoalName:        "primary_producer",
		popIronName:        "primary_producer",
		popTraderName:      "arbitrageur",
		popTransformerName: "industrial_transformer",
		popFreighterName:   "freighter",
	}
	for name, archetype := range wantArchetypes {
		b, ok := byName[name]
		if !ok {
			t.Fatalf("falta el bot %q en la población aprovisionada", name)
		}
		got := stringOrEmpty(t, ctx, pool,
			`SELECT archetype::text FROM auth.bot_profiles WHERE account_id = $1`, b.AccountID)
		if got != archetype {
			t.Fatalf("arquetipo persistido de %s: %q, esperado %q", name, got, archetype)
		}
	}
	coalBotID := byName[popCoalName].AccountID
	ironBotID := byName[popIronName].AccountID
	traderBotID := byName[popTraderName].AccountID
	transformerBotID := byName[popTransformerName].AccountID
	freighterBotID := byName[popFreighterName].AccountID

	// ── Bucles reales de los cinco bots (una goroutine por bot) ──────────────
	startRunner("bots_orchestrator", orch.Run)

	// ── (a) Setup de los cinco: minas, alto horno y compras de insumos ───────
	// SIN avanzar el reloj: con BuildSimSeconds=0 los edificios pasan a
	// operational en el primer barrido, pero NINGÚN lote termina, así que los
	// productores no tienen existencias y no compiten por las compras del
	// transformador. La red vial hasta cada edificio nuevo la tiende el test
	// (las carreteras son infraestructura del mundo, no de las corporaciones).
	const sqlBuildingOf = `
		SELECT b.id FROM world.buildings b
		  JOIN world.building_types bt ON bt.id = b.building_type_id
		 WHERE b.owner_account_id = $1 AND bt.code = $2`
	var coalMineID, ironMineID, plantID uuid.UUID
	pollPhase(t, 180*time.Second, "fase (a): implantación de los cinco arquetipos",
		func() {
			// El ramal road de cada edificio lo tiende el ALTA (world/buildings):
			// el test NO toca la red — sin ramal, los bots no podrían comprar
			// camión ni planificar ruta y la fase no avanzaría.
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
			ironBuys := countRows(t, ctx, pool, sqlOpenBuyOf, transformerBotID, ironOreID)
			coalBuys := countRows(t, ctx, pool, sqlOpenBuyOf, transformerBotID, coalID)
			ok := plantStatus == "operational" && recipe == "smelt_steel" && ironBuys == 1 && coalBuys == 1
			return ok, fmt.Sprintf("horno[%s receta=%s] compras del transformador[iron=%d coal=%d]",
				plantStatus, recipe, ironBuys, coalBuys)
		})
	plantNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, plantID)
	// Los tres edificios nacieron conectados a la red vial por su propia alta.
	for _, n := range []uuid.UUID{
		queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, coalMineID),
		queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, ironMineID),
		plantNode,
	} {
		requireRoadSpur(t, ctx, pool, n)
	}

	// Las solicitudes del transformador se derivan de la receta y del catálogo:
	// cantidad = consumo por lote × 10, precio = base_price × 1,10.
	ironBuy := popOpenBuy(t, ctx, pool, transformerBotID, ironOreID)
	coalBuy := popOpenBuy(t, ctx, pool, transformerBotID, coalID)
	if ironBuy.qty != popIronBuyQty || ironBuy.price != 110 {
		t.Fatalf("solicitud de iron_ore del transformador: qty=%d precio=%d, esperado %d/110", ironBuy.qty, ironBuy.price, popIronBuyQty)
	}
	if coalBuy.qty != popCoalBuyQty || coalBuy.price != 66 {
		t.Fatalf("solicitud de coal del transformador: qty=%d precio=%d, esperado %d/66", coalBuy.qty, coalBuy.price, popCoalBuyQty)
	}
	if ironBuy.destination != plantNode || coalBuy.destination != plantNode {
		t.Fatalf("las compras del transformador deben tener destino su horno %s (iron %s, coal %s)",
			plantNode, ironBuy.destination, coalBuy.destination)
	}

	// ── (b) Norte (humana) sirve los dos insumos con camión y ruta reales ────
	norte := popLogin(t, ctx, apiURL, traderName, traderSecret)
	ironAcc := popAccept(t, ctx, norte, ironBuy.id, ironBuy.qty, norteNode)
	coalAcc := popAccept(t, ctx, norte, coalBuy.id, coalBuy.qty, norteNode)
	ironContract := popWaitServed(t, ctx, norte, ironAcc)
	coalContract := popWaitServed(t, ctx, norte, coalAcc)
	ironShipment := popWaitShipment(t, ctx, pool, ironContract)
	coalShipment := popWaitShipment(t, ctx, pool, coalContract)
	popDispatch(t, ctx, norte, ironShipment, norteNode, plantNode)
	popDispatch(t, ctx, norte, coalShipment, norteNode, plantNode)
	pollPhase(t, 120*time.Second, "fase (b): entrega y liquidación de los insumos del transformador",
		func() { advanceSim(t, ctx, pool, popTransitStep) },
		func() (bool, string) {
			ironStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM ledger.contracts WHERE id = $1`, ironContract)
			coalStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM ledger.contracts WHERE id = $1`, coalContract)
			return ironStatus == "settled" && coalStatus == "settled",
				fmt.Sprintf("contratos de insumo: iron=%s coal=%s", ironStatus, coalStatus)
		})
	// Las dos entregas fueron ÍNTEGRAS y el material entró en el horno. Se mide
	// la ENTRADA ACUMULADA (partidas positivas de la cuenta stock_free de la
	// planta) y no el saldo: la fundición ya puede haber consumido parte —
	// consume 20 de iron_ore y 10 de coal por lote— en cuanto llegó el primer
	// insumo, y ese consumo es precisamente lo que se quiere que ocurra.
	for _, in := range []struct {
		contract uuid.UUID
		product  uuid.UUID
		qty      int64
		code     string
	}{
		{ironContract, ironOreID, popIronBuyQty, "iron_ore"},
		{coalContract, coalID, popCoalBuyQty, "coal"},
	} {
		var fill int
		var delivered, agreed int64
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(fill_bp, 0), quantity_delivered, quantity_agreed
			  FROM ledger.contracts WHERE id = $1`, in.contract).Scan(&fill, &delivered, &agreed); err != nil {
			t.Fatalf("contrato de %s del transformador: %v", in.code, err)
		}
		if fill != 10_000 || delivered != agreed || delivered != in.qty {
			t.Fatalf("entrega de %s al horno: fill=%d entregado=%d acordado=%d, esperado 10000/%d/%d",
				in.code, fill, delivered, agreed, in.qty, in.qty)
		}
		if got := popStockInflow(t, ctx, pool, transformerBotID, in.product, plantID); got != in.qty {
			t.Fatalf("entrada acumulada de %s en el horno: %d, esperada %d", in.code, got, in.qty)
		}
	}

	// ── (c) Norte publica el FLETE y la GANGA: transportista y comerciante ───
	freightPub := popPublishFreight(t, ctx, norte, ironOreID, norteNode, demoNode)
	bargainPub := popPublishBargain(t, ctx, norte, ironOreID, norteNode)

	// ── (d) El transformador funde, el transportista entrega y el comerciante
	//        compra: el mismo avance del reloj empuja las tres economías ──────
	var freightID, bargainContract uuid.UUID
	pollPhase(t, 240*time.Second, "fase (d): acero fundido, flete entregado y ganga arbitrada",
		func() { advanceSim(t, ctx, pool, popSmeltStep) },
		func() (bool, string) {
			steel := popStockFreeOrZero(t, ctx, pool, transformerBotID, steelID, plantID)
			steelSells := countRows(t, ctx, pool, sqlOpenSellOf, transformerBotID, steelID)
			if freightID == uuid.Nil {
				freightID = optionalUUID(t, ctx, pool, `
					SELECT id FROM ledger.freight_contracts
					 WHERE carrier_account_id = $1 AND shipper_account_id = $2`, freighterBotID, norteID)
			}
			freightStatus := ""
			if freightID != uuid.Nil {
				freightStatus = stringOrEmpty(t, ctx, pool,
					`SELECT status::text FROM ledger.freight_contracts WHERE id = $1`, freightID)
			}
			if bargainContract == uuid.Nil {
				bargainContract = optionalUUID(t, ctx, pool, `
					SELECT id FROM ledger.contracts
					 WHERE buyer_account_id = $1 AND seller_account_id = $2 AND product_id = $3 AND status = 'settled'`,
					traderBotID, norteID, ironOreID)
			}
			ok := steel >= popSteelPerBatch && steelSells >= 1 &&
				freightStatus == "settled" && bargainContract != uuid.Nil
			return ok, fmt.Sprintf("acero=%d ventas=%d flete=%q ganga=%v",
				steel, steelSells, freightStatus, bargainContract != uuid.Nil)
		})

	// ── LOS CINCO ROLES VIVOS a la vez (métricas del orquestador) ────────────
	// ii_bot_decisions_total es la métrica de auditoría del GDD §13.3 (cada
	// decisión contada por bot y tipo) e ii_bots_active la población que la
	// densidad dinámica gobierna. Se leen ANTES del apagado: la afirmación es
	// que los cinco arquetipos están jugando SIMULTÁNEAMENTE.
	decisions := popScrapeByLabel(t, botsReg, "ii_bot_decisions_total", "bot")
	for _, name := range []string{popCoalName, popIronName, popTraderName, popTransformerName, popFreighterName} {
		if decisions[name] < 1 {
			t.Fatalf("el bot %q no registró ninguna decisión auditable (ii_bot_decisions_total=%v)", name, decisions[name])
		}
	}
	active := popScrapeByLabel(t, botsReg, "ii_bots_active", "archetype")
	for _, archetype := range bots.DensityArchetypes {
		if active[archetype] != 1 {
			t.Fatalf("ii_bots_active{archetype=%q} = %v, esperado 1 (los cinco arquetipos activos)",
				archetype, active[archetype])
		}
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

	// ── (c) MANUFACTURA: los tres lotes encolados rindieron 24 lingotes ──────
	if got := stockFreeOf(t, ctx, pool, transformerBotID, steelID, plantID); got < popSteelPerBatch {
		t.Fatalf("acero libre en el horno: %d, esperado al menos un lote (%d)", got, popSteelPerBatch)
	}
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.production_batches
		 WHERE building_id = $1 AND status = 'completed'`, plantID); n < 1 {
		t.Fatalf("lotes de fundición completados: %d, esperado al menos 1", n)
	}
	// La venta del acero acredita la OPTIMIZACIÓN SIMPLE del arquetipo: sin
	// velas OHLC el coste unitario sale del catálogo —(20×100 + 10×60)/8 = 325,
	// bajo la referencia 400 del acero, luego el margen es positivo— y el precio
	// es coste × 1,25 = 407 (techo del catálogo 1600, no aplica). La cantidad
	// depende de cuántos lotes hubieran terminado al publicar: siempre lotes
	// enteros de 8.
	var steelPrice, steelQty int64
	var steelOrigin uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT unit_price, quantity_total, origin_node_id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, transformerBotID, steelID).
		Scan(&steelPrice, &steelQty, &steelOrigin); err != nil {
		t.Fatalf("venta de acero del transformador: %v", err)
	}
	if steelPrice != 407 || steelOrigin != plantNode {
		t.Fatalf("venta de acero: precio=%d origen=%s, esperado 407 (coste 325 × 1,25) desde el horno %s",
			steelPrice, steelOrigin, plantNode)
	}
	if steelQty < popSteelPerBatch || steelQty%popSteelPerBatch != 0 {
		t.Fatalf("cantidad publicada de acero: %d, esperados lotes enteros de %d", steelQty, popSteelPerBatch)
	}

	// ── (d) FLETE: liquidado al 100%, carga en destino y transportista cobrado ─
	var freightStatus string
	var freightFill int
	var freightPrice, freightDeclared int64
	if err := pool.QueryRow(ctx, `
		SELECT status::text, COALESCE(fill_bp, 0), freight_price, declared_value
		  FROM ledger.freight_contracts WHERE id = $1`, freightID).
		Scan(&freightStatus, &freightFill, &freightPrice, &freightDeclared); err != nil {
		t.Fatalf("contrato de flete del transportista: %v", err)
	}
	if freightStatus != "settled" || freightFill != 10_000 {
		t.Fatalf("flete tras la entrega: status=%s fill=%d, esperado settled/10000", freightStatus, freightFill)
	}
	if freightPrice != popFreightQty*popFreightTariff || freightDeclared != popFreightDeclared {
		t.Fatalf("flete: precio=%d declarado=%d, esperado %d/%d",
			freightPrice, freightDeclared, popFreightQty*popFreightTariff, popFreightDeclared)
	}
	if got := stockFreeOf(t, ctx, pool, norteID, ironOreID, demoWH); got != popFreightQty {
		t.Fatalf("carga entregada al cargador en el almacén de destino: %d, esperada %d", got, popFreightQty)
	}
	// El transportista cobró la tarifa y recuperó la garantía; solo pagó camión.
	wantFreighterCash := popCapital - popTruckPrice + popFreightQty*popFreightTariff
	if got := cashOf(t, ctx, pool, freighterBotID); got != wantFreighterCash {
		t.Fatalf("caja del transportista: %d, esperada %d (capital − camión + tarifa)", got, wantFreighterCash)
	}
	if got := popGuaranteeOf(t, ctx, pool, freighterBotID); got != 0 {
		t.Fatalf("garantía del transportista sin liberar tras la entrega: %d (bloqueó %d al aceptar)",
			got, popFreightGuarantee)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, freighterBotID); n != 1 {
		t.Fatalf("flota del transportista: %d camiones, esperado 1 (comprado en el origen del flete)", n)
	}

	// ── (e) ARBITRAJE: el comerciante compró la ganga y la tiene en almacén ──
	var bargainQty, bargainPrice int64
	if err := pool.QueryRow(ctx, `
		SELECT quantity_delivered, unit_price FROM ledger.contracts WHERE id = $1`, bargainContract).
		Scan(&bargainQty, &bargainPrice); err != nil {
		t.Fatalf("contrato de la ganga del comerciante: %v", err)
	}
	if bargainQty != popBargainQty || bargainPrice != popBargainPrice {
		t.Fatalf("ganga arbitrada: %d unidades a %d, esperado %d a %d",
			bargainQty, bargainPrice, popBargainQty, popBargainPrice)
	}
	if got := stockTotalOf(t, ctx, pool, traderBotID, ironOreID, popBuildingOfNode(t, ctx, pool, norteNode)); got != popBargainQty {
		t.Fatalf("stock de iron_ore del comerciante en el almacén de Norte: %d, esperado %d", got, popBargainQty)
	}

	// Las dos publicaciones de Norte quedaron íntegramente servidas por los
	// bots: ni una unidad de la ganga ni de la carga se quedó sin contraparte.
	for _, pub := range []struct {
		id   string
		what string
	}{{freightPub, "solicitud de flete"}, {bargainPub, "ganga"}} {
		var remaining int64
		if err := pool.QueryRow(ctx,
			`SELECT quantity_remaining FROM ledger.publications WHERE id = $1`, pub.id).Scan(&remaining); err != nil {
			t.Fatalf("%s de Norte: %v", pub.what, err)
		}
		if remaining != 0 {
			t.Fatalf("%s de Norte: %d unidades sin servir, esperado 0", pub.what, remaining)
		}
	}

	// ── Coherencia final sobre la BD quiescente ──────────────────────────────
	assertBalancedLedger(t, ctx, pool)
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable final: %d divergencias (err %v), esperado 0", disc, err)
	}
}

// ─── Consultas reutilizadas ──────────────────────────────────────────────────

// sqlOpenBuyOf y sqlOpenSellOf cuentan las publicaciones VISIBLES de una
// corporación para un producto.
const (
	sqlOpenBuyOf = `
		SELECT count(*) FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`
	sqlOpenSellOf = `
		SELECT count(*) FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`
)

// popPublication es la vista mínima de una publicación del tablón.
type popPublication struct {
	id          string
	qty         int64
	price       int64
	destination uuid.UUID
}

// popOpenBuy devuelve la solicitud de compra visible de un producto.
func popOpenBuy(t *testing.T, ctx context.Context, pool *pgxpool.Pool, publisher, productID uuid.UUID) popPublication {
	t.Helper()
	var out popPublication
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id, quantity_total, unit_price, destination_node_id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, publisher, productID).
		Scan(&id, &out.qty, &out.price, &out.destination); err != nil {
		t.Fatalf("solicitud de compra de %s: %v", productID, err)
	}
	out.id = id.String()
	return out
}

// popStockFreeOrZero es stockFreeOf tolerante a la ausencia de cuenta: mientras
// el horno no funde su primer lingote, la cuenta stock_free del acero todavía no
// existe (se crea con el primer asiento), y el poll debe poder observarlo.
func popStockFreeOrZero(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2 AND warehouse_building_id = $3`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock_free de %s (%s): %v", owner, product, err)
	}
	return b
}

// popStockInflow suma las ENTRADAS (partidas positivas) de la cuenta stock_free
// de (dueño, producto, almacén): lo que ha llegado al almacén a lo largo de toda
// la partida, con independencia de lo que se haya consumido después.
func popStockInflow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(e.amount), 0)
		  FROM ledger.entries e
		  JOIN ledger.accounts a ON a.id = e.account_id
		 WHERE a.kind = 'stock_free' AND a.owner_account_id = $1 AND a.product_id = $2
		   AND a.warehouse_building_id = $3 AND e.amount > 0`,
		owner, product, warehouse).Scan(&total); err != nil {
		t.Fatalf("entradas de stock_free de %s (%s): %v", owner, product, err)
	}
	return total
}

// popGuaranteeOf suma las garantías vivas (cuentas espejo) de una corporación.
func popGuaranteeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind = 'guarantee' AND owner_account_id = $1`, owner).Scan(&total); err != nil {
		t.Fatalf("garantías de %s: %v", owner, err)
	}
	return total
}

// popBuildingOfNode devuelve el edificio asociado a un nodo del grafo.
func popBuildingOfNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, nodeID uuid.UUID) uuid.UUID {
	t.Helper()
	return queryUUID(t, ctx, pool, `SELECT building_id FROM world.network_nodes WHERE id = $1`, nodeID)
}

// orZero devuelve el id ya conocido o el recién leído (memoiza sin repetir la
// consulta en cada vuelta del poll).
func orZero(known, fresh uuid.UUID) uuid.UUID {
	if known != uuid.Nil {
		return known
	}
	return fresh
}

// ─── Norte: la corporación HUMANA que juega contra los bots por el SDK ───────

// popLogin abre una sesión del SDK contra la API pública.
func popLogin(t *testing.T, ctx context.Context, apiURL, name, secret string) *botsdk.Client {
	t.Helper()
	c, err := botsdk.New(botsdk.Options{BaseURL: apiURL})
	if err != nil {
		t.Fatalf("botsdk.New: %v", err)
	}
	if _, err := c.Login(ctx, name, secret); err != nil {
		t.Fatalf("login de %s: %v", name, err)
	}
	return c
}

// popAccept acepta una publicación como vendedor (origen = su almacén).
func popAccept(t *testing.T, ctx context.Context, c *botsdk.Client, publicationID string, qty int64, originNode uuid.UUID) string {
	t.Helper()
	qtyStr, err := botsdk.QtyFromInt64(qty)
	if err != nil {
		t.Fatalf("cantidad inválida %d: %v", qty, err)
	}
	acc, err := c.Accept(ctx, publicationID, qtyStr, originNode.String())
	if err != nil {
		t.Fatalf("aceptando la publicación %s: %v", publicationID, err)
	}
	return acc.ID
}

// popWaitServed espera al sorteo del CCRI (wall-clock, lo dispara el worker
// real) y devuelve el contrato adjudicado.
func popWaitServed(t *testing.T, ctx context.Context, c *botsdk.Client, acceptanceID string) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		acc, err := c.GetAcceptance(ctx, acceptanceID)
		if err == nil && acc.Status == botsdk.AcceptanceServed {
			if acc.ContractID == "" {
				t.Fatalf("aceptación servida sin contract_id: %+v", acc)
			}
			id, perr := uuid.Parse(acc.ContractID)
			if perr != nil {
				t.Fatalf("contract_id inválido %q: %v", acc.ContractID, perr)
			}
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el sorteo de la aceptación %s (err %v)", acceptanceID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// popWaitShipment espera a que el shipment_creator materialice el cargamento
// del contrato (consumidor del outbox, cross-context).
func popWaitShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contractID uuid.UUID) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		id := optionalUUID(t, ctx, pool, `SELECT id FROM world.shipments WHERE contract_id = $1`, contractID)
		if id != uuid.Nil {
			return id.String()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el cargamento del contrato %s", contractID)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// popDispatch asegura camión y ruta y despacha el cargamento origen→destino.
func popDispatch(t *testing.T, ctx context.Context, c *botsdk.Client, shipmentID string, origin, dest uuid.UUID) {
	t.Helper()
	vehicleID := popEnsureVehicleAt(t, ctx, c, origin)
	routeID := popEnsureRoute(t, ctx, c, origin, dest)
	if _, err := c.Dispatch(ctx, shipmentID, vehicleID, routeID); err != nil {
		t.Fatalf("despachando el cargamento %s: %v", shipmentID, err)
	}
}

// popEnsureVehicleAt devuelve un camión idle en el nodo, comprándolo si hace
// falta (el cargador humano mueve su propia mercancía).
func popEnsureVehicleAt(t *testing.T, ctx context.Context, c *botsdk.Client, nodeID uuid.UUID) string {
	t.Helper()
	page, err := c.ListVehicles(ctx, botsdk.VehiclesQuery{PageQuery: botsdk.PageQuery{Limit: 200}})
	if err != nil {
		t.Fatalf("listando la flota: %v", err)
	}
	for _, v := range page.Items {
		if v.Status == botsdk.VehicleIdle && v.Position.AtNodeID == nodeID.String() {
			return v.ID
		}
	}
	types, err := c.VehicleTypes(ctx, botsdk.VehicleTypesQuery{PageQuery: botsdk.PageQuery{Limit: 200}})
	if err != nil {
		t.Fatalf("listando tipos de vehículo: %v", err)
	}
	var truckID string
	for _, vt := range types.Items {
		if vt.Code == "truck_small" {
			truckID = vt.ID
		}
	}
	if truckID == "" {
		t.Fatal("el catálogo no tiene truck_small")
	}
	v, err := c.PurchaseVehicle(ctx, botsdk.VehiclePurchase{VehicleTypeID: truckID, DeliveryNodeID: nodeID.String()})
	if err != nil {
		t.Fatalf("comprando el camión en %s: %v", nodeID, err)
	}
	return v.ID
}

// popEnsureRoute planifica y crea (o reutiliza) una ruta por carretera.
func popEnsureRoute(t *testing.T, ctx context.Context, c *botsdk.Client, origin, dest uuid.UUID) string {
	t.Helper()
	name := "e2e " + origin.String() + "→" + dest.String()
	routes, err := c.ListRoutes(ctx, botsdk.RoutesQuery{PageQuery: botsdk.PageQuery{Limit: 200}})
	if err != nil {
		t.Fatalf("listando rutas: %v", err)
	}
	for _, r := range routes.Items {
		if r.Name == name {
			return r.ID
		}
	}
	plan, err := c.PlanRoute(ctx, botsdk.RoutePlanRequest{
		OriginNodeID:      origin.String(),
		DestinationNodeID: dest.String(),
		Modes:             []botsdk.LinkMode{botsdk.ModeRoad},
	})
	if err != nil {
		t.Fatalf("planificando la ruta %s→%s: %v", origin, dest, err)
	}
	legs := make([]string, len(plan.Legs))
	for i, leg := range plan.Legs {
		legs[i] = leg.LinkID
	}
	route, err := c.CreateRoute(ctx, botsdk.RouteCreate{Name: name, Kind: botsdk.RouteOnDemand, Legs: legs})
	if err != nil {
		t.Fatalf("creando la ruta %s: %v", name, err)
	}
	return route.ID
}

// popPublishFreight publica la solicitud de flete del CARGADOR (kind=freight):
// producto de la carga, origen, destino, tarifa por unidad y valor declarado.
func popPublishFreight(t *testing.T, ctx context.Context, c *botsdk.Client, productID, origin, dest uuid.UUID) string {
	t.Helper()
	qty, err := botsdk.QtyFromInt64(popFreightQty)
	if err != nil {
		t.Fatalf("cantidad inválida: %v", err)
	}
	pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationFreight,
		ProductID:          productID.String(),
		QuantityTotal:      qty,
		UnitPrice:          botsdk.MoneyFromInt64(popFreightTariff),
		OriginNodeID:       origin.String(),
		DestinationNodeID:  dest.String(),
		DeclaredValue:      botsdk.MoneyFromInt64(popFreightDeclared),
		DeliverySimSeconds: popFreightWindowSim,
	})
	if err != nil {
		t.Fatalf("publicando la solicitud de flete: %v", err)
	}
	return pub.ID
}

// popPublishBargain publica la ganga que el comerciante debe detectar.
func popPublishBargain(t *testing.T, ctx context.Context, c *botsdk.Client, productID, origin uuid.UUID) string {
	t.Helper()
	qty, err := botsdk.QtyFromInt64(popBargainQty)
	if err != nil {
		t.Fatalf("cantidad inválida: %v", err)
	}
	minLot, err := botsdk.QtyFromInt64(50)
	if err != nil {
		t.Fatalf("min_lot inválido: %v", err)
	}
	pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationSell,
		ProductID:          productID.String(),
		QuantityTotal:      qty,
		UnitPrice:          botsdk.MoneyFromInt64(popBargainPrice),
		MinLot:             minLot,
		OriginNodeID:       origin.String(),
		DeliverySimSeconds: popSimDay,
	})
	if err != nil {
		t.Fatalf("publicando la ganga: %v", err)
	}
	return pub.ID
}

// ─── Lectura de métricas: exactamente lo que raspa Prometheus ────────────────

// popScrapeByLabel raspa el registry POR HTTP (el mismo handler promhttp que
// sirve /metrics) y devuelve la suma de los valores de la métrica name
// agrupados por el valor de la etiqueta label. Leer la exposición de texto —en
// lugar de los colectores— comprueba de paso que la métrica se publica con el
// nombre y las etiquetas documentadas.
func popScrapeByLabel(t *testing.T, reg *prometheus.Registry, name, label string) map[string]float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("raspando el registry: status %d", rec.Code)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, name) {
			continue
		}
		rest := line[len(name):]
		if rest == "" || (rest[0] != '{' && rest[0] != ' ') {
			continue // otra métrica con el mismo prefijo
		}
		labels := ""
		if strings.HasPrefix(rest, "{") {
			end := strings.Index(rest, "}")
			if end < 0 {
				t.Fatalf("línea de métrica mal formada: %q", line)
			}
			labels = rest[1:end]
			rest = rest[end+1:]
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			t.Fatalf("valor no numérico en %q: %v", line, err)
		}
		out[popLabelValue(labels, label)] += value
	}
	return out
}

// popLabelValue extrae el valor de una etiqueta de la lista serializada de
// Prometheus (name="value",other="value").
func popLabelValue(labels, label string) string {
	for _, part := range strings.Split(labels, `",`) {
		key, value, ok := strings.Cut(strings.TrimSpace(part), `="`)
		if !ok {
			continue
		}
		if key == label {
			return strings.TrimSuffix(value, `"`)
		}
	}
	return ""
}
