package power

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// PowerLine es una línea de transmisión del dominio.
type PowerLine struct {
	ID                      uuid.UUID
	OwnerAccountID          uuid.UUID
	RegionID                uuid.UUID
	PathGeoJSON             string
	LengthM                 int32
	Status                  string
	ConditionPct            int32
	MaintenancePaidUntilSim int64
	UpdatedAtSim            int64
}

// SpotTick es el resultado agregado de un tick del spot en una región.
type SpotTick struct {
	RegionID           uuid.UUID
	TickSim            int64
	IntervalSim        int64
	ClosingPrice       int64
	DemandUnits        int64
	SuppliedUnits      int64
	CurtailedUnits     int64
	CurtailedBuildings int32
}

// Dispatch es el despacho (generator) o consumo (consumer) de un edificio en
// un tick, al precio de cierre.
type Dispatch struct {
	RegionID       uuid.UUID
	TickSim        int64
	BuildingID     uuid.UUID
	OwnerAccountID uuid.UUID
	Role           string
	Units          int64
	UnitPrice      int64
	Amount         int64
}

// LineFilter son los filtros de GET /world/power-lines (catálogo público).
type LineFilter struct {
	RegionID *uuid.UUID
	Cursor   string
	Limit    int
}

// ─── Cursor keyset (id ASC, UUIDv7 ≈ orden de creación) ──────────────────────

type lineCursor struct {
	ID uuid.UUID
}

func encodeCursor(id uuid.UUID) string {
	return keyset.Encode(lineCursor{ID: id})
}

func decodeCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[lineCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}

// normalizeLimit acota el tamaño de página al contrato (default 50, máx 200).
func normalizeLimit(n int) int32 {
	if n <= 0 {
		return DefaultPageLimit
	}
	if int32(n) > MaxPageLimit {
		return MaxPageLimit
	}
	return int32(n)
}
