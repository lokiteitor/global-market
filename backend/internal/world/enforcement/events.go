package enforcement

import "strconv"

// Tipos de agregado y de evento que world/enforcement emite por el outbox
// transaccional (SAD/ADR-008), en la MISMA tx que el cambio de estado.
const (
	AggregateBuilding   = "building"
	AggregateConcession = "concession"

	// EventBuildingSeized lo CONSUME contracts/system_liquidator (publica el
	// stock embargado como ofertas de venta del sistema vía CCRI).
	EventBuildingSeized = "building.seized"
	// EventConcessionReverted es informativo/WS (el suelo volvió al sistema).
	EventConcessionReverted = "concession.reverted"
)

// Motivos del embargo (campo reason de building.seized), derivados del estado
// del edificio en el momento del embargo.
const (
	ReasonAbandoned     = "abandoned"      // el edificio llegó a 'abandoned' por mantenimiento impagado
	ReasonCanonReverted = "canon_reverted" // el suelo revirtió por canon impagado (edificio aún operativo/dañado)
)

// Payloads JSON de los eventos. Dinero/stock viajan SIEMPRE como string de punto
// fijo, jamás como float; los sim-time como enteros; los uuid como string.

// SeizedStockItem es una línea de stock libre embargado (retirada in situ).
type SeizedStockItem struct {
	ProductID           string `json:"product_id"`
	Quantity            string `json:"quantity"`
	WarehouseBuildingID string `json:"warehouse_building_id"`
}

// BuildingSeizedPayload es el payload de building.seized (contrato de evento fijo
// del Incremento 6a). origin_node_id es el nodo logístico del edificio (retirada
// in situ); stock es TODO el stock_free del edificio en el momento del embargo.
type BuildingSeizedPayload struct {
	BuildingID     string            `json:"building_id"`
	OwnerAccountID string            `json:"owner_account_id"`
	RegionID       string            `json:"region_id"`
	OriginNodeID   string            `json:"origin_node_id"`
	Reason         string            `json:"reason"`
	Stock          []SeizedStockItem `json:"stock"`
	SeizedAtSim    int64             `json:"seized_at_sim"`
}

// ConcessionRevertedPayload es el payload de concession.reverted.
type ConcessionRevertedPayload struct {
	ConcessionID  string `json:"concession_id"`
	FormerHolder  string `json:"former_holder"`
	RegionID      string `json:"region_id"`
	RevertedAtSim int64  `json:"reverted_at_sim"`
}

// fixed serializa un importe/stock de punto fijo como string del contrato.
func fixed(v int64) string { return strconv.FormatInt(v, 10) }
