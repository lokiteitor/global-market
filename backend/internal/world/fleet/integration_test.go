package fleet_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
)

// Parámetros del fixture (coordenadas, longitudes y velocidades elegidas para un
// tiempo de viaje por segmento de 3600 sim-seconds: ceil(20km*1.0/100kmh)=1h).
const (
	segTravelSecs int64 = 3600
	widgetStock   int64 = 10000
	shipQty       int64 = 100
	truckPrice    int64 = 50000
	truckFuel     int64 = 1000 // tanque lleno = fuel_per_100km*autonomy/100 = 100*1000/100
	fuelPerRoute  int64 = 40   // 100 * 40000m / 100000
)

type netFixture struct {
	widget, diesel        uuid.UUID
	originWH, destWH      uuid.UUID
	originNode, midNode   uuid.UUID
	destNode, isolated    uuid.UUID
	segA, segB            uuid.UUID
	truckType, miniType   uuid.UUID
	routeFull, routeShort uuid.UUID
}

// TestFleetIntegration ejercita world/fleet contra una BD real migrada (0001-0009)
// con el seed del Incremento 1 más un fixture de red vial propio: compra de
// vehículo, validación/rechazo del despacho, tránsito analítico completo (avance
// por segmentos, llegada y entrega física), avería forzada + reanudación,
// shipment_creator y job de congestión.
func TestFleetIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger:              ledger.DefaultOptions(),
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	norte := accountID(t, ctx, pool, seed.DefaultTraderName)
	region := regionID(t, ctx, pool, seed.RegionName)
	bank := accountID(t, ctx, pool, seed.CentralBankName)
	poor := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO auth.accounts (id, kind, name) VALUES ($1,'human'::auth.account_kind,$2)`, poor, "Corp Pobre")

	fx := seedNetwork(t, ctx, pool, region, demo, norte, bank)

	simNow := int64(1000)
	sim := &advSim{now: &simNow}
	svc, err := fleet.NewService(pool, sim, fleet.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, &simNow, logger)

	// ── (1) Compra de vehículo: coste al sink, alta idle con tanque lleno ──────
	t.Run("compra de vehiculo cobra al sink", func(t *testing.T) {
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		rec := do(t, demoMux, http.MethodPost, "/world/vehicles",
			fmt.Sprintf(`{"vehicle_type_id":%q,"delivery_node_id":%q}`, fx.truckType, fx.originNode))
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST vehicle: status %d (body %s)", rec.Code, rec.Body.String())
		}
		v := dataOf[vehicleDTO](t, rec)
		if v.Status != "idle" {
			t.Fatalf("estado del vehículo comprado = %q, esperado idle", v.Status)
		}
		if v.Fuel != fmt.Sprint(truckFuel) {
			t.Fatalf("fuel inicial = %q, esperado %d (tanque lleno)", v.Fuel, truckFuel)
		}
		if v.Position.AtNodeID != fx.originNode.String() {
			t.Fatalf("posición at_node = %q, esperado %q", v.Position.AtNodeID, fx.originNode)
		}
		if v.Position.Location == nil || len(v.Position.Location.Coordinates) != 2 ||
			!approx(v.Position.Location.Coordinates[0], 10000) || !approx(v.Position.Location.Coordinates[1], 10000) {
			t.Fatalf("location del nodo = %+v, esperado [10000,10000]", v.Position.Location)
		}
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != truckPrice {
			t.Fatalf("caja bajó %d, esperado precio %d", d, truckPrice)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != truckPrice {
			t.Fatalf("sink subió %d, esperado precio %d", d, truckPrice)
		}
	})

	t.Run("compra rechaza fondos, nodo incompatible y refs inexistentes", func(t *testing.T) {
		poorMux := newMux(svc, poor, &simNow, logger)
		rec := do(t, poorMux, http.MethodPost, "/world/vehicles",
			fmt.Sprintf(`{"vehicle_type_id":%q,"delivery_node_id":%q}`, fx.truckType, fx.originNode))
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "INSUFFICIENT_FUNDS") {
			t.Fatalf("compra sin fondos: status %d body %s", rec.Code, rec.Body.String())
		}
		// Nodo sin enlace del modo (aislado) → 422.
		rec = do(t, demoMux, http.MethodPost, "/world/vehicles",
			fmt.Sprintf(`{"vehicle_type_id":%q,"delivery_node_id":%q}`, fx.truckType, fx.isolated))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("compra en nodo incompatible: status %d body %s", rec.Code, rec.Body.String())
		}
		// Tipo inexistente → 404.
		rec = do(t, demoMux, http.MethodPost, "/world/vehicles",
			fmt.Sprintf(`{"vehicle_type_id":%q,"delivery_node_id":%q}`, uuid.Must(uuid.NewV7()), fx.originNode))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("compra con tipo inexistente: status %d body %s", rec.Code, rec.Body.String())
		}
	})

	// ── (2) shipment_creator: contract.confirmed (buy cross-node) → cargamento ─
	t.Run("shipment_creator crea cargamento y mueve inventario", func(t *testing.T) {
		cid := makeReservedContract(t, ctx, pool, demo, norte, fx.widget, fx.originWH, fx.originNode, fx.destNode, shipQty, 500000)
		invBefore := inventoryQty(t, ctx, pool, fx.originWH, fx.widget)

		creator := fleet.NewShipmentCreator(logger, nil)
		creator.NewConsumer(pool) // fija el repo base; el consumidor lo arranca el engine

		ev := outbox.Event{
			EventID:       uuid.Must(uuid.NewV7()),
			AggregateType: "contract", AggregateID: cid, EventType: "contract.confirmed",
			SimTimeAt: 2000,
			Payload: mustJSON(map[string]any{
				"contract_id": cid.String(), "kind": "buy",
				"seller_account_id": demo.String(), "buyer_account_id": norte.String(),
				"product_id": fx.widget.String(), "quantity": fmt.Sprint(shipQty),
				"origin_node_id": fx.originNode.String(), "destination_node_id": fx.destNode.String(),
				"deadline_sim": 500000, "confirmed_at_sim": 2000,
			}),
		}
		tx := begin(t, ctx, pool)
		if err := creator.Handle(ctx, tx, ev); err != nil {
			t.Fatalf("Handle contract.confirmed: %v", err)
		}
		commit(t, ctx, tx)

		if d := invBefore - inventoryQty(t, ctx, pool, fx.originWH, fx.widget); d != shipQty {
			t.Fatalf("inventario de origen bajó %d, esperado %d (stock movido al cargamento)", d, shipQty)
		}
		shID, status, atNode, dest := shipmentForContract(t, ctx, pool, cid)
		if status != "in_warehouse" || atNode != fx.originNode.String() || dest != fx.destNode.String() {
			t.Fatalf("cargamento creado: status=%s at_node=%s dest=%s", status, atNode, dest)
		}
		if !outboxHas(t, ctx, pool, "shipment.created", shID) {
			t.Fatal("no se emitió shipment.created")
		}
		// Coherencia físico↔contable con el cargamento in_warehouse contabilizado.
		if d := discrepancyAt(t, ctx, pool, fx.originWH, fx.widget); d != 0 {
			t.Fatalf("discrepancia de reconciliación en origen tras crear el cargamento = %d, esperado 0", d)
		}
	})

	// ── (3) Despacho: validación y rechazos ───────────────────────────────────
	t.Run("despacho valida la ruta y pone en transito", func(t *testing.T) {
		simNow = 1000
		_, shID := stageShipment(t, ctx, pool, demo, norte, fx.widget, fx.originWH, fx.originNode, fx.destNode, shipQty, 500000)
		veh := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, truckFuel, 0)

		rec := do(t, demoMux, http.MethodPost, "/world/shipments/"+shID.String()+"/dispatch",
			fmt.Sprintf(`{"vehicle_id":%q,"route_id":%q}`, veh, fx.routeFull))
		if rec.Code != http.StatusOK {
			t.Fatalf("dispatch válido: status %d body %s", rec.Code, rec.Body.String())
		}
		sh := dataOf[shipmentDTO](t, rec)
		if sh.Status != "in_transit" || sh.VehicleID != veh.String() {
			t.Fatalf("cargamento tras despacho: status=%s vehicle=%s", sh.Status, sh.VehicleID)
		}
		vstatus, _, onSeg, _, _, _ := vehicleRow(t, ctx, pool, veh)
		if vstatus != "in_transit" || onSeg != fx.segA.String() {
			t.Fatalf("vehículo tras despacho: status=%s on_segment=%s (esperado in_transit/segA)", vstatus, onSeg)
		}
		if d := discrepancyAt(t, ctx, pool, fx.originWH, fx.widget); d != 0 {
			t.Fatalf("discrepancia en origen con cargamento in_transit = %d, esperado 0", d)
		}
	})

	t.Run("despacho rechaza ruta incorrecta, capacidad, combustible, estado y ajeno", func(t *testing.T) {
		simNow = 1000
		// Ruta que no llega al destino (termina en el nodo intermedio).
		_, sh1 := stageShipment(t, ctx, pool, demo, norte, fx.widget, fx.originWH, fx.originNode, fx.destNode, shipQty, 500000)
		veh1 := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, truckFuel, 0)
		if _, err := svc.DispatchShipment(ctx, demo, sh1, fleet.ShipmentDispatch{VehicleID: veh1, RouteID: fx.routeShort}); !isValidation(err) {
			t.Fatalf("ruta que no llega al destino: err=%v (esperado validación 422)", err)
		}
		// Capacidad insuficiente (vehículo mini).
		mini := makeVehicle(t, ctx, pool, demo, fx.miniType, fx.originNode, truckFuel, 0)
		if _, err := svc.DispatchShipment(ctx, demo, sh1, fleet.ShipmentDispatch{VehicleID: mini, RouteID: fx.routeFull}); !isValidation(err) {
			t.Fatalf("capacidad insuficiente: err=%v (esperado validación 422)", err)
		}
		// Combustible insuficiente.
		lowFuel := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, fuelPerRoute-1, 0)
		if _, err := svc.DispatchShipment(ctx, demo, sh1, fleet.ShipmentDispatch{VehicleID: lowFuel, RouteID: fx.routeFull}); !isValidation(err) {
			t.Fatalf("combustible insuficiente: err=%v (esperado validación 422)", err)
		}
		// Vehículo no idle (in_transit).
		busy := makeTransitVehicle(t, ctx, pool, demo, fx.truckType, fx.segA)
		if _, err := svc.DispatchShipment(ctx, demo, sh1, fleet.ShipmentDispatch{VehicleID: busy, RouteID: fx.routeFull}); !errorIs(err, "no está idle") {
			t.Fatalf("vehículo no idle: err=%v (esperado 409)", err)
		}
		// Cargamento ajeno (autenticado como norte).
		if _, err := svc.DispatchShipment(ctx, norte, sh1, fleet.ShipmentDispatch{VehicleID: veh1, RouteID: fx.routeFull}); !errorIs(err, "otra corporación") {
			t.Fatalf("cargamento ajeno: err=%v (esperado 403)", err)
		}
	})

	// ── (4) Tránsito: avance analítico, llegada y entrega física ──────────────
	t.Run("transito avanza por segmentos, llega y entrega", func(t *testing.T) {
		resetFleet(t, ctx, pool) // aísla del tránsito dejado por sub-tests previos
		simNow = 1000
		destBefore := inventoryQty(t, ctx, pool, fx.destWH, fx.widget)
		_, shID := stageShipment(t, ctx, pool, demo, norte, fx.widget, fx.originWH, fx.originNode, fx.destNode, shipQty, 500000)
		veh := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, truckFuel, 0)
		if _, err := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: veh, RouteID: fx.routeFull}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		wopts := fleet.DefaultWorkerOptions()
		wopts.Roll = func() float64 { return 1.0 } // nunca avería
		worker, err := fleet.NewTransitWorker(pool, sim, wopts, logger, nil)
		if err != nil {
			t.Fatalf("NewTransitWorker: %v", err)
		}

		// Posición analítica intermedia: a mitad del primer segmento (reloj
		// congelado en t=2800, entrada en 1000, viaje 3600 ⇒ 50%).
		simNow = 2800
		v := getVehicle(t, demoMux, veh)
		if v.Position.OnSegmentID != fx.segA.String() {
			t.Fatalf("posición intermedia on_segment=%s, esperado segA", v.Position.OnSegmentID)
		}
		if v.Position.SegmentProgressPct == nil || *v.Position.SegmentProgressPct < 40 || *v.Position.SegmentProgressPct > 60 {
			t.Fatalf("progreso intermedio = %v, esperado ~50", v.Position.SegmentProgressPct)
		}
		if v.Position.Location == nil || !approx(v.Position.Location.Coordinates[0], 20000) || !approx(v.Position.Location.Coordinates[1], 10000) {
			t.Fatalf("location interpolada = %+v, esperado ~[20000,10000]", v.Position.Location)
		}

		// El primer segmento vence (t = 1000+3600): avanza al segundo leg.
		simNow = 1000 + segTravelSecs
		worker.RunOnce(ctx)
		vstatus, _, onSeg, _, _, _ := vehicleRow(t, ctx, pool, veh)
		if vstatus != "in_transit" || onSeg != fx.segB.String() {
			t.Fatalf("tras primer segmento: status=%s on_segment=%s (esperado in_transit/segB)", vstatus, onSeg)
		}

		// El segundo segmento vence: llegada al destino y entrega física.
		simNow = 1000 + 2*segTravelSecs
		worker.RunOnce(ctx)
		vstatus, atNode, _, fuel, wear, _ := vehicleRow(t, ctx, pool, veh)
		if vstatus != "idle" || atNode != fx.destNode.String() {
			t.Fatalf("tras llegada: status=%s at_node=%s (esperado idle/destNode)", vstatus, atNode)
		}
		if fuel != truckFuel-fuelPerRoute {
			t.Fatalf("combustible tras la ruta = %d, esperado %d", fuel, truckFuel-fuelPerRoute)
		}
		if wear != 2 { // 1 por segmento, 2 segmentos
			t.Fatalf("desgaste tras la ruta = %d, esperado 2", wear)
		}
		shStatus, shNode, shVeh := shipmentRow(t, ctx, pool, shID)
		if shStatus != "delivered" || shNode != fx.destNode.String() || shVeh != "" {
			t.Fatalf("cargamento entregado: status=%s at_node=%s vehicle=%s", shStatus, shNode, shVeh)
		}
		if d := inventoryQty(t, ctx, pool, fx.destWH, fx.widget) - destBefore; d != shipQty {
			t.Fatalf("stock físico entregado en destino = %d, esperado %d", d, shipQty)
		}
		if !outboxHas(t, ctx, pool, "shipment.arrived", shID) {
			t.Fatal("no se emitió shipment.arrived")
		}
		if !outboxHas(t, ctx, pool, "vehicle.arrived", veh) {
			t.Fatal("no se emitió vehicle.arrived")
		}
	})

	// ── (5) Avería forzada y reanudación sin perder carga ─────────────────────
	t.Run("averia fuerza broken y reanuda tras reparacion sin perder carga", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		_, shID := stageShipment(t, ctx, pool, demo, norte, fx.widget, fx.originWH, fx.originNode, fx.destNode, shipQty, 900000)
		veh := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, truckFuel, 100) // desgaste alto
		if _, err := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: veh, RouteID: fx.routeFull}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		breakNow := true
		wopts := fleet.DefaultWorkerOptions()
		wopts.RepairSimSeconds = 1800
		wopts.Roll = func() float64 {
			if breakNow {
				return 0.0 // < p ⇒ avería segura
			}
			return 1.0
		}
		worker, err := fleet.NewTransitWorker(pool, sim, wopts, logger, nil)
		if err != nil {
			t.Fatalf("NewTransitWorker: %v", err)
		}

		simNow = 1000 + segTravelSecs
		worker.RunOnce(ctx)
		vstatus, _, onSeg, _, _, repairUntil := vehicleRow(t, ctx, pool, veh)
		if vstatus != "broken" || onSeg != fx.segA.String() {
			t.Fatalf("tras avería: status=%s on_segment=%s (esperado broken/segA)", vstatus, onSeg)
		}
		if repairUntil == nil {
			t.Fatal("repair_until_sim no fijado tras la avería")
		}
		// La carga espera a bordo (sigue in_transit sobre el vehículo).
		if s, _, v := shipmentRow(t, ctx, pool, shID); s != "in_transit" || v != veh.String() {
			t.Fatalf("carga tras avería: status=%s vehicle=%s (esperado in_transit a bordo)", s, v)
		}
		if !outboxHas(t, ctx, pool, "vehicle.broken", veh) {
			t.Fatal("no se emitió vehicle.broken")
		}

		// Reparación vencida: reanuda (re-entra al mismo segmento).
		breakNow = false
		simNow = *repairUntil
		worker.RunOnce(ctx)
		if s, _, onSeg, _, _, _ := vehicleRow(t, ctx, pool, veh); s != "in_transit" || onSeg != fx.segA.String() {
			t.Fatalf("tras reanudar: status=%s on_segment=%s (esperado in_transit/segA reentrado)", s, onSeg)
		}
		// Completa la ruta: la carga se entrega íntegra (no se perdió).
		simNow = *repairUntil + 2*segTravelSecs
		worker.RunOnce(ctx) // vence segA reentrado → segB
		simNow = *repairUntil + 4*segTravelSecs
		worker.RunOnce(ctx) // vence segB → llegada
		if s, node, _ := shipmentRow(t, ctx, pool, shID); s != "delivered" || node != fx.destNode.String() {
			t.Fatalf("carga tras reanudación: status=%s at_node=%s (esperado delivered en destino)", s, node)
		}
	})

	// ── (6) Job de congestión actualiza la EMA por segmento ───────────────────
	t.Run("job de congestion actualiza la EMA", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		// Dos vehículos in_transit sobre segB elevan su congestión.
		makeTransitVehicle(t, ctx, pool, demo, fx.truckType, fx.segB)
		makeTransitVehicle(t, ctx, pool, demo, fx.truckType, fx.segB)
		before := segmentCongestion(t, ctx, pool, fx.segB)

		wopts := fleet.DefaultWorkerOptions()
		wopts.CongestionCapacityRef = 1 // 2 vehículos ⇒ carga_normalizada = 2
		worker, err := fleet.NewTransitWorker(pool, sim, wopts, logger, nil)
		if err != nil {
			t.Fatalf("NewTransitWorker: %v", err)
		}
		if _, err := worker.RunCongestionOnce(ctx); err != nil {
			t.Fatalf("RunCongestionOnce: %v", err)
		}
		after := segmentCongestion(t, ctx, pool, fx.segB)
		// EMA = 0.3*2 + 0.7*before(=1.0) = 1.3.
		if after <= before || after < 1.25 || after > 1.35 {
			t.Fatalf("congestión de segB antes=%.3f después=%.3f, esperado ~1.3", before, after)
		}
	})
}

// ─── Fixture de red vial ──────────────────────────────────────────────────────

func seedNetwork(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, demo, norte, bank uuid.UUID) netFixture {
	t.Helper()
	fx := netFixture{
		widget: createProduct(t, ctx, pool, "widget", false),
		diesel: createProduct(t, ctx, pool, "diesel", true),
	}
	// Concesiones y tipo de almacén.
	concDemo := uuid.Must(uuid.NewV7())
	concNorte := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.land_concessions (id, region_id, holder_account_id, parcel, canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1,$2,$3,ST_GeomFromText('POLYGON((0 0,90000 0,90000 90000,0 90000,0 0))',0),1000,90,9000000,'active',0)`, concDemo, region, demo)
	exec(t, ctx, pool, `INSERT INTO world.land_concessions (id, region_id, holder_account_id, parcel, canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1,$2,$3,ST_GeomFromText('POLYGON((0 0,90000 0,90000 90000,0 90000,0 0))',0),1000,90,9000000,'active',0)`, concNorte, region, norte)
	whType := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.building_types (id, code, name, footprint_cells, max_level, base_storage, placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1,'fleet_warehouse','Almacén',4,4,10000000,'{}'::jsonb,'{}'::jsonb,1000,10)`, whType)

	fx.originWH = insertBuilding(t, ctx, pool, region, demo, concDemo, whType, 10000, 10000)
	fx.destWH = insertBuilding(t, ctx, pool, region, norte, concNorte, whType, 50000, 10000)
	fx.originNode = insertNode(t, ctx, pool, region, &fx.originWH, "warehouse", 10000, 10000)
	fx.midNode = insertNode(t, ctx, pool, region, nil, "junction", 30000, 10000)
	fx.destNode = insertNode(t, ctx, pool, region, &fx.destWH, "warehouse", 50000, 10000)
	fx.isolated = insertNode(t, ctx, pool, region, nil, "junction", 80000, 80000)

	linkA := insertLink(t, ctx, pool, fx.originNode, fx.midNode, 20000, 100, "10000 10000,30000 10000")
	linkB := insertLink(t, ctx, pool, fx.midNode, fx.destNode, 20000, 100, "30000 10000,50000 10000")
	fx.segA = insertSegment(t, ctx, pool, region, linkA, 20000, "10000 10000,30000 10000")
	fx.segB = insertSegment(t, ctx, pool, region, linkB, 20000, "30000 10000,50000 10000")

	// Vehículos: camión (capacidad holgada) y mini (capacidad insuficiente).
	fx.truckType = insertVehicleType(t, ctx, pool, "truck", fx.diesel, 1000, 100, 100, 1000, truckPrice)
	fx.miniType = insertVehicleType(t, ctx, pool, "mini_truck", fx.diesel, 10, 100, 100, 1000, 5000)

	// Rutas propias de demo: completa (origen→mid→destino) y corta (origen→mid).
	fx.routeFull = insertRoute(t, ctx, pool, demo, "full", []uuid.UUID{linkA, linkB})
	fx.routeShort = insertRoute(t, ctx, pool, demo, "short", []uuid.UUID{linkA})

	// Stock de widget en el almacén de origen (coherente físico↔contable).
	seedStock(t, ctx, pool, demo, fx.originWH, fx.widget, bank, widgetStock)
	return fx
}

func insertBuilding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, owner, conc, btype uuid.UUID, x, y int) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	wkt := fmt.Sprintf("POLYGON((%d %d,%d %d,%d %d,%d %d,%d %d))", x, y, x+100, y, x+100, y+100, x, y+100, x, y)
	exec(t, ctx, pool, `INSERT INTO world.buildings (id, owner_account_id, region_id, concession_id, building_type_id, footprint, level, status, condition_pct, fuel_stock, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,ST_GeomFromText($6,0),1,'operational',100,0,0)`, id, owner, region, conc, btype, wkt)
	return id
}

func insertNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region uuid.UUID, building *uuid.UUID, kind string, x, y int) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.network_nodes (id, kind, region_id, building_id, location)
		VALUES ($1,$2::world.node_kind,$3,$4,ST_GeomFromText($5,0))`, id, kind, region, building, fmt.Sprintf("POINT(%d %d)", x, y))
	return id
}

func insertLink(t *testing.T, ctx context.Context, pool *pgxpool.Pool, from, to uuid.UUID, lengthM, speed int, lineCoords string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.network_links (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1,'road',$2,$3,ST_GeomFromText($4,0),$5,100,$6)`, id, from, to, "LINESTRING("+lineCoords+")", lengthM, speed)
	return id
}

func insertSegment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, link uuid.UUID, lengthM int, lineCoords string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema, updated_at_sim)
		VALUES ($1,$2,$3,1,ST_GeomFromText($4,0),$5,1.0,0)`, id, link, region, "LINESTRING("+lineCoords+")", lengthM)
	return id
}

func insertVehicleType(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string, fuelProduct uuid.UUID, cargo, speed, fuelPer100km, autonomy, price int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.vehicle_types (id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id, fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day)
		VALUES ($1,$2,$2,'road',$3,$4,$5,$6,$7,$8,100)`, id, code, cargo, speed, fuelProduct, fuelPer100km, autonomy, price)
	return id
}

func insertRoute(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID, name string, links []uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.routes (id, owner_account_id, name, kind) VALUES ($1,$2,$3,'on_demand')`, id, owner, name)
	for i, link := range links {
		exec(t, ctx, pool, `INSERT INTO world.route_legs (route_id, leg_index, link_id) VALUES ($1,$2,$3)`, id, i, link)
	}
	return id
}

func createProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string, isFuel bool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.products (id, code, name, class, unit_volume, base_price, price_floor, price_ceiling, is_fuel)
		VALUES ($1,$2,$2,'basic',1,100,10,1000,$3)`, id, code, isFuel)
	return id
}

// seedStock funda el stock coherente físico↔contable: crea la cuenta stock_free,
// asienta production_output (+stock_free / -world_source) e inserta el inventario.
func seedStock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, building, product, bank uuid.UUID, qty int64) {
	t.Helper()
	ws := ensureWorldSource(t, ctx, pool, product, bank)
	sf := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id) VALUES ($1,'stock_free',$2,$3,$4)`, sf, owner, product, building)
	postLedger(t, ctx, pool, "production_output", []ledgerEntry{{sf, qty}, {ws, -qty}})
	exec(t, ctx, pool, `INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim) VALUES ($1,$2,$3,0)`, building, product, qty)
}

func ensureWorldSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, product, bank uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='world_source' AND product_id=$1`, product).Scan(&id); err == nil {
		return id
	}
	id = uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id) VALUES ($1,'world_source',$2,$3)`, id, bank, product)
	return id
}

// makeReservedContract crea un contrato CCRI activo con su reserva de stock
// (stock_free → stock_reserved del contrato), sin cargamento. Es el estado tras
// contract.confirmed que consume el shipment_creator.
func makeReservedContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seller, buyer, product, originWH, originNode, destNode uuid.UUID, qty, deadline int64) uuid.UUID {
	t.Helper()
	cid := uuid.Must(uuid.NewV7())
	reserve := uuid.Must(uuid.NewV7())
	guarantee := uuid.Must(uuid.NewV7())
	escrow := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id, reference_id) VALUES ($1,'stock_reserved',$2,$3,$4,$5)`, reserve, seller, product, originWH, cid)
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, reference_id) VALUES ($1,'guarantee',$2,$3)`, guarantee, seller, cid)
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, reference_id) VALUES ($1,'escrow',$2,$3)`, escrow, buyer, cid)
	sf := stockFreeAccountID(t, ctx, pool, seller, product, originWH)
	postLedger(t, ctx, pool, "contract_confirmation", []ledgerEntry{{sf, -qty}, {reserve, qty}})
	exec(t, ctx, pool, `INSERT INTO ledger.contracts (id, channel, buyer_account_id, seller_account_id, product_id, quantity_agreed, unit_price, origin_node_id, destination_node_id, deadline_sim, status, stock_reserve_account_id, seller_guarantee_account_id, escrow_account_id, confirmed_at_sim)
		VALUES ($1,'board',$2,$3,$4,$5,1,$6,$7,$8,'active',$9,$10,$11,0)`, cid, buyer, seller, product, qty, originNode, destNode, deadline, reserve, guarantee, escrow)
	return cid
}

// stageShipment materializa el cargamento in_warehouse como lo haría el
// shipment_creator (reserva + cargamento + salida del stock físico del almacén).
func stageShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seller, buyer, product, originWH, originNode, destNode uuid.UUID, qty, deadline int64) (uuid.UUID, uuid.UUID) {
	t.Helper()
	cid := makeReservedContract(t, ctx, pool, seller, buyer, product, originWH, originNode, destNode, qty, deadline)
	shID := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.shipments (id, owner_account_id, product_id, quantity, contract_id, at_node_id, destination_node_id, deadline_sim, status, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'in_warehouse',0)`, shID, seller, product, qty, cid, originNode, destNode, deadline)
	exec(t, ctx, pool, `UPDATE world.building_inventories SET quantity = quantity - $3 WHERE building_id=$1 AND product_id=$2`, originWH, product, qty)
	return cid, shID
}

// resetFleet aísla un sub-test que arranca el motor de tránsito de los vehículos
// y cargamentos que dejaron los sub-tests previos (el barrido es global): borra
// toda la flota y sus cargamentos (los contratos/cuentas del ledger permanecen).
func resetFleet(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	exec(t, ctx, pool, `DELETE FROM world.shipments`)
	exec(t, ctx, pool, `DELETE FROM world.vehicles`)
}

func makeVehicle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, vtype, atNode uuid.UUID, fuel, wear int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.vehicles (id, vehicle_type_id, owner_account_id, status, wear_pct, fuel, at_node_id, updated_at_sim)
		VALUES ($1,$2,$3,'idle',$4,$5,$6,0)`, id, vtype, owner, wear, fuel, atNode)
	return id
}

func makeTransitVehicle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, vtype, segment uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	af := `{"base_speed_kmh":100,"congestion_ema":1.0,"length_m":20000,"dir":1}`
	exec(t, ctx, pool, `INSERT INTO world.vehicles (id, vehicle_type_id, owner_account_id, status, wear_pct, fuel, on_segment_id, segment_entered_sim, advance_fn, updated_at_sim)
		VALUES ($1,$2,$3,'in_transit',0,1000,$4,1000,$5::jsonb,0)`, id, vtype, owner, segment, af)
	return id
}

// ─── Consultas de aserción ────────────────────────────────────────────────────

func inventoryQty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT quantity FROM world.building_inventories WHERE building_id=$1 AND product_id=$2),0)`, building, product)
}

func cashBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, acc uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT balance FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1),0)`, acc)
}

func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	return scalarInt(t, ctx, pool, `SELECT balance FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`)
}

func segmentCongestion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seg uuid.UUID) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(ctx, `SELECT congestion_ema::float8 FROM world.link_segments WHERE id=$1`, seg).Scan(&v); err != nil {
		t.Fatalf("congestión de %s: %v", seg, err)
	}
	return v
}

func stockFreeAccountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, building uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='stock_free' AND owner_account_id=$1 AND product_id=$2 AND warehouse_building_id=$3`, owner, product, building).Scan(&id); err != nil {
		t.Fatalf("stockFreeAccountID: %v", err)
	}
	return id
}

// discrepancyAt replica la reconciliación física↔contable para un (almacén,
// producto): building_inventories + cargamentos en vuelo (atribuidos por la
// cuenta de reserva) menos el stock comprometible (free+reserved). 0 = coherente.
func discrepancyAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `
		SELECT
		  (COALESCE((SELECT quantity FROM world.building_inventories WHERE building_id=$1 AND product_id=$2),0)
		   + COALESCE((SELECT SUM(sh.quantity) FROM world.shipments sh
		        JOIN ledger.contracts c ON c.id = sh.contract_id
		        JOIN ledger.accounts a ON a.id = c.stock_reserve_account_id
		        WHERE sh.status IN ('in_warehouse','in_transit','at_terminal')
		          AND a.warehouse_building_id=$1 AND a.product_id=$2),0))
		  - COALESCE((SELECT SUM(balance) FROM ledger.accounts
		        WHERE kind IN ('stock_free','stock_reserved') AND warehouse_building_id=$1 AND product_id=$2),0)`,
		building, product)
}

func vehicleRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (status, atNode, onSeg string, fuel int64, wear int32, repairUntil *int64) {
	t.Helper()
	var an, os *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT status::text, at_node_id, on_segment_id, fuel, wear_pct, repair_until_sim FROM world.vehicles WHERE id=$1`, id).
		Scan(&status, &an, &os, &fuel, &wear, &repairUntil); err != nil {
		t.Fatalf("vehículo %s: %v", id, err)
	}
	return status, uuidStr(an), uuidStr(os), fuel, wear, repairUntil
}

func shipmentRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (status, atNode, vehicle string) {
	t.Helper()
	var an, veh *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT status::text, at_node_id, vehicle_id FROM world.shipments WHERE id=$1`, id).Scan(&status, &an, &veh); err != nil {
		t.Fatalf("cargamento %s: %v", id, err)
	}
	return status, uuidStr(an), uuidStr(veh)
}

func shipmentForContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool, contract uuid.UUID) (id uuid.UUID, status, atNode, dest string) {
	t.Helper()
	var an, d *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id, status::text, at_node_id, destination_node_id FROM world.shipments WHERE contract_id=$1`, contract).Scan(&id, &status, &an, &d); err != nil {
		t.Fatalf("cargamento del contrato %s: %v", contract, err)
	}
	return id, status, uuidStr(an), uuidStr(d)
}

func outboxHas(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string, aggregateID uuid.UUID) bool {
	t.Helper()
	var present bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM outbox.events WHERE event_type=$1 AND aggregate_id=$2)`, eventType, aggregateID).Scan(&present); err != nil {
		t.Fatalf("outbox %s/%s: %v", eventType, aggregateID, err)
	}
	return present
}

func scalarInt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// ─── HTTP y utilidades ────────────────────────────────────────────────────────

type geoJSON struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type positionDTO struct {
	AtNodeID           string   `json:"at_node_id"`
	OnSegmentID        string   `json:"on_segment_id"`
	SegmentProgressPct *float64 `json:"segment_progress_pct"`
	Location           *geoJSON `json:"location"`
}

type vehicleDTO struct {
	ID            string      `json:"id"`
	VehicleTypeID string      `json:"vehicle_type_id"`
	Status        string      `json:"status"`
	WearPct       int32       `json:"wear_pct"`
	Fuel          string      `json:"fuel"`
	RouteID       string      `json:"route_id"`
	Position      positionDTO `json:"position"`
}

type shipmentDTO struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	VehicleID string `json:"vehicle_id"`
	AtNodeID  string `json:"at_node_id"`
}

func getVehicle(t *testing.T, mux *http.ServeMux, id uuid.UUID) vehicleDTO {
	t.Helper()
	rec := do(t, mux, http.MethodGet, "/world/vehicles/"+id.String(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET vehicle: status %d body %s", rec.Code, rec.Body.String())
	}
	return dataOf[vehicleDTO](t, rec)
}

func isValidation(err error) bool { return errorIs(err, "world/fleet: parámetros inválidos") }

func errorIs(err error, substr string) bool {
	return err != nil && strings.Contains(err.Error(), substr)
}

func approx(a, b float64) bool {
	d := a - b
	return d < 0.5 && d > -0.5
}

func uuidStr(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func begin(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return tx
}

func commit(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

type ledgerEntry struct {
	account uuid.UUID
	amount  int64
}

func postLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, entries []ledgerEntry) {
	t.Helper()
	tx := begin(t, ctx, pool)
	defer tx.Rollback(ctx) //nolint:errcheck
	txID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1,$2::ledger.transaction_kind,0)`, txID, kind); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(ctx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1,$2,$3,$4)`, uuid.Must(uuid.NewV7()), txID, e.account, e.amount); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	commit(t, ctx, tx)
}

// ─── Sim, identidad, meta y arranque de BD ────────────────────────────────────

type advSim struct{ now *int64 }

func (a *advSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(*a.now) }

type fakeMeta struct{ now *int64 }

func (m fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(simtime.SimTime(*m.now)), SimTimeSeconds: *m.now, ServerTime: time.Now().UTC()}
}

type fakeIdentity struct{ acc uuid.UUID }

func (i fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return i.acc, true }

func newMux(svc *fleet.Service, acc uuid.UUID, now *int64, logger *slog.Logger) *http.ServeMux {
	h := fleet.NewHandlers(svc, fakeIdentity{acc}, fakeMeta{now}, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func dataOf[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decodificando data: %v (body %s)", err, rec.Body.String())
	}
	return resp.Data
}

func accountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("cuenta %q: %v", name, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func exec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldfleettest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	if _, err := migrate.New(conn, "../../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
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
