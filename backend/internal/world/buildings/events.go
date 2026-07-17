package buildings

import (
	"strconv"

	"github.com/google/uuid"
)

// Tipos de agregado y de evento que world/buildings emite por el outbox
// transaccional (SAD/ADR-008), en la misma tx que el cambio de estado.
// building.constructed lo emite el motor al completar la construcción.
const (
	AggregateBuilding = "building"

	EventBuildingCreated     = "building.created"
	EventBuildingUpdated     = "building.updated"
	EventBuildingUpgraded    = "building.upgraded"
	EventBuildingConstructed = "building.constructed"
)

// Payloads JSON de los eventos. Dinero/stock viajan SIEMPRE como string de punto
// fijo, jamás como float; los sim-time como enteros. Los campos opcionales se
// omiten vacíos.

// BuildingCreatedPayload es el payload de building.created.
type BuildingCreatedPayload struct {
	BuildingID     string `json:"building_id"`
	OwnerAccountID string `json:"owner_account_id"`
	RegionID       string `json:"region_id"`
	ConcessionID   string `json:"concession_id"`
	BuildingTypeID string `json:"building_type_id"`
	NodeID         string `json:"node_id"`
	NodeKind       string `json:"node_kind"`
	BuildCost      string `json:"build_cost"`
	CreatedAtSim   int64  `json:"created_at_sim"`
}

// BuildingUpdatedPayload es el payload de building.updated (cambio de receta o
// inicio de mantenimiento).
type BuildingUpdatedPayload struct {
	BuildingID     string `json:"building_id"`
	OwnerAccountID string `json:"owner_account_id"`
	Status         string `json:"status"`
	ActiveRecipeID string `json:"active_recipe_id,omitempty"`
	UpdatedAtSim   int64  `json:"updated_at_sim"`
}

// BuildingUpgradedPayload es el payload de building.upgraded.
type BuildingUpgradedPayload struct {
	BuildingID     string `json:"building_id"`
	OwnerAccountID string `json:"owner_account_id"`
	Level          int32  `json:"level"`
	UpgradeCost    string `json:"upgrade_cost"`
	UpgradedAtSim  int64  `json:"upgraded_at_sim"`
}

// fixed serializa un importe/cantidad de punto fijo como string del contrato.
func fixed(v int64) string { return strconv.FormatInt(v, 10) }

// uuidOrEmpty serializa un uuid opcional ("" si es nil, para omitempty).
func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
