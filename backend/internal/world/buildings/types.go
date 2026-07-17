package buildings

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Building es una instalación de una corporación (schema Building del contrato).
// footprint es GeoJSON plano (string, SRID 0); dinero/stock son int64.
type Building struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	RegionID       uuid.UUID
	ConcessionID   uuid.UUID
	BuildingTypeID uuid.UUID
	Footprint      string // GeoJSON Polygon plano (SRID 0)
	Level          int32
	Status         string
	ActiveRecipeID *uuid.UUID
	ConditionPct   int32
	FuelStock      int64
	CreatedAt      time.Time
	UpdatedAtSim   int64
}

// InventoryItem es una fila del inventario físico (schema InventoryItem).
type InventoryItem struct {
	BuildingID   uuid.UUID
	ProductID    uuid.UUID
	Quantity     int64
	UpdatedAtSim int64
}

// ─── Entradas (cuerpo de las peticiones) ─────────────────────────────────────

// BuildingInput es el cuerpo de POST /world/buildings (BuildingCreate).
type BuildingInput struct {
	BuildingTypeID uuid.UUID
	ConcessionID   uuid.UUID
	// Footprint es el polígono GeoJSON plano ya validado en forma; se proyecta a
	// SRID 0 en la BD.
	Footprint []byte
}

// BuildingUpdateInput es el cuerpo de PATCH /world/buildings/{id}
// (BuildingUpdate). Distingue "campo ausente" de "receta a null" (detener línea).
type BuildingUpdateInput struct {
	// SetRecipe indica si el campo active_recipe_id venía presente en el cuerpo.
	SetRecipe bool
	// RecipeID es la receta a activar (nil con SetRecipe=true detiene la línea).
	RecipeID *uuid.UUID
	// StartMaintenance solicita pasar el edificio a in_maintenance.
	StartMaintenance bool
}

// ─── Filtros ─────────────────────────────────────────────────────────────────

// BuildingFilter son los filtros de GET /world/buildings (SOLO propios).
type BuildingFilter struct {
	RegionID       *uuid.UUID
	Status         string // "" = sin filtro
	BuildingTypeID *uuid.UUID
	Cursor         string
	Limit          int
}

// ─── Cursor keyset (id ASC, UUIDv7 ≈ orden de creación) ──────────────────────

type buildingCursor struct {
	ID uuid.UUID
}

func encodeCursor(id uuid.UUID) string {
	return keyset.Encode(buildingCursor{ID: id})
}

func decodeCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[buildingCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}
