package logistics

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Cursor keyset opaco (meta.next_cursor del contrato) de los listados de
// logistics. Todos ordenan ASC por id (UUIDv7 ≈ orden de creación), así que la
// clave es el id de la última fila. El cursor lleva un discriminador de entidad:
// un cursor emitido por un listado no vale para otro (Decode lo rechaza con
// ErrInvalidCursor en lugar de paginar una entidad con la clave de otra).

type cursorKind int64

const (
	cursorNode  cursorKind = 1
	cursorLink  cursorKind = 2
	cursorRoute cursorKind = 3
)

// logisticsCursor es la clave keyset de una página de logistics.
type logisticsCursor struct {
	Kind int64 // discriminador de entidad (cursorKind)
	ID   uuid.UUID
}

// encodeCursor serializa el cursor de la página siguiente de una entidad.
func encodeCursor(kind cursorKind, id uuid.UUID) string {
	return keyset.Encode(logisticsCursor{Kind: int64(kind), ID: id})
}

// decodeCursor valida y deserializa un cursor para la entidad esperada; rechaza
// los de otra entidad con ErrInvalidCursor.
func decodeCursor(raw string, kind cursorKind) (uuid.UUID, error) {
	c, err := keyset.Decode[logisticsCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if c.Kind != int64(kind) {
		return uuid.UUID{}, fmt.Errorf("%w: el cursor pertenece a otro listado", ErrInvalidCursor)
	}
	return c.ID, nil
}

// decodeAfter interpreta el cursor de una entidad (vacío = primera página).
func decodeAfter(cursor string, kind cursorKind) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	id, err := decodeCursor(cursor, kind)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
