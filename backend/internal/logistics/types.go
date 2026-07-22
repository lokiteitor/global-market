package logistics

import (
	"time"

	"github.com/google/uuid"
)

// ─── Enums del dominio (literales del contrato; se validan por conjunto para
// no acoplar el paquete Go a internal/world) ─────────────────────────────────

// Modos de transporte del contrato (world.link_mode).
const (
	ModeRoad = "road"
	ModeRail = "rail"
	ModeSea  = "sea"
)

// Clases de nodo del contrato (world.node_kind).
var validNodeKinds = map[string]bool{
	"mine": true, "factory": true, "warehouse": true, "port": true,
	"station": true, "distribution_center": true, "junction": true, "city_gate": true,
}

// Modos válidos del contrato.
var validModes = map[string]bool{ModeRoad: true, ModeRail: true, ModeSea: true}

// Tipos de ruta del contrato (world.route_kind).
const (
	RouteKindFixedLine = "fixed_line"
	RouteKindOnDemand  = "on_demand"
)

var validRouteKinds = map[string]bool{RouteKindFixedLine: true, RouteKindOnDemand: true}

// Criterios de optimización del route-plan.
const (
	OptimizeTime = "time"
	OptimizeCost = "cost"
)

func validNodeKind(s string) bool  { return validNodeKinds[s] }
func validMode(s string) bool      { return validModes[s] }
func validRouteKind(s string) bool { return validRouteKinds[s] }

// ─── Tipos de dominio del grafo ──────────────────────────────────────────────

// NetworkNode es un nodo del grafo logístico (schema NetworkNode). Location es
// un GeoJSON plano (SRID 0) ya proyectado por la BD.
type NetworkNode struct {
	ID         uuid.UUID
	Kind       string
	RegionID   uuid.UUID
	BuildingID *uuid.UUID
	CityID     *uuid.UUID
	Location   string
	TerminalID *uuid.UUID
}

// LinkSegment es un segmento de un enlace, con su congestión suavizada (EMA)
// del shard que lo simula (schema LinkSegment).
type LinkSegment struct {
	ID            uuid.UUID
	LinkID        uuid.UUID
	RegionID      uuid.UUID
	Seq           int32
	LengthM       int32
	CongestionEma float64
	UpdatedAtSim  int64
}

// NetworkLink es un enlace de uso común del grafo con sus segmentos (schema
// NetworkLink). Path es un GeoJSON plano (SRID 0).
type NetworkLink struct {
	ID              uuid.UUID
	Mode            string
	FromNodeID      uuid.UUID
	ToNodeID        uuid.UUID
	Path            string
	LengthM         int32
	CapacityPerHour int32
	BaseSpeedKmh    int32
	Segments        []LinkSegment
}

// ─── Tipos de dominio de rutas ───────────────────────────────────────────────

// RouteLeg es un tramo de una ruta: un enlace en una posición ordenada.
type RouteLeg struct {
	LegIndex int32
	LinkID   uuid.UUID
}

// Route es una ruta propietaria (schema Route) con sus tramos.
type Route struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	Name           string
	Kind           string
	Active         bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Legs           []RouteLeg
}

// ─── Tipos de dominio del route-plan ─────────────────────────────────────────

// RoutePlanLeg es un tramo del plan sugerido (schema RoutePlanLeg).
// TransshipmentTerminalID solo está presente si el tramo termina en un cambio
// de modo con terminal de transbordo.
type RoutePlanLeg struct {
	Seq                     int
	LinkID                  uuid.UUID
	Mode                    string
	EtaSimSeconds           int64
	TransshipmentTerminalID *uuid.UUID
}

// RoutePlan es el plan sugerido por el asistente de ruta óptima (schema
// RoutePlan). HasCost indica si EstimatedCost es significativo (siempre lo es
// cuando hay ruta; queda por si algún criterio futuro no lo calcula).
type RoutePlan struct {
	OriginNodeID       uuid.UUID
	DestinationNodeID  uuid.UUID
	Legs               []RoutePlanLeg
	TotalEtaSimSeconds int64
	EstimatedCost      int64
	HasCost            bool
}

// ─── Filtros y entradas ──────────────────────────────────────────────────────

// NodeFilter son los filtros de GET /logistics/network/nodes.
type NodeFilter struct {
	RegionID *uuid.UUID
	Kind     string
	Cursor   string
	Limit    int
}

// LinkFilter son los filtros de GET /logistics/network/links.
type LinkFilter struct {
	RegionID   *uuid.UUID
	Mode       string
	FromNodeID *uuid.UUID
	Cursor     string
	Limit      int
}

// RouteFilter son los filtros de GET /logistics/routes.
type RouteFilter struct {
	Kind   string
	Active *bool
	Cursor string
	Limit  int
}

// RouteInput es el cuerpo de POST /logistics/routes (schema RouteCreate).
type RouteInput struct {
	Name string
	Kind string
	Legs []uuid.UUID
}

// RouteUpdate es el cuerpo de PATCH /logistics/routes/{id} (schema RouteUpdate).
// Los campos nil no se tocan; Legs nil conserva la secuencia, Legs no-nil la
// reemplaza (con la misma validación de contigüidad que la creación).
type RouteUpdate struct {
	Name   *string
	Active *bool
	Legs   *[]uuid.UUID
}

// PlanRequest es la petición de pathfinding (schema RoutePlanRequest). Modes
// vacío = todos los modos; Optimize vacío = time; CargoVolume 0 = sin volumen.
type PlanRequest struct {
	Origin      uuid.UUID
	Destination uuid.UUID
	Modes       []string
	Optimize    string
	CargoVolume int64
}
