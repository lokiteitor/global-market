// Ejecución logística REAL del CCRI (Incremento 3, Fase 1: logística terrestre)
// de proceso a proceso contra una BD real: el árbol de rutas REAL del gateway
// (internal/gateway.BuildHandler, idéntico a cmd/gateway), el worker CCRI del
// sorteo (internal/contracts), el MOTOR DE TRÁNSITO (internal/world/fleet) y los
// consumidores cross-context del outbox — shipment_creator (world) y
// delivery_confirmer (contracts) — disparados directamente. Ningún mock.
//
// DEMO publica una SOLICITUD DE COMPRA (buy) de iron_ore con destino = su
// almacén; NORTE (que tiene iron_ore) la acepta con origen = su almacén
// (cross-node) → sorteo → contract.confirmed (buy cross-node) → el
// shipment_creator crea el cargamento en el almacén de Norte y descuenta su
// inventario físico → Norte compra un camión, computa un route-plan Norte→Demo,
// crea la ruta y DESPACHA el cargamento → el TransitWorker mueve el vehículo por
// los segmentos (se avanza el reloj) hasta llegar al almacén de Demo →
// shipment.arrived → el delivery_confirmer registra la entrega on-time y LIQUIDA:
// Demo recibe el iron_ore en SU almacén (físico + contable), Norte cobra, las
// garantías se liberan y el contrato queda settled con fill 10000. La contabilidad
// y el plano físico cuadran en CADA paso (incluida la coherencia con los
// cargamentos in_transit) y la reconciliación física↔contable final es 0.
//
// El reloj de simulación se CONGELA y se avanza por SQL para que los segmentos
// venzan de forma determinista sin esperas de pared (mismo patrón que
// production_e2e). El sorteo del CCRI usa ventanas wall-clock cortas.
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

// Parámetros del ciclo logístico que el test reproduce en sus aserciones.
const (
	logBuyQty       int64 = 1_000 // iron_ore comprado (volumen 2·1000 = 2000 ≤ cargo 6000)
	logUnitPrice    int64 = 100   // dentro de [20, 400] de iron_ore
	logValue              = logBuyQty * logUnitPrice
	logGuarantee          = logValue / 10 // garantía del vendedor (10%)
	logDeliverySecs int64 = 500_000       // plazo holgado: la llegada es on-time
	logSimBase      int64 = 1_000_000
	logInitialIron  int64 = 5_000  // stock inicial de iron_ore por corporación (seed)
	logTruckPrice   int64 = 90_000 // truck_large purchase_price
	logTransitStep  int64 = 5_000  // > tiempo de viaje de un segmento (3600 sim-s)
)

func TestLogisticsDeliveryE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Seed del mundo (incluye la red vial y la flota del Incremento 3) ──────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ── Reloj CONGELADO: lo controlamos por SQL ──────────────────────────────
	freezeSim(t, ctx, pool, logSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	truckLargeID := queryUUID(t, ctx, pool, `SELECT id FROM world.vehicle_types WHERE code = $1`, "truck_large")
	demoNode, demoWH := warehouseNodeOf(t, ctx, pool, demoID)
	norteNode, norteWH := warehouseNodeOf(t, ctx, pool, norteID)

	// La red vial se sembró conexa: existe el junction y los enlaces road en
	// ambos sentidos (Fase 1: 1 segmento por enlace, congestión fluida).
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.network_nodes WHERE kind = 'junction'`); n != 1 {
		t.Fatalf("nodos junction sembrados: %d, esperado 1", n)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.network_links WHERE mode = 'road'`); n != 6 {
		t.Fatalf("enlaces road sembrados: %d, esperado 6 (bidireccionales Demo–junction–Norte y junction–ciudad)", n)
	}

	// ── Gateway real (ventanas de sorteo cortas; reloj sin caché) ────────────
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth: auth.Options{
				LoginPerMin: auth.DefaultRateLoginPerMin,
				APIRPS:      auth.DefaultRateAPIRPS,
				APIBurst:    auth.DefaultRateAPIBurst,
			},
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

	// Motor de tránsito con avería desactivada (Roll = 1.0): la ruta es
	// determinista (el resto del motor —combustible, avance, entrega— es real).
	transitOpts := fleet.DefaultWorkerOptions()
	transitOpts.Roll = func() float64 { return 1.0 }
	transitWorker, err := fleet.NewTransitWorker(pool, reader, transitOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("fleet.NewTransitWorker: %v", err)
	}

	// Consumidores cross-context del outbox.
	shipmentCreator := fleet.NewShipmentCreator(logger, prometheus.NewRegistry())
	scConsumer := shipmentCreator.NewConsumer(pool, outbox.WithLogger(logger))
	deliveryConfirmer, err := contracts.NewDeliveryConfirmer(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewDeliveryConfirmer: %v", err)
	}
	dcConsumer := deliveryConfirmer.NewConsumer(pool, outbox.WithLogger(logger))

	// Reconciliación física↔contable (mismo lector).
	reconWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}

	demoToken := login(t, srv, demoName, demoSecret)
	norteToken := login(t, srv, traderName, traderSecret)

	demoCash0 := cashOf(t, ctx, pool, demoID)
	norteCash0 := cashOf(t, ctx, pool, norteID)
	assertReconciled(t, ctx, reconWorker, "baseline")

	// ── (1) DEMO publica una COMPRA (buy) de iron_ore con destino = su almacén ─
	r := call(t, srv, http.MethodPost, "/api/v1/contracts/publications", demoToken, map[string]any{
		"kind":                 "buy",
		"product_id":           ironOreID.String(),
		"quantity_total":       itoa(logBuyQty),
		"unit_price":           itoa(logUnitPrice),
		"min_lot":              "500",
		"destination_node_id":  demoNode.String(),
		"delivery_sim_seconds": logDeliverySecs,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar buy: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	pub := asMap(t, r.body["data"], "data")
	pubID, _ := pub["id"].(string)
	if pub["kind"] != "buy" || pub["status"] != "draw_window" || pubID == "" {
		t.Fatalf("publicación buy inesperada: %v", pub)
	}
	assertMeta(t, r.body, "publicar buy")
	// Demo retiene el 100% del valor en escrow.
	if got := cashOf(t, ctx, pool, demoID); got != demoCash0-logValue {
		t.Fatalf("caja de Demo tras publicar buy: %d, esperado %d", got, demoCash0-logValue)
	}

	// ── (2) NORTE acepta con origen = su almacén (cross-node) ────────────────
	idemKey := uuid.NewString()
	r = callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+pubID+"/acceptances",
		norteToken, idemKey, map[string]any{"quantity": itoa(logBuyQty), "origin_node_id": norteNode.String()})
	if r.status != http.StatusCreated {
		t.Fatalf("aceptar buy: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	accID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if accID == "" {
		t.Fatal("aceptación sin id")
	}
	// Norte congela 1000 de stock (free → reserved) y la garantía del 10%.
	if got := stockFreeOf(t, ctx, pool, norteID, ironOreID, norteWH); got != logInitialIron-logBuyQty {
		t.Fatalf("stock_free de iron_ore de Norte tras aceptar: %d, esperado %d", got, logInitialIron-logBuyQty)
	}
	if got := cashOf(t, ctx, pool, norteID); got != norteCash0-logGuarantee {
		t.Fatalf("caja de Norte tras aceptar: %d, esperado %d", got, norteCash0-logGuarantee)
	}

	// ── (3) Sorteo → contract.confirmed (buy cross-node) ─────────────────────
	contractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, norteToken, accID)
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("contrato: status %d (cuerpo: %s)", r.status, r.raw)
	}
	c := asMap(t, r.body["data"], "data")
	if c["status"] != "active" ||
		c["buyer_account_id"] != demoID.String() || c["seller_account_id"] != norteID.String() {
		t.Fatalf("contrato confirmado inesperado: %v", c)
	}
	// Cross-node REAL: origen (Norte) ≠ destino (Demo) ⇒ NO se liquida in situ.
	if c["origin_node_id"] == c["destination_node_id"] {
		t.Fatalf("contrato debía ser cross-node: origin (%v) == destination (%v)", c["origin_node_id"], c["destination_node_id"])
	}
	if c["origin_node_id"] != norteNode.String() || c["destination_node_id"] != demoNode.String() {
		t.Fatalf("nodos del contrato inesperados: %v", c)
	}
	assertReconciled(t, ctx, reconWorker, "tras confirmar (sin cargamento aún)")

	// ── (4) shipment_creator: cargamento en Norte + descuento de inventario ──
	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")

	shipmentID := findShipment(t, ctx, srv, norteToken, contractID)
	sh := getShipment(t, srv, norteToken, shipmentID)
	if sh["status"] != "in_warehouse" || sh["owner_account_id"] != norteID.String() ||
		sh["at_node_id"] != norteNode.String() || sh["quantity"] != itoa(logBuyQty) {
		t.Fatalf("cargamento creado inesperado: %v", sh)
	}
	// El stock físico dejó el almacén de Norte (pasó al cargamento); el reservado
	// contable no cambia (sigue reservado, sólo cambió su ubicación física).
	if got := inventoryQty(t, ctx, pool, norteWH, ironOreID); got != logInitialIron-logBuyQty {
		t.Fatalf("inventario físico de iron_ore en Norte tras crear el cargamento: %d, esperado %d", got, logInitialIron-logBuyQty)
	}
	if got := stockReservedOf(t, ctx, pool, norteID, ironOreID, norteWH); got != logBuyQty {
		t.Fatalf("stock reservado de Norte: %d, esperado %d", got, logBuyQty)
	}
	// Coherencia con cargamentos in_warehouse: reconciliación sigue a cero.
	assertReconciled(t, ctx, reconWorker, "cargamento in_warehouse")
	assertBalancedLedger(t, ctx, pool)

	// ── (5) Norte compra un camión en su almacén ─────────────────────────────
	r = call(t, srv, http.MethodPost, "/api/v1/world/vehicles", norteToken, map[string]any{
		"vehicle_type_id":  truckLargeID.String(),
		"delivery_node_id": norteNode.String(),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("comprar camión: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	veh := asMap(t, r.body["data"], "data")
	vehID, _ := veh["id"].(string)
	if veh["status"] != "idle" || vehID == "" {
		t.Fatalf("vehículo inesperado: %v", veh)
	}
	if pos := asMap(t, veh["position"], "position"); pos["at_node_id"] != norteNode.String() {
		t.Fatalf("el camión no está en el almacén de Norte: %v", pos)
	}
	if got := cashOf(t, ctx, pool, norteID); got != norteCash0-logGuarantee-logTruckPrice {
		t.Fatalf("caja de Norte tras comprar el camión: %d, esperado %d", got, norteCash0-logGuarantee-logTruckPrice)
	}

	// ── (6) route-plan Norte→Demo y creación de la ruta ──────────────────────
	r = call(t, srv, http.MethodPost, "/api/v1/logistics/route-plans", norteToken, map[string]any{
		"origin_node_id":      norteNode.String(),
		"destination_node_id": demoNode.String(),
		"modes":               []string{"road"},
	})
	if r.status != http.StatusOK {
		t.Fatalf("route-plan: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	plan := asMap(t, r.body["data"], "data")
	if plan["origin_node_id"] != norteNode.String() || plan["destination_node_id"] != demoNode.String() {
		t.Fatalf("route-plan con extremos inesperados: %v", plan)
	}
	legs, ok := plan["legs"].([]any)
	if !ok || len(legs) != 2 {
		t.Fatalf("route-plan: esperados 2 legs (Norte→junction→Demo), cuerpo: %s", r.raw)
	}
	planLinks := make([]any, len(legs))
	for i, l := range legs {
		leg := asMap(t, l, "leg")
		link, _ := leg["link_id"].(string)
		if link == "" {
			t.Fatalf("leg %d del route-plan sin link_id: %v", i, leg)
		}
		planLinks[i] = link
	}

	r = call(t, srv, http.MethodPost, "/api/v1/logistics/routes", norteToken, map[string]any{
		"name": "Norte→Demo", "kind": "on_demand", "legs": planLinks,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("crear ruta: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	route := asMap(t, r.body["data"], "data")
	routeID, _ := route["id"].(string)
	if route["owner_account_id"] != norteID.String() || route["active"] != true || routeID == "" {
		t.Fatalf("ruta creada inesperada: %v", route)
	}

	// ── (7) DESPACHO del cargamento en el camión por la ruta ─────────────────
	r = call(t, srv, http.MethodPost, "/api/v1/world/shipments/"+shipmentID+"/dispatch", norteToken, map[string]any{
		"vehicle_id": vehID, "route_id": routeID,
	})
	if r.status != http.StatusOK {
		t.Fatalf("despachar: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	dispatched := asMap(t, r.body["data"], "data")
	if dispatched["status"] != "in_transit" || dispatched["vehicle_id"] != vehID {
		t.Fatalf("cargamento despachado inesperado: %v", dispatched)
	}
	// En tránsito el stock sigue atribuido al almacén de origen: reconciliación 0.
	assertReconciled(t, ctx, reconWorker, "cargamento in_transit")

	// ── (8) El TransitWorker mueve el vehículo hasta el almacén de Demo ───────
	demoIron0 := inventoryQty(t, ctx, pool, demoWH, ironOreID)
	driveTransitUntilDelivered(t, ctx, pool, transitWorker, uuid.MustParse(shipmentID))

	// Entrega física: el cargamento reposa en el nodo de Demo y su stock se
	// integró al inventario físico del almacén de Demo.
	sh = getShipment(t, srv, norteToken, shipmentID)
	if sh["status"] != "delivered" || sh["at_node_id"] != demoNode.String() {
		t.Fatalf("cargamento tras el tránsito: %v (esperado delivered en el nodo de Demo)", sh)
	}
	if got := inventoryQty(t, ctx, pool, demoWH, ironOreID) - demoIron0; got != logBuyQty {
		t.Fatalf("stock físico entregado en el almacén de Demo: %d, esperado %d", got, logBuyQty)
	}
	// El vehículo llegó idle al nodo de Demo.
	veh = getVehicleJSON(t, srv, norteToken, vehID)
	if veh["status"] != "idle" {
		t.Fatalf("estado del vehículo tras llegar: %v, esperado idle", veh["status"])
	}
	if pos := asMap(t, veh["position"], "position"); pos["at_node_id"] != demoNode.String() {
		t.Fatalf("el vehículo no llegó al nodo de Demo: %v", pos)
	}

	// ── (9) delivery_confirmer: entrega on-time y LIQUIDACIÓN ────────────────
	drainConsumer(t, ctx, pool, dcConsumer, deliveryConfirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")

	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("contrato liquidado: status %d (cuerpo: %s)", r.status, r.raw)
	}
	c = asMap(t, r.body["data"], "data")
	if c["status"] != "settled" || c["quantity_delivered"] != itoa(logBuyQty) {
		t.Fatalf("contrato no liquidado como se esperaba: %v", c)
	}
	if fill, _ := c["fill_bp"].(float64); fill != 10000 {
		t.Fatalf("fill_bp: %v, esperado 10000 (100%%)", c["fill_bp"])
	}
	// La entrega quedó registrada a tiempo.
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID+"/deliveries", demoToken, nil)
	deliveries, ok := r.body["data"].([]any)
	if r.status != http.StatusOK || !ok || len(deliveries) != 1 {
		t.Fatalf("entregas: esperada 1, cuerpo: %s", r.raw)
	}
	if d := asMap(t, deliveries[0], "delivery[0]"); d["quantity"] != itoa(logBuyQty) || d["on_time"] != true {
		t.Fatalf("entrega inesperada: %v", d)
	}

	// ── (10) Saldos finales: Demo recibe el iron_ore, Norte cobra ────────────
	// Demo (comprador): pagó el valor (escrow → Norte) y recibió 1000 de iron_ore
	// EN SU almacén (físico + contable).
	if got := cashOf(t, ctx, pool, demoID); got != demoCash0-logValue {
		t.Fatalf("caja de Demo final: %d, esperado %d", got, demoCash0-logValue)
	}
	if got := stockFreeOf(t, ctx, pool, demoID, ironOreID, demoWH); got != logInitialIron+logBuyQty {
		t.Fatalf("stock_free de iron_ore de Demo en su almacén: %d, esperado %d", got, logInitialIron+logBuyQty)
	}
	// Norte (vendedor): cobró el valor y recuperó la garantía completa; había
	// gastado el precio del camión al sink.
	if got := cashOf(t, ctx, pool, norteID); got != norteCash0+logValue-logTruckPrice {
		t.Fatalf("caja de Norte final: %d, esperado %d", got, norteCash0+logValue-logTruckPrice)
	}
	guaranteeAcc, _ := c["seller_guarantee_account_id"].(string)
	if guaranteeAcc != "" {
		if got := balanceByAccountID(t, ctx, pool, uuid.MustParse(guaranteeAcc)); got != 0 {
			t.Fatalf("garantía del contrato: %d, esperado 0 (recuperada)", got)
		}
	}

	// ── (11) Coherencia final: ledger a cero + reconciliación física 0 ───────
	assertBalancedLedger(t, ctx, pool)
	assertReconciled(t, ctx, reconWorker, "final (entrega liquidada)")
}

// TestLogisticsExpiryReleaseE2E ejerce el PILAR en el fallo: un contrato cross-node
// vence con su cargamento aún EN TRÁNSITO. El barrido de vencimiento del CCRI
// liquida pro-rata (fill 0 → failed) y emite contract.expired_undelivered; el
// consumidor world shipment_releaser DETIENE el cargamento y libera su stock in
// situ en el almacén de ORIGEN (el mismo donde el ledger liberó el reservado no
// entregado) — nada se teletransporta. La reconciliación física↔contable se
// mantiene a cero.
func TestLogisticsExpiryReleaseE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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
	freezeSim(t, ctx, pool, logSimBase)

	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	truckLargeID := queryUUID(t, ctx, pool, `SELECT id FROM world.vehicle_types WHERE code = $1`, "truck_large")
	demoNode, _ := warehouseNodeOf(t, ctx, pool, demoID)
	norteNode, norteWH := warehouseNodeOf(t, ctx, pool, norteID)

	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool: pool, Logger: logger, Registry: prometheus.NewRegistry(),
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

	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	shipmentCreator := fleet.NewShipmentCreator(logger, prometheus.NewRegistry())
	scConsumer := shipmentCreator.NewConsumer(pool, outbox.WithLogger(logger))
	shipmentReleaser := fleet.NewShipmentReleaser(logger, prometheus.NewRegistry())
	relConsumer := shipmentReleaser.NewConsumer(pool, outbox.WithLogger(logger))
	reconWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}

	demoToken := login(t, srv, demoName, demoSecret)
	norteToken := login(t, srv, traderName, traderSecret)

	// Demo publica buy con PLAZO CORTO; Norte acepta cross-node.
	const shortDeadline int64 = 3_000
	r := call(t, srv, http.MethodPost, "/api/v1/contracts/publications", demoToken, map[string]any{
		"kind": "buy", "product_id": ironOreID.String(), "quantity_total": itoa(logBuyQty),
		"unit_price": itoa(logUnitPrice), "min_lot": "500", "destination_node_id": demoNode.String(),
		"delivery_sim_seconds": shortDeadline,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar buy: status %d (cuerpo: %s)", r.status, r.raw)
	}
	pubID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	r = callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+pubID+"/acceptances",
		norteToken, uuid.NewString(), map[string]any{"quantity": itoa(logBuyQty), "origin_node_id": norteNode.String()})
	if r.status != http.StatusCreated {
		t.Fatalf("aceptar buy: status %d (cuerpo: %s)", r.status, r.raw)
	}
	accID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	contractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, norteToken, accID)

	// Cargamento creado y despachado (queda EN TRÁNSITO).
	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")
	shipmentID := findShipment(t, ctx, srv, norteToken, contractID)
	r = call(t, srv, http.MethodPost, "/api/v1/world/vehicles", norteToken, map[string]any{
		"vehicle_type_id": truckLargeID.String(), "delivery_node_id": norteNode.String(),
	})
	vehID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	r = call(t, srv, http.MethodPost, "/api/v1/logistics/route-plans", norteToken, map[string]any{
		"origin_node_id": norteNode.String(), "destination_node_id": demoNode.String(), "modes": []string{"road"},
	})
	legs, _ := asMap(t, r.body["data"], "data")["legs"].([]any)
	planLinks := make([]any, len(legs))
	for i, l := range legs {
		planLinks[i], _ = asMap(t, l, "leg")["link_id"].(string)
	}
	r = call(t, srv, http.MethodPost, "/api/v1/logistics/routes", norteToken, map[string]any{
		"name": "Norte→Demo", "kind": "on_demand", "legs": planLinks,
	})
	routeID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	r = call(t, srv, http.MethodPost, "/api/v1/world/shipments/"+shipmentID+"/dispatch", norteToken, map[string]any{
		"vehicle_id": vehID, "route_id": routeID,
	})
	if r.status != http.StatusOK || asMap(t, r.body["data"], "data")["status"] != "in_transit" {
		t.Fatalf("despachar: status %d (cuerpo: %s)", r.status, r.raw)
	}
	assertReconciled(t, ctx, reconWorker, "in_transit antes del vencimiento")

	// El reloj rebasa el plazo SIN que el cargamento llegue: el barrido de
	// vencimiento del CCRI liquida (fill 0 → failed) y avisa a world.
	advanceSim(t, ctx, pool, shortDeadline+2_000)
	driveExpiry(t, ctx, ccriWorker, contractID, pool)

	// El shipment_releaser detiene el cargamento y lo libera in situ en ORIGEN.
	drainConsumer(t, ctx, pool, relConsumer, shipmentReleaser.Handle, fleet.ConsumerShipmentReleaser, "contract.expired_undelivered")

	sh := getShipment(t, srv, norteToken, shipmentID)
	if sh["status"] != "released_in_situ" || sh["at_node_id"] != norteNode.String() || sh["vehicle_id"] != nil {
		t.Fatalf("cargamento liberado in situ inesperado: %v", sh)
	}
	// El stock físico volvió al almacén de ORIGEN (Norte), donde el ledger liberó
	// el reservado no entregado: físico y contable casan.
	if got := inventoryQty(t, ctx, pool, norteWH, ironOreID); got != logInitialIron {
		t.Fatalf("inventario físico de iron_ore en Norte tras liberar: %d, esperado %d", got, logInitialIron)
	}
	if got := stockFreeOf(t, ctx, pool, norteID, ironOreID, norteWH); got != logInitialIron {
		t.Fatalf("stock_free de iron_ore de Norte tras liberar: %d, esperado %d", got, logInitialIron)
	}
	if got := stockReservedOf(t, ctx, pool, norteID, ironOreID, norteWH); got != 0 {
		t.Fatalf("stock reservado de Norte tras liberar: %d, esperado 0", got)
	}
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, demoToken, nil)
	if st := asMap(t, r.body["data"], "data")["status"]; st != "failed" {
		t.Fatalf("estado del contrato vencido sin entregar: %v, esperado failed", st)
	}
	assertBalancedLedger(t, ctx, pool)
	assertReconciled(t, ctx, reconWorker, "final (liberación in situ)")
}

// driveExpiry dispara el barrido del CCRI hasta que el contrato queda liquidado
// por vencimiento (status != active) o falla por timeout.
func driveExpiry(t *testing.T, ctx context.Context, w *contracts.Worker, contractID string, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		w.RunOnce(ctx)
		var status string
		if err := pool.QueryRow(ctx, `SELECT status::text FROM ledger.contracts WHERE id = $1`, contractID).Scan(&status); err != nil {
			t.Fatalf("estado del contrato %s: %v", contractID, err)
		}
		if status != "active" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el vencimiento del contrato %s (estado %s)", contractID, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ─── Conducción del motor de tránsito y los consumidores ──────────────────────

// driveTransitUntilDelivered avanza el reloj congelado por encima del tiempo de
// viaje de un segmento y dispara el barrido del motor, repitiéndolo hasta que el
// cargamento queda entregado (o falla por timeout).
func driveTransitUntilDelivered(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w *fleet.TransitWorker, shipmentID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		advanceSim(t, ctx, pool, logTransitStep)
		w.RunOnce(ctx)
		if shipmentStatusOf(t, ctx, pool, shipmentID) == "delivered" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando la entrega del cargamento %s (estado %s)", shipmentID, shipmentStatusOf(t, ctx, pool, shipmentID))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drainConsumer arranca un consumidor del outbox hasta que su cursor alcanza el
// último evento de eventType, y lo detiene limpiamente (mismo patrón que el
// agregador OHLC).
func drainConsumer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, consumer *outbox.Consumer, handle outbox.Handler, name, eventType string) {
	t.Helper()
	target := maxSeqOf(t, ctx, pool, eventType)
	if target == 0 {
		t.Fatalf("no hay eventos %s que consumir", eventType)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, handle) }()

	deadline := time.Now().Add(20 * time.Second)
	for cursorOf(t, ctx, pool, name) < target {
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatalf("timeout esperando al consumidor %s (cursor %d < %d)", name, cursorOf(t, ctx, pool, name), target)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("consumidor %s devolvió error en el apagado: %v", name, err)
	}
}

// ─── Lecturas auxiliares ──────────────────────────────────────────────────────

// warehouseNodeOf devuelve el nodo del grafo y el edificio del almacén de una
// corporación (el warehouse sembrado del Incremento 1).
func warehouseNodeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) (node, building uuid.UUID) {
	t.Helper()
	if err := pool.QueryRow(ctx, `
		SELECT n.id, b.id
		  FROM world.network_nodes n
		  JOIN world.buildings b ON b.id = n.building_id
		 WHERE b.owner_account_id = $1 AND n.kind = 'warehouse'`, owner).Scan(&node, &building); err != nil {
		t.Fatalf("almacén de %s: %v", owner, err)
	}
	return node, building
}

func stockReservedOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind = 'stock_reserved' AND owner_account_id = $1 AND product_id = $2 AND warehouse_building_id = $3`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock_reserved de %s: %v", owner, err)
	}
	return b
}

func shipmentStatusOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, shipmentID uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `SELECT status::text FROM world.shipments WHERE id = $1`, shipmentID).Scan(&s); err != nil {
		t.Fatalf("estado del cargamento %s: %v", shipmentID, err)
	}
	return s
}

// findShipment localiza el cargamento del contrato vía la API (propiedad de Norte).
func findShipment(t *testing.T, ctx context.Context, srv *httptest.Server, token, contractID string) string {
	t.Helper()
	r := call(t, srv, http.MethodGet, "/api/v1/world/shipments?contract_id="+contractID, token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("listar cargamentos: status %d (cuerpo: %s)", r.status, r.raw)
	}
	list, ok := r.body["data"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("cargamentos del contrato %s: esperado 1, cuerpo: %s", contractID, r.raw)
	}
	id, _ := asMap(t, list[0], "shipment")["id"].(string)
	if id == "" {
		t.Fatalf("cargamento sin id: %s", r.raw)
	}
	return id
}

func getShipment(t *testing.T, srv *httptest.Server, token, shipmentID string) map[string]any {
	t.Helper()
	r := call(t, srv, http.MethodGet, "/api/v1/world/shipments/"+shipmentID, token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("cargamento %s: status %d (cuerpo: %s)", shipmentID, r.status, r.raw)
	}
	return asMap(t, r.body["data"], "data")
}

func getVehicleJSON(t *testing.T, srv *httptest.Server, token, vehicleID string) map[string]any {
	t.Helper()
	r := call(t, srv, http.MethodGet, "/api/v1/world/vehicles/"+vehicleID, token, nil)
	if r.status != http.StatusOK {
		t.Fatalf("vehículo %s: status %d (cuerpo: %s)", vehicleID, r.status, r.raw)
	}
	return asMap(t, r.body["data"], "data")
}

// assertReconciled comprueba que la reconciliación física↔contable no arroja
// divergencias en ese punto del proceso.
func assertReconciled(t *testing.T, ctx context.Context, w *production.Worker, where string) {
	t.Helper()
	n, err := w.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconciliación (%s): %v", where, err)
	}
	if n != 0 {
		t.Fatalf("reconciliación física↔contable (%s): %d divergencias, esperado 0", where, n)
	}
}
