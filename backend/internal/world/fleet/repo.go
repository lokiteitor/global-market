package fleet

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). No abre transacciones — el
// servicio/motor decide el ámbito transaccional y deriva un Repo con WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo { return &Repo{q: sqlcgen.New(db)} }

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo { return &Repo{q: r.q.WithTx(tx)} }

// ─── Catálogo de tipos de vehículo ────────────────────────────────────────────

// ListVehicleTypes lista el catálogo con filtro por modo y keyset.
func (r *Repo) ListVehicleTypes(ctx context.Context, mode string, afterID *uuid.UUID, limit int32) ([]VehicleType, error) {
	rows, err := r.q.ListVehicleTypes(ctx, sqlcgen.ListVehicleTypesParams{
		Mode:      nullLinkMode(mode),
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando tipos de vehículo: %w", err)
	}
	out := make([]VehicleType, len(rows))
	for i, row := range rows {
		out[i] = VehicleType{
			ID: row.ID, Code: row.Code, Name: row.Name, Mode: string(row.Mode),
			CargoCapacity: row.CargoCapacity, SpeedKmh: row.SpeedKmh, FuelProductID: row.FuelProductID,
			FuelPer100km: row.FuelPer100km, AutonomyKm: row.AutonomyKm,
			PurchasePrice: row.PurchasePrice, OperatingCostPerDay: row.OperatingCostPerDay,
		}
	}
	return out, nil
}

// GetVehicleType devuelve un tipo por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetVehicleType(ctx context.Context, id uuid.UUID) (VehicleType, error) {
	row, err := r.q.GetVehicleType(ctx, id)
	if err != nil {
		return VehicleType{}, err
	}
	return VehicleType{
		ID: row.ID, Code: row.Code, Name: row.Name, Mode: string(row.Mode),
		CargoCapacity: row.CargoCapacity, SpeedKmh: row.SpeedKmh, FuelProductID: row.FuelProductID,
		FuelPer100km: row.FuelPer100km, AutonomyKm: row.AutonomyKm,
		PurchasePrice: row.PurchasePrice, OperatingCostPerDay: row.OperatingCostPerDay,
	}, nil
}

// ─── Flota con posición analítica ─────────────────────────────────────────────

// ListVehicles lista los vehículos de un titular con la posición derivada.
func (r *Repo) ListVehicles(ctx context.Context, owner uuid.UUID, f VehicleFilter, afterID *uuid.UUID, limit int32, simNow simtime.SimTime) ([]Vehicle, error) {
	rows, err := r.q.ListVehicles(ctx, sqlcgen.ListVehiclesParams{
		SimNow:         int64(simNow),
		OwnerAccountID: owner,
		Status:         nullVehicleStatus(f.Status),
		RouteID:        f.RouteID,
		AfterID:        afterID,
		PageLimit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando vehículos de %s: %w", owner, err)
	}
	out := make([]Vehicle, len(rows))
	for i, row := range rows {
		out[i] = vehicleFromList(row)
	}
	return out, nil
}

// GetVehicle devuelve un vehículo con posición; pgx.ErrNoRows si no existe.
func (r *Repo) GetVehicle(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (Vehicle, error) {
	row, err := r.q.GetVehicle(ctx, sqlcgen.GetVehicleParams{SimNow: int64(simNow), ID: id})
	if err != nil {
		return Vehicle{}, err
	}
	return vehicleFromGet(row), nil
}

// vehicleHead es la vista bloqueada de un vehículo para comando/despacho.
type vehicleHead struct {
	ID             uuid.UUID
	VehicleTypeID  uuid.UUID
	OwnerAccountID uuid.UUID
	Status         string
	Fuel           int64
	WearPct        int32
	RouteID        *uuid.UUID
	AtNodeID       *uuid.UUID
	OnSegmentID    *uuid.UUID
}

// LockVehicle bloquea un vehículo (FOR UPDATE); pgx.ErrNoRows si no existe.
func (r *Repo) LockVehicle(ctx context.Context, id uuid.UUID) (vehicleHead, error) {
	row, err := r.q.LockVehicle(ctx, id)
	if err != nil {
		return vehicleHead{}, err
	}
	return vehicleHead{
		ID: row.ID, VehicleTypeID: row.VehicleTypeID, OwnerAccountID: row.OwnerAccountID,
		Status: string(row.Status), Fuel: row.Fuel, WearPct: row.WearPct,
		RouteID: row.RouteID, AtNodeID: row.AtNodeID, OnSegmentID: row.OnSegmentID,
	}, nil
}

// insertVehicleParams son los parámetros de InsertVehicle.
type insertVehicleParams struct {
	ID            uuid.UUID
	VehicleTypeID uuid.UUID
	Owner         uuid.UUID
	Fuel          int64
	AtNode        uuid.UUID
	SimNow        simtime.SimTime
}

// InsertVehicle da de alta un vehículo idle en el nodo de entrega.
func (r *Repo) InsertVehicle(ctx context.Context, p insertVehicleParams) (uuid.UUID, error) {
	node := p.AtNode
	id, err := r.q.InsertVehicle(ctx, sqlcgen.InsertVehicleParams{
		ID: p.ID, VehicleTypeID: p.VehicleTypeID, OwnerAccountID: p.Owner,
		Fuel: p.Fuel, AtNodeID: &node, SimNow: int64(p.SimNow),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/fleet: dando de alta el vehículo %s: %w", p.ID, err)
	}
	return id, nil
}

// SetVehicleRoute asigna/retira la ruta de un vehículo.
func (r *Repo) SetVehicleRoute(ctx context.Context, id uuid.UUID, routeID *uuid.UUID, simNow simtime.SimTime) error {
	if err := r.q.SetVehicleRoute(ctx, sqlcgen.SetVehicleRouteParams{RouteID: routeID, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: asignando ruta al vehículo %s: %w", id, err)
	}
	return nil
}

// SetVehicleMaintenance programa mantenimiento (in_maintenance, wear 0).
func (r *Repo) SetVehicleMaintenance(ctx context.Context, id uuid.UUID, repairUntil simtime.SimTime, simNow simtime.SimTime) error {
	ru := int64(repairUntil)
	if err := r.q.SetVehicleMaintenance(ctx, sqlcgen.SetVehicleMaintenanceParams{RepairUntilSim: &ru, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: programando mantenimiento del vehículo %s: %w", id, err)
	}
	return nil
}

// ─── Rutas (lectura para validar el despacho) ─────────────────────────────────

// GetRouteOwnerActive devuelve el titular y el estado activo de una ruta;
// pgx.ErrNoRows si no existe.
func (r *Repo) GetRouteOwnerActive(ctx context.Context, id uuid.UUID) (uuid.UUID, bool, error) {
	row, err := r.q.GetRouteOwnerActive(ctx, id)
	if err != nil {
		return uuid.Nil, false, err
	}
	return row.OwnerAccountID, row.Active, nil
}

// routeEndpoints son los extremos y la distancia total de una ruta.
type routeEndpoints struct {
	FirstFromNode uuid.UUID
	LastToNode    uuid.UUID
	TotalLengthM  int64
	LegCount      int64
}

// GetRouteEndpoints devuelve los extremos, la distancia y el número de legs.
func (r *Repo) GetRouteEndpoints(ctx context.Context, routeID uuid.UUID) (routeEndpoints, error) {
	row, err := r.q.GetRouteEndpoints(ctx, routeID)
	if err != nil {
		return routeEndpoints{}, err
	}
	return routeEndpoints{
		FirstFromNode: row.FirstFromNode, LastToNode: row.LastToNode,
		TotalLengthM: row.TotalLengthM, LegCount: row.LegCount,
	}, nil
}

// segmentSpec describe un segmento a recorrer (para poblar advance_fn).
type segmentSpec struct {
	SegmentID     uuid.UUID
	LengthM       int32
	CongestionEma float64
	BaseSpeedKmh  int32
	FromNodeID    uuid.UUID
	ToNodeID      uuid.UUID
}

// GetRouteFirstSegment devuelve el primer segmento del primer leg; pgx.ErrNoRows
// si la ruta no tiene tramos con segmento.
func (r *Repo) GetRouteFirstSegment(ctx context.Context, routeID uuid.UUID) (segmentSpec, error) {
	row, err := r.q.GetRouteFirstSegment(ctx, routeID)
	if err != nil {
		return segmentSpec{}, err
	}
	return segmentSpec{
		SegmentID: row.SegmentID, LengthM: row.LengthM, CongestionEma: row.CongestionEma,
		BaseSpeedKmh: row.BaseSpeedKmh, FromNodeID: row.FromNodeID, ToNodeID: row.ToNodeID,
	}, nil
}

// ─── Nodos y productos ────────────────────────────────────────────────────────

// nodeInfo es la existencia + edificio de un nodo.
type nodeInfo struct {
	ID         uuid.UUID
	RegionID   uuid.UUID
	BuildingID *uuid.UUID
}

// GetNode devuelve un nodo; pgx.ErrNoRows si no existe.
func (r *Repo) GetNode(ctx context.Context, id uuid.UUID) (nodeInfo, error) {
	row, err := r.q.GetNode(ctx, id)
	if err != nil {
		return nodeInfo{}, err
	}
	return nodeInfo{ID: row.ID, RegionID: row.RegionID, BuildingID: row.BuildingID}, nil
}

// NodeHasModeLink indica si el nodo está conectado a un enlace del modo.
func (r *Repo) NodeHasModeLink(ctx context.Context, node uuid.UUID, mode string) (bool, error) {
	ok, err := r.q.NodeHasModeLink(ctx, sqlcgen.NodeHasModeLinkParams{Mode: sqlcgen.WorldLinkMode(mode), NodeID: node})
	if err != nil {
		return false, fmt.Errorf("world/fleet: comprobando accesibilidad del nodo %s: %w", node, err)
	}
	return ok, nil
}

// GetProductUnitVolume devuelve el volumen por unidad; pgx.ErrNoRows si no existe.
func (r *Repo) GetProductUnitVolume(ctx context.Context, id uuid.UUID) (int32, error) {
	return r.q.GetProductUnitVolume(ctx, id)
}

// ─── Cargamentos ──────────────────────────────────────────────────────────────

// ListShipments lista los cargamentos de un titular con filtros y keyset.
func (r *Repo) ListShipments(ctx context.Context, owner uuid.UUID, f ShipmentFilter, afterID *uuid.UUID, limit int32) ([]Shipment, error) {
	rows, err := r.q.ListShipments(ctx, sqlcgen.ListShipmentsParams{
		OwnerAccountID: owner,
		Status:         nullShipmentStatus(f.Status),
		ContractID:     f.ContractID,
		VehicleID:      f.VehicleID,
		AfterID:        afterID,
		PageLimit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando cargamentos de %s: %w", owner, err)
	}
	out := make([]Shipment, len(rows))
	for i, row := range rows {
		out[i] = Shipment{
			ID: row.ID, OwnerAccountID: row.OwnerAccountID, ProductID: row.ProductID,
			Quantity: row.Quantity, ContractID: row.ContractID, FreightContractID: row.FreightContractID,
			VehicleID: row.VehicleID, AtNodeID: row.AtNodeID, Status: string(row.Status), UpdatedAtSim: row.UpdatedAtSim,
		}
	}
	return out, nil
}

// GetShipment devuelve un cargamento; pgx.ErrNoRows si no existe.
func (r *Repo) GetShipment(ctx context.Context, id uuid.UUID) (Shipment, error) {
	row, err := r.q.GetShipment(ctx, id)
	if err != nil {
		return Shipment{}, err
	}
	return Shipment{
		ID: row.ID, OwnerAccountID: row.OwnerAccountID, ProductID: row.ProductID,
		Quantity: row.Quantity, ContractID: row.ContractID, FreightContractID: row.FreightContractID,
		VehicleID: row.VehicleID, AtNodeID: row.AtNodeID, Status: string(row.Status), UpdatedAtSim: row.UpdatedAtSim,
	}, nil
}

// shipmentHead es la vista bloqueada de un cargamento para despacharlo.
type shipmentHead struct {
	ID                uuid.UUID
	OwnerAccountID    uuid.UUID
	ProductID         uuid.UUID
	Quantity          int64
	ContractID        *uuid.UUID
	VehicleID         *uuid.UUID
	AtNodeID          *uuid.UUID
	DestinationNodeID *uuid.UUID
	DeadlineSim       *int64
	Status            string
	UpdatedAtSim      int64
}

// LockShipmentForDispatch bloquea un cargamento (FOR UPDATE); pgx.ErrNoRows si no
// existe.
func (r *Repo) LockShipmentForDispatch(ctx context.Context, id uuid.UUID) (shipmentHead, error) {
	row, err := r.q.LockShipmentForDispatch(ctx, id)
	if err != nil {
		return shipmentHead{}, err
	}
	return shipmentHead{
		ID: row.ID, OwnerAccountID: row.OwnerAccountID, ProductID: row.ProductID, Quantity: row.Quantity,
		ContractID: row.ContractID, VehicleID: row.VehicleID, AtNodeID: row.AtNodeID,
		DestinationNodeID: row.DestinationNodeID, DeadlineSim: row.DeadlineSim, Status: string(row.Status),
		UpdatedAtSim: row.UpdatedAtSim,
	}, nil
}

// DispatchShipment pone el cargamento a bordo del vehículo y en tránsito.
func (r *Repo) DispatchShipment(ctx context.Context, id, vehicleID uuid.UUID, simNow simtime.SimTime) error {
	vid := vehicleID
	if err := r.q.DispatchShipment(ctx, sqlcgen.DispatchShipmentParams{VehicleID: &vid, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: despachando el cargamento %s: %w", id, err)
	}
	return nil
}

// dispatchVehicleParams son los parámetros de DispatchVehicle.
type dispatchVehicleParams struct {
	ID        uuid.UUID
	RouteID   uuid.UUID
	OnSegment uuid.UUID
	AdvanceFn []byte
	SimNow    simtime.SimTime
}

// DispatchVehicle arranca el vehículo in_transit sobre el primer segmento.
func (r *Repo) DispatchVehicle(ctx context.Context, p dispatchVehicleParams) error {
	rid, seg, sim := p.RouteID, p.OnSegment, int64(p.SimNow)
	if err := r.q.DispatchVehicle(ctx, sqlcgen.DispatchVehicleParams{
		RouteID: &rid, OnSegmentID: &seg, SimNow: &sim, AdvanceFn: p.AdvanceFn, ID: p.ID,
	}); err != nil {
		return fmt.Errorf("world/fleet: arrancando el vehículo %s: %w", p.ID, err)
	}
	return nil
}

// ─── Consumidor shipment_creator ──────────────────────────────────────────────

// insertShipmentParams son los parámetros de InsertShipmentInWarehouse.
type insertShipmentParams struct {
	ID          uuid.UUID
	Owner       uuid.UUID
	Product     uuid.UUID
	Quantity    int64
	Contract    uuid.UUID
	AtNode      uuid.UUID
	Destination uuid.UUID
	Deadline    simtime.SimTime
	SimNow      simtime.SimTime
}

// InsertShipmentInWarehouse crea el cargamento in_warehouse en el origen.
func (r *Repo) InsertShipmentInWarehouse(ctx context.Context, p insertShipmentParams) (uuid.UUID, error) {
	contract, atNode, dest, deadline := p.Contract, p.AtNode, p.Destination, int64(p.Deadline)
	id, err := r.q.InsertShipmentInWarehouse(ctx, sqlcgen.InsertShipmentInWarehouseParams{
		ID: p.ID, OwnerAccountID: p.Owner, ProductID: p.Product, Quantity: p.Quantity,
		ContractID: &contract, AtNodeID: &atNode, DestinationNodeID: &dest, DeadlineSim: &deadline,
		SimNow: int64(p.SimNow),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/fleet: creando el cargamento del contrato %s: %w", p.Contract, err)
	}
	return id, nil
}

// ShipmentExistsForContract indica si ya hay un cargamento para el contrato.
func (r *Repo) ShipmentExistsForContract(ctx context.Context, contractID uuid.UUID) (bool, error) {
	cid := contractID
	present, err := r.q.ShipmentExistsForContract(ctx, &cid)
	if err != nil {
		return false, fmt.Errorf("world/fleet: comprobando cargamento del contrato %s: %w", contractID, err)
	}
	return present, nil
}

// GetInventoryQty devuelve la cantidad física de un producto en un edificio.
func (r *Repo) GetInventoryQty(ctx context.Context, building, product uuid.UUID) (int64, error) {
	q, err := r.q.GetBuildingInventoryQty(ctx, sqlcgen.GetBuildingInventoryQtyParams{BuildingID: building, ProductID: product})
	if err != nil {
		return 0, fmt.Errorf("world/fleet: consultando inventario (%s, %s): %w", building, product, err)
	}
	return q, nil
}

// ConsumeInventory descuenta stock físico de un edificio (la fila debe cubrirlo).
func (r *Repo) ConsumeInventory(ctx context.Context, building, product uuid.UUID, amount int64, simNow simtime.SimTime) error {
	if err := r.q.ConsumeBuildingInventory(ctx, sqlcgen.ConsumeBuildingInventoryParams{
		BuildingID: building, ProductID: product, Amount: amount, SimNow: int64(simNow),
	}); err != nil {
		return fmt.Errorf("world/fleet: descontando inventario (%s, %s, -%d): %w", building, product, amount, err)
	}
	return nil
}

// AddInventory suma stock físico a un edificio (alta por entrega).
func (r *Repo) AddInventory(ctx context.Context, building, product uuid.UUID, amount int64, simNow simtime.SimTime) error {
	if err := r.q.AddBuildingInventory(ctx, sqlcgen.AddBuildingInventoryParams{
		BuildingID: building, ProductID: product, Amount: amount, SimNow: int64(simNow),
	}); err != nil {
		return fmt.Errorf("world/fleet: sumando inventario (%s, %s, +%d): %w", building, product, amount, err)
	}
	return nil
}

// ─── Motor de tránsito ────────────────────────────────────────────────────────

// ListDueTransitVehicleIDs lista los vehículos in_transit con segmento vencido.
func (r *Repo) ListDueTransitVehicleIDs(ctx context.Context, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	sim := int64(simNow)
	ids, err := r.q.ListDueTransitVehicleIDs(ctx, sqlcgen.ListDueTransitVehicleIDsParams{SimNow: &sim, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando vehículos con segmento vencido: %w", err)
	}
	return ids, nil
}

// transitVehicle es la vista bloqueada de un vehículo in_transit con su segmento.
type transitVehicle struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	Fuel           int64
	WearPct        int32
	RouteID        *uuid.UUID
	RouteLegIndex  *int32
	OnSegmentID    *uuid.UUID
	AdvanceFn      []byte
	LinkID         uuid.UUID
	SegmentSeq     int32
	SegmentLengthM int32
	FromNodeID     uuid.UUID
	ToNodeID       uuid.UUID
	BaseSpeedKmh   int32
	FuelPer100km   int64
	SpeedKmh       int32
}

// LockTransitVehicle bloquea un vehículo in_transit; pgx.ErrNoRows si otra
// instancia lo tomó (SKIP LOCKED) o dejó de estar in_transit.
func (r *Repo) LockTransitVehicle(ctx context.Context, id uuid.UUID) (transitVehicle, error) {
	row, err := r.q.LockTransitVehicle(ctx, id)
	if err != nil {
		return transitVehicle{}, err
	}
	return transitVehicle{
		ID: row.ID, OwnerAccountID: row.OwnerAccountID, Fuel: row.Fuel, WearPct: row.WearPct,
		RouteID: row.RouteID, RouteLegIndex: row.RouteLegIndex, OnSegmentID: row.OnSegmentID, AdvanceFn: row.AdvanceFn,
		LinkID: row.LinkID, SegmentSeq: row.SegmentSeq, SegmentLengthM: row.SegmentLengthM,
		FromNodeID: row.FromNodeID, ToNodeID: row.ToNodeID, BaseSpeedKmh: row.BaseSpeedKmh,
		FuelPer100km: row.FuelPer100km, SpeedKmh: row.SpeedKmh,
	}, nil
}

// GetNextSegmentInLink devuelve el siguiente segmento del mismo enlace;
// pgx.ErrNoRows si era el último.
func (r *Repo) GetNextSegmentInLink(ctx context.Context, linkID uuid.UUID, afterSeq int32) (segmentSpec, error) {
	row, err := r.q.GetNextSegmentInLink(ctx, sqlcgen.GetNextSegmentInLinkParams{LinkID: linkID, AfterSeq: afterSeq})
	if err != nil {
		return segmentSpec{}, err
	}
	return segmentSpec{SegmentID: row.SegmentID, LengthM: row.LengthM, CongestionEma: row.CongestionEma, BaseSpeedKmh: row.BaseSpeedKmh}, nil
}

// nextLegSegment es el primer segmento del siguiente leg con su nodo destino.
type nextLegSegment struct {
	LegIndex      int32
	SegmentID     uuid.UUID
	LengthM       int32
	CongestionEma float64
	BaseSpeedKmh  int32
	ToNodeID      uuid.UUID
}

// GetNextLegFirstSegment devuelve el primer segmento del leg nextLegIndex;
// pgx.ErrNoRows si no hay siguiente leg (llegada final).
func (r *Repo) GetNextLegFirstSegment(ctx context.Context, routeID uuid.UUID, nextLegIndex int32) (nextLegSegment, error) {
	row, err := r.q.GetNextLegFirstSegment(ctx, sqlcgen.GetNextLegFirstSegmentParams{RouteID: routeID, NextLegIndex: nextLegIndex})
	if err != nil {
		return nextLegSegment{}, err
	}
	return nextLegSegment{
		LegIndex: row.LegIndex, SegmentID: row.SegmentID, LengthM: row.LengthM,
		CongestionEma: row.CongestionEma, BaseSpeedKmh: row.BaseSpeedKmh, ToNodeID: row.ToNodeID,
	}, nil
}

// advanceParams son los parámetros de AdvanceVehicleToSegment.
type advanceParams struct {
	ID        uuid.UUID
	OnSegment uuid.UUID
	LegIndex  int32
	AdvanceFn []byte
	Fuel      int64
	WearPct   int32
	SimNow    simtime.SimTime
}

// AdvanceVehicleToSegment mueve el vehículo al siguiente segmento.
func (r *Repo) AdvanceVehicleToSegment(ctx context.Context, p advanceParams) error {
	seg, leg, sim := p.OnSegment, p.LegIndex, int64(p.SimNow)
	if err := r.q.AdvanceVehicleToSegment(ctx, sqlcgen.AdvanceVehicleToSegmentParams{
		OnSegmentID: &seg, RouteLegIndex: &leg, SimNow: &sim, AdvanceFn: p.AdvanceFn,
		Fuel: p.Fuel, WearPct: p.WearPct, ID: p.ID,
	}); err != nil {
		return fmt.Errorf("world/fleet: avanzando el vehículo %s: %w", p.ID, err)
	}
	return nil
}

// ArriveVehicle asienta la llegada al nodo destino final.
func (r *Repo) ArriveVehicle(ctx context.Context, id, atNode uuid.UUID, fuel int64, wear int32, simNow simtime.SimTime) error {
	node := atNode
	if err := r.q.ArriveVehicle(ctx, sqlcgen.ArriveVehicleParams{AtNodeID: &node, Fuel: fuel, WearPct: wear, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: asentando llegada del vehículo %s: %w", id, err)
	}
	return nil
}

// BreakVehicle asienta una avería (broken sobre el mismo segmento).
func (r *Repo) BreakVehicle(ctx context.Context, id uuid.UUID, repairUntil simtime.SimTime, fuel int64, wear int32, simNow simtime.SimTime) error {
	ru := int64(repairUntil)
	if err := r.q.BreakVehicle(ctx, sqlcgen.BreakVehicleParams{RepairUntilSim: &ru, Fuel: fuel, WearPct: wear, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: asentando avería del vehículo %s: %w", id, err)
	}
	return nil
}

// StrandVehicle detiene el vehículo por falta de combustible en el nodo previo.
func (r *Repo) StrandVehicle(ctx context.Context, id, atNode uuid.UUID, simNow simtime.SimTime) error {
	node := atNode
	if err := r.q.StrandVehicle(ctx, sqlcgen.StrandVehicleParams{AtNodeID: &node, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: deteniendo el vehículo %s: %w", id, err)
	}
	return nil
}

// recoveryVehicle es un vehículo broken/in_maintenance a reanudar.
type recoveryVehicle struct {
	ID     uuid.UUID
	Status string
}

// ListDueRecoveryVehicleIDs lista broken/in_maintenance con recuperación vencida.
func (r *Repo) ListDueRecoveryVehicleIDs(ctx context.Context, simNow simtime.SimTime, limit int32) ([]recoveryVehicle, error) {
	sim := int64(simNow)
	rows, err := r.q.ListDueRecoveryVehicleIDs(ctx, sqlcgen.ListDueRecoveryVehicleIDsParams{SimNow: &sim, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando vehículos a reanudar: %w", err)
	}
	out := make([]recoveryVehicle, len(rows))
	for i, row := range rows {
		out[i] = recoveryVehicle{ID: row.ID, Status: string(row.Status)}
	}
	return out, nil
}

// LockRecoveryVehicle bloquea un broken/in_maintenance; pgx.ErrNoRows si otra
// instancia lo tomó.
func (r *Repo) LockRecoveryVehicle(ctx context.Context, id uuid.UUID) (recoveryVehicle, error) {
	row, err := r.q.LockRecoveryVehicle(ctx, id)
	if err != nil {
		return recoveryVehicle{}, err
	}
	return recoveryVehicle{ID: row.ID, Status: string(row.Status)}, nil
}

// ResumeBrokenVehicle reanuda una avería re-entrando al mismo segmento.
func (r *Repo) ResumeBrokenVehicle(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) error {
	sim := int64(simNow)
	if err := r.q.ResumeBrokenVehicle(ctx, sqlcgen.ResumeBrokenVehicleParams{SimNow: &sim, ID: id}); err != nil {
		return fmt.Errorf("world/fleet: reanudando la avería del vehículo %s: %w", id, err)
	}
	return nil
}

// FinishMaintenanceVehicle devuelve un in_maintenance a idle.
func (r *Repo) FinishMaintenanceVehicle(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) error {
	if err := r.q.FinishMaintenanceVehicle(ctx, sqlcgen.FinishMaintenanceVehicleParams{SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: finalizando mantenimiento del vehículo %s: %w", id, err)
	}
	return nil
}

// shipmentDelivery es un cargamento a bordo con destino el nodo de llegada.
type shipmentDelivery struct {
	ID         uuid.UUID
	Owner      uuid.UUID
	ProductID  uuid.UUID
	Quantity   int64
	ContractID *uuid.UUID
}

// ListVehicleShipmentsForNode lista los cargamentos a bordo con destino nodeID.
func (r *Repo) ListVehicleShipmentsForNode(ctx context.Context, vehicleID, nodeID uuid.UUID) ([]shipmentDelivery, error) {
	vid, nid := vehicleID, nodeID
	rows, err := r.q.ListVehicleShipmentsForNode(ctx, sqlcgen.ListVehicleShipmentsForNodeParams{VehicleID: &vid, NodeID: &nid})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando cargamentos del vehículo %s con destino %s: %w", vehicleID, nodeID, err)
	}
	out := make([]shipmentDelivery, len(rows))
	for i, row := range rows {
		out[i] = shipmentDelivery{ID: row.ID, Owner: row.OwnerAccountID, ProductID: row.ProductID, Quantity: row.Quantity, ContractID: row.ContractID}
	}
	return out, nil
}

// DeliverShipment marca un cargamento entregado en el nodo destino.
func (r *Repo) DeliverShipment(ctx context.Context, id, atNode uuid.UUID, simNow simtime.SimTime) error {
	node := atNode
	if err := r.q.DeliverShipment(ctx, sqlcgen.DeliverShipmentParams{AtNodeID: &node, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: entregando el cargamento %s: %w", id, err)
	}
	return nil
}

// ─── Transbordo en terminal (ruta multimodal por tramos) ──────────────────────

// terminal es la vista mínima de una terminal intermodal (transbordo).
type terminal struct {
	ID                   uuid.UUID
	NodeID               uuid.UUID
	OwnerAccountID       uuid.UUID
	TransshipmentPerHour int32
}

// GetTerminalByNode devuelve la terminal intermodal de un nodo; pgx.ErrNoRows si el
// nodo no tiene terminal (no es punto de transbordo).
func (r *Repo) GetTerminalByNode(ctx context.Context, node uuid.UUID) (terminal, error) {
	row, err := r.q.GetTerminalByNode(ctx, node)
	if err != nil {
		return terminal{}, err
	}
	return terminal{
		ID: row.ID, NodeID: row.NodeID, OwnerAccountID: row.OwnerAccountID,
		TransshipmentPerHour: row.TransshipmentPerHour,
	}, nil
}

// CountRouteLegsWrongMode cuenta los tramos de una ruta cuyo enlace no es del modo
// dado (0 = ruta de un solo modo compatible con el vehículo).
func (r *Repo) CountRouteLegsWrongMode(ctx context.Context, routeID uuid.UUID, mode string) (int64, error) {
	n, err := r.q.CountRouteLegsWrongMode(ctx, sqlcgen.CountRouteLegsWrongModeParams{
		RouteID: routeID, Mode: sqlcgen.WorldLinkMode(mode),
	})
	if err != nil {
		return 0, fmt.Errorf("world/fleet: contando tramos de modo ajeno en la ruta %s: %w", routeID, err)
	}
	return n, nil
}

// transshipCandidate es un cargamento a bordo cuyo destino NO es el nodo de llegada
// (candidato a transbordo si el nodo tiene terminal).
type transshipCandidate struct {
	ID          uuid.UUID
	Owner       uuid.UUID
	ProductID   uuid.UUID
	Quantity    int64
	ContractID  *uuid.UUID
	Destination *uuid.UUID
}

// ListVehicleShipmentsToTransship lista los cargamentos a bordo con destino distinto
// del nodo de llegada (candidatos a transbordo).
func (r *Repo) ListVehicleShipmentsToTransship(ctx context.Context, vehicleID, nodeID uuid.UUID) ([]transshipCandidate, error) {
	vid, nid := vehicleID, nodeID
	rows, err := r.q.ListVehicleShipmentsToTransship(ctx, sqlcgen.ListVehicleShipmentsToTransshipParams{VehicleID: &vid, NodeID: &nid})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando cargamentos a transbordar del vehículo %s en %s: %w", vehicleID, nodeID, err)
	}
	out := make([]transshipCandidate, len(rows))
	for i, row := range rows {
		out[i] = transshipCandidate{
			ID: row.ID, Owner: row.OwnerAccountID, ProductID: row.ProductID,
			Quantity: row.Quantity, ContractID: row.ContractID, Destination: row.DestinationNodeID,
		}
	}
	return out, nil
}

// TransshipShipment deja un cargamento at_terminal en el nodo de la terminal (fuera
// del vehículo) a la espera del siguiente tramo.
func (r *Repo) TransshipShipment(ctx context.Context, id, atNode uuid.UUID, simNow simtime.SimTime) error {
	node := atNode
	if err := r.q.TransshipShipment(ctx, sqlcgen.TransshipShipmentParams{AtNodeID: &node, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/fleet: transbordando el cargamento %s en %s: %w", id, atNode, err)
	}
	return nil
}

// segmentCongestion es la EMA recalculada de un segmento.
type segmentCongestion struct {
	ID            uuid.UUID
	CongestionEma float64
}

// RecomputeSegmentCongestion recalcula la EMA de todos los segmentos.
func (r *Repo) RecomputeSegmentCongestion(ctx context.Context, capacityRef float64, simNow simtime.SimTime) ([]segmentCongestion, error) {
	rows, err := r.q.RecomputeSegmentCongestion(ctx, sqlcgen.RecomputeSegmentCongestionParams{CapacityRef: capacityRef, SimNow: int64(simNow)})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: recalculando congestión: %w", err)
	}
	out := make([]segmentCongestion, len(rows))
	for i, row := range rows {
		out[i] = segmentCongestion{ID: row.ID, CongestionEma: row.CongestionEma}
	}
	return out, nil
}

// CountInTransitVehicles cuenta los vehículos en tránsito.
func (r *Repo) CountInTransitVehicles(ctx context.Context) (int64, error) {
	return r.q.CountInTransitVehicles(ctx)
}

// ─── Soporte de ledger (reutiliza queries compartidas) ────────────────────────

// ledgerAccount es la vista mínima de una cuenta del ledger (id y saldo).
type ledgerAccount struct {
	ID      uuid.UUID
	Balance int64
}

// GetCashAccount devuelve la caja de una corporación; pgx.ErrNoRows si no existe.
func (r *Repo) GetCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	o := owner
	row, err := r.q.GetCashAccount(ctx, &o)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// GetSinkAccount devuelve la cuenta sink del banco central; pgx.ErrNoRows si el
// seed no la creó.
func (r *Repo) GetSinkAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetSinkAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo (los triggers de 0004 garantizan saldo, no-negatividad y doble
// entrada). Los IDs (UUIDv7) los genera la aplicación (ADR-018).
func (r *Repo) PostLedgerTransaction(ctx context.Context, kind sqlcgen.LedgerTransactionKind, simAt simtime.SimTime, reference uuid.UUID, description string, entries []entryAmount) error {
	txID, err := newUUIDv7()
	if err != nil {
		return err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	ref := reference
	if err := r.q.InsertLedgerTransaction(ctx, sqlcgen.InsertLedgerTransactionParams{
		ID: txID, Kind: kind, SimTimeAt: int64(simAt), ReferenceID: &ref, Description: desc,
	}); err != nil {
		return fmt.Errorf("world/fleet: asentando la cabecera %s de %s: %w", kind, reference, err)
	}
	for _, e := range entries {
		entryID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := r.q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{
			ID: entryID, TransactionID: txID, AccountID: e.AccountID, Amount: e.Amount,
		}); err != nil {
			return fmt.Errorf("world/fleet: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// ─── Conversión y utilidades ──────────────────────────────────────────────────

func vehicleFromList(row sqlcgen.ListVehiclesRow) Vehicle {
	return Vehicle{
		ID: row.ID, VehicleTypeID: row.VehicleTypeID, OwnerAccountID: row.OwnerAccountID,
		Status: string(row.Status), WearPct: row.WearPct, Fuel: row.Fuel,
		RouteID: row.RouteID, RouteLegIndex: row.RouteLegIndex, RepairUntilSim: row.RepairUntilSim,
		UpdatedAtSim: row.UpdatedAtSim,
		Position: Position{
			AtNodeID: row.AtNodeID, OnSegmentID: row.OnSegmentID,
			SegmentProgressPct: progressFromAny(row.SegmentProgressPct),
			Location:           locString(row.Location),
		},
	}
}

func vehicleFromGet(row sqlcgen.GetVehicleRow) Vehicle {
	return Vehicle{
		ID: row.ID, VehicleTypeID: row.VehicleTypeID, OwnerAccountID: row.OwnerAccountID,
		Status: string(row.Status), WearPct: row.WearPct, Fuel: row.Fuel,
		RouteID: row.RouteID, RouteLegIndex: row.RouteLegIndex, RepairUntilSim: row.RepairUntilSim,
		UpdatedAtSim: row.UpdatedAtSim,
		Position: Position{
			AtNodeID: row.AtNodeID, OnSegmentID: row.OnSegmentID,
			SegmentProgressPct: progressFromAny(row.SegmentProgressPct),
			Location:           locString(row.Location),
		},
	}
}

// progressFromAny convierte el segment_progress_pct derivado (float8 nullable que
// sqlc expone como interface{}) a *float64: nil cuando el vehículo está en un
// nodo (el contrato omite segment_progress_pct), el avance cuando circula.
func progressFromAny(v interface{}) *float64 {
	switch t := v.(type) {
	case float64:
		f := t
		return &f
	case float32:
		f := float64(t)
		return &f
	default:
		return nil
	}
}

// locString extrae el GeoJSON (text) que ST_AsGeoJSON proyecta como interface{}.
func locString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func nullLinkMode(mode string) sqlcgen.NullWorldLinkMode {
	return sqlcgen.NullWorldLinkMode{WorldLinkMode: sqlcgen.WorldLinkMode(mode), Valid: mode != ""}
}

func nullVehicleStatus(status string) sqlcgen.NullWorldVehicleStatus {
	return sqlcgen.NullWorldVehicleStatus{WorldVehicleStatus: sqlcgen.WorldVehicleStatus(status), Valid: status != ""}
}

func nullShipmentStatus(status string) sqlcgen.NullWorldShipmentStatus {
	return sqlcgen.NullWorldShipmentStatus{WorldShipmentStatus: sqlcgen.WorldShipmentStatus(status), Valid: status != ""}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/fleet: generando UUIDv7: %w", err)
	}
	return id, nil
}
