package fleet_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
)

// TestFleetMultimodal ejercita el TRANSPORTE FERROVIARIO y MARÍTIMO y el TRANSBORDO
// intermodal (Incremento 7, FASE 2 MUNDO) contra una BD real migrada + seed mínimo.
// Fixture propio: una región con un almacén de origen, una terminal intermodal
// (junction con terminal) y un almacén destino, unidos por road (origen→terminal),
// rail (terminal→destino) y un ramal sea aislado (puerto→isla).
//
// Cubre: (a) un tren se mueve por un enlace rail y llega; (b) un barco por sea; (c)
// un vehículo de modo incorrecto es rechazado en el despacho; (d) un cargamento
// hace un transbordo road→rail en la terminal (at_terminal → puerta de tiempo de
// transbordo → despacho en tren → llegada); (e) el road intra-región sigue
// funcionando.
func TestFleetMultimodal(t *testing.T) {
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
	bank := accountID(t, ctx, pool, seed.CentralBankName)
	region := regionID(t, ctx, pool, seed.RegionName)

	fx := seedMultimodalNetwork(t, ctx, pool, region, demo, bank)

	simNow := int64(1000)
	sim := &advSim{now: &simNow}
	svc, err := fleet.NewService(pool, sim, fleet.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Parámetros de tránsito deterministas: 3600 sim-seconds por segmento vencido
	// (nunca avería para aislar el camino feliz).
	worker := func() *fleet.TransitWorker {
		wopts := fleet.DefaultWorkerOptions()
		wopts.Roll = func() float64 { return 1.0 }
		w, werr := fleet.NewTransitWorker(pool, sim, wopts, logger, nil)
		if werr != nil {
			t.Fatalf("NewTransitWorker: %v", werr)
		}
		return w
	}

	// ── (a) Un tren recorre un enlace RAIL entre nodos y llega ────────────────
	t.Run("tren recorre rail y llega", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		shID := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.termNode, fx.destNode, mmQty)
		train := makeVehicle(t, ctx, pool, demo, fx.trainType, fx.termNode, 1000, 0)
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: train, RouteID: fx.railRoute}); derr != nil {
			t.Fatalf("dispatch tren: %v", derr)
		}
		if s, _, onSeg, _, _, _ := vehicleRow(t, ctx, pool, train); s != "in_transit" || onSeg != fx.railSeg.String() {
			t.Fatalf("tren tras despacho: status=%s on_segment=%s (esperado in_transit/railSeg)", s, onSeg)
		}
		// El segmento rail (20 km a 120 km/h ⇒ ceil(0.167 h)=1 h=3600 s) vence.
		simNow = 1000 + 3600
		worker().RunOnce(ctx)
		if s, atNode, _, _, _, _ := vehicleRow(t, ctx, pool, train); s != "idle" || atNode != fx.destNode.String() {
			t.Fatalf("tren tras llegada: status=%s at_node=%s (esperado idle/destNode)", s, atNode)
		}
		if s, node, _ := shipmentRow(t, ctx, pool, shID); s != "delivered" || node != fx.destNode.String() {
			t.Fatalf("cargamento del tren: status=%s at_node=%s (esperado delivered/destNode)", s, node)
		}
		if !outboxHas(t, ctx, pool, "shipment.arrived", shID) {
			t.Fatal("el tren no emitió shipment.arrived")
		}
	})

	// ── (b) Un barco recorre un enlace SEA y llega ────────────────────────────
	t.Run("barco recorre sea y llega", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		shID := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.portNode, fx.islandNode, mmQty)
		ship := makeVehicle(t, ctx, pool, demo, fx.shipType, fx.portNode, 1000, 0)
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: ship, RouteID: fx.seaRoute}); derr != nil {
			t.Fatalf("dispatch barco: %v", derr)
		}
		// El segmento sea (50 km a 40 km/h ⇒ ceil(1.25 h)=2 h=7200 s) vence.
		simNow = 1000 + 7200
		worker().RunOnce(ctx)
		if s, atNode, _, _, _, _ := vehicleRow(t, ctx, pool, ship); s != "idle" || atNode != fx.islandNode.String() {
			t.Fatalf("barco tras llegada: status=%s at_node=%s (esperado idle/islandNode)", s, atNode)
		}
		if s, _, _ := shipmentRow(t, ctx, pool, shID); s != "delivered" {
			t.Fatalf("cargamento del barco: status=%s (esperado delivered)", s)
		}
		if !outboxHas(t, ctx, pool, "shipment.arrived", shID) {
			t.Fatal("el barco no emitió shipment.arrived")
		}
	})

	// ── (c) Un vehículo de modo incorrecto es rechazado en el despacho ────────
	t.Run("modo incorrecto rechazado en el despacho", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		// Ruta rail despachada por un CAMIÓN (road): un camión no circula por rail.
		shID := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.termNode, fx.destNode, mmQty)
		truck := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.termNode, 1000, 0)
		_, err := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: truck, RouteID: fx.railRoute})
		if !errors.Is(err, fleet.ErrWrongVehicleMode) {
			t.Fatalf("camión en ruta rail: err=%v (esperado ErrWrongVehicleMode)", err)
		}
		// Ruta road despachada por un TREN (rail): un tren no circula por road.
		sh2 := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.originNode, fx.destNode, mmQty)
		train := makeVehicle(t, ctx, pool, demo, fx.trainType, fx.originNode, 1000, 0)
		_, err = svc.DispatchShipment(ctx, demo, sh2, fleet.ShipmentDispatch{VehicleID: train, RouteID: fx.roadToDestRoute})
		if !errors.Is(err, fleet.ErrWrongVehicleMode) {
			t.Fatalf("tren en ruta road: err=%v (esperado ErrWrongVehicleMode)", err)
		}
	})

	// ── (d) Transbordo road→rail en terminal: at_terminal, puerta de tiempo, tren ─
	t.Run("transbordo road a rail en terminal", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		// Cargamento en el almacén de origen con destino MÁS ALLÁ de la terminal.
		shID := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.originNode, fx.destNode, mmQty)

		// Tramo 1 (road): origen → terminal. La ruta acaba en la terminal, no en el
		// destino final (extremo válido por ser terminal de transbordo).
		truck := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, 1000, 0)
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: truck, RouteID: fx.roadToTermRoute}); derr != nil {
			t.Fatalf("dispatch road tramo 1: %v", derr)
		}
		// El camión llega a la terminal (20 km a 100 km/h ⇒ 1 h): TRANSBORDO.
		arrival := int64(1000 + 3600)
		simNow = arrival
		worker().RunOnce(ctx)
		if s, node, veh := shipmentRow(t, ctx, pool, shID); s != "at_terminal" || node != fx.termNode.String() || veh != "" {
			t.Fatalf("tras el tramo road: status=%s at_node=%s vehicle=%s (esperado at_terminal/termNode/sin vehículo)", s, node, veh)
		}
		if s, atNode, _, _, _, _ := vehicleRow(t, ctx, pool, truck); s != "idle" || atNode != fx.termNode.String() {
			t.Fatalf("camión tras transbordo: status=%s at_node=%s (esperado idle/termNode)", s, atNode)
		}
		if !outboxHas(t, ctx, pool, "shipment.at_terminal", shID) {
			t.Fatal("no se emitió shipment.at_terminal")
		}

		// Tramo 2 (rail): terminal → destino. La puerta de tiempo de transbordo
		// (vol 100 / 120 por hora ⇒ ceil=1 h=3600 s) impide el despacho inmediato.
		train := makeVehicle(t, ctx, pool, demo, fx.trainType, fx.termNode, 1000, 0)
		simNow = arrival + 1000 // aún dentro del transbordo (< 3600)
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: train, RouteID: fx.railRoute}); !errors.Is(derr, fleet.ErrTransshipmentPending) {
			t.Fatalf("despacho durante el transbordo: err=%v (esperado ErrTransshipmentPending)", derr)
		}
		// Cumplido el tiempo de transbordo, el despacho en el tren procede.
		simNow = arrival + 3600
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: train, RouteID: fx.railRoute}); derr != nil {
			t.Fatalf("dispatch rail tramo 2: %v", derr)
		}
		if s, _, veh := shipmentRow(t, ctx, pool, shID); s != "in_transit" || veh != train.String() {
			t.Fatalf("tras despacho rail: status=%s vehicle=%s (esperado in_transit a bordo del tren)", s, veh)
		}
		// El tren completa el tramo rail (3600 s) y ENTREGA en el destino.
		simNow = arrival + 3600 + 3600
		worker().RunOnce(ctx)
		if s, node, _ := shipmentRow(t, ctx, pool, shID); s != "delivered" || node != fx.destNode.String() {
			t.Fatalf("tras el tramo rail: status=%s at_node=%s (esperado delivered/destNode)", s, node)
		}
		if !outboxHas(t, ctx, pool, "shipment.arrived", shID) {
			t.Fatal("el tramo rail no emitió shipment.arrived")
		}
	})

	// ── (e) El road intra-región sigue funcionando (sin regresión) ────────────
	t.Run("road intra-region sigue funcionando", func(t *testing.T) {
		resetFleet(t, ctx, pool)
		simNow = 1000
		shID := insertPlainShipment(t, ctx, pool, demo, fx.widget, fx.originNode, fx.destNode, mmQty)
		truck := makeVehicle(t, ctx, pool, demo, fx.truckType, fx.originNode, 1000, 0)
		if _, derr := svc.DispatchShipment(ctx, demo, shID, fleet.ShipmentDispatch{VehicleID: truck, RouteID: fx.roadToDestRoute}); derr != nil {
			t.Fatalf("dispatch road directo: %v", derr)
		}
		// Ruta road de 2 tramos (origen→terminal→destino): dos segmentos de 3600 s.
		simNow = 1000 + 3600
		worker().RunOnce(ctx) // vence el primer segmento → avanza al segundo leg
		simNow = 1000 + 7200
		worker().RunOnce(ctx) // vence el segundo → llegada y entrega
		if s, node, _ := shipmentRow(t, ctx, pool, shID); s != "delivered" || node != fx.destNode.String() {
			t.Fatalf("road directo: status=%s at_node=%s (esperado delivered/destNode)", s, node)
		}
	})
}

// ─── Fixture multimodal ───────────────────────────────────────────────────────

const mmQty int64 = 100

type mmFixture struct {
	widget                                      uuid.UUID
	originNode, termNode, destNode              uuid.UUID
	portNode, islandNode                        uuid.UUID
	roadSeg, railSeg                            uuid.UUID
	truckType, trainType, shipType              uuid.UUID
	roadToTermRoute, railRoute, roadToDestRoute uuid.UUID
	seaRoute                                    uuid.UUID
}

// seedMultimodalNetwork siembra el grafo multimodal: origen —road→ terminal —rail→
// destino, y puerto —sea→ isla, con una terminal intermodal en el nodo de cambio de
// modo y los tipos de vehículo road/rail/sea. Geometrías SRID 0 planar (ADR-019).
func seedMultimodalNetwork(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, demo, bank uuid.UUID) mmFixture {
	t.Helper()
	fx := mmFixture{widget: createProduct(t, ctx, pool, "widget", false)}
	createProduct(t, ctx, pool, "diesel", true) // combustible del catálogo (no usado aquí)

	// Nodos. La terminal es un junction que enlaza road y rail (cambio de modo).
	fx.originNode = insertNode(t, ctx, pool, region, nil, "warehouse", 10000, 10000)
	fx.termNode = insertNode(t, ctx, pool, region, nil, "junction", 30000, 10000)
	fx.destNode = insertNode(t, ctx, pool, region, nil, "station", 50000, 10000)
	fx.portNode = insertNode(t, ctx, pool, region, nil, "port", 10000, 50000)
	fx.islandNode = insertNode(t, ctx, pool, region, nil, "port", 60000, 50000)

	// Enlaces (un segmento por enlace) y sus segmentos.
	roadLink := insertLinkMode(t, ctx, pool, region, "road", fx.originNode, fx.termNode, 20000, 100, 100, "10000 10000,30000 10000")
	railLink := insertLinkMode(t, ctx, pool, region, "rail", fx.termNode, fx.destNode, 20000, 200, 120, "30000 10000,50000 10000")
	roadLink2 := insertLinkMode(t, ctx, pool, region, "road", fx.termNode, fx.destNode, 20000, 100, 100, "30000 10000,50000 10000")
	seaLink := insertLinkMode(t, ctx, pool, region, "sea", fx.portNode, fx.islandNode, 50000, 400, 40, "10000 50000,60000 50000")
	fx.roadSeg = insertSegment(t, ctx, pool, region, roadLink, 20000, "10000 10000,30000 10000")
	fx.railSeg = insertSegment(t, ctx, pool, region, railLink, 20000, "30000 10000,50000 10000")
	insertSegment(t, ctx, pool, region, roadLink2, 20000, "30000 10000,50000 10000")
	insertSegment(t, ctx, pool, region, seaLink, 50000, "10000 50000,60000 50000")

	// Terminal intermodal en el nodo de cambio de modo (owner = banco central).
	insertTerminalRow(t, ctx, pool, fx.termNode, bank, 120)

	// Tipos de vehículo por modo (combustible: widget, irrelevante para el tránsito).
	fx.truckType = insertVehicleTypeMode(t, ctx, pool, "mm_truck", "road", fx.widget, 6000, 100, 100, 2000, 40000)
	fx.trainType = insertVehicleTypeMode(t, ctx, pool, "mm_train", "rail", fx.widget, 40000, 120, 60, 3000, 500000)
	fx.shipType = insertVehicleTypeMode(t, ctx, pool, "mm_ship", "sea", fx.widget, 120000, 40, 120, 8000, 1200000)

	// Rutas de demo por tramo de un solo modo.
	fx.roadToTermRoute = insertRoute(t, ctx, pool, demo, "road-a-terminal", []uuid.UUID{roadLink})
	fx.railRoute = insertRoute(t, ctx, pool, demo, "rail-a-destino", []uuid.UUID{railLink})
	fx.roadToDestRoute = insertRoute(t, ctx, pool, demo, "road-directo", []uuid.UUID{roadLink, roadLink2})
	fx.seaRoute = insertRoute(t, ctx, pool, demo, "sea-a-isla", []uuid.UUID{seaLink})
	return fx
}

// insertLinkMode inserta un enlace dirigido de un modo cualquiera (road/rail/sea).
func insertLinkMode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region uuid.UUID, mode string, from, to uuid.UUID, lengthM, capacity, speed int, lineCoords string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.network_links (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1,$2::world.link_mode,$3,$4,ST_GeomFromText($5,0),$6,$7,$8)`,
		id, mode, from, to, "LINESTRING("+lineCoords+")", lengthM, capacity, speed)
	return id
}

// insertVehicleTypeMode inserta un tipo de vehículo de un modo cualquiera.
func insertVehicleTypeMode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code, mode string, fuelProduct uuid.UUID, cargo, speed, fuelPer100km, autonomy, price int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.vehicle_types (id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id, fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day)
		VALUES ($1,$2,$2,$3::world.link_mode,$4,$5,$6,$7,$8,$9,100)`, id, code, mode, cargo, speed, fuelProduct, fuelPer100km, autonomy, price)
	return id
}

// insertTerminalRow inserta una terminal intermodal en un nodo.
func insertTerminalRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, node, owner uuid.UUID, perHour int) {
	t.Helper()
	exec(t, ctx, pool, `INSERT INTO world.terminals (id, node_id, owner_account_id, transshipment_per_hour)
		VALUES ($1,$2,$3,$4)`, uuid.Must(uuid.NewV7()), node, owner, perHour)
}

// insertPlainShipment inserta un cargamento in_warehouse en un nodo con destino
// registrado, sin contrato (aísla el tránsito físico de la maquinaria del CCRI).
func insertPlainShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, atNode, destNode uuid.UUID, qty int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.shipments (id, owner_account_id, product_id, quantity, at_node_id, destination_node_id, status, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,'in_warehouse',0)`, id, owner, product, qty, atNode, destNode)
	return id
}
