// Bucle económico COMPLETO del Incremento 6b (ECONOMY BALANCER, GDD 5.5/5.6/5.7/
// 18.1) de proceso a proceso contra una BD real, sin mocks: demuestra el ciclo
// faucet(la ciudad paga) → producción/venta → entrega física → consumo (sumidero
// final) → crecimiento urbano.
//
// El Economy Balancer corre como los binarios reales —DemandWorker (decide y
// publica las buys de las ciudades por la API ESTÁNDAR del Contract Service, sin
// canal privilegiado), su Consumer del outbox (consume las entregas urbanas) y el
// AnalyticsWorker (macro)— junto al worker CCRI del sorteo, el motor de tránsito y
// los consumidores cross-context (shipment_creator, delivery_confirmer), todos con
// el MISMO reloj de simulación CONGELADO y avanzado por SQL. El gateway real
// (BuildHandler) sirve las aceptaciones y la logística de Norte.
//
// Historia (sobre el mundo sembrado; la ciudad Nueva Askadia, nivel 2):
//
//	(1) El DemandWorker recalcula la demanda de la ciudad y PUBLICA una BUY de
//	    iron_ore por el PORT: en el tablón aparece una publicación buy cuyo
//	    publicador es la CUENTA DE LA CIUDAD y cuyo destino es su CENTRO DE
//	    DISTRIBUCIÓN. Como se drenó la caja de la ciudad, el Balancer la PRE-FONDEA
//	    por EMISIÓN del banco central (faucet, GDD 5.5): la emisión baja exactamente
//	    el escrow de la buy y queda un asiento "Fondeo de ciudad (faucet)".
//	(2) NORTE (que tiene iron_ore) acepta la buy con origen = su almacén
//	    (cross-node) → sorteo → contrato buy cross-node.
//	(3) shipment_creator crea el cargamento en Norte → Norte compra un camión,
//	    planifica Norte→ciudad, crea la ruta y DESPACHA → el TransitWorker mueve el
//	    vehículo hasta el centro de distribución → entrega física.
//	(4) delivery_confirmer liquida on-time: la ciudad recibe el iron_ore como
//	    stock_free en su centro, NORTE COBRA (dinero nuevo en circulación) y el
//	    contrato queda settled con fill 100%.
//	(5) El Consumer del Balancer consume la entrega urbana (city stock_free →
//	    world_source, transacción consumption, ADR-022): la ciudad es sumidero final
//	    real (no acumula inventario) y su supply_index sube.
//	(6) Con el suministro acumulado, el siguiente recálculo SUBE A LA CIUDAD DE
//	    NIVEL (2→3): población +10%, D0 +20% y se DESBLOQUEA steel_ingot (aparece su
//	    buy en el tablón). city.level_up emitido.
//	(7) El AnalyticsWorker escribe economy_indicators con masa monetaria, PIB,
//	    emisión y absorción COHERENTES (emisión − absorción = masa monetaria sobre
//	    el bucket de historia completa, invariante de doble entrada).
//
// Verificación contable final sobre la BD quiescente: ledger a cero por activo,
// ninguna caja negativa y reconciliación física↔contable 0.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/balancer"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// Parámetros del bucle del Balancer que el test fija para hacer el crecimiento
// urbano determinista sin depender de los umbrales de producción (semanas-sim).
const (
	// balSimBase es el ancla del reloj congelado (día-sim redondo, cuentas limpias).
	balSimBase int64 = 1_000_000
	// balLevelupBase es el umbral base de supply_index (bajo, para que una sola
	// entrega cruce el umbral de subida del nivel 2→3 = base×nivel = 2000).
	balLevelupBase = 1000.0
	// balCityName es la ciudad sembrada (consumidor final del mundo industrial).
	balCityName = "Nueva Askadia"
)

func TestEconomyBalancerCityLoopE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado (incluye la ciudad, su centro de distribución conectado a
	//    la red vial, y la caja/capital de la ciudad) ───────────────────────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, balSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	steelID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "steel_ingot")
	cityID := queryUUID(t, ctx, pool, `SELECT id FROM world.cities WHERE name = $1`, balCityName)
	cityAccountID := queryUUID(t, ctx, pool, `SELECT account_id FROM world.cities WHERE name = $1`, balCityName)
	cityDistNode := queryUUID(t, ctx, pool,
		`SELECT id FROM world.network_nodes WHERE kind = 'distribution_center' AND city_id = $1`, cityID)
	cityDistBuilding := queryUUID(t, ctx, pool,
		`SELECT building_id FROM world.network_nodes WHERE id = $1`, cityDistNode)
	norteNode, _ := warehouseNodeOf(t, ctx, pool, norteID)

	// El centro de distribución quedó conectado a la red vial (junction↔ciudad):
	// sin ese enlace la entrega física a la ciudad sería irrealizable.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.network_links
		 WHERE mode = 'road' AND (from_node_id = $1 OR to_node_id = $1)`, cityDistNode); n != 2 {
		t.Fatalf("enlaces road del centro de distribución: %d, esperado 2 (bidireccional junction↔ciudad)", n)
	}

	// ── Fixtures deterministas de la curva/nivel de la ciudad ────────────────
	// steel_ingot se BLOQUEA hasta el nivel 3 (el seed lo abre en el 2): así en el
	// nivel 2 la ciudad solo demanda iron_ore, y steel se desbloquea al subir.
	mustExecE2E(t, ctx, pool,
		`UPDATE world.city_demand SET unlocked_at_level = 3, updated_at_sim = $1 WHERE city_id = $2 AND product_id = $3`,
		balSimBase, cityID, steelID)
	// iron_ore: supply_ema alineado con D0 (rawRatio≈1, precio≈base, sin sorpresas)
	// y sello de recálculo = ahora (ventana limpia).
	mustExecE2E(t, ctx, pool,
		`UPDATE world.city_demand SET supply_ema = 1000, recent_supply = 0, updated_at_sim = $1 WHERE city_id = $2 AND product_id = $3`,
		balSimBase, cityID, ironOreID)
	// supply_index en la banda ESTABLE del nivel 2 con el umbral base del test
	// (banda [base×1, base×2) = [1000, 2000)): 1500 ni sube ni baja hasta que el
	// consumo lo empuje sobre 2000.
	mustExecE2E(t, ctx, pool,
		`UPDATE world.cities SET level = 2, population = 50000, supply_index = 1500, updated_at_sim = $1 WHERE id = $2`,
		balSimBase, cityID)

	// ── Gateway real (ventanas de sorteo wall-clock cortas; reloj sin caché) ──
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	contractsOpts.PublicationTTLSimSeconds = 10_000_000 // las buys de ciudad no expiran durante el test
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth:        auth.Options{LoginPerMin: auth.DefaultRateLoginPerMin, APIRPS: auth.DefaultRateAPIRPS, APIBurst: auth.DefaultRateAPIBurst},
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

	// ── Motores y consumidores reales (todos con el MISMO lector congelado) ──
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	transitOpts := fleet.DefaultWorkerOptions()
	transitOpts.Roll = func() float64 { return 1.0 } // sin avería: ruta determinista
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
	reconWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}

	// ── El Economy Balancer: DemandWorker (con el PORT sobre el Contract Service
	//    estándar), su Consumer del outbox y el AnalyticsWorker macro ──────────
	bopts := balancer.DefaultOptions()
	bopts.LevelupIndexBase = balLevelupBase
	bopts.SupplyIndexDecayPerSimDay = 0 // sin decaimiento: el nivel solo lo mueve el suministro
	bopts.SaturationMin = 0.1
	bopts.SaturationMax = 1.0 // acota la buy a ~D0 (qty manejable para un camión)
	bopts.BuyTargetDays = 1.0 // horizonte de compra 1 día-sim → qty ≈ D0
	bopts.CityBuyDeadlineSim = 500_000
	bopts.AnalyticsBucketSim = 10_000_000 // bucket de historia completa: coherencia macro exacta
	bmetrics := balancer.NewMetrics(prometheus.NewRegistry())

	port := e2eCityBuyPort{svc: ccriSvc}
	demandWorker, err := balancer.NewDemandWorker(pool, port, reader, bopts, bmetrics, logger)
	if err != nil {
		t.Fatalf("balancer.NewDemandWorker: %v", err)
	}
	cityConsumer, err := balancer.NewConsumer(bopts, bmetrics, logger)
	if err != nil {
		t.Fatalf("balancer.NewConsumer: %v", err)
	}
	cityConsumerRunner := cityConsumer.NewOutboxConsumer(pool, outbox.WithLogger(logger))
	analyticsWorker, err := balancer.NewAnalyticsWorker(pool, reader, bopts, bmetrics, logger)
	if err != nil {
		t.Fatalf("balancer.NewAnalyticsWorker: %v", err)
	}

	norteToken := login(t, srv, traderName, traderSecret)

	// ── (1) La ciudad, drenada, publica una BUY de iron_ore; el Balancer la
	//        pre-fondea por EMISIÓN (faucet) ───────────────────────────────────
	drainCashTo(t, ctx, pool, cityAccountID, 0) // fuerza el pre-fondeo del Balancer
	emissionBefore := emissionBalanceE2E(t, ctx, pool)

	demandWorker.RunOnce(ctx)

	// En el tablón hay una buy cuyo publicador es la ciudad y cuyo destino es su
	// centro de distribución (la API estándar, sin canal privilegiado).
	buyPubID := optionalUUID(t, ctx, pool, `
		SELECT id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window', 'open', 'micro_window')`, cityAccountID, ironOreID)
	if buyPubID == uuid.Nil {
		t.Fatal("el Balancer no publicó la buy de iron_ore de la ciudad")
	}
	var ironQty, ironPrice int64
	var buyDest uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT quantity_total, unit_price, destination_node_id FROM ledger.publications WHERE id = $1`, buyPubID).
		Scan(&ironQty, &ironPrice, &buyDest); err != nil {
		t.Fatalf("buy de la ciudad: %v", err)
	}
	if buyDest != cityDistNode {
		t.Fatalf("destino de la buy de la ciudad: %s, esperado el centro de distribución %s", buyDest, cityDistNode)
	}
	if ironQty <= 0 || ironPrice <= 0 {
		t.Fatalf("buy de la ciudad con qty/precio no positivos: qty=%d precio=%d", ironQty, ironPrice)
	}
	ironValue := ironQty * ironPrice

	// El faucet: la emisión bajó EXACTAMENTE el escrow de la buy (la caja estaba a
	// 0), y quedó el asiento de fondeo de ciudad.
	if got := emissionBalanceE2E(t, ctx, pool); got != emissionBefore-ironValue {
		t.Fatalf("emisión de fondeo de ciudad: %d, esperado %d (Δ = −%d, el escrow de la buy)", got, emissionBefore-ironValue, ironValue)
	}
	if n := countRows(t, ctx, pool,
		`SELECT count(*) FROM ledger.transactions WHERE description LIKE 'Fondeo de ciudad (faucet)%'`); n < 1 {
		t.Fatal("no se asentó ninguna transacción de fondeo de ciudad (faucet)")
	}
	// La caja de la ciudad volvió a 0: lo emitido quedó bloqueado en el escrow.
	if got := cashOf(t, ctx, pool, cityAccountID); got != 0 {
		t.Fatalf("caja de la ciudad tras publicar la buy: %d, esperado 0 (emitido → escrow)", got)
	}

	// ── (2) NORTE acepta la buy de la ciudad con origen = su almacén (cross-node) ─
	norteCash0 := cashOf(t, ctx, pool, norteID)
	r := callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+buyPubID.String()+"/acceptances",
		norteToken, uuid.NewString(), map[string]any{"quantity": itoa(ironQty), "origin_node_id": norteNode.String()})
	if r.status != http.StatusCreated {
		t.Fatalf("Norte acepta la buy de la ciudad: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	accID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if accID == "" {
		t.Fatal("aceptación sin id")
	}

	// ── (3) Sorteo → contrato buy cross-node (comprador = ciudad) ────────────
	contractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, norteToken, accID)
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, norteToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("contrato: status %d (cuerpo: %s)", r.status, r.raw)
	}
	c := asMap(t, r.body["data"], "data")
	if c["status"] != "active" || c["buyer_account_id"] != cityAccountID.String() || c["seller_account_id"] != norteID.String() {
		t.Fatalf("contrato de ciudad inesperado: %v", c)
	}
	if c["origin_node_id"] != norteNode.String() || c["destination_node_id"] != cityDistNode.String() {
		t.Fatalf("nodos del contrato de ciudad inesperados: %v", c)
	}

	// ── (4) shipment_creator → Norte compra camión, planifica, crea ruta y despacha ─
	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")
	shipmentID := findShipment(t, ctx, srv, norteToken, contractID)

	truckLargeID := queryUUID(t, ctx, pool, `SELECT id FROM world.vehicle_types WHERE code = $1`, "truck_large")
	r = call(t, srv, http.MethodPost, "/api/v1/world/vehicles", norteToken, map[string]any{
		"vehicle_type_id": truckLargeID.String(), "delivery_node_id": norteNode.String(),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("comprar camión: status %d (cuerpo: %s)", r.status, r.raw)
	}
	vehID, _ := asMap(t, r.body["data"], "data")["id"].(string)

	r = call(t, srv, http.MethodPost, "/api/v1/logistics/route-plans", norteToken, map[string]any{
		"origin_node_id": norteNode.String(), "destination_node_id": cityDistNode.String(), "modes": []string{"road"},
	})
	if r.status != http.StatusOK {
		t.Fatalf("route-plan Norte→ciudad: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	legs, ok := asMap(t, r.body["data"], "data")["legs"].([]any)
	if !ok || len(legs) == 0 {
		t.Fatalf("route-plan Norte→ciudad sin legs (¿centro de distribución desconectado?): %s", r.raw)
	}
	planLinks := make([]any, len(legs))
	for i, l := range legs {
		planLinks[i], _ = asMap(t, l, "leg")["link_id"].(string)
	}
	r = call(t, srv, http.MethodPost, "/api/v1/logistics/routes", norteToken, map[string]any{
		"name": "Norte→ciudad", "kind": "on_demand", "legs": planLinks,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("crear ruta: status %d (cuerpo: %s)", r.status, r.raw)
	}
	routeID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	r = call(t, srv, http.MethodPost, "/api/v1/world/shipments/"+shipmentID+"/dispatch", norteToken, map[string]any{
		"vehicle_id": vehID, "route_id": routeID,
	})
	if r.status != http.StatusOK || asMap(t, r.body["data"], "data")["status"] != "in_transit" {
		t.Fatalf("despachar: status %d (cuerpo: %s)", r.status, r.raw)
	}

	// ── El TransitWorker mueve el camión hasta el centro de distribución ──────
	driveTransitUntilDelivered(t, ctx, pool, transitWorker, uuid.MustParse(shipmentID))
	sh := getShipment(t, srv, norteToken, shipmentID)
	if sh["status"] != "delivered" || sh["at_node_id"] != cityDistNode.String() {
		t.Fatalf("cargamento tras el tránsito: %v (esperado delivered en el centro de distribución)", sh)
	}

	// ── (5) delivery_confirmer liquida on-time: la ciudad recibe, Norte cobra ─
	drainConsumer(t, ctx, pool, dcConsumer, deliveryConfirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")

	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, norteToken, nil)
	c = asMap(t, r.body["data"], "data")
	if c["status"] != "settled" || c["quantity_delivered"] != itoa(ironQty) {
		t.Fatalf("contrato de ciudad no liquidado como se esperaba: %v", c)
	}
	if fill, _ := c["fill_bp"].(float64); fill != 10000 {
		t.Fatalf("fill_bp: %v, esperado 10000 (100%%)", c["fill_bp"])
	}
	// La ciudad recibió el iron_ore como stock_free en su centro de distribución.
	if got := stockFreeOf(t, ctx, pool, cityAccountID, ironOreID, cityDistBuilding); got != ironQty {
		t.Fatalf("stock_free de iron_ore de la ciudad en su centro: %d, esperado %d", got, ironQty)
	}
	// Norte cobró (dinero nuevo en circulación): +valor − precio del camión.
	if got := cashOf(t, ctx, pool, norteID); got != norteCash0+ironValue-logTruckPrice {
		t.Fatalf("caja de Norte tras cobrar: %d, esperado %d (+%d cobro − %d camión)", got, norteCash0+ironValue-logTruckPrice, ironValue, logTruckPrice)
	}

	// ── (6) El Consumer del Balancer CONSUME la entrega urbana (sumidero final) ─
	supplyIdxBefore := citySupplyIndexE2E(t, ctx, pool, cityID)
	// world_source arranca NEGATIVO (el stock inicial de las corporaciones se
	// EXTRAJO del mundo, ADR-022): el consumo lo devuelve, subiéndolo en ironQty.
	worldSourceBefore := worldSourceBalanceE2E(t, ctx, pool, ironOreID)
	drainConsumer(t, ctx, pool, cityConsumerRunner, cityConsumer.Handle, balancer.ConsumerName, "contract.settled")

	// La ciudad no acumula inventario: su stock_free volvió a 0 (consumido).
	if got := stockFreeOf(t, ctx, pool, cityAccountID, ironOreID, cityDistBuilding); got != 0 {
		t.Fatalf("stock_free de la ciudad tras el consumo: %d, esperado 0 (sumidero final)", got)
	}
	// El consumo se asentó como transacción consumption (ADR-022): +world_source /
	// −city stock_free.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.transactions WHERE kind = 'consumption' AND reference_id = $1`,
		uuid.MustParse(contractID)); n < 1 {
		t.Fatal("no se asentó la transacción de consumo (consumption) de la entrega urbana")
	}
	if got := worldSourceBalanceE2E(t, ctx, pool, ironOreID); got-worldSourceBefore != ironQty {
		t.Fatalf("Δworld_source de iron_ore tras el consumo: %d, esperado +%d (el stock volvió al mundo)", got-worldSourceBefore, ironQty)
	}
	// El suministro subió el supply_index de la ciudad por encima del umbral 2→3.
	if got := citySupplyIndexE2E(t, ctx, pool, cityID); !(got > supplyIdxBefore && got >= balLevelupBase*2) {
		t.Fatalf("supply_index de la ciudad tras el consumo: %g, esperado > %g y ≥ %g", got, supplyIdxBefore, balLevelupBase*2)
	}

	// ── (7) El siguiente recálculo SUBE DE NIVEL (2→3): +población, +D0, steel ─
	d0IronBefore := cityDemandD0E2E(t, ctx, pool, cityID, ironOreID)
	seqBefore := maxSeqOf(t, ctx, pool, "city.level_up")
	demandWorker.RunOnce(ctx)

	var level int32
	var population int64
	if err := pool.QueryRow(ctx, `SELECT level, population FROM world.cities WHERE id = $1`, cityID).Scan(&level, &population); err != nil {
		t.Fatalf("nivel/población de la ciudad: %v", err)
	}
	if level != 3 {
		t.Fatalf("nivel de la ciudad tras el suministro: %d, esperado 3 (subió de nivel)", level)
	}
	if population != 55000 {
		t.Fatalf("población tras subir de nivel: %d, esperado 55000 (+10%%)", population)
	}
	if got := cityDemandD0E2E(t, ctx, pool, cityID, ironOreID); got != d0IronBefore*12/10 {
		t.Fatalf("D0 de iron_ore tras subir de nivel: %d, esperado %d (+20%%)", got, d0IronBefore*12/10)
	}
	if seq := maxSeqOf(t, ctx, pool, "city.level_up"); seq <= seqBefore {
		t.Fatal("no se emitió city.level_up al subir de nivel")
	}
	// steel_ingot (desbloqueado en el nivel 3) ya se DEMANDA: su buy aparece en el
	// tablón publicada por la ciudad.
	steelPub := optionalUUID(t, ctx, pool, `
		SELECT id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window', 'open', 'micro_window')`, cityAccountID, steelID)
	if steelPub == uuid.Nil {
		t.Fatal("steel_ingot no se desbloqueó: el Balancer no publicó su buy tras subir de nivel")
	}

	// ── El AnalyticsWorker escribe economy_indicators macro coherentes ───────
	analyticsWorker.RunOnce(ctx)
	var moneySupply, gdp, emissionTotal, absorptionTotal int64
	if err := pool.QueryRow(ctx, `
		SELECT money_supply, simulated_gdp, emission_total, absorption_total
		  FROM analytics.economy_indicators ORDER BY bucket_start_sim DESC LIMIT 1`).
		Scan(&moneySupply, &gdp, &emissionTotal, &absorptionTotal); err != nil {
		t.Fatalf("economy_indicators: %v", err)
	}
	// money_supply es exactamente cash+escrow+guarantee (custody es stock, se excluye).
	wantMoney := moneySupplyE2E(t, ctx, pool)
	if moneySupply != wantMoney {
		t.Fatalf("money_supply de economy_indicators: %d, esperado %d (cash+escrow+guarantee)", moneySupply, wantMoney)
	}
	// El PIB del bucket contabiliza la venta entregada a la ciudad (dinero nuevo).
	if gdp < ironValue {
		t.Fatalf("simulated_gdp del bucket: %d, esperado ≥ %d (la venta entregada)", gdp, ironValue)
	}
	if emissionTotal <= 0 {
		t.Fatalf("emission_total del bucket: %d, esperado > 0 (hubo faucets)", emissionTotal)
	}
	// Invariante de doble entrada sobre el bucket de historia completa: emisión −
	// absorción = masa monetaria (todo el dinero nació de la emisión, GDD 5.5).
	if emissionTotal-absorptionTotal != moneySupply {
		t.Fatalf("coherencia macro: emisión(%d) − absorción(%d) = %d, esperado money_supply %d",
			emissionTotal, absorptionTotal, emissionTotal-absorptionTotal, moneySupply)
	}

	// ── Auditoría contable final sobre la BD quiescente ──────────────────────
	assertNoNegativeCashE2E(t, ctx, pool)
	assertBalancedLedger(t, ctx, pool)
	// La ciudad no retuvo inventario: reconciliación física↔contable a cero.
	if disc, err := reconWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d divergencias (err %v), esperado 0", disc, err)
	}
}

// e2eCityBuyPort implementa balancer.PublicationCreator (el PORT del Balancer)
// con el Contract Service real: publica la buy de la ciudad por
// contracts.CreatePublication —el MISMO camino estándar (validación, escrow,
// ventana de sorteo) que cualquier otra publicación del tablón, sin canal
// privilegiado (GDD 18.1)—. Réplica del adaptador del composition root (cmd/engine).
type e2eCityBuyPort struct{ svc *contracts.Service }

func (p e2eCityBuyPort) CreateCityBuy(ctx context.Context, by balancer.CityBuy) error {
	product := by.ProductID
	dest := by.DestinationNodeID
	_, err := p.svc.CreatePublication(ctx, by.CityAccountID, contracts.PublicationInput{
		Kind:               contracts.KindBuy,
		Channel:            contracts.ChannelBoard,
		ProductID:          &product,
		QuantityTotal:      by.Quantity,
		UnitPrice:          by.UnitPrice,
		DestinationNodeID:  &dest,
		DeliverySimSeconds: int64(by.DeliverySimSeconds),
	})
	return err
}

// ─── Lecturas auxiliares del Balancer ─────────────────────────────────────────

// citySupplyIndexE2E lee el índice de suministro histórico de una ciudad.
func citySupplyIndexE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cityID uuid.UUID) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(ctx, `SELECT supply_index FROM world.cities WHERE id = $1`, cityID).Scan(&v); err != nil {
		t.Fatalf("supply_index de la ciudad %s: %v", cityID, err)
	}
	return v
}

// cityDemandD0E2E lee la demanda base D0 de una (ciudad, producto).
func cityDemandD0E2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cityID, productID uuid.UUID) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx,
		`SELECT d0_per_sim_day FROM world.city_demand WHERE city_id = $1 AND product_id = $2`, cityID, productID).Scan(&v); err != nil {
		t.Fatalf("d0 de (%s,%s): %v", cityID, productID, err)
	}
	return v
}

// worldSourceBalanceE2E lee el saldo de la cuenta world_source de un producto
// (contrapartida física del banco central, ADR-022): sube al consumir la ciudad.
func worldSourceBalanceE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, productID uuid.UUID) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts WHERE kind = 'world_source' AND product_id = $1`, productID).Scan(&v); err != nil {
		t.Fatalf("world_source de %s: %v", productID, err)
	}
	return v
}

// moneySupplyE2E suma la masa monetaria (cash+escrow+guarantee): la magnitud que
// economy_indicators.money_supply debe reproducir.
func moneySupplyE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts WHERE kind IN ('cash', 'escrow', 'guarantee')`).Scan(&v); err != nil {
		t.Fatalf("masa monetaria: %v", err)
	}
	return v
}
