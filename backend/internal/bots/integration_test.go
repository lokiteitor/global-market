// Integración del Bot Orchestration Service (ADR-024) contra una BD real y el
// gateway REAL (internal/gateway.BuildHandler, idéntico a cmd/gateway) servido
// con httptest, con los motores del engine disparados en el test: worker CCRI
// (sorteo/TTL/liquidación), motor de producción (construcción diferida +
// lotes), motor de tránsito y los consumidores cross-context del outbox
// (shipment_creator, delivery_confirmer). Ningún mock.
//
// Cubre el mandato del incremento:
//
//  1. PROVISIONING idempotente: la segunda corrida no re-capitaliza ni pisa
//     credenciales/perfiles.
//  2. Un coal_producer completa su SETUP (concesión → mina → operational →
//     receta → lotes) a base de Decide repetidos con el reloj congelado
//     avanzando por SQL, y mantiene su publicación de venta.
//  3. Con una solicitud de compra de coal publicada por OTRO actor (el
//     iron_producer manteniendo su combustible — bots comerciando con bots),
//     el coal_producer la acepta y, al confirmarse el contrato, despacha el
//     cargamento (camión + plan de ruta + ruta + dispatch) y la entrega llega
//     avanzando el reloj: bots moviendo la logística REAL de punta a punta.
//  4. El trader compra una ganga del tablón y la re-lista con margen desde el
//     almacén ajeno donde reposa el stock (regla verificada del arquetipo).
//
// El reloj de simulación se CONGELA y avanza por SQL (patrón tests/e2e); el
// sorteo usa ventanas wall-clock cortas. Se omite si II_TEST_DATABASE_URL no
// está definida (BD efímera propia, migraciones reales).
package bots_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
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
	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
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
	itDemoName    = "Demo"
	itDemoSecret  = "bots-demo-secret"
	itNorteName   = "Norte Trading"
	itNorteSecret = "bots-norte-secret"
	itSecretSeed  = "it-bots-seed"
	itCapital     = 500_000
	itSimBase     = 1_000_000

	coalBotName   = "Bot Carbonera 01"
	ironBotName   = "Bot Minera 01"
	traderBotName = "Bot Mercader 01"

	// batchStep rebasa la duración de un lote (3600 s de sim) por barrido.
	batchStep int64 = 4_000
	// transitStep rebasa el tiempo de viaje de cualquier segmento del test.
	transitStep int64 = 5_000
)

func TestBotsEconomicLoopIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
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

	// ── Gateway real (ventanas de sorteo cortas, rate limits holgados) ───────
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: gateway.Options{
			Auth: auth.Options{
				LoginPerMin: 60,
				APIRPS:      1_000,
				APIBurst:    2_000,
			},
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
	defer srv.Close()
	apiURL := srv.URL + gateway.APIPrefix

	// ── Motores del engine con el MISMO lector congelado ─────────────────────
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
	shipmentCreator := fleet.NewShipmentCreator(logger, prometheus.NewRegistry())
	scConsumer := shipmentCreator.NewConsumer(pool, outbox.WithLogger(logger))
	deliveryConfirmer, err := contracts.NewDeliveryConfirmer(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewDeliveryConfirmer: %v", err)
	}
	dcConsumer := deliveryConfirmer.NewConsumer(pool, outbox.WithLogger(logger))

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'coal'`)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	junctionID := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE kind = 'junction' AND region_id = $1`, regionID)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, itNorteName)
	norteNode := queryUUID(t, ctx, pool, `
		SELECT n.id FROM world.network_nodes n
		  JOIN world.buildings b ON b.id = n.building_id
		 WHERE b.owner_account_id = $1 AND n.kind = 'warehouse'`, norteID)

	botsOpts := bots.Options{
		CoalProducers: 1, IronProducers: 1, Traders: 1,
		SecretSeed: itSecretSeed, Capital: itCapital,
		Tick: time.Second, Addr: ":0", APIURL: apiURL,
	}
	orch, err := bots.NewOrchestrator(pool, botsOpts, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}

	// ── (1) Provisioning idempotente ─────────────────────────────────────────
	emission0 := emissionBalance(t, ctx, pool)
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
	}
	for name, wantArchetype := range map[string]string{
		coalBotName:   "primary_producer",
		ironBotName:   "primary_producer",
		traderBotName: "arbitrageur",
	} {
		b, ok := byName[name]
		if !ok {
			t.Fatalf("falta el bot %q en la población", name)
		}
		var kind, archetype string
		var behavior []byte
		if err := pool.QueryRow(ctx, `
			SELECT a.kind::text, bp.archetype::text, bp.behavior
			  FROM auth.accounts a JOIN auth.bot_profiles bp ON bp.account_id = a.id
			 WHERE a.id = $1`, b.AccountID).Scan(&kind, &archetype, &behavior); err != nil {
			t.Fatalf("cuenta/perfil de %s: %v", name, err)
		}
		if kind != "bot" || archetype != wantArchetype || len(behavior) <= 2 {
			t.Fatalf("perfil de %s inesperado: kind=%s archetype=%s behavior=%s", name, kind, archetype, behavior)
		}
		if got := cashOf(t, ctx, pool, b.AccountID); got != itCapital {
			t.Fatalf("caja de %s tras capitalizar: %d, esperado %d", name, got, itCapital)
		}
	}
	if got := emissionBalance(t, ctx, pool); got != emission0-3*itCapital {
		t.Fatalf("emisión tras capitalizar: %d, esperado %d", got, emission0-3*itCapital)
	}
	coalHash0 := credentialHash(t, ctx, pool, byName[coalBotName].AccountID)

	// Segunda corrida: NO re-capitaliza, NO pisa credenciales ni perfiles.
	if _, err := orch.Provision(ctx); err != nil {
		t.Fatalf("Provision (segunda corrida): %v", err)
	}
	for name, b := range byName {
		if got := cashOf(t, ctx, pool, b.AccountID); got != itCapital {
			t.Fatalf("caja de %s tras re-provisionar: %d (re-capitalizó)", name, got)
		}
	}
	if got := credentialHash(t, ctx, pool, byName[coalBotName].AccountID); got != coalHash0 {
		t.Fatal("la segunda corrida sobrescribió la credencial")
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.transactions WHERE kind = 'bot_capitalization'`); n != 3 {
		t.Fatalf("transacciones bot_capitalization: %d, esperadas 3", n)
	}

	// ── Clientes SDK de los bots (Decide dirigido por el test) ───────────────
	metrics := bots.NewMetrics(prometheus.NewRegistry())
	coalClient := loginBot(t, ctx, apiURL, byName[coalBotName])
	coalState := bots.NewState()
	coalBot := bots.NewCoalProducer(bots.DefaultCoalProducerConfig(), coalBotName, logger, metrics)

	ironClient := loginBot(t, ctx, apiURL, byName[ironBotName])
	ironState := bots.NewState()
	ironBot := bots.NewIronProducer(bots.DefaultIronProducerConfig(), ironBotName, logger, metrics)

	traderClient := loginBot(t, ctx, apiURL, byName[traderBotName])
	traderState := bots.NewState()
	traderBot := bots.NewTrader(bots.DefaultTraderConfig(itCapital), traderBotName, logger, metrics)

	// ── (2) El coal_producer completa su setup y produce ─────────────────────
	coalBotID := byName[coalBotName].AccountID
	deadline := time.Now().Add(90 * time.Second)
	var coalMineID, coalMineNode uuid.UUID
	for {
		if err := coalBot.Decide(ctx, coalClient, coalState); err != nil {
			t.Fatalf("coal Decide: %v", err)
		}
		prodWorker.RunOnce(ctx)
		if coalMineID == uuid.Nil {
			coalMineID = maybeUUID(t, ctx, pool, `
				SELECT b.id FROM world.buildings b
				  JOIN world.building_types bt ON bt.id = b.building_type_id
				 WHERE b.owner_account_id = $1 AND bt.code = 'coal_mine'`, coalBotID)
		}
		if coalMineID != uuid.Nil {
			if free := stockFreeOf(t, ctx, pool, coalBotID, coalID, coalMineID); free >= 200 {
				break
			}
			advanceSim(t, ctx, pool, batchStep)
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout: el coal_producer no completó setup + producción (>= 200 de coal libre)")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Setup completo verificado por el estado del mundo.
	var mineStatus, activeRecipe string
	if err := pool.QueryRow(ctx, `
		SELECT b.status::text, COALESCE(r.code, '')
		  FROM world.buildings b LEFT JOIN world.recipes r ON r.id = b.active_recipe_id
		 WHERE b.id = $1`, coalMineID).Scan(&mineStatus, &activeRecipe); err != nil {
		t.Fatalf("estado de la mina: %v", err)
	}
	if mineStatus != "operational" || activeRecipe != "mine_coal" {
		t.Fatalf("mina del bot: status=%s receta=%s (esperado operational/mine_coal)", mineStatus, activeRecipe)
	}
	coalMineNode = queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, coalMineID)
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell'
		   AND status IN ('draw_window','open','micro_window')`, coalBotID); n != 1 {
		t.Fatalf("ventas activas del coal_producer: %d, esperada exactamente 1", n)
	}

	// ── (3a) El iron_producer completa su setup y publica la compra de coal ──
	ironBotID := byName[ironBotName].AccountID
	deadline = time.Now().Add(60 * time.Second)
	var fuelBuyID uuid.UUID
	for {
		if err := ironBot.Decide(ctx, ironClient, ironState); err != nil {
			t.Fatalf("iron Decide: %v", err)
		}
		prodWorker.RunOnce(ctx)
		fuelBuyID = maybeUUID(t, ctx, pool, `
			SELECT id FROM ledger.publications
			 WHERE publisher_account_id = $1 AND kind = 'buy'
			   AND status IN ('draw_window','open','micro_window')`, ironBotID)
		if fuelBuyID != uuid.Nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout: el iron_producer no publicó su solicitud de compra de coal")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var buyPrice, buyQty int64
	if err := pool.QueryRow(ctx, `
		SELECT unit_price, quantity_total FROM ledger.publications WHERE id = $1`, fuelBuyID).
		Scan(&buyPrice, &buyQty); err != nil {
		t.Fatalf("solicitud de combustible: %v", err)
	}
	if buyPrice != 66 || buyQty != 200 {
		t.Fatalf("solicitud de combustible: precio %d / qty %d, esperado 66 (110%% de 60) / 200", buyPrice, buyQty)
	}
	ironMineID := queryUUID(t, ctx, pool, `
		SELECT b.id FROM world.buildings b
		  JOIN world.building_types bt ON bt.id = b.building_type_id
		 WHERE b.owner_account_id = $1 AND bt.code = 'iron_mine'`, ironBotID)
	ironMineNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, ironMineID)

	// ── (3b) Red vial hasta las minas de los bots (infraestructura del mundo) ─
	linkNodesBothWays(t, ctx, pool, regionID, coalMineNode, junctionID)
	linkNodesBothWays(t, ctx, pool, regionID, ironMineNode, junctionID)

	// ── (3c) El coal_producer acepta la compra y DESPACHA el cargamento ──────
	coalCashBefore := cashOf(t, ctx, pool, coalBotID)
	if err := coalBot.Decide(ctx, coalClient, coalState); err != nil {
		t.Fatalf("coal Decide (aceptación): %v", err)
	}
	accID, ok := coalState.PendingAcceptance(fuelBuyID.String())
	if !ok {
		t.Fatal("el coal_producer no aceptó la solicitud de compra de coal")
	}
	contractID := driveDrawUntilServed(t, ctx, ccriWorker, coalClient, accID)

	// El shipment_creator materializa el cargamento en la mina del carbonero.
	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")

	// Decide repetidos: comprar camión → plan de ruta → crear ruta → dispatch.
	deadline = time.Now().Add(30 * time.Second)
	for {
		if err := coalBot.Decide(ctx, coalClient, coalState); err != nil {
			t.Fatalf("coal Decide (despacho): %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status::text FROM world.shipments WHERE contract_id = $1`, contractID).Scan(&status); err != nil {
			t.Fatalf("estado del cargamento: %v", err)
		}
		if status == "in_transit" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: el coal_producer no despachó el cargamento (estado %s)", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// El camión del bot existe y fue comprado en su nodo.
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, coalBotID); n != 1 {
		t.Fatalf("flota del coal_producer: %d camiones, esperado 1", n)
	}

	// ── (3d) Tránsito real hasta la mina del hierro y liquidación ────────────
	shipmentID := queryUUID(t, ctx, pool, `SELECT id FROM world.shipments WHERE contract_id = $1`, contractID)
	driveTransitUntilDelivered(t, ctx, pool, transitWorker, shipmentID)
	drainConsumer(t, ctx, pool, dcConsumer, deliveryConfirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")

	var cStatus string
	var fillBP int
	if err := pool.QueryRow(ctx, `SELECT status::text, COALESCE(fill_bp, 0) FROM ledger.contracts WHERE id = $1`, contractID).
		Scan(&cStatus, &fillBP); err != nil {
		t.Fatalf("contrato: %v", err)
	}
	if cStatus != "settled" || fillBP != 10_000 {
		t.Fatalf("contrato de combustible: status=%s fill=%d, esperado settled/10000", cStatus, fillBP)
	}
	// El hierro recibió su combustible en SU mina (físico + contable).
	if got := stockFreeOf(t, ctx, pool, ironBotID, coalID, ironMineID); got != 200 {
		t.Fatalf("coal libre del iron_producer en su mina: %d, esperado 200", got)
	}
	if got := inventoryQty(t, ctx, pool, ironMineID, coalID); got != 200 {
		t.Fatalf("coal físico en la mina de hierro: %d, esperado 200", got)
	}
	// El carbonero cobró: valor (13200) + garantía recuperada − camión (40000).
	wantCash := coalCashBefore - buyQty*buyPrice/10 - 40_000 + buyQty*buyPrice + buyQty*buyPrice/10
	if got := cashOf(t, ctx, pool, coalBotID); got != wantCash {
		t.Fatalf("caja del coal_producer tras liquidar: %d, esperado %d", got, wantCash)
	}

	// ── (4) El trader compra una ganga y re-lista con margen desde el almacén
	//        ajeno donde reposa el stock ─────────────────────────────────────
	norteClient := newSDKClient(t, apiURL)
	if _, err := norteClient.Login(ctx, itNorteName, itNorteSecret); err != nil {
		t.Fatalf("login Norte: %v", err)
	}
	minLot, _ := botsdk.QtyFromInt64(50)
	gangaPub, err := norteClient.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationSell,
		ProductID:          coalID.String(),
		QuantityTotal:      "200",
		UnitPrice:          "54", // 90% del base_price 60: bajo el umbral 95% del trader
		MinLot:             minLot,
		OriginNodeID:       norteNode.String(),
		DeliverySimSeconds: 86_400,
	})
	if err != nil {
		t.Fatalf("Norte publicando la ganga: %v", err)
	}
	if err := traderBot.Decide(ctx, traderClient, traderState); err != nil {
		t.Fatalf("trader Decide (compra): %v", err)
	}
	traderAccID, ok := traderState.PendingAcceptance(gangaPub.ID)
	if !ok {
		t.Fatal("el trader no aceptó la ganga (54 <= 95% de 60)")
	}
	_ = driveDrawUntilServed(t, ctx, ccriWorker, traderClient, traderAccID)

	// La venta era in situ: el stock del trader reposa en el almacén de Norte.
	traderID := byName[traderBotName].AccountID
	norteWH := queryUUID(t, ctx, pool, `SELECT building_id FROM world.network_nodes WHERE id = $1`, norteNode)
	if got := stockFreeOf(t, ctx, pool, traderID, coalID, norteWH); got != 200 {
		t.Fatalf("stock del trader en el almacén de Norte: %d, esperado 200", got)
	}

	// Re-listado con margen +15% sobre lo pagado, desde el nodo AJENO.
	if err := traderBot.Decide(ctx, traderClient, traderState); err != nil {
		t.Fatalf("trader Decide (re-listado): %v", err)
	}
	var relistPrice, relistQty int64
	var relistOrigin uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT unit_price, quantity_total, origin_node_id FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'sell'
		   AND status IN ('draw_window','open','micro_window')`, traderID).
		Scan(&relistPrice, &relistQty, &relistOrigin); err != nil {
		t.Fatalf("re-listado del trader: %v", err)
	}
	if relistPrice != 63 || relistQty != 200 || relistOrigin != norteNode {
		t.Fatalf("re-listado: precio=%d qty=%d origen=%s, esperado 63 (54×1,15 techo) / 200 / nodo de Norte %s",
			relistPrice, relistQty, relistOrigin, norteNode)
	}

	// ── Coherencia final: ledger a cero y reconciliación física↔contable 0 ───
	assertBalancedLedger(t, ctx, pool)
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d divergencias (err %v), esperado 0", disc, err)
	}
}

// ─── Conducción de motores ────────────────────────────────────────────────────

// driveDrawUntilServed dispara el barrido del CCRI hasta que la aceptación
// queda servida (vía la API, con el cliente del propio bot) y devuelve el
// contract_id.
func driveDrawUntilServed(t *testing.T, ctx context.Context, w *contracts.Worker, c *botsdk.Client, accID string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		w.RunOnce(ctx)
		acc, err := c.GetAcceptance(ctx, accID)
		if err == nil && acc.Status == botsdk.AcceptanceServed {
			if acc.ContractID == "" {
				t.Fatalf("aceptación servida sin contract_id: %+v", acc)
			}
			return acc.ContractID
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el sorteo de la aceptación %s (estado %v, err %v)", accID, acc.Status, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// driveTransitUntilDelivered avanza el reloj y dispara el motor de tránsito
// hasta que el cargamento queda entregado.
func driveTransitUntilDelivered(t *testing.T, ctx context.Context, pool *pgxpool.Pool, w *fleet.TransitWorker, shipmentID uuid.UUID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		advanceSim(t, ctx, pool, transitStep)
		w.RunOnce(ctx)
		var status string
		if err := pool.QueryRow(ctx, `SELECT status::text FROM world.shipments WHERE id = $1`, shipmentID).Scan(&status); err != nil {
			t.Fatalf("estado del cargamento %s: %v", shipmentID, err)
		}
		if status == "delivered" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando la entrega del cargamento %s (estado %s)", shipmentID, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drainConsumer arranca un consumidor del outbox hasta que su cursor alcanza
// el último evento del tipo dado y lo detiene limpiamente (patrón tests/e2e).
func drainConsumer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, consumer *outbox.Consumer, handle outbox.Handler, name, eventType string) {
	t.Helper()
	var target int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(seq), 0) FROM outbox.events WHERE event_type = $1`, eventType).Scan(&target); err != nil {
		t.Fatalf("max(seq) de %s: %v", eventType, err)
	}
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

// ─── Fixture de red vial ──────────────────────────────────────────────────────

// linkNodesBothWays crea enlaces road dirigidos en ambos sentidos entre dos
// nodos (con su segmento único, congestión fluida), como la red del seed: los
// bots construyen minas pero las carreteras son infraestructura del mundo.
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
	if err := pool.QueryRow(ctx, `SELECT ST_X(location), ST_Y(location) FROM world.network_nodes WHERE id = $1`, nodeID).Scan(&x, &y); err != nil {
		t.Fatalf("ubicación del nodo %s: %v", nodeID, err)
	}
	return x, y
}

// ─── Clientes SDK ─────────────────────────────────────────────────────────────

func newSDKClient(t *testing.T, apiURL string) *botsdk.Client {
	t.Helper()
	c, err := botsdk.New(botsdk.Options{BaseURL: apiURL})
	if err != nil {
		t.Fatalf("botsdk.New: %v", err)
	}
	return c
}

func loginBot(t *testing.T, ctx context.Context, apiURL string, bot bots.ProvisionedBot) *botsdk.Client {
	t.Helper()
	c := newSDKClient(t, apiURL)
	if _, err := c.Login(ctx, bot.Name, bot.Secret); err != nil {
		t.Fatalf("login del bot %s: %v", bot.Name, err)
	}
	return c
}

// ─── Reloj congelado ──────────────────────────────────────────────────────────

func freezeSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, at int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE world.sim_clock SET frozen = true, sim_time_at = $1, wall_anchor = now(), updated_at = now() WHERE id = 1`, at); err != nil {
		t.Fatalf("congelando el reloj: %v", err)
	}
}

func advanceSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, delta int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE world.sim_clock SET sim_time_at = sim_time_at + $1, updated_at = now() WHERE id = 1`, delta); err != nil {
		t.Fatalf("avanzando el reloj: %v", err)
	}
}

// ─── Lecturas auxiliares ──────────────────────────────────────────────────────

func queryUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

// maybeUUID devuelve uuid.Nil si la consulta no encuentra fila.
func maybeUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, sql, args...).Scan(&id)
	if err == pgx.ErrNoRows {
		return uuid.Nil
	}
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func cashOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx,
		`SELECT balance FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1`, owner).Scan(&b); err != nil {
		t.Fatalf("caja de %s: %v", owner, err)
	}
	return b
}

func stockFreeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2 AND warehouse_building_id = $3`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock_free de %s: %v", owner, err)
	}
	return b
}

func inventoryQty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	t.Helper()
	var q int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(quantity, 0) FROM world.building_inventories WHERE building_id = $1 AND product_id = $2`,
		building, product).Scan(&q); err != nil {
		if err == pgx.ErrNoRows {
			return 0
		}
		t.Fatalf("inventario físico (%s, %s): %v", building, product, err)
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

func credentialHash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID) string {
	t.Helper()
	var h string
	if err := pool.QueryRow(ctx, `SELECT secret_hash FROM auth.account_credentials WHERE account_id = $1`, accountID).Scan(&h); err != nil {
		t.Fatalf("credencial de %s: %v", accountID, err)
	}
	return h
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func cursorOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(ctx,
		`SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = $1`, name).Scan(&seq); err != nil {
		return 0 // cursor aún no registrado
	}
	return seq
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

// ─── BD efímera ───────────────────────────────────────────────────────────────

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("botstest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
