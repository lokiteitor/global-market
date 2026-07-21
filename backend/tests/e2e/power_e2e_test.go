package e2e

// E2E de la red eléctrica regional (GDD 5.8 Fase 3, ADR-025) — el ciclo
// completo del Definition of Done del incremento:
//
//   construir una térmica → abastecerla de carbón POR LOGÍSTICA (CCRI buy +
//   camión) → conectar consumidores con una línea de transmisión → el tick del
//   spot despacha por orden de mérito y cobra el precio de cierre uniforme →
//   producción eléctrica real (lote completado con suministro) → déficit y
//   RECORTE ROTATORIO pausando producción (paused_no_power) → insolvencia sin
//   deuda → mantenimiento de línea impagado → abandoned (deja de conducir) →
//   el ledger cuadra por activo y ningún saldo queda negativo.
//
// Reparto: NORTE construye y oferta la central (y la línea); DEMO electrifica
// dos altos hornos con la receta smelt_steel_electric y puja por el suministro.
// Así los pagos del spot son transferencias reales entre corporaciones.

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
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/enforcement"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

const (
	// pwSimBase es el ancla del reloj congelado (múltiplo exacto del intervalo
	// del spot: los buckets caen en fronteras limpias).
	pwSimBase int64 = 3_600_000

	pwCoalQty       int64 = 200 // carbón entregado por logística a la central
	pwCoalPrice     int64 = 60
	pwOfferPrice    int64 = 80  // oferta de la térmica por unidad de energía
	pwBidPrice      int64 = 150 // puja de los hornos (iguales → rotación)
	pwEnergyPerTick int64 = 10  // power_per_hour del horno × 1 hora-sim por tick
	pwSpotInterval  int64 = 3_600
)

func TestPowerGridSpotMarketE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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
	freezeSim(t, ctx, pool, pwSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "coal")
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	steelID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "steel_ingot")
	coalPlantTypeID := queryUUID(t, ctx, pool, `SELECT id FROM world.building_types WHERE code = $1`, "coal_power_plant")
	hydroTypeID := queryUUID(t, ctx, pool, `SELECT id FROM world.building_types WHERE code = $1`, "hydro_power_plant")
	furnaceTypeID := queryUUID(t, ctx, pool, `SELECT id FROM world.building_types WHERE code = $1`, "blast_furnace")
	electricRecipeID := queryUUID(t, ctx, pool, `SELECT id FROM world.recipes WHERE code = $1`, "smelt_steel_electric")
	demoNode, _ := warehouseNodeOf(t, ctx, pool, demoID)

	// ── Gateway real (ventanas de sorteo cortas; reloj sin caché) ────────────
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	contractsOpts.PublicationTTLSimSeconds = 100_000_000
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

	// ── Motores reales sobre el mismo reloj congelado ────────────────────────
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
	transitOpts.Roll = func() float64 { return 1.0 } // sin averías: tránsito determinista
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
	prodWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0, ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}
	enfWorker, err := enforcement.NewWorker(pool, reader, enforcement.DefaultWorkerOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("enforcement.NewWorker: %v", err)
	}
	powerWorker, err := balancer.NewPowerWorker(pool, reader, balancer.DefaultOptions(), balancer.NewMetrics(prometheus.NewRegistry()), logger)
	if err != nil {
		t.Fatalf("balancer.NewPowerWorker: %v", err)
	}

	demoToken := login(t, srv, demoName, demoSecret)
	norteToken := login(t, srv, traderName, traderSecret)

	// ── (1) NORTE: concesión + central térmica; DEMO: concesión + 2 hornos ───
	r := call(t, srv, http.MethodPost, "/api/v1/world/concessions", norteToken, map[string]any{
		"region_id": regionID.String(), "parcel": geoRect(9_000, 4_000, 11_500, 6_500),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("concesión de Norte: status %d (cuerpo: %s)", r.status, r.raw)
	}
	norteConcession, _ := asMap(t, r.body["data"], "data")["id"].(string)

	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings", norteToken, map[string]any{
		"building_type_id": coalPlantTypeID.String(),
		"concession_id":    norteConcession,
		"footprint":        geoRect(10_000, 5_000, 10_400, 5_400),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("construir central: status %d (cuerpo: %s)", r.status, r.raw)
	}
	plantID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	plantUUID := uuid.MustParse(plantID)
	drivePlacement(t, ctx, srv, prodWorker, norteToken, plantID, "operational")
	plantNode := queryUUID(t, ctx, pool, `SELECT id FROM world.network_nodes WHERE building_id = $1`, plantUUID)

	// Emplazamiento de la hidro: Askadia es plains y el tipo exige bioma coast
	// ("ríos/agua" del GDD 5.8 materializado como requires_biome, ADR-025 §5).
	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings", norteToken, map[string]any{
		"building_type_id": hydroTypeID.String(),
		"concession_id":    norteConcession,
		"footprint":        geoRect(9_200, 4_200, 9_600, 4_600),
	})
	if r.status != http.StatusUnprocessableEntity || errCode(t, r) != "PLACEMENT_INVALID" {
		t.Fatalf("hidro en plains: status %d / %s, esperado 422 PLACEMENT_INVALID (cuerpo: %s)", r.status, errCode(t, r), r.raw)
	}

	r = call(t, srv, http.MethodPost, "/api/v1/world/concessions", demoToken, map[string]any{
		"region_id": regionID.String(), "parcel": geoRect(4_500, 4_500, 7_500, 7_500),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("concesión de Demo: status %d (cuerpo: %s)", r.status, r.raw)
	}
	demoConcession, _ := asMap(t, r.body["data"], "data")["id"].(string)

	buildFurnace := func(minX, minY int64) (string, uuid.UUID) {
		r := call(t, srv, http.MethodPost, "/api/v1/world/buildings", demoToken, map[string]any{
			"building_type_id": furnaceTypeID.String(),
			"concession_id":    demoConcession,
			"footprint":        geoRect(minX, minY, minX+400, minY+400),
		})
		if r.status != http.StatusCreated {
			t.Fatalf("construir horno: status %d (cuerpo: %s)", r.status, r.raw)
		}
		id, _ := asMap(t, r.body["data"], "data")["id"].(string)
		drivePlacement(t, ctx, srv, prodWorker, demoToken, id, "operational")
		r = call(t, srv, http.MethodPatch, "/api/v1/world/buildings/"+id, demoToken, map[string]any{
			"active_recipe_id": electricRecipeID.String(),
		})
		if r.status != http.StatusOK {
			t.Fatalf("configurar receta eléctrica: status %d (cuerpo: %s)", r.status, r.raw)
		}
		return id, uuid.MustParse(id)
	}
	furnace1ID, furnace1 := buildFurnace(5_000, 5_000)
	furnace2ID, furnace2 := buildFurnace(6_500, 6_500)

	// Insumos de los hornos (físico + contable a la vez, como loadFuel).
	loadFuel(t, ctx, pool, demoID, ironOreID, furnace1, 100, pwSimBase)
	loadFuel(t, ctx, pool, demoID, ironOreID, furnace2, 100, pwSimBase)

	// ── (2) NORTE tiende la línea de transmisión (pool regional) ─────────────
	norteCashBeforeLine := cashOf(t, ctx, pool, norteID)
	r = call(t, srv, http.MethodPost, "/api/v1/world/power-lines", norteToken, map[string]any{
		"path": geoLine([][2]int64{{5_200, 5_200}, {10_200, 5_200}}),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("construir línea: status %d (cuerpo: %s)", r.status, r.raw)
	}
	line := asMap(t, r.body["data"], "data")
	lineID, _ := line["id"].(string)
	if line["status"] != "operational" || int64Num(t, line["length_m"], "length_m") != 5_000 {
		t.Fatalf("línea inesperada: %v", line)
	}
	// Coste por longitud al sink: 5 km × 5000/km.
	if got := cashOf(t, ctx, pool, norteID); got != norteCashBeforeLine-25_000 {
		t.Fatalf("caja de Norte tras la línea: %d, esperado %d (coste 25000 por 5 km)", got, norteCashBeforeLine-25_000)
	}
	// Un trazado que sale de la región (las interconexiones interregionales son
	// expansión futura) se rechaza server-side.
	r = call(t, srv, http.MethodPost, "/api/v1/world/power-lines", norteToken, map[string]any{
		"path": geoLine([][2]int64{{5_000, 5_000}, {60_000, 5_000}}),
	})
	if r.status != http.StatusUnprocessableEntity || errCode(t, r) != "PLACEMENT_INVALID" {
		t.Fatalf("línea inter-región: status %d / %s, esperado 422 PLACEMENT_INVALID", r.status, errCode(t, r))
	}

	// ── (3) Oferta del generador y pujas de los consumidores ─────────────────
	r = call(t, srv, http.MethodPut, "/api/v1/world/power-plants/"+plantID+"/offer", norteToken,
		map[string]any{"unit_price": itoa(pwOfferPrice)})
	if r.status != http.StatusOK {
		t.Fatalf("oferta de la central: status %d (cuerpo: %s)", r.status, r.raw)
	}
	// Un horno no es una central: no puede ofertar.
	r = call(t, srv, http.MethodPut, "/api/v1/world/power-plants/"+furnace1ID+"/offer", demoToken,
		map[string]any{"unit_price": itoa(pwOfferPrice)})
	if r.status != http.StatusUnprocessableEntity {
		t.Fatalf("oferta sobre un horno: status %d, esperado 422", r.status)
	}
	// La puja es del dueño: Norte no puede pujar por un horno de Demo.
	r = call(t, srv, http.MethodPut, "/api/v1/world/buildings/"+furnace1ID+"/power-bid", norteToken,
		map[string]any{"unit_price": itoa(pwBidPrice)})
	if r.status != http.StatusForbidden {
		t.Fatalf("puja ajena: status %d, esperado 403", r.status)
	}
	for _, id := range []string{furnace1ID, furnace2ID} {
		r = call(t, srv, http.MethodPut, "/api/v1/world/buildings/"+id+"/power-bid", demoToken,
			map[string]any{"unit_price": itoa(pwBidPrice)})
		if r.status != http.StatusOK {
			t.Fatalf("puja del horno: status %d (cuerpo: %s)", r.status, r.raw)
		}
	}

	// ── (4) Abastecimiento POR LOGÍSTICA: Norte compra carbón con destino la
	//        central; Demo (vendedor) lo acepta y lo transporta en camión ─────
	r = call(t, srv, http.MethodPost, "/api/v1/contracts/publications", norteToken, map[string]any{
		"kind":                 "buy",
		"product_id":           coalID.String(),
		"quantity_total":       itoa(pwCoalQty),
		"unit_price":           itoa(pwCoalPrice),
		"destination_node_id":  plantNode.String(),
		"delivery_sim_seconds": int64(500_000),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar buy de carbón: status %d (cuerpo: %s)", r.status, r.raw)
	}
	coalPubID, _ := asMap(t, r.body["data"], "data")["id"].(string)

	r = callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+coalPubID+"/acceptances",
		demoToken, uuid.NewString(), map[string]any{"quantity": itoa(pwCoalQty), "origin_node_id": demoNode.String()})
	if r.status != http.StatusCreated {
		t.Fatalf("aceptar buy de carbón: status %d (cuerpo: %s)", r.status, r.raw)
	}
	coalAccID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	coalContractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, demoToken, coalAccID)

	drainConsumer(t, ctx, pool, scConsumer, shipmentCreator.Handle, fleet.ConsumerShipmentCreator, "contract.confirmed")
	coalShipmentID := findShipment(t, ctx, srv, demoToken, coalContractID)

	truckLargeID := queryUUID(t, ctx, pool, `SELECT id FROM world.vehicle_types WHERE code = $1`, "truck_large")
	r = call(t, srv, http.MethodPost, "/api/v1/world/vehicles", demoToken, map[string]any{
		"vehicle_type_id": truckLargeID.String(), "delivery_node_id": demoNode.String(),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("comprar camión: status %d (cuerpo: %s)", r.status, r.raw)
	}
	vehID, _ := asMap(t, r.body["data"], "data")["id"].(string)

	r = call(t, srv, http.MethodPost, "/api/v1/logistics/route-plans", demoToken, map[string]any{
		"origin_node_id": demoNode.String(), "destination_node_id": plantNode.String(), "modes": []string{"road"},
	})
	if r.status != http.StatusOK {
		t.Fatalf("route-plan almacén→central: status %d (cuerpo: %s)", r.status, r.raw)
	}
	legs, ok := asMap(t, r.body["data"], "data")["legs"].([]any)
	if !ok || len(legs) == 0 {
		t.Fatalf("route-plan sin legs (¿central desconectada de la red vial?): %s", r.raw)
	}
	planLinks := make([]any, len(legs))
	for i, l := range legs {
		planLinks[i], _ = asMap(t, l, "leg")["link_id"].(string)
	}
	r = call(t, srv, http.MethodPost, "/api/v1/logistics/routes", demoToken, map[string]any{
		"name": "almacén→central", "kind": "on_demand", "legs": planLinks,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("crear ruta: status %d (cuerpo: %s)", r.status, r.raw)
	}
	routeID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	r = call(t, srv, http.MethodPost, "/api/v1/world/shipments/"+coalShipmentID+"/dispatch", demoToken, map[string]any{
		"vehicle_id": vehID, "route_id": routeID,
	})
	if r.status != http.StatusOK {
		t.Fatalf("despachar carbón: status %d (cuerpo: %s)", r.status, r.raw)
	}
	driveTransitUntilDelivered(t, ctx, pool, transitWorker, uuid.MustParse(coalShipmentID))
	drainConsumer(t, ctx, pool, dcConsumer, deliveryConfirmer.Handle, contracts.ConsumerDeliveryConfirmer, "shipment.arrived")

	// El carbón llegó a la central por logística: físico + contable del COMPRADOR.
	if got := stockFreeOf(t, ctx, pool, norteID, coalID, plantUUID); got != pwCoalQty {
		t.Fatalf("stock_free de carbón de Norte en la central: %d, esperado %d", got, pwCoalQty)
	}
	assertBalancedLedger(t, ctx, pool)

	// ── (5) FASE A — sin déficit: solo el horno 1 demanda; el tick despacha y
	//        cobra el precio de cierre; con suministro sostenido el lote
	//        eléctrico COMPLETA ─────────────────────────────────────────────────
	alignSimToSpotBoundary(t, ctx, pool)
	b1 := querySimNow(t, ctx, pool)

	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings/"+furnace1ID+"/production-batches", demoToken, map[string]any{
		"recipe_id": electricRecipeID.String(), "batches_queued": 2,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("encolar lotes eléctricos: status %d (cuerpo: %s)", r.status, r.raw)
	}

	demoCashT0 := cashOf(t, ctx, pool, demoID)
	norteCashT0 := cashOf(t, ctx, pool, norteID)
	powerWorker.RunOnce(ctx)

	tick1 := spotTickRow(t, ctx, pool, regionID, b1)
	if tick1.closing != pwOfferPrice || tick1.demand != pwEnergyPerTick || tick1.supplied != pwEnergyPerTick || tick1.curtailed != 0 {
		t.Fatalf("tick 1 inesperado: %+v (esperado cierre %d, demanda/suministro %d)", tick1, pwOfferPrice, pwEnergyPerTick)
	}
	pay := pwEnergyPerTick * pwOfferPrice // 10 × 80
	if got := cashOf(t, ctx, pool, demoID); got != demoCashT0-pay {
		t.Fatalf("caja de Demo tras el tick 1: %d, esperado %d (pagó %d al cierre)", got, demoCashT0-pay, pay)
	}
	if got := cashOf(t, ctx, pool, norteID); got != norteCashT0+pay {
		t.Fatalf("caja de Norte tras el tick 1: %d, esperado %d (cobró %d al cierre)", got, norteCashT0+pay, pay)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.transactions WHERE kind = 'power_spot'`); n != 1 {
		t.Fatalf("asientos power_spot: %d, esperado 1", n)
	}
	// La térmica quemó combustible físico por el despacho (ADR-022): 10 × 1.
	if got := stockFreeOf(t, ctx, pool, norteID, coalID, plantUUID); got != pwCoalQty-pwEnergyPerTick {
		t.Fatalf("carbón contable de la central tras el tick 1: %d, esperado %d", got, pwCoalQty-pwEnergyPerTick)
	}
	if got := inventoryQty(t, ctx, pool, plantUUID, coalID); got != pwCoalQty-pwEnergyPerTick {
		t.Fatalf("carbón físico de la central tras el tick 1: %d, esperado %d", got, pwCoalQty-pwEnergyPerTick)
	}

	// Tick 2 (bucket siguiente): sigue sirviendo al horno 1.
	advanceSim(t, ctx, pool, pwSpotInterval)
	powerWorker.RunOnce(ctx)
	// Tercer bucket: el lote (7200 s desde b1) vence justo en la frontera; el
	// suministro del tick 2 lo cubre (gracia de medio intervalo) y COMPLETA.
	advanceSim(t, ctx, pool, pwSpotInterval)
	prodWorker.RunOnce(ctx)
	if got := stockFreeOf(t, ctx, pool, demoID, steelID, furnace1); got != 8 {
		t.Fatalf("acero producido con electricidad: %d, esperado 8 (el lote eléctrico debe completar servido)", got)
	}
	if got := batchStatusOfBuilding(t, ctx, pool, furnace1); got != "running" {
		t.Fatalf("lote del horno 1 tras completar el primero: %q, esperado running (2º lote en marcha)", got)
	}

	// ── (6) FASE B — déficit y RECORTE ROTATORIO: el horno 2 también demanda
	//        (20 > 10): cada tick sirve a uno y pausa al otro, alternando ─────
	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings/"+furnace2ID+"/production-batches", demoToken, map[string]any{
		"recipe_id": electricRecipeID.String(), "batches_queued": 2,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("encolar lotes del horno 2: status %d (cuerpo: %s)", r.status, r.raw)
	}
	powerWorker.RunOnce(ctx) // liquida el bucket actual (aún no liquidado)
	bDeficit := querySimNow(t, ctx, pool)
	tickD := spotTickRow(t, ctx, pool, regionID, bDeficit)
	if tickD.demand != 2*pwEnergyPerTick || tickD.supplied != pwEnergyPerTick || tickD.curtailed != pwEnergyPerTick || tickD.curtailedBuildings != 1 {
		t.Fatalf("tick de déficit inesperado: %+v (esperado demanda 20, suministro 10, recorte 10, 1 edificio)", tickD)
	}
	pausedFirst := pausedNoPowerOf(t, ctx, pool, furnace1, furnace2)
	if n := countRows(t, ctx, pool, `
		SELECT count(*) FROM outbox.events WHERE event_type = 'power.curtailed' AND aggregate_id = $1`, pausedFirst); n < 1 {
		t.Fatalf("no se emitió power.curtailed para el edificio recortado %s", pausedFirst)
	}

	// Regresión (revisión adversarial): el barrido de producción NO debe
	// deshacer el recorte reanudando con la gracia residual del tick anterior —
	// el tick cierra la cobertura de los no servidos.
	prodWorker.RunOnce(ctx)
	if got := batchStatusOfBuilding(t, ctx, pool, pausedFirst); got != "paused_no_power" {
		t.Fatalf("el barrido de producción reanudó al recortado (%q): la gracia residual debe quedar cerrada", got)
	}
	// Regresión: encolar durante la pausa por suministro NO promueve un segundo
	// lote a running (un lote pausado sigue siendo el activo del edificio).
	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings/"+pausedFirst.String()+"/production-batches", demoToken, map[string]any{
		"recipe_id": electricRecipeID.String(), "batches_queued": 1,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("encolar durante la pausa: status %d (cuerpo: %s)", r.status, r.raw)
	}
	if got, _ := asMap(t, r.body["data"], "data")["status"].(string); got != "queued" {
		t.Fatalf("lote encolado durante paused_no_power: %q, esperado queued (no debe promoverse)", got)
	}

	// Tick siguiente: la ROTACIÓN invierte el recorte (el recortado más
	// reciente se sirve; el otro pausa) — GDD 5.8.
	advanceSim(t, ctx, pool, pwSpotInterval)
	powerWorker.RunOnce(ctx)
	pausedSecond := pausedNoPowerOf(t, ctx, pool, furnace1, furnace2)
	if pausedFirst == pausedSecond {
		t.Fatalf("el recorte NO rotó: el edificio %s fue recortado en dos ticks consecutivos", pausedFirst)
	}

	// ── (7) FASE C — insolvencia sin deuda (GDD 5.9): sin caja no hay compra;
	//        ambos hornos quedan sin suministro y NINGÚN saldo baja de 0 ──────
	drainCashTo(t, ctx, pool, demoID, 0)
	advanceSim(t, ctx, pool, pwSpotInterval)
	powerWorker.RunOnce(ctx)
	bInsolvent := querySimNow(t, ctx, pool)
	tickI := spotTickRow(t, ctx, pool, regionID, bInsolvent)
	if tickI.supplied != 0 || tickI.closing != 0 || tickI.curtailedBuildings != 2 {
		t.Fatalf("tick de insolvencia inesperado: %+v (esperado suministro 0, cierre 0, 2 edificios sin servir)", tickI)
	}
	for _, f := range []uuid.UUID{furnace1, furnace2} {
		if got := batchStatusOfBuilding(t, ctx, pool, f); got != "paused_no_power" {
			t.Fatalf("horno %s tras la insolvencia: %q, esperado paused_no_power", f, got)
		}
	}
	assertNoNegativeCashE2E(t, ctx, pool)

	// ── (8) FASE D — mantenimiento de línea impagado: degrada y ABANDONED
	//        (deja de conducir; la región pierde su pool) ─────────────────────
	drainCashTo(t, ctx, pool, norteID, 0)
	advanceSim(t, ctx, pool, 30*simtime.SimDay)
	enfWorker.RunMaintenanceOnce(ctx)
	var lineStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM world.power_lines WHERE id = $1`, uuid.MustParse(lineID)).Scan(&lineStatus); err != nil {
		t.Fatalf("estado de la línea: %v", err)
	}
	if lineStatus != "abandoned" {
		t.Fatalf("línea tras 30 días-sim impagados: %q, esperado abandoned", lineStatus)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events WHERE event_type = 'power_line.abandoned'`); n != 1 {
		t.Fatalf("eventos power_line.abandoned: %d, esperado 1", n)
	}
	// Sin líneas operativas la región queda fuera del spot: no hay ticks nuevos.
	ticksBefore := countRows(t, ctx, pool, `SELECT count(*) FROM world.power_spot_ticks`)
	advanceSim(t, ctx, pool, pwSpotInterval)
	powerWorker.RunOnce(ctx)
	if got := countRows(t, ctx, pool, `SELECT count(*) FROM world.power_spot_ticks`); got != ticksBefore {
		t.Fatalf("ticks tras abandonar la línea: %d, esperado %d (la región no debe liquidar sin pool)", got, ticksBefore)
	}

	// ── (9) Lecturas del contrato ────────────────────────────────────────────
	r = call(t, srv, http.MethodGet, "/api/v1/world/power/spot?region_id="+regionID.String(), demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /world/power/spot: status %d (cuerpo: %s)", r.status, r.raw)
	}
	if ticks, _ := r.body["data"].([]any); len(ticks) < 4 {
		t.Fatalf("histórico del spot: %d ticks, esperado >= 4", len(ticks))
	}
	r = call(t, srv, http.MethodGet, "/api/v1/world/power/dispatches?building_id="+furnace1ID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /world/power/dispatches: status %d (cuerpo: %s)", r.status, r.raw)
	}
	if items, _ := r.body["data"].([]any); len(items) < 1 {
		t.Fatal("el horno 1 debe tener despachos registrados")
	}
	r = call(t, srv, http.MethodGet, "/api/v1/world/power/dispatches?building_id="+plantID, demoToken, nil)
	if r.status != http.StatusForbidden {
		t.Fatalf("despachos de la central con token ajeno: status %d, esperado 403", r.status)
	}
	r = call(t, srv, http.MethodGet, "/api/v1/world/power-lines?region_id="+regionID.String(), demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /world/power-lines: status %d", r.status)
	}
	if lines, _ := r.body["data"].([]any); len(lines) != 1 {
		t.Fatalf("catálogo de líneas: %d, esperado 1", len(lines))
	}

	// ── (10) Auditoría final: el ledger cuadra POR ACTIVO, ningún saldo quedó
	//         negativo y físico == contable (reconciliación 0) ────────────────
	assertBalancedLedger(t, ctx, pool)
	assertNoNegativeCashE2E(t, ctx, pool)
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d discrepancias (err %v), esperado 0", disc, err)
	}
}

// ─── Auxiliares del E2E de la red eléctrica ──────────────────────────────────

// geoLine construye un LineString GeoJSON-like plano (SRID 0, metros).
func geoLine(points [][2]int64) map[string]any {
	coords := make([][]int64, len(points))
	for i, p := range points {
		coords[i] = []int64{p[0], p[1]}
	}
	return map[string]any{"type": "LineString", "coordinates": coords}
}

// errCode extrae error.code de una respuesta de error del contrato.
func errCode(t *testing.T, r response) string {
	t.Helper()
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// querySimNow devuelve el sim-time del reloj congelado.
func querySimNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx, `SELECT sim_time_at FROM world.sim_clock WHERE id = 1`).Scan(&v); err != nil {
		t.Fatalf("sim_clock: %v", err)
	}
	return v
}

// alignSimToSpotBoundary avanza el reloj congelado hasta la siguiente frontera
// de bucket del spot (múltiplo del intervalo).
func alignSimToSpotBoundary(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	cur := querySimNow(t, ctx, pool)
	if pad := pwSpotInterval - cur%pwSpotInterval; pad != pwSpotInterval {
		advanceSim(t, ctx, pool, pad)
	}
}

// spotTick es la fila agregada de un tick del spot.
type spotTick struct {
	closing            int64
	demand             int64
	supplied           int64
	curtailed          int64
	curtailedBuildings int
}

// spotTickRow lee el tick del bucket que CONTIENE a simAt (los avances del
// test no siempre caen en la frontera exacta).
func spotTickRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region uuid.UUID, simAt int64) spotTick {
	t.Helper()
	bucket := (simAt / pwSpotInterval) * pwSpotInterval
	var s spotTick
	if err := pool.QueryRow(ctx, `
		SELECT closing_price, demand_units, supplied_units, curtailed_units, curtailed_buildings
		FROM world.power_spot_ticks WHERE region_id = $1 AND tick_sim = $2`, region, bucket).
		Scan(&s.closing, &s.demand, &s.supplied, &s.curtailed, &s.curtailedBuildings); err != nil {
		t.Fatalf("tick del spot (región %s, bucket %d): %v", region, bucket, err)
	}
	return s
}

// batchStatusOfBuilding devuelve el estado del lote ACTIVO del edificio
// (running o pausado; falla si no hay ninguno).
func batchStatusOfBuilding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT status FROM world.production_batches
		WHERE building_id = $1 AND status IN ('running', 'paused_no_fuel', 'paused_no_workers', 'paused_no_power')
		ORDER BY queue_position LIMIT 1`, building).Scan(&status); err != nil {
		t.Fatalf("lote activo de %s: %v", building, err)
	}
	return status
}

// pausedNoPowerOf devuelve cuál de los dos edificios tiene el lote en
// paused_no_power (exactamente uno debe tenerlo).
func pausedNoPowerOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, a, b uuid.UUID) uuid.UUID {
	t.Helper()
	sa, sb := batchStatusOfBuilding(t, ctx, pool, a), batchStatusOfBuilding(t, ctx, pool, b)
	switch {
	case sa == "paused_no_power" && sb == "running":
		return a
	case sb == "paused_no_power" && sa == "running":
		return b
	default:
		t.Fatalf("recorte no exclusivo: horno A=%s, horno B=%s (esperado uno paused_no_power y otro running)", sa, sb)
		return uuid.Nil
	}
}
