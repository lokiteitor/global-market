package production

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Batch es un lote (orden) de la cola de producción (schema ProductionBatch del
// contrato). batches_queued son los batches de la orden; batches_done los ya
// producidos. progress_pct/eta_sim SOLO están presentes para el lote en curso y
// se DERIVAN analíticamente en la consulta (no persisten).
type Batch struct {
	ID            uuid.UUID
	BuildingID    uuid.UUID
	RecipeID      uuid.UUID
	BatchesQueued int32
	BatchesDone   int32
	Status        string
	QueuePosition int32
	StartedAtSim  *int64
	// Derivados analíticos (nil salvo en el lote running).
	ProgressPct *float64
	EtaSim      *int64
}

// BatchInput es el cuerpo de POST /world/buildings/{id}/production-batches
// (ProductionBatchCreate).
type BatchInput struct {
	RecipeID      uuid.UUID
	BatchesQueued int32
	// QueuePosition es la posición deseada en la cola (nil = al final).
	QueuePosition *int32
}

// BatchFilter son los filtros de GET /world/buildings/{id}/production-batches.
type BatchFilter struct {
	Status string // "" = sin filtro
	Cursor string
	Limit  int
}

// ─── Cursor keyset (id ASC, UUIDv7 ≈ orden de creación) ──────────────────────

type batchCursor struct {
	ID uuid.UUID
}

func encodeCursor(id uuid.UUID) string {
	return keyset.Encode(batchCursor{ID: id})
}

func decodeCursor(raw string) (uuid.UUID, error) {
	c, err := keyset.Decode[batchCursor](raw)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}

// deriveProgress rellena progress_pct y eta_sim de un lote running a partir de
// (started_at_sim, duración efectiva, simNow). Para lotes no-running deja los
// derivados en nil (el contrato los omite). progress_pct se acota a [0, 100].
func deriveProgress(b *Batch, batchSimSeconds int64, levelCurve []byte, level int32, simNow int64) {
	if b.Status != string(statusRunning) || b.StartedAtSim == nil {
		return
	}
	eff := effectiveBatchSeconds(batchSimSeconds, levelCurve, level)
	start := *b.StartedAtSim
	eta := start + eff
	elapsed := simNow - start
	pct := 0.0
	if eff > 0 {
		pct = float64(elapsed) / float64(eff) * 100
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	b.ProgressPct = &pct
	b.EtaSim = &eta
}
