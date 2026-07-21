package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// SQLSTATE y constraints que este subpaquete traduce a errores tipados.
const (
	sqlstateCheckViolation = "23514" // check_violation
	sqlstateFKViolation    = "23503" // foreign_key_violation

	constraintNonNegative = "ck_accounts_non_negative"
)

// SimSource entrega el sim-time actual del mundo. Producción: *clock.Reader (o
// el reloj del engine); los tests inyectan un reloj fijo.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service implementa la cola de producción del contrato (handlers GET/POST/
// DELETE). Encolar y cancelar corren en una única transacción SERIALIZABLE
// (platform/db.RunSerializable) con el evento del outbox en la misma tx.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	queued    prometheus.Counter
	cancelled prometheus.Counter
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("world/production: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("world/production: el SimSource es obligatorio")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		pool:   pool,
		repo:   NewRepo(pool),
		sim:    sim,
		opts:   opts,
		logger: logger,
		queued: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_production_batches_queued_total",
			Help: "Total de órdenes de producción encoladas.",
		}),
		cancelled: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_production_batches_cancelled_total",
			Help: "Total de órdenes de producción canceladas.",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.queued, s.cancelled)
	}
	return s, nil
}

// ─── GET: cola de producción de un edificio ───────────────────────────────────

// ListBatches devuelve los lotes de un edificio propio con progress_pct/eta_sim
// derivados analíticamente para el lote en curso. 404/403 por propiedad.
func (s *Service) ListBatches(ctx context.Context, owner, buildingID uuid.UUID, f BatchFilter) ([]Batch, string, error) {
	if f.Status != "" && !validBatchStatus(f.Status) {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	var afterID *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = &id
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	if _, err := s.getOwnedBuilding(ctx, s.repo, owner, buildingID); err != nil {
		return nil, "", err
	}

	simNow := int64(s.sim.Now(ctx))
	rows, err := s.repo.ListBatches(ctx, buildingID, f.Status, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(rows[len(rows)-1].Batch.ID)
	}
	out := make([]Batch, len(rows))
	for i, row := range rows {
		b := row.Batch
		deriveProgress(&b, row.BatchSimSeconds, row.LevelCurve, row.Level, simNow)
		out[i] = b
	}
	return out, next, nil
}

// ─── POST: encolar lotes ──────────────────────────────────────────────────────

// QueueBatches encola uno o varios lotes de una receta soportada por el tipo del
// edificio y, si no hay lote activo, promueve la cabeza de la cola a running.
func (s *Service) QueueBatches(ctx context.Context, owner, buildingID uuid.UUID, in BatchInput) (Batch, error) {
	if in.BatchesQueued < 1 {
		return Batch{}, fmt.Errorf("%w: batches_queued debe ser >= 1", ErrValidation)
	}
	if in.QueuePosition != nil && *in.QueuePosition < 0 {
		return Batch{}, fmt.Errorf("%w: queue_position debe ser >= 0", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var out Batch
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		b, err := s.getOwnedBuilding(ctx, r, owner, buildingID)
		if err != nil {
			return err
		}
		if b.Status != string(sqlcgen.WorldBuildingStatusOperational) {
			return fmt.Errorf("%w (estado %s)", ErrBuildingNotOperational, b.Status)
		}

		rec, err := r.GetRecipe(ctx, in.RecipeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: la receta %s no existe", ErrValidation, in.RecipeID)
		case err != nil:
			return fmt.Errorf("world/production: consultando la receta %s: %w", in.RecipeID, err)
		}
		if rec.BuildingTypeID != b.BuildingTypeID {
			return fmt.Errorf("%w (%s)", ErrRecipeNotSupported, in.RecipeID)
		}

		pos := int32(0)
		if in.QueuePosition != nil {
			pos = *in.QueuePosition
		} else {
			pos, err = r.NextQueuePosition(ctx, buildingID)
			if err != nil {
				return err
			}
		}

		batchID, err := newUUIDv7()
		if err != nil {
			return err
		}
		inserted, err := r.InsertBatch(ctx, insertBatchParams{
			ID:            batchID,
			BuildingID:    buildingID,
			RecipeID:      in.RecipeID,
			BatchesQueued: in.BatchesQueued,
			QueuePosition: pos,
			UpdatedAtSim:  simNow,
		})
		if err != nil {
			return err
		}
		out = inserted

		// Si el edificio no tiene lote activo, promueve la cabeza de la cola a
		// running (puede ser el recién insertado u otro anterior en cola).
		active, err := r.CountActiveBatches(ctx, buildingID)
		if err != nil {
			return err
		}
		if active == 0 {
			head, err := r.LockNextQueuedHead(ctx, buildingID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				// nada que promover (carrera): se queda encolado
			case err != nil:
				return err
			default:
				promoted, err := r.SetBatchRunning(ctx, head.ID, simNow)
				if err != nil {
					return err
				}
				if promoted.ID == out.ID {
					out = promoted
				}
			}
		}

		return outbox.Emit(ctx, tx, int64(simNow), AggregateBatch, out.ID, EventBatchQueued, BatchQueuedPayload{
			BatchID:       out.ID.String(),
			BuildingID:    buildingID.String(),
			RecipeID:      in.RecipeID.String(),
			BatchesQueued: out.BatchesQueued,
			QueuePosition: out.QueuePosition,
			Status:        out.Status,
			QueuedAtSim:   int64(simNow),
		})
	})
	if err != nil {
		return Batch{}, mapLedgerError(err)
	}
	s.queued.Inc()
	s.logger.Info("lote de producción encolado",
		slog.String("batch_id", out.ID.String()),
		slog.String("building_id", buildingID.String()),
		slog.String("recipe_id", in.RecipeID.String()),
		slog.Int("batches_queued", int(out.BatchesQueued)),
		slog.String("status", out.Status))
	return out, nil
}

// ─── DELETE: cancelar un lote ─────────────────────────────────────────────────

// CancelBatch cancela lo NO producido de un lote (409 si ya está completed/
// cancelled) y promueve la cola si el lote cancelado estaba activo.
func (s *Service) CancelBatch(ctx context.Context, owner, batchID uuid.UUID) (Batch, error) {
	simNow := s.sim.Now(ctx)

	var out Batch
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		row, err := r.LockBatchForCancel(ctx, batchID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrBatchNotFound, batchID)
		case err != nil:
			return fmt.Errorf("world/production: bloqueando el lote %s: %w", batchID, err)
		}
		if row.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrForbidden, batchID)
		}
		if row.Batch.Status == string(statusCompleted) || row.Batch.Status == string(statusCancelled) {
			return fmt.Errorf("%w (estado %s)", ErrBatchNotCancellable, row.Batch.Status)
		}
		wasActive := row.Batch.Status == string(statusRunning) ||
			row.Batch.Status == string(statusPausedNoFuel) ||
			row.Batch.Status == string(statusPausedNoWorkers) ||
			row.Batch.Status == string(statusPausedNoPower)

		out, err = r.SetBatchCancelled(ctx, batchID, simNow)
		if err != nil {
			return err
		}

		// La cola avanza: si el lote cancelado ocupaba el hueco activo, promueve
		// la siguiente cabeza queued a running.
		if wasActive {
			head, err := r.LockNextQueuedHead(ctx, row.Batch.BuildingID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				// no hay siguiente
			case err != nil:
				return err
			default:
				if _, err := r.SetBatchRunning(ctx, head.ID, simNow); err != nil {
					return err
				}
			}
		}

		return outbox.Emit(ctx, tx, int64(simNow), AggregateBatch, out.ID, EventBatchCancelled, BatchCancelledPayload{
			BatchID:        out.ID.String(),
			BuildingID:     out.BuildingID.String(),
			BatchesDone:    out.BatchesDone,
			BatchesQueued:  out.BatchesQueued,
			CancelledAtSim: int64(simNow),
		})
	})
	if err != nil {
		return Batch{}, mapLedgerError(err)
	}
	s.cancelled.Inc()
	s.logger.Info("lote de producción cancelado",
		slog.String("batch_id", out.ID.String()),
		slog.String("building_id", out.BuildingID.String()),
		slog.Int("batches_done", int(out.BatchesDone)),
		slog.Int("batches_queued", int(out.BatchesQueued)))
	return out, nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// getOwnedBuilding localiza un edificio y verifica la propiedad (404/403).
func (s *Service) getOwnedBuilding(ctx context.Context, r *Repo, owner, id uuid.UUID) (buildingHead, error) {
	b, err := r.GetBuilding(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return buildingHead{}, fmt.Errorf("%w (%s)", ErrBuildingNotFound, id)
	case err != nil:
		return buildingHead{}, fmt.Errorf("world/production: consultando el edificio %s: %w", id, err)
	}
	if b.OwnerAccountID != owner {
		return buildingHead{}, fmt.Errorf("%w (%s)", ErrForbidden, id)
	}
	return b, nil
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case int32(limit) > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

// validBatchStatus indica si s es un estado de lote válido del enum.
func validBatchStatus(s string) bool {
	switch sqlcgen.WorldBatchStatus(s) {
	case statusQueued, statusRunning, statusPausedNoFuel, statusPausedNoWorkers, statusPausedNoPower,
		statusCompleted, statusCancelled:
		return true
	}
	return false
}

// mapLedgerError traduce violaciones de invariantes de la BD (carreras resueltas
// por constraint) a errores tipados del subpaquete.
func mapLedgerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == sqlstateFKViolation:
			return fmt.Errorf("%w: referencia inexistente (%s)", ErrValidation, pgErr.ConstraintName)
		// No-negatividad de las cuentas: el motor la trata como estancamiento
		// esperado del lote, no como un fallo con SQLSTATE crudo.
		case pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative:
			return fmt.Errorf("%w: %s", ErrInsufficientBalance, pgErr.Message)
		}
	}
	return err
}
