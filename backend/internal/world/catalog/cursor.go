package catalog

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Cursor keyset opaco (meta.next_cursor del contrato) de los listados de
// catálogo. Todos ordenan ASC por id (UUIDv7 ≈ orden de creación), así que la
// clave es el id de la última fila. El cursor lleva además un discriminador de
// entidad: un cursor emitido por un listado no vale para otro (Decode lo
// rechaza con ErrInvalidCursor en lugar de paginar una entidad con la clave de
// otra).

// cursorKind discrimina la entidad de un cursor de catálogo.
type cursorKind int64

const (
	cursorRegion       cursorKind = 1
	cursorProduct      cursorKind = 2
	cursorBuildingType cursorKind = 3
	cursorRecipe       cursorKind = 4
	cursorDeposit      cursorKind = 5
	cursorCity         cursorKind = 6
)

// catalogCursor es la clave keyset de una página de catálogo.
type catalogCursor struct {
	Kind int64 // discriminador de entidad (cursorKind)
	ID   uuid.UUID
}

// encodeCursor serializa el cursor de la página siguiente de una entidad.
func encodeCursor(kind cursorKind, id uuid.UUID) string {
	return keyset.Encode(catalogCursor{Kind: int64(kind), ID: id})
}

// decodeCursor valida y deserializa un cursor de catálogo para la entidad
// esperada; rechaza los de otra entidad con ErrInvalidCursor.
func decodeCursor(raw string, kind cursorKind) (uuid.UUID, error) {
	c, err := keyset.Decode[catalogCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if c.Kind != int64(kind) {
		return uuid.UUID{}, fmt.Errorf("%w: el cursor pertenece a otro listado", ErrInvalidCursor)
	}
	return c.ID, nil
}
