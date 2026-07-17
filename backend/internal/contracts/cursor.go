package contracts

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Cursor keyset opaco del tablón (meta.next_cursor del contrato). La clave de
// ordenación depende del sort activo (unit_price, published_at_sim o
// delivery_sim_seconds — todas int64), así que el cursor lleva un
// discriminador del orden: un cursor emitido para un sort no vale para otro
// (Decode lo rechaza con ErrInvalidCursor en lugar de devolver una página con
// orden incoherente).

// boardCursor es la clave keyset de la página del tablón.
type boardCursor struct {
	Sort int64 // discriminador del orden (código de sortCode)
	Key  int64 // valor de la clave de orden de la última fila
	ID   uuid.UUID
}

// sortCode asigna un código estable a cada orden del contrato.
func sortCode(s BoardSort) int64 {
	switch s {
	case SortUnitPriceDesc:
		return 1
	case SortPublishedAtDesc:
		return 2
	case SortDeadlineAsc:
		return 3
	default: // SortUnitPriceAsc
		return 0
	}
}

// boardSortKey devuelve el valor de la clave de orden de una publicación para
// el sort activo.
func boardSortKey(s BoardSort, p Publication) int64 {
	switch s {
	case SortPublishedAtDesc:
		return int64(p.PublishedAtSim)
	case SortDeadlineAsc:
		return int64(p.DeliverySimSeconds)
	default: // unit_price_asc / unit_price_desc
		return p.UnitPrice
	}
}

// encodeBoardCursor serializa el cursor de la página siguiente del tablón.
func encodeBoardCursor(s BoardSort, p Publication) string {
	return keyset.Encode(boardCursor{Sort: sortCode(s), Key: boardSortKey(s, p), ID: p.ID})
}

// decodeBoardCursor valida y deserializa un cursor del tablón para el sort
// solicitado.
func decodeBoardCursor(raw string, s BoardSort) (key int64, id uuid.UUID, err error) {
	c, err := keyset.Decode[boardCursor](raw)
	if err != nil {
		return 0, uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if c.Sort != sortCode(s) {
		return 0, uuid.UUID{}, fmt.Errorf("%w: el cursor pertenece a otro orden", ErrInvalidCursor)
	}
	return c.Key, c.ID, nil
}

// contractCursor es la clave keyset de la página de contratos (orden id DESC:
// los UUIDv7 preservan el orden de creación, primero los más recientes).
type contractCursor struct {
	ID uuid.UUID
}

// encodeContractCursor serializa el cursor de la página siguiente de contratos.
func encodeContractCursor(id uuid.UUID) string {
	return keyset.Encode(contractCursor{ID: id})
}

// decodeContractCursor valida y deserializa un cursor de contratos.
func decodeContractCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[contractCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}
