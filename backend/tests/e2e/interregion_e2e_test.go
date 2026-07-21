// Comercio INTER-REGIÓN con transporte MULTIMODAL real (Incremento 7, FASE 2
// MUNDO, GDD 7.2/7.3/9/15.1) de proceso a proceso contra una BD real, sin mocks.
// Sobre el mundo procedural (seed Askadia + worldgen: regiones conectadas por
// rail/sea, terminales intermodales en los junctions) demuestra dos cosas:
//
//   - TestBalancerServesGeneratedCityE2E (mandato §2): el Economy Balancer ATIENDE
//     las ciudades que AÑADE el generador. El DemandWorker barre TODAS las
//     world.cities (no solo Askadia): recalcula la demanda de una ciudad generada,
//     la PRE-FONDEA por emisión del banco central (faucet) y PUBLICA su buy por la
//     API estándar del Contract Service, con destino su CENTRO DE DISTRIBUCIÓN
//     (creado por worldgen). Prueba que city_demand + caja + centro de distribución
//     que siembra el generador bastan para que el balancer las descubra y sirva.
//
//   - TestInterRegionMultimodalDeliveryE2E (mandato §3): una ciudad de OTRA región
//     publica una buy de iron_ore; NORTE (en Askadia) la acepta con origen su
//     almacén. La entrega EXIGE cruzar la frontera inter-región por rail (o sea):
//     Norte planifica una ruta MULTIMODAL (road→rail→road), la parte por modo, compra
//     el vehículo de cada modo (camión/tren) y DESPACHA POR TRAMOS con TRANSBORDO en
//     las terminales intermodales; el TransitWorker mueve el cargamento cruzando la
//     frontera (segmentos de region_id distinto del mismo enlace inter-región) hasta
//     el centro de distribución de la ciudad; el delivery_confirmer LIQUIDA. Se
//     verifica: el cargamento cruzó la frontera (pasó por segmentos de dos regiones),
//     hubo transbordo (at_terminal) en cada cambio de modo, la ciudad recibió el
//     iron_ore, el contrato quedó settled, el ledger cuadra a cero y la
//     reconciliación física↔contable es 0.
//
// El reloj de simulación se CONGELA y se avanza por SQL para vencer los segmentos y
// las puertas de transbordo de forma determinista (mismo patrón que logistics_e2e).
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
	"github.com/jackc/pgx/v5"
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
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
	"github.com/lokiteitor/global-market/backend/internal/worldgen"
)

// Parámetros deterministas del escenario inter-región.
const (
	iiSimBase       int64 = 1_000_000
	iiBuyQty        int64 = 50  // iron_ore (volumen 2·50 = 100 ≤ cargo de cualquier vehículo)
	iiUnitPrice     int64 = 100 // dentro de [20,400] de iron_ore
	iiValue               = iiBuyQty * iiUnitPrice
	iiDeliverySecs  int64 = 5_000_000 // plazo holgado: la entrega multimodal es on-time
	iiTransitStep   int64 = 5_000     // > tiempo de viaje de un segmento (3600 sim-s)
	iiTransshipGate int64 = 20_000    // > tiempo de transbordo de una terminal (≥3600 sim-s)
)

// iiVehicleCodeByMode mapea el modo de un tramo al tipo de vehículo con el que se
// recorre (catálogo del seed para road; catálogo aditivo del worldgen para rail/sea).
var iiVehicleCodeByMode = map[string]string{
	"road": "truck_large",
	"rail": "freight_train",
	"sea":  "cargo_ship",
}

// ─── §2: el Balancer atiende las ciudades generadas ──────────────────────────

func TestBalancerServesGeneratedCityE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	seedWorld(t, ctx, pool, logger)
	runWorldgen(t, ctx, pool, logger)
	freezeSim(t, ctx, pool, iiSimBase)

	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")

	// Una ciudad AÑADIDA por el generador (fuera de Askadia): tiene su cuenta,
	// caja prefondeada, demanda de iron_ore y centro de distribución conectado.
	var cityID, cityAccountID uuid.UUID
	var cityName string
	if err := pool.QueryRow(ctx, `
		SELECT c.id, c.account_id, c.name
		  FROM world.cities c JOIN world.regions r ON r.id = c.region_id
		 WHERE NOT (r.grid_x = 0 AND r.grid_y = 0)
		 ORDER BY c.name LIMIT 1`).Scan(&cityID, &cityAccountID, &cityName); err != nil {
		t.Fatalf("localizando una ciudad generada: %v", err)
	}
	cityDistNode := queryUUID(t, ctx, pool,
		`SELECT id FROM world.network_nodes WHERE kind = 'distribution_center' AND city_id = $1`, cityID)

	// El centro de distribución de la ciudad generada quedó conectado a la red vial
	// de su región (junction↔centro), condición para que su entrega sea realizable.
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.network_links
		 WHERE mode = 'road' AND (from_node_id = $1 OR to_node_id = $1)`, cityDistNode); n < 2 {
		t.Fatalf("enlaces road del centro de distribución de %q: %d, esperado ≥2 (bidireccional junction↔ciudad)", cityName, n)
	}

	// Fixtures deterministas de la curva/nivel de la ciudad generada (mismo patrón
	// que balancer_e2e): iron_ore desbloqueado, supply_ema alineado con D0, banda de
	// nivel estable.
	mustExecE2E(t, ctx, pool,
		`UPDATE world.city_demand SET supply_ema = 1000, recent_supply = 0, updated_at_sim = $1 WHERE city_id = $2 AND product_id = $3`,
		iiSimBase, cityID, ironOreID)
	mustExecE2E(t, ctx, pool,
		`UPDATE world.cities SET level = 2, population = 50000, supply_index = 1500, updated_at_sim = $1 WHERE id = $2`,
		iiSimBase, cityID)

	// Contract Service real + lector del reloj congelado + DemandWorker con el PORT
	// estándar (contracts.CreatePublication), réplica del adaptador de cmd/engine.
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.PublicationTTLSimSeconds = 10_000_000
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	bopts := balancer.DefaultOptions()
	bopts.SupplyIndexDecayPerSimDay = 0
	bopts.SaturationMin = 0.1
	bopts.SaturationMax = 1.0
	bopts.BuyTargetDays = 1.0
	bopts.CityBuyDeadlineSim = 500_000
	bmetrics := balancer.NewMetrics(prometheus.NewRegistry())
	demandWorker, err := balancer.NewDemandWorker(pool, e2eCityBuyPort{svc: ccriSvc}, reader, bopts, bmetrics, logger)
	if err != nil {
		t.Fatalf("balancer.NewDemandWorker: %v", err)
	}

	// Drena la caja de la ciudad generada: fuerza el pre-fondeo por emisión (faucet).
	drainCashTo(t, ctx, pool, cityAccountID, 0)
	emissionBefore := emissionBalanceE2E(t, ctx, pool)

	// El Balancer barre TODAS las ciudades (incluidas las del generador) y publica.
	demandWorker.RunOnce(ctx)

	// La ciudad generada tiene ahora una buy VIVA en el tablón, con destino su centro
	// de distribución (la API estándar, sin canal privilegiado).
	buyPubID := optionalUUID(t, ctx, pool, `
		SELECT id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window', 'open', 'micro_window')`, cityAccountID, ironOreID)
	if buyPubID == uuid.Nil {
		t.Fatalf("el Balancer no publicó la buy de iron_ore de la ciudad generada %q", cityName)
	}
	var qty, price int64
	var buyDest uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT quantity_total, unit_price, destination_node_id FROM ledger.publications WHERE id = $1`, buyPubID).
		Scan(&qty, &price, &buyDest); err != nil {
		t.Fatalf("buy de la ciudad generada: %v", err)
	}
	if buyDest != cityDistNode {
		t.Fatalf("destino de la buy de %q: %s, esperado su centro de distribución %s", cityName, buyDest, cityDistNode)
	}
	if qty <= 0 || price <= 0 {
		t.Fatalf("buy de %q con qty/precio no positivos: qty=%d precio=%d", cityName, qty, price)
	}

	// El faucet: la emisión bajó EXACTAMENTE el escrow de la buy (la caja estaba a 0)
	// y quedó el asiento de fondeo de ciudad — el balancer pre-fondea a las nuevas
	// ciudades igual que a la sembrada.
	value := qty * price
	if got := emissionBalanceE2E(t, ctx, pool); got != emissionBefore-value {
		t.Fatalf("emisión de fondeo de la ciudad generada: %d, esperado %d (Δ = −%d, el escrow)", got, emissionBefore-value, value)
	}
	if n := countRows(t, ctx, pool,
		`SELECT count(*) FROM ledger.transactions WHERE description LIKE 'Fondeo de ciudad (faucet)%'`); n < 1 {
		t.Fatalf("no se asentó el fondeo (faucet) de la ciudad generada %q", cityName)
	}

	// Coherencia contable global tras el faucet.
	assertNoNegativeCashE2E(t, ctx, pool)
	assertBalancedLedger(t, ctx, pool)
	t.Logf("§2 OK: el Balancer publicó y prefondeó la buy de la ciudad generada %q (qty=%d precio=%d)", cityName, qty, price)
}

// ─── §3: entrega inter-región con transporte multimodal ──────────────────────

func TestInterRegionMultimodalDeliveryE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	seedWorld(t, ctx, pool, logger)
	runWorldgen(t, ctx, pool, logger)
	freezeSim(t, ctx, pool, iiSimBase)

	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	norteNode, _ := warehouseNodeOf(t, ctx, pool, norteID)
	askadiaRegion := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE grid_x = 0 AND grid_y = 0`)

	// Descubre en el grafo REAL un caso inter-región: un enlace rail/sea que sale del
	// junction de Askadia hacia el junction de una región vecina cuya región tiene una
	// ciudad con centro de distribución. Ese centro es el destino que EXIGE cruzar la
	// frontera (no hay road inter-región).
	tgt := discoverInterRegionTarget(t, ctx, pool, askadiaRegion)
	t.Logf("§3 objetivo: cruce por %s hacia la región (%d,%d), ciudad %q", tgt.crossMode, tgt.gx, tgt.gy, tgt.cityName)

	// La ciudad de la otra región publica su buy (la misma API estándar que usa el
	// Balancer: publicador = la cuenta de la ciudad, destino = su centro).
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	contractsOpts.PublicationTTLSimSeconds = 10_000_000
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	if err := (e2eCityBuyPort{svc: ccriSvc}).CreateCityBuy(ctx, balancer.CityBuy{
		CityAccountID:      tgt.cityAccountID,
		ProductID:          ironOreID,
		Quantity:           iiBuyQty,
		UnitPrice:          iiUnitPrice,
		DestinationNodeID:  tgt.cityDistNode,
		DeliverySimSeconds: simtime.SimTime(iiDeliverySecs),
	}); err != nil {
		t.Fatalf("publicar la buy de la ciudad de otra región: %v", err)
	}
	buyPubID := queryUUID(t, ctx, pool, `
		SELECT id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, tgt.cityAccountID, ironOreID)

	// Gateway real (para las acciones de Norte: aceptar, comprar vehículos, planificar,
	// crear rutas, despachar) y motores/consumidores con el MISMO reloj congelado.
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

	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	transitOpts := fleet.DefaultWorkerOptions()
	transitOpts.Roll = func() float64 { return 1.0 } // sin avería: tránsito determinista
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

	norteToken := login(t, srv, traderName, traderSecret)
	norteCash0 := cashOf(t, ctx, pool, norteID)
	assertReconciled(t, ctx, reconWorker, "baseline")

	// ── (1) Norte acepta la buy de la ciudad con origen su almacén (cross-región) ─
	r := callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+buyPubID.String()+"/acceptances",
		norteToken, uuid.NewString(), map[string]any{"quantity": itoa(iiBuyQty), "origin_node_id": norteNode.String()})
	if r.status != http.StatusCreated {
		t.Fatalf("Norte acepta la buy: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	accID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if accID == "" {
		t.Fatal("aceptación sin id")
	}

	// ── (2) Sorteo → contrato buy cross-región (comprador = ciudad de otra región) ─
	contractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, norteToken, accID)
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, norteToken, nil)
	c := asMap(t, r.body["data"], "data")
	if c["status"] != "active" || c["buyer_account_id"] != tgt.cityAccountID.String() || c["seller_account_id"] != norteID.String() {
		t.Fatalf("contrato inter-región inesperado: %v", c)
	}
	if c["origin_node_id"] != norteNode.String() || c["destination_node_id"] != tgt.cityDistNode.String() {
		t.Fatalf("nodos del contrato inter-región inesperados: %v", c)
	}
	assertReconciled(t, ctx, reconWorker, "tras confirmar")

	// ── (3) shipment_creator: cargamento en el almacén de Norte ──────────────────
	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")
	shipmentID := uuid.MustParse(findShipment(t, ctx, srv, norteToken, contractID))

	// ── (4) Ruta MULTIMODAL Norte→ciudad y partición por modo ────────────────────
	legs := planInterRegionRoute(t, ctx, pool, srv, norteToken, norteNode, tgt.cityDistNode)
	if !legsAreMultimodal(legs) {
		t.Fatalf("la ruta Norte→ciudad no es multimodal: %+v", legs)
	}
	// El tramo de cruce (rail/sea) usa un enlace inter-región partido en ≥2 regiones.
	crossLeg := firstLegOfMode(t, legs, tgt.crossMode)
	if regs := linkSegmentRegions(t, ctx, pool, crossLeg.linkID); len(regs) < 2 {
		t.Fatalf("el enlace de cruce %s no está partido por la frontera: regiones=%v", crossLeg.linkID, regs)
	}
	runs := splitRunsByMode(legs)
	t.Logf("§3 ruta multimodal con %d tramos en %d despachos por modo", len(legs), len(runs))

	// ── (5) Despacho POR TRAMOS con transbordo en terminal; el TransitWorker cruza
	//        la frontera ─────────────────────────────────────────────────────────
	crossedRegions := map[uuid.UUID]bool{}
	sawAtTerminal := false
	for i, run := range runs {
		last := i == len(runs)-1
		startNode := run.legs[0].fromNode

		// Puerta de transbordo: si el cargamento espera en una terminal (at_terminal),
		// se avanza el reloj para consumir el tiempo de transbordo antes de re-despachar.
		if shipmentStatusOf(t, ctx, pool, shipmentID) == "at_terminal" {
			sawAtTerminal = true
			advanceSim(t, ctx, pool, iiTransshipGate)
		}

		vehicleTypeID := queryUUID(t, ctx, pool, `SELECT id FROM world.vehicle_types WHERE code = $1`, iiVehicleCodeByMode[run.mode])
		vehID := buyVehicleAt(t, srv, norteToken, vehicleTypeID, startNode)
		routeID := createRoute(t, srv, norteToken, "tramo-"+run.mode, run.linkIDs())
		dispatchShipment(t, srv, norteToken, shipmentID.String(), vehID, routeID)

		target := "at_terminal"
		if last {
			target = "delivered"
		}
		driveLegRegions(t, ctx, pool, transitWorker, shipmentID, uuid.MustParse(vehID), target, crossedRegions)
	}

	// ── (6) El cargamento CRUZÓ la frontera y hubo TRANSBORDO ────────────────────
	if !sawAtTerminal {
		t.Fatal("no hubo ningún transbordo (at_terminal) en la ruta multimodal")
	}
	if !crossedRegions[askadiaRegion] || !crossedRegions[tgt.regionID] {
		t.Fatalf("el cargamento no cruzó la frontera: regiones observadas=%v (esperado incluir Askadia %s y %s)",
			regionKeys(crossedRegions), askadiaRegion, tgt.regionID)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events WHERE event_type = 'shipment.at_terminal' AND aggregate_id = $1`, shipmentID); n < 1 {
		t.Fatal("no se emitió ningún shipment.at_terminal")
	}
	sh := getShipment(t, srv, norteToken, shipmentID.String())
	if sh["status"] != "delivered" || sh["at_node_id"] != tgt.cityDistNode.String() {
		t.Fatalf("cargamento tras el tránsito: %v (esperado delivered en el centro de %q)", sh, tgt.cityName)
	}

	// ── (7) delivery_confirmer: entrega on-time y LIQUIDACIÓN ────────────────────
	drainConsumer(t, ctx, pool, dcConsumer, deliveryConfirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")

	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, norteToken, nil)
	c = asMap(t, r.body["data"], "data")
	if c["status"] != "settled" || c["quantity_delivered"] != itoa(iiBuyQty) {
		t.Fatalf("contrato inter-región no liquidado: %v", c)
	}
	if fill, _ := c["fill_bp"].(float64); fill != 10000 {
		t.Fatalf("fill_bp: %v, esperado 10000 (100%%)", c["fill_bp"])
	}
	// La ciudad de la otra región recibió el iron_ore como stock_free en su centro.
	cityDistBuilding := queryUUID(t, ctx, pool, `SELECT building_id FROM world.network_nodes WHERE id = $1`, tgt.cityDistNode)
	if got := stockFreeOf(t, ctx, pool, tgt.cityAccountID, ironOreID, cityDistBuilding); got != iiBuyQty {
		t.Fatalf("stock_free de la ciudad de otra región en su centro: %d, esperado %d", got, iiBuyQty)
	}
	// Norte cobró el valor (dinero nuevo en circulación) tras pagar los vehículos y la
	// garantía; la garantía se recupera al liquidar, así que el neto de caja es
	// +valor − (precio de los vehículos comprados).
	spentOnVehicles := norteCash0 + iiValue - cashOf(t, ctx, pool, norteID)
	if spentOnVehicles <= 0 {
		t.Fatalf("caja de Norte no refleja el cobro esperado: cash0=%d final=%d valor=%d", norteCash0, cashOf(t, ctx, pool, norteID), iiValue)
	}

	// ── (8) Coherencia final: sin cajas negativas, ledger a cero, reconciliación 0 ─
	assertNoNegativeCashE2E(t, ctx, pool)
	assertBalancedLedger(t, ctx, pool)
	assertReconciled(t, ctx, reconWorker, "final (entrega inter-región liquidada)")
}

// ─── Descubrimiento del caso inter-región en el grafo generado ────────────────

// interRegionTarget describe una ciudad de una región vecina de Askadia alcanzable
// por un enlace inter-región (rail/sea) desde el junction de Askadia.
type interRegionTarget struct {
	crossMode     string
	regionID      uuid.UUID
	gx, gy        int
	cityName      string
	cityAccountID uuid.UUID
	cityDistNode  uuid.UUID
}

// discoverInterRegionTarget busca, sobre el mundo generado, un enlace rail/sea que
// salga del junction de Askadia hacia el junction de una región vecina TERRESTRE con
// ciudad y centro de distribución. Determinista (ORDER BY estable). Sin candidatos,
// omite el test (topología de la semilla).
func discoverInterRegionTarget(t *testing.T, ctx context.Context, pool *pgxpool.Pool, askadiaRegion uuid.UUID) interRegionTarget {
	t.Helper()
	var tgt interRegionTarget
	err := pool.QueryRow(ctx, `
		SELECT il.mode::text, rn.id, rn.grid_x, rn.grid_y, c.name, c.account_id, dc.id
		  FROM world.network_nodes aj
		  JOIN world.network_links il ON il.from_node_id = aj.id AND il.mode IN ('rail','sea')
		  JOIN world.network_nodes nj ON nj.id = il.to_node_id AND nj.kind = 'junction'
		  JOIN world.regions rn ON rn.id = nj.region_id AND rn.id <> $1
		  JOIN world.cities c ON c.region_id = rn.id
		  JOIN world.network_nodes dc ON dc.city_id = c.id AND dc.kind = 'distribution_center'
		 WHERE aj.region_id = $1 AND aj.kind = 'junction'
		 ORDER BY il.mode, rn.grid_x, rn.grid_y, c.name
		 LIMIT 1`, askadiaRegion).
		Scan(&tgt.crossMode, &tgt.regionID, &tgt.gx, &tgt.gy, &tgt.cityName, &tgt.cityAccountID, &tgt.cityDistNode)
	if err == pgx.ErrNoRows {
		t.Skip("el mundo generado no ofrece una ciudad vecina inter-región (topología de la semilla)")
	}
	if err != nil {
		t.Fatalf("descubriendo el objetivo inter-región: %v", err)
	}
	return tgt
}

// ─── Ruta multimodal: planificación, partición por modo y ejecución ──────────

// interRegionLeg es un tramo del plan con su enlace, modo y extremos (resueltos del
// grafo para poder partir la ruta por modo y comprar vehículos en los nodos justos).
type interRegionLeg struct {
	linkID   uuid.UUID
	mode     string
	fromNode uuid.UUID
	toNode   uuid.UUID
}

// modeRun es una secuencia contigua de tramos de un solo modo: un despacho.
type modeRun struct {
	mode string
	legs []interRegionLeg
}

func (rn modeRun) linkIDs() []any {
	out := make([]any, len(rn.legs))
	for i, l := range rn.legs {
		out[i] = l.linkID.String()
	}
	return out
}

// planInterRegionRoute pide al gateway el route-plan Norte→ciudad (todos los modos)
// y resuelve los extremos de cada enlace desde el grafo (para partir por modo y
// comprar los vehículos en los nodos justos).
func planInterRegionRoute(t *testing.T, ctx context.Context, pool *pgxpool.Pool, srv *httptest.Server, token string, origin, dest uuid.UUID) []interRegionLeg {
	t.Helper()
	r := call(t, srv, http.MethodPost, "/api/v1/logistics/route-plans", token, map[string]any{
		"origin_node_id": origin.String(), "destination_node_id": dest.String(),
	})
	if r.status != http.StatusOK {
		t.Fatalf("route-plan inter-región: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	raw, ok := asMap(t, r.body["data"], "data")["legs"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("route-plan inter-región sin legs: %s", r.raw)
	}
	legs := make([]interRegionLeg, len(raw))
	for i, l := range raw {
		m := asMap(t, l, "leg")
		linkID := uuid.MustParse(m["link_id"].(string))
		var from, to uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT from_node_id, to_node_id FROM world.network_links WHERE id = $1`, linkID).Scan(&from, &to); err != nil {
			t.Fatalf("extremos del enlace %s: %v", linkID, err)
		}
		legs[i] = interRegionLeg{linkID: linkID, mode: m["mode"].(string), fromNode: from, toNode: to}
	}
	return legs
}

// legsAreMultimodal indica si la ruta combina ≥2 modos (implica transbordo).
func legsAreMultimodal(legs []interRegionLeg) bool {
	for _, l := range legs[1:] {
		if l.mode != legs[0].mode {
			return true
		}
	}
	return false
}

// firstLegOfMode devuelve el primer tramo del modo dado (falla si no hay).
func firstLegOfMode(t *testing.T, legs []interRegionLeg, mode string) interRegionLeg {
	t.Helper()
	for _, l := range legs {
		if l.mode == mode {
			return l
		}
	}
	t.Fatalf("la ruta no tiene ningún tramo de modo %s: %+v", mode, legs)
	return interRegionLeg{}
}

// splitRunsByMode agrupa los tramos en carreras contiguas de un solo modo (cada una
// = un despacho con transbordo en la terminal entre carreras). Un tren jamás circula
// por road ni un camión por rail/sea: por eso cada carrera es de un único modo.
func splitRunsByMode(legs []interRegionLeg) []modeRun {
	var runs []modeRun
	for _, l := range legs {
		if n := len(runs); n > 0 && runs[n-1].mode == l.mode {
			runs[n-1].legs = append(runs[n-1].legs, l)
			continue
		}
		runs = append(runs, modeRun{mode: l.mode, legs: []interRegionLeg{l}})
	}
	return runs
}

// driveLegRegions avanza el reloj y el TransitWorker hasta que el cargamento alcanza
// el estado objetivo (at_terminal en un tramo intermedio, delivered en el último),
// acumulando en crossed las regiones de los segmentos que el vehículo va recorriendo
// (evidencia dinámica del cruce de frontera).
func driveLegRegions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w *fleet.TransitWorker,
	shipmentID, vehicleID uuid.UUID, target string, crossed map[uuid.UUID]bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for {
		if shipmentStatusOf(t, ctx, pool, shipmentID) == target {
			return
		}
		if rid, ok := vehicleSegmentRegion(ctx, pool, vehicleID); ok {
			crossed[rid] = true
		}
		advanceSim(t, ctx, pool, iiTransitStep)
		w.RunOnce(ctx)
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando estado %q del cargamento %s (actual %s)",
				target, shipmentID, shipmentStatusOf(t, ctx, pool, shipmentID))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ─── Consultas y acciones de apoyo ───────────────────────────────────────────

// vehicleSegmentRegion devuelve la región del segmento sobre el que viaja el
// vehículo (ok=false si está idle/sin segmento).
func vehicleSegmentRegion(ctx context.Context, pool *pgxpool.Pool, vehicleID uuid.UUID) (uuid.UUID, bool) {
	var region uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT s.region_id FROM world.vehicles v
		  JOIN world.link_segments s ON s.id = v.on_segment_id
		 WHERE v.id = $1`, vehicleID).Scan(&region)
	if err != nil {
		return uuid.Nil, false
	}
	return region, true
}

// linkSegmentRegions devuelve el conjunto de regiones de los segmentos de un enlace
// (un enlace inter-región tiene 2, una por lado de la frontera).
func linkSegmentRegions(t *testing.T, ctx context.Context, pool *pgxpool.Pool, linkID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT DISTINCT region_id FROM world.link_segments WHERE link_id = $1`, linkID)
	if err != nil {
		t.Fatalf("regiones del enlace %s: %v", linkID, err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var r uuid.UUID
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan región de segmento: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// regionKeys devuelve las claves de un set de regiones (para mensajes de error).
func regionKeys(m map[uuid.UUID]bool) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// buyVehicleAt compra un vehículo del tipo dado entregado en un nodo y devuelve su id.
func buyVehicleAt(t *testing.T, srv *httptest.Server, token string, vehicleTypeID, nodeID uuid.UUID) string {
	t.Helper()
	r := call(t, srv, http.MethodPost, "/api/v1/world/vehicles", token, map[string]any{
		"vehicle_type_id": vehicleTypeID.String(), "delivery_node_id": nodeID.String(),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("comprar vehículo %s en %s: status %d (cuerpo: %s)", vehicleTypeID, nodeID, r.status, r.raw)
	}
	id, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if id == "" {
		t.Fatalf("vehículo comprado sin id: %s", r.raw)
	}
	return id
}

// createRoute crea una ruta on_demand con la secuencia de enlaces dada y devuelve su id.
func createRoute(t *testing.T, srv *httptest.Server, token, name string, linkIDs []any) string {
	t.Helper()
	r := call(t, srv, http.MethodPost, "/api/v1/logistics/routes", token, map[string]any{
		"name": name, "kind": "on_demand", "legs": linkIDs,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("crear ruta %q: status %d (cuerpo: %s)", name, r.status, r.raw)
	}
	id, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if id == "" {
		t.Fatalf("ruta creada sin id: %s", r.raw)
	}
	return id
}

// dispatchShipment despacha un cargamento en un vehículo por una ruta (un tramo de
// un solo modo).
func dispatchShipment(t *testing.T, srv *httptest.Server, token, shipmentID, vehicleID, routeID string) {
	t.Helper()
	r := call(t, srv, http.MethodPost, "/api/v1/world/shipments/"+shipmentID+"/dispatch", token, map[string]any{
		"vehicle_id": vehicleID, "route_id": routeID,
	})
	if r.status != http.StatusOK || asMap(t, r.body["data"], "data")["status"] != "in_transit" {
		t.Fatalf("despachar el cargamento %s: status %d (cuerpo: %s)", shipmentID, r.status, r.raw)
	}
}

// ─── Bootstrap del mundo ──────────────────────────────────────────────────────

// seedWorld siembra Askadia (mundo base del seed) con las credenciales de e2e.
func seedWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	t.Helper()
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// runWorldgen genera (aditivo) el mundo multi-región procedural sobre el seed de
// Askadia con la configuración canónica (semilla 42, grilla 3×3, regiones de 50 km).
func runWorldgen(t *testing.T, ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) {
	t.Helper()
	if _, err := worldgen.Generate(ctx, pool, worldgen.DefaultOptions(), logger); err != nil {
		t.Fatalf("worldgen: %v", err)
	}
}
