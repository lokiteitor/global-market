package fleet

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// ─── Tipos de dominio (schemas del contrato) ──────────────────────────────────

// VehicleType es un tipo del catálogo de vehículos (schema VehicleType).
type VehicleType struct {
	ID                  uuid.UUID
	Code                string
	Name                string
	Mode                string
	CargoCapacity       int64
	SpeedKmh            int32
	FuelProductID       uuid.UUID
	FuelPer100km        int64
	AutonomyKm          int32
	PurchasePrice       int64
	OperatingCostPerDay int64
}

// Position es la posición física DERIVADA analíticamente al observarla (schema
// VehiclePosition). at_node XOR on_segment; para on_segment se derivan el avance
// y la location interpolada; para at_node, la location del nodo.
type Position struct {
	AtNodeID           *uuid.UUID
	OnSegmentID        *uuid.UUID
	SegmentProgressPct *float64
	// Location es el GeoJSON plano (SRID 0) ya serializado por ST_AsGeoJSON, o ""
	// si no hay geometría disponible.
	Location string
}

// Vehicle es un vehículo de la flota con su posición derivada (schema Vehicle).
type Vehicle struct {
	ID             uuid.UUID
	VehicleTypeID  uuid.UUID
	OwnerAccountID uuid.UUID
	Status         string
	WearPct        int32
	Fuel           int64
	RouteID        *uuid.UUID
	RouteLegIndex  *int32
	RepairUntilSim *int64
	UpdatedAtSim   int64
	Position       Position
}

// Shipment es un cargamento propio (schema Shipment).
type Shipment struct {
	ID                uuid.UUID
	OwnerAccountID    uuid.UUID
	ProductID         uuid.UUID
	Quantity          int64
	ContractID        *uuid.UUID
	FreightContractID *uuid.UUID
	VehicleID         *uuid.UUID
	AtNodeID          *uuid.UUID
	Status            string
	UpdatedAtSim      int64
}

// ─── Entradas y filtros ───────────────────────────────────────────────────────

// VehicleTypeFilter son los filtros de GET /world/vehicle-types.
type VehicleTypeFilter struct {
	Mode   string
	Cursor string
	Limit  int
}

// VehicleFilter son los filtros de GET /world/vehicles.
type VehicleFilter struct {
	Status  string
	RouteID *uuid.UUID
	Cursor  string
	Limit   int
}

// ShipmentFilter son los filtros de GET /world/shipments.
type ShipmentFilter struct {
	Status     string
	ContractID *uuid.UUID
	VehicleID  *uuid.UUID
	Cursor     string
	Limit      int
}

// VehiclePurchase es el cuerpo de POST /world/vehicles (schema VehiclePurchase).
type VehiclePurchase struct {
	VehicleTypeID  uuid.UUID
	DeliveryNodeID uuid.UUID
}

// VehicleUpdate es el cuerpo de PATCH /world/vehicles/{id} (schema
// VehicleUpdate). SetRoute distingue "no enviado" de "enviado null".
type VehicleUpdate struct {
	SetRoute            bool
	RouteID             *uuid.UUID
	ScheduleMaintenance bool
}

// ShipmentDispatch es el cuerpo de POST /world/shipments/{id}/dispatch (schema
// ShipmentDispatch v1.3.0).
type ShipmentDispatch struct {
	VehicleID uuid.UUID
	RouteID   uuid.UUID
}

// ─── Función de avance analítica (advance_fn JSONB) ───────────────────────────

// advanceFn es la función de avance persistida al entrar a un segmento
// (world.vehicles.advance_fn). La congestión es la SNAPSHOT del momento de
// entrada: la llegada no se recalcula al variar la congestión (GDD 1.1/7.3).
type advanceFn struct {
	BaseSpeedKmh  int32   `json:"base_speed_kmh"`
	CongestionEma float64 `json:"congestion_ema"`
	LengthM       int32   `json:"length_m"`
	Dir           int     `json:"dir"`
}

func (a advanceFn) marshal() ([]byte, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("world/fleet: serializando advance_fn: %w", err)
	}
	return b, nil
}

func decodeAdvanceFn(raw []byte) (advanceFn, error) {
	var a advanceFn
	if err := json.Unmarshal(raw, &a); err != nil {
		return advanceFn{}, fmt.Errorf("world/fleet: leyendo advance_fn: %w", err)
	}
	return a, nil
}

// ─── Fórmulas del motor de tránsito ───────────────────────────────────────────
//
// El tiempo de viaje de un segmento (fórmula VINCULANTE: factor = 1/congestion_ema;
// t = ceil(length_km * congestion_ema / base_speed_kmh) * 3600) es propiedad ÚNICA
// de la función SQL world.segment_travel_seconds (migración 0009), compartida por
// la derivación analítica de la posición (GET vehicle) y por el barrido de
// segmentos vencidos del motor. El código Go no la reimplementa para no arriesgar
// divergencia: consulta la BD (ListDueTransitVehicleIDs / GetVehicle).

// fuelForDistance calcula el combustible que consume recorrer distanceM metros:
// fuel_per_100km * distanceM / 100000 (100 km = 100000 m), truncado. math/big
// evita el desbordamiento de la multiplicación intermedia.
func fuelForDistance(fuelPer100km int64, distanceM int64) int64 {
	if fuelPer100km <= 0 || distanceM <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(fuelPer100km), big.NewInt(distanceM))
	n.Quo(n, big.NewInt(100000))
	return n.Int64()
}

// requiredVolume calcula quantity*unitVolume con guarda de overflow (math/big).
func requiredVolume(quantity int64, unitVolume int32) (int64, error) {
	n := new(big.Int).Mul(big.NewInt(quantity), big.NewInt(int64(unitVolume)))
	if !n.IsInt64() {
		return 0, ErrOverflow
	}
	return n.Int64(), nil
}

// transshipmentSeconds calcula el tiempo de transbordo (sim-segundos) de un volumen
// en una terminal de tasa perHour (unidades de volumen por hora). Se redondea a
// HORAS enteras —misma granularidad que world.segment_travel_seconds del motor—
// con suelo de una hora para todo transbordo real. perHour<=0 o volumen<=0
// (defensivo) → una hora.
func transshipmentSeconds(volume int64, perHour int32) int64 {
	if volume <= 0 || perHour <= 0 {
		return 3600
	}
	hours := (volume + int64(perHour) - 1) / int64(perHour) // ceil
	if hours < 1 {
		hours = 1
	}
	return hours * 3600
}

// ─── Cursores keyset (id ASC, UUIDv7 ≈ orden de creación) ─────────────────────

type idCursor struct {
	ID uuid.UUID
}

func encodeCursor(id uuid.UUID) string { return keyset.Encode(idCursor{ID: id}) }

func decodeCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[idCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}
