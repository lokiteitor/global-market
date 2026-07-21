// Integración del arquetipo industrial_transformer (GDD 13.2/13.3) contra una
// BD real y el gateway REAL (internal/gateway.BuildHandler) servido con
// httptest, con los motores del engine disparados desde el test: motor de
// producción (construcción diferida + lotes), worker CCRI (sorteo/liquidación),
// motor de tránsito y los consumidores cross-context del outbox
// (shipment_creator, delivery_confirmer). Ningún mock.
//
// Cubre el mandato del arquetipo:
//
//  1. SETUP incremental por Decide: concesión sobre suelo libre → blast_furnace
//     → operational → receta smelt_steel → cola.
//  2. ABASTECIMIENTO: publica UNA solicitud de compra por insumo (iron_ore
//     input y coal combustible) con destino su horno, derivando umbral y
//     cantidad de la RECETA y el precio del catálogo.
//  3. ENTREGA REAL: otra corporación (Norte, humana) sirve esas compras y
//     mueve la mercancía con camión y ruta hasta el horno; el stock aparece en
//     la planta y la producción arranca.
//  4. OPTIMIZACIÓN SIMPLE: con el mineral caro (mercado OHLC) el margen
//     esperado es negativo y el bot NO produce ni publica (decisión auditable
//     skip_production/negative_margin); cuando el mercado se corrige vuelve a
//     encolar y publica el acero con margen sobre el coste de insumos.
//
// El reloj de simulación se CONGELA y avanza por SQL (patrón tests/e2e). Se
// omite si II_TEST_DATABASE_URL no está definida.
package bots_test

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
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/world/buildings"
	"github.com/lokiteitor/global-market/backend/internal/world/catalog"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/land"
	"github.com/lokiteitor/global-market/backend/internal/world/production"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

const (
	transformerBotName = "Bot Siderúrgica 01"
	// smeltStep rebasa la duración de un lote de smelt_steel (7200 s de sim).
	smeltStep int64 = 8_000
)

func TestIndustrialTransformerIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	env := newBotsEnv(t, ctx, adminURL, bots.Options{
		Transformers: 1,
		SecretSeed:   itSecretSeed, Capital: itCapital,
		TransformerMarginBP: bots.DefaultTransformerMarginBP,
		FreighterMarginBP:   bots.DefaultFreighterMarginBP,
		Tick:                time.Second, Addr: ":0",
	})
	pool := env.pool

	provisioned, err := env.orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(provisioned) != 1 || provisioned[0].Name != transformerBotName {
		t.Fatalf("población aprovisionada inesperada: %+v", provisioned)
	}
	var archetype string
	if err := pool.QueryRow(ctx, `
		SELECT bp.archetype::text FROM auth.bot_profiles bp WHERE bp.account_id = $1`,
		provisioned[0].AccountID).Scan(&archetype); err != nil {
		t.Fatalf("perfil del transformador: %v", err)
	}
	if archetype != "industrial_transformer" {
		t.Fatalf("arquetipo persistido %q, esperado industrial_transformer", archetype)
	}

	botID := provisioned[0].AccountID
	client := loginBot(t, ctx, env.apiURL, provisioned[0])
	state := bots.NewState()
	metrics := bots.NewMetrics(prometheus.NewRegistry())
	bot := bots.NewIndustrialTransformer(
		bots.DefaultIndustrialTransformerConfig(bots.DefaultTransformerMarginBP),
		transformerBotName, env.logger, metrics)

	ironID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'coal'`)
	steelID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'steel_ingot'`)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, itNorteName)
	norteNode := queryUUID(t, ctx, pool, `
		SELECT n.id FROM world.network_nodes n
		  JOIN world.buildings b ON b.id = n.building_id
		 WHERE b.owner_account_id = $1 AND n.kind = 'warehouse'`, norteID)

	// ── (1) SETUP: concesión → alto horno → operational → receta → cola ──────
	var plantID uuid.UUID
	deadline := time.Now().Add(60 * time.Second)
	for {
		if err := bot.Decide(ctx, client, state); err != nil {
			t.Fatalf("transformer Decide (setup): %v", err)
		}
		env.prodWorker.RunOnce(ctx)
		if plantID == uuid.Nil {
			plantID = maybeUUID(t, ctx, pool, `
				SELECT b.id FROM world.buildings b
				  JOIN world.building_types bt ON bt.id = b.building_type_id
				 WHERE b.owner_account_id = $1 AND bt.code = 'blast_furnace'`, botID)
		}
		if plantID != uuid.Nil && countRows(t, ctx, pool,
			`SELECT count(*) FROM world.production_batches WHERE building_id = $1`, plantID) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout: el transformador no completó su setup (concesión → horno → receta → cola)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var plantStatus, activeRecipe string
	if err := pool.QueryRow(ctx, `
		SELECT b.status::text, COALESCE(r.code, '')
		  FROM world.buildings b LEFT JOIN world.recipes r ON r.id = b.active_recipe_id
		 WHERE b.id = $1`, plantID).Scan(&plantStatus, &activeRecipe); err != nil {
		t.Fatalf("estado del horno: %v", err)
	}
	if plantStatus != "operational" || activeRecipe != "smelt_steel" {
		t.Fatalf("horno del bot: status=%s receta=%s (esperado operational/smelt_steel)", plantStatus, activeRecipe)
	}
	plantNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, plantID)

	// ── (2) ABASTECIMIENTO: una compra por insumo, derivada de la receta ─────
	// smelt_steel consume 20 de iron_ore + 10 de coal por lote:
	//   cantidad = consumo × InputBuyBatches(10)  ⇒ 200 y 100
	//   precio   = base_price × InputBuyPriceBP(110%) ⇒ 110 y 66
	ironBuy := buyPublicationOf(t, ctx, pool, botID, ironID)
	coalBuy := buyPublicationOf(t, ctx, pool, botID, coalID)
	if ironBuy.qty != 200 || ironBuy.price != 110 {
		t.Fatalf("solicitud de iron_ore: qty=%d precio=%d, esperado 200/110", ironBuy.qty, ironBuy.price)
	}
	if coalBuy.qty != 100 || coalBuy.price != 66 {
		t.Fatalf("solicitud de coal: qty=%d precio=%d, esperado 100/66", coalBuy.qty, coalBuy.price)
	}
	if ironBuy.destination != plantNode || coalBuy.destination != plantNode {
		t.Fatalf("las compras deben tener destino el horno %s (iron %s, coal %s)",
			plantNode, ironBuy.destination, coalBuy.destination)
	}

	// ── (3) ENTREGA REAL: Norte sirve las dos compras con camión y ruta ──────
	requireRoadSpur(t, ctx, pool, plantNode)
	norteClient := newSDKClient(t, env.apiURL)
	if _, err := norteClient.Login(ctx, itNorteName, itNorteSecret); err != nil {
		t.Fatalf("login Norte: %v", err)
	}
	env.serveBuy(t, ctx, norteClient, ironBuy.id, ironBuy.qty, norteNode, plantNode)
	env.serveBuy(t, ctx, norteClient, coalBuy.id, coalBuy.qty, norteNode, plantNode)

	if got := stockFreeOf(t, ctx, pool, botID, ironID, plantID); got != 200 {
		t.Fatalf("iron_ore libre en el horno: %d, esperado 200", got)
	}
	if got := inventoryQty(t, ctx, pool, plantID, coalID); got != 100 {
		t.Fatalf("coal físico en el horno: %d, esperado 100", got)
	}

	// ── (4) PRODUCCIÓN: los 3 lotes encolados funden 24 lingotes ─────────────
	deadline = time.Now().Add(60 * time.Second)
	for stockFreeOf(t, ctx, pool, botID, steelID, plantID) < 24 {
		advanceSim(t, ctx, pool, smeltStep)
		env.prodWorker.RunOnce(ctx)
		if time.Now().After(deadline) {
			t.Fatalf("timeout: el horno no fundió 24 lingotes (stock %d)",
				stockFreeOf(t, ctx, pool, botID, steelID, plantID))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.production_batches
		 WHERE building_id = $1 AND status NOT IN ('completed','cancelled')`, plantID); pending != 0 {
		t.Fatalf("órdenes vivas tras fundir los 3 lotes: %d, esperado 0", pending)
	}

	// ── (5) MARGEN NEGATIVO: mineral carísimo ⇒ ni funde ni publica ──────────
	// Con iron_ore a 200 el coste unitario del lingote es
	// ceil((20×200 + 10×60)/8) = 583 > 400 (referencia del acero): el bot para
	// la cola y no vende, aunque tenga 24 lingotes libres y la cola vacía.
	simNow := currentSim(t, ctx, pool)
	insertOhlcCandle(t, ctx, pool, ironID, regionID, simNow-3_600, 200)
	if err := bot.Decide(ctx, client, state); err != nil {
		t.Fatalf("transformer Decide (margen negativo): %v", err)
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(transformerBotName, "skip_production")); got != 1 {
		t.Fatalf("decisiones skip_production: %v, esperada 1 (margen negativo auditado)", got)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.production_batches WHERE building_id = $1`, plantID); n != 1 {
		t.Fatalf("órdenes de producción con margen negativo: %d, esperada 1 (ninguna nueva)", n)
	}
	if n := activeSellCount(t, ctx, pool, botID, steelID); n != 0 {
		t.Fatalf("ventas de acero con margen negativo: %d, esperada 0", n)
	}

	// ── (6) MERCADO CORREGIDO: vuelve a encolar y publica con margen ─────────
	// La vela más reciente manda: iron_ore a 110 ⇒ coste = ceil((20×110 +
	// 10×60)/8) = 350 y precio de venta = ceil(350 × 1,25) = 438.
	insertOhlcCandle(t, ctx, pool, ironID, regionID, simNow, 110)
	if err := bot.Decide(ctx, client, state); err != nil {
		t.Fatalf("transformer Decide (margen positivo): %v", err)
	}
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM world.production_batches
		 WHERE building_id = $1 AND status NOT IN ('completed','cancelled')`, plantID); n != 1 {
		t.Fatalf("órdenes vivas tras recuperar el margen: %d, esperada 1", n)
	}
	var sellPrice, sellQty int64
	var sellOrigin uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT unit_price, quantity_total, origin_node_id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, botID, steelID).
		Scan(&sellPrice, &sellQty, &sellOrigin); err != nil {
		t.Fatalf("venta de acero del transformador: %v", err)
	}
	if sellPrice != 438 || sellQty != 24 || sellOrigin != plantNode {
		t.Fatalf("venta de acero: precio=%d qty=%d origen=%s, esperado 438 (coste 350 × 1,25) / 24 / %s",
			sellPrice, sellQty, sellOrigin, plantNode)
	}

	// ── Coherencia final: ledger a cero y reconciliación física↔contable 0 ───
	assertBalancedLedger(t, ctx, pool)
	if disc, err := env.prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d divergencias (err %v), esperado 0", disc, err)
	}
}

// ─── Entorno compartido de los tests de arquetipos ───────────────────────────

// botsEnv es el mundo de un test de arquetipo: BD efímera sembrada con el reloj
// congelado, gateway real servido con httptest y los motores del engine listos
// para dispararse a mano.
type botsEnv struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	apiURL string
	orch   *bots.Orchestrator

	prodWorker    *production.Worker
	ccriWorker    *contracts.Worker
	transitWorker *fleet.TransitWorker

	shipmentCreator *fleet.ShipmentCreator
	scConsumer      *outbox.Consumer
	freightCreator  *fleet.FreightShipmentCreator
	fcConsumer      *outbox.Consumer
	confirmer       *contracts.DeliveryConfirmer
	dcConsumer      *outbox.Consumer
	settler         *contracts.FreightSettler
	fsConsumer      *outbox.Consumer
}

// newBotsEnv levanta el mundo del test: migraciones, seed, reloj congelado,
// gateway real y motores. opts son las opciones del orquestador (la población
// la fija cada test).
func newBotsEnv(t *testing.T, ctx context.Context, adminURL string, opts bots.Options) *botsEnv {
	t.Helper()
	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: itDemoName, DemoSecret: itDemoSecret,
		TraderName: itNorteName, TraderSecret: itNorteSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, itSimBase)

	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	contractsOpts.CancelCooldownSeconds = 0 // sin cooldown: el test retira publicaciones al instante
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: gateway.Options{
			Auth:        auth.Options{LoginPerMin: 60, APIRPS: 1_000, APIBurst: 2_000},
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
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	apiURL := srv.URL + gateway.APIPrefix

	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	prodWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	transitOpts := fleet.DefaultWorkerOptions()
	transitOpts.Roll = func() float64 { return 1.0 } // sin averías: ruta determinista
	transitWorker, err := fleet.NewTransitWorker(pool, reader, transitOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("fleet.NewTransitWorker: %v", err)
	}
	confirmer, err := contracts.NewDeliveryConfirmer(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewDeliveryConfirmer: %v", err)
	}
	settler, err := contracts.NewFreightSettler(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewFreightSettler: %v", err)
	}
	shipmentCreator := fleet.NewShipmentCreator(logger, prometheus.NewRegistry())
	freightCreator := fleet.NewFreightShipmentCreator(logger, prometheus.NewRegistry())

	opts.APIURL = apiURL
	orch, err := bots.NewOrchestrator(pool, opts, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	return &botsEnv{
		pool: pool, logger: logger, apiURL: apiURL, orch: orch,
		prodWorker: prodWorker, ccriWorker: ccriWorker, transitWorker: transitWorker,
		shipmentCreator: shipmentCreator, scConsumer: shipmentCreator.NewConsumer(pool, outbox.WithLogger(logger)),
		freightCreator: freightCreator, fcConsumer: freightCreator.NewConsumer(pool, outbox.WithLogger(logger)),
		confirmer: confirmer, dcConsumer: confirmer.NewConsumer(pool, outbox.WithLogger(logger)),
		settler: settler, fsConsumer: settler.NewConsumer(pool, outbox.WithLogger(logger)),
	}
}

// serveBuy atiende una solicitud de compra como VENDEDOR humano: acepta con
// origen su almacén, deja que el sorteo confirme el contrato, materializa el
// cargamento, compra camión si hace falta, planifica y crea la ruta, despacha y
// conduce el tránsito hasta la entrega confirmada.
func (e *botsEnv) serveBuy(t *testing.T, ctx context.Context, seller *botsdk.Client, publicationID string, qty int64, originNode, destNode uuid.UUID) {
	t.Helper()
	qtyStr, err := botsdk.QtyFromInt64(qty)
	if err != nil {
		t.Fatalf("cantidad inválida: %v", err)
	}
	acc, err := seller.Accept(ctx, publicationID, qtyStr, originNode.String())
	if err != nil {
		t.Fatalf("aceptando la compra %s: %v", publicationID, err)
	}
	contractID := driveDrawUntilServed(t, ctx, e.ccriWorker, seller, acc.ID)
	drainConsumer(t, ctx, e.pool, e.scConsumer, e.shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")

	shipmentID := queryUUID(t, ctx, e.pool, `SELECT id FROM world.shipments WHERE contract_id = $1`, contractID)
	vehicleID := e.ensureIdleVehicle(t, ctx, seller, originNode)
	routeID := e.ensureRoute(t, ctx, seller, originNode, destNode)
	if _, err := seller.Dispatch(ctx, shipmentID.String(), vehicleID, routeID); err != nil {
		t.Fatalf("despachando el cargamento %s: %v", shipmentID, err)
	}
	driveTransitUntilDelivered(t, ctx, e.pool, e.transitWorker, shipmentID)
	drainConsumer(t, ctx, e.pool, e.dcConsumer, e.confirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")
}

// ensureIdleVehicle devuelve un camión idle del cliente en el nodo dado,
// comprándolo si no lo tiene.
func (e *botsEnv) ensureIdleVehicle(t *testing.T, ctx context.Context, c *botsdk.Client, nodeID uuid.UUID) string {
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
	v, err := c.PurchaseVehicle(ctx, botsdk.VehiclePurchase{VehicleTypeID: truckID, DeliveryNodeID: nodeID.String()})
	if err != nil {
		t.Fatalf("comprando el camión en %s: %v", nodeID, err)
	}
	return v.ID
}

// ensureRoute planifica y crea (o reutiliza) una ruta por carretera entre dos
// nodos para el cliente dado.
func (e *botsEnv) ensureRoute(t *testing.T, ctx context.Context, c *botsdk.Client, origin, dest uuid.UUID) string {
	t.Helper()
	name := "test " + origin.String() + "→" + dest.String()
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

// ─── Lecturas auxiliares de los tests de arquetipos ──────────────────────────

// buyPub es la vista mínima de una solicitud de compra publicada.
type buyPub struct {
	id          string
	qty         int64
	price       int64
	destination uuid.UUID
}

// buyPublicationOf devuelve la solicitud de compra activa de un producto.
func buyPublicationOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, publisher, productID uuid.UUID) buyPub {
	t.Helper()
	var out buyPub
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

// activeSellCount cuenta las ventas visibles de un producto publicadas por una
// corporación.
func activeSellCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, publisher, productID uuid.UUID) int {
	t.Helper()
	return countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, publisher, productID)
}

// currentSim lee el sim-time congelado del reloj.
func currentSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var at int64
	if err := pool.QueryRow(ctx, `SELECT sim_time_at FROM world.sim_clock WHERE id = 1`).Scan(&at); err != nil {
		t.Fatalf("leyendo el reloj: %v", err)
	}
	return at
}

// insertOhlcCandle siembra una vela OHLC del mercado (analytics): el precio de
// referencia que leen los bots por GET /market/ohlc. Es estado del mundo, no un
// mock: el agregador escribe exactamente esta tabla desde contract.settled.
func insertOhlcCandle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, productID, regionID uuid.UUID, bucketStartSim, price int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO analytics.market_ohlc
		       (product_id, region_id, bucket_start_sim, bucket_sim_secs,
		        open_price, high_price, low_price, close_price, volume, contract_count)
		VALUES ($1, $2, $3, 3600, $4, $4, $4, $4, 100, 1)
		ON CONFLICT (product_id, region_id, bucket_start_sim) DO UPDATE
		    SET close_price = EXCLUDED.close_price, high_price = EXCLUDED.high_price,
		        low_price = EXCLUDED.low_price, open_price = EXCLUDED.open_price`,
		productID, regionID, bucketStartSim, price); err != nil {
		t.Fatalf("sembrando la vela OHLC de %s: %v", productID, err)
	}
}
