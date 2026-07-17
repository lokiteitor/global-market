package land

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Concession es una concesión de suelo (schema Concession del contrato). El
// dinero es int64 de punto fijo; parcel es GeoJSON plano (string, SRID 0).
type Concession struct {
	ID              uuid.UUID
	RegionID        uuid.UUID
	HolderAccountID uuid.UUID
	Parcel          string // GeoJSON Polygon plano (SRID 0)
	CanonAmount     int64
	PeriodSimDays   int32
	ExpiresAtSim    int64
	Status          string
	GrantedAtSim    int64
}

// ConcessionTransfer es un traspaso ejecutado (schema ConcessionTransfer).
type ConcessionTransfer struct {
	ID            uuid.UUID
	ConcessionID  uuid.UUID
	FromAccountID uuid.UUID
	ToAccountID   uuid.UUID
	Price         int64
	SystemFee     int64
	OccurredAtSim int64
}

// ─── Entradas (cuerpo de las peticiones) ─────────────────────────────────────

// ConcessionInput es el cuerpo de POST /world/concessions (ConcessionCreate).
type ConcessionInput struct {
	RegionID uuid.UUID
	// Parcel es el polígono GeoJSON plano ya validado en forma (type Polygon,
	// anillo cerrado); se proyecta a SRID 0 en la BD.
	Parcel []byte
}

// TransferInput es el cuerpo de POST /world/concession-transfers
// (ConcessionTransferCreate).
type TransferInput struct {
	ConcessionID uuid.UUID
	ToAccountID  uuid.UUID
	Price        int64
}

// ─── Filtros de los listados ─────────────────────────────────────────────────

// ConcessionFilter son los filtros de GET /world/concessions (SOLO propias).
type ConcessionFilter struct {
	Status   string // "" = sin filtro
	RegionID *uuid.UUID
	Cursor   string
	Limit    int
}

// ─── Cursor keyset (id ASC, UUIDv7 ≈ orden de creación) ──────────────────────

// concessionCursor es la clave keyset de una página de concesiones.
type concessionCursor struct {
	ID uuid.UUID
}

// encodeCursor serializa el cursor de la página siguiente.
func encodeCursor(id uuid.UUID) string {
	return keyset.Encode(concessionCursor{ID: id})
}

// decodeCursor valida y deserializa un cursor de concesiones.
func decodeCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[concessionCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}
