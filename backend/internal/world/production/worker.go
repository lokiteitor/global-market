package production

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Etiquetas de la métrica de duración por barrido.
const (
	sweepConstruction = "construction"
	sweepProduction   = "production"
	sweepReconcile    = "reconcile"
)

// reconcileScanLimit acota cuántas divergencias físico↔contable devuelve la
// reconciliación por pasada (a la escala de Fases 0-2, muy por encima del
// universo de inventarios).
const reconcileScanLimit int32 = 10000

// Worker es el MOTOR event-driven del Incremento 2 (para el engine): completa la
// construcción diferida, barre analíticamente los lotes de producción vencidos
// y reconcilia el plano físico contra el contable. Cada edificio/lote se procesa
// en SU PROPIA transacción SERIALIZABLE, bloqueado con FOR UPDATE SKIP LOCKED,
// de modo que varias instancias pueden correr en paralelo sin pisarse.
type Worker struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   WorkerOptions
	logger *slog.Logger

	constructed       prometheus.Counter
	batchesCompleted  prometheus.Counter
	paused            *prometheus.CounterVec
	storageFull       prometheus.Counter
	output            *prometheus.CounterVec
	extracted         *prometheus.CounterVec
	sweepDuration     *prometheus.HistogramVec
	reconciliationGap prometheus.Gauge
}

// NewWorker construye el motor sobre el pool compartido. reg registra sus
// métricas (nil las deja sin instrumentar: tests); logger nil usa slog.Default.
func NewWorker(pool *pgxpool.Pool, sim SimSource, opts WorkerOptions, logger *slog.Logger, reg prometheus.Registerer) (*Worker, error) {
	if pool == nil {
		return nil, errors.New("world/production: el worker requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("world/production: el worker requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	w := &Worker{
		pool:   pool,
		repo:   NewRepo(pool),
		sim:    sim,
		opts:   opts,
		logger: logger,
		constructed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_constructed_total",
			Help: "Total de edificios que completaron su construcción diferida.",
		}),
		batchesCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_production_batches_completed_total",
			Help: "Total de batches de producción completados (por ciclo).",
		}),
		paused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_production_paused_total",
			Help: "Total de paradas de lote por motivo (no_fuel, no_workers, no_inputs, no_deposit).",
		}, []string{"reason"}),
		storageFull: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_production_storage_full_total",
			Help: "Total de ciclos no progresados por almacén lleno (el lote permanece running).",
		}),
		output: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_production_output_total",
			Help: "Total de unidades producidas por producto.",
		}, []string{"product"}),
		extracted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_resource_extracted_total",
			Help: "Total de unidades extraídas de yacimientos por producto.",
		}, []string{"product"}),
		sweepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ii_production_sweep_duration_seconds",
			Help:    "Duración de cada barrido del motor de producción, por tipo.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sweep"}),
		reconciliationGap: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_reconciliation_discrepancies",
			Help: "Divergencias físico↔contable detectadas en la última reconciliación (esperado 0).",
		}),
	}
	if reg != nil {
		reg.MustRegister(w.constructed, w.batchesCompleted, w.paused, w.storageFull,
			w.output, w.extracted, w.sweepDuration, w.reconciliationGap)
	}
	return w, nil
}

// Run ejecuta el bucle del motor hasta que ctx se cancele (nil al apagado
// limpio). Cada iteración barre construcción y producción; la reconciliación se
// dispara cuando su intervalo propio venció.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("world/production: motor iniciado",
		slog.Duration("sweep_interval", w.opts.SweepInterval),
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Int64("build_sim_seconds", w.opts.BuildSimSeconds),
		slog.Duration("reconcile_interval", w.opts.ReconcileInterval))
	lastReconcile := time.Now()
	for {
		w.RunOnce(ctx)
		if time.Since(lastReconcile) >= w.opts.ReconcileInterval {
			w.runSweep(ctx, sweepReconcile, func(ctx context.Context) (int, error) { return w.Reconcile(ctx) })
			lastReconcile = time.Now()
		}
		if !sleepJitter(ctx, w.opts.SweepInterval) {
			w.logger.Info("world/production: motor detenido")
			return nil
		}
	}
}

// RunOnce ejecuta una pasada de los barridos de construcción y producción.
// Aislado para los tests, que controlan el disparo.
func (w *Worker) RunOnce(ctx context.Context) {
	w.runSweep(ctx, sweepConstruction, w.sweepConstruction)
	w.runSweep(ctx, sweepProduction, w.sweepProduction)
}

// runSweep cronometra un barrido y registra su duración y un error global.
func (w *Worker) runSweep(ctx context.Context, name string, fn func(context.Context) (int, error)) {
	start := time.Now()
	n, err := fn(ctx)
	w.sweepDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	if err != nil {
		w.logger.Warn("world/production: barrido con error al listar candidatos",
			slog.String("sweep", name), slog.Any("error", err))
		return
	}
	if n > 0 {
		w.logger.Debug("world/production: barrido completado",
			slog.String("sweep", name), slog.Int("procesados", n))
	}
}

// ─── (1) Barrido de construcción ──────────────────────────────────────────────

// sweepConstruction completa la construcción de los edificios vencidos.
func (w *Worker) sweepConstruction(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	ids, err := w.repo.ListDueConstructionIDs(ctx, w.opts.BuildSimSeconds, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		done, err := w.completeConstruction(ctx, id, simNow)
		if err != nil {
			w.logger.Warn("world/production: fallo completando una construcción",
				slog.String("building_id", id.String()), slog.Any("error", err))
			continue
		}
		if done {
			processed++
			w.constructed.Inc()
		}
	}
	return processed, nil
}

// completeConstruction pasa un edificio a operational en su propia transacción y
// emite building.constructed.
func (w *Worker) completeConstruction(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (bool, error) {
	done := false
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		done = false
		r := w.repo.WithTx(tx)
		if _, err := r.LockDueConstruction(ctx, id, w.opts.BuildSimSeconds, simNow); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // ya completado o tomado por otra instancia
			}
			return err
		}
		c, err := r.CompleteConstruction(ctx, id, simNow)
		if err != nil {
			return err
		}
		done = true
		return outbox.Emit(ctx, tx, int64(simNow), AggregateBuilding, c.ID, EventBuildingConstructed, BuildingConstructedPayload{
			BuildingID:       c.ID.String(),
			OwnerAccountID:   c.OwnerAccountID.String(),
			RegionID:         c.RegionID.String(),
			BuildingTypeID:   c.BuildingTypeID.String(),
			ConstructedAtSim: int64(simNow),
		})
	})
	return done, err
}

// ─── (2) Barrido de producción ────────────────────────────────────────────────

// batchOutcome acumula los efectos de procesar un lote para volcarlos a las
// métricas UNA sola vez tras el COMMIT (un reintento de serialización re-ejecuta
// el cuerpo; solo el commit definitivo cuenta).
type batchOutcome struct {
	completed bool
	paused    string           // motivo si el lote pasó a paused_* (enum)
	stalled   string           // motivo si el lote no avanzó (running, se reintenta)
	output    map[string]int64 // producto → unidades producidas
	extracted map[string]int64 // producto → unidades extraídas
}

func newBatchOutcome() *batchOutcome {
	return &batchOutcome{output: map[string]int64{}, extracted: map[string]int64{}}
}

// sweepProduction procesa los lotes activos (running/pausados) vencidos.
func (w *Worker) sweepProduction(ctx context.Context) (int, error) {
	ids, err := w.repo.ListActiveBatchIDs(ctx, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		if err := w.processBatch(ctx, id); err != nil {
			w.logger.Warn("world/production: fallo procesando un lote",
				slog.String("batch_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

// processBatch bloquea y procesa un lote en su propia transacción: completa un
// batch vencido, lo pausa, o (si estaba pausado) intenta reanudarlo.
func (w *Worker) processBatch(ctx context.Context, id uuid.UUID) error {
	var oc *batchOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = newBatchOutcome()
		r := w.repo.WithTx(tx)
		pb, err := r.LockBatchForProcessing(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // tomado por otra instancia o ya no aplica
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		switch pb.Batch.Status {
		case string(statusRunning):
			return w.runningBatch(ctx, r, tx, pb, simNow, oc)
		case string(statusPausedNoFuel):
			return w.tryResume(ctx, r, pb, simNow, reasonNoFuel)
		case string(statusPausedNoWorkers):
			return w.tryResume(ctx, r, pb, simNow, reasonNoWorkers)
		default:
			return nil
		}
	})
	if err == nil && oc != nil {
		w.flush(oc)
	}
	return err
}

// flush vuelca los efectos acumulados a las métricas.
func (w *Worker) flush(oc *batchOutcome) {
	if oc.completed {
		w.batchesCompleted.Inc()
	}
	if oc.paused != "" {
		w.paused.WithLabelValues(oc.paused).Inc()
	}
	switch {
	case oc.stalled == reasonStorageFull:
		w.storageFull.Inc()
	case oc.stalled != "":
		w.paused.WithLabelValues(oc.stalled).Inc()
	}
	for product, qty := range oc.output {
		w.output.WithLabelValues(product).Add(float64(qty))
	}
	for product, qty := range oc.extracted {
		w.extracted.WithLabelValues(product).Add(float64(qty))
	}
}

// runningBatch completa un lote running SI su batch en curso venció (progreso
// analítico: started_at_sim + duración efectiva <= simNow).
func (w *Worker) runningBatch(ctx context.Context, r *Repo, tx pgx.Tx, pb procBatch, simNow simtime.SimTime, oc *batchOutcome) error {
	if pb.Batch.StartedAtSim == nil {
		return nil
	}
	eff := effectiveBatchSeconds(pb.BatchSimSeconds, pb.LevelCurve, pb.Level)
	if *pb.Batch.StartedAtSim+eff > int64(simNow) {
		return nil // aún no vence
	}
	return w.completeBatch(ctx, r, tx, pb, simNow, oc)
}

// completeBatch cierra un batch vencido en la misma transacción: valida
// combustible, salario, insumos/yacimiento y capacidad de almacén (fase de
// comprobación); si todo se cumple, consume combustible/insumos, extrae o
// produce (plano físico + contable juntos, GDD 15.3), cobra el salario al sink y
// avanza la cola. Cualquier bloqueo económico pausa el lote (paused_*); una
// carencia de material o de almacén deja el lote running sin avanzar (se
// reintenta).
func (w *Worker) completeBatch(ctx context.Context, r *Repo, tx pgx.Tx, pb procBatch, simNow simtime.SimTime, oc *batchOutcome) error {
	owner := pb.OwnerAccountID
	building := pb.Batch.BuildingID

	ings, err := r.ListRecipeIngredients(ctx, pb.Batch.RecipeID)
	if err != nil {
		return err
	}
	var inputs, outputs []ingredient
	for _, ing := range ings {
		if ing.Role == string(sqlcgen.WorldIngredientRoleInput) {
			inputs = append(inputs, ing)
		} else {
			outputs = append(outputs, ing)
		}
	}
	mine := isMine(pb.BuildingTypeCode, pb.PlacementRules)

	// ── Fase de comprobación (solo lecturas/locks; sin mutaciones) ──

	// (1) Combustible físico (GDD 5.8).
	hasFuel := pb.FuelProductID != nil && pb.FuelPerBatch > 0
	if hasFuel {
		avail, err := r.GetInventoryQty(ctx, building, *pb.FuelProductID)
		if err != nil {
			return err
		}
		if avail < pb.FuelPerBatch {
			return w.pause(ctx, r, tx, pb, statusPausedNoFuel, reasonNoFuel, simNow, oc)
		}
	}

	// (2) Fondos para el salario (cash del dueño). Sin fondos → insolvencia sin
	//     deuda (GDD 5.9): paused_no_workers.
	wage, err := w.computeWage(ctx, r, pb)
	if err != nil {
		return err
	}
	if wage > 0 {
		cash, err := r.GetCashAccount(ctx, owner)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return w.pause(ctx, r, tx, pb, statusPausedNoWorkers, reasonNoWorkers, simNow, oc)
			}
			return err
		}
		if cash.Balance < wage {
			return w.pause(ctx, r, tx, pb, statusPausedNoWorkers, reasonNoWorkers, simNow, oc)
		}
	}

	// (3) Insumos (manufactura) o yacimiento (extracción).
	depositByOutput := map[uuid.UUID]uuid.UUID{}
	if mine {
		radius := extractionRadiusM(pb.PlacementRules)
		for _, o := range outputs {
			depositID, remaining, err := r.LockNearestDeposit(ctx, building, o.ProductID, radius)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					oc.stalled = reasonNoDeposit
					return nil // running, se reintenta cuando haya recurso
				}
				return err
			}
			if remaining < o.Quantity {
				oc.stalled = reasonNoDeposit
				return nil
			}
			depositByOutput[o.ProductID] = depositID
		}
	} else {
		for _, in := range inputs {
			avail, err := r.GetInventoryQty(ctx, building, in.ProductID)
			if err != nil {
				return err
			}
			if avail < in.Quantity {
				oc.stalled = reasonNoInputs
				return nil
			}
		}
	}

	// (4) Capacidad de almacén (storage del tipo × nivel). Si el resultado neto
	//     no cabe, el lote no avanza (running) y se registra el evento de almacén
	//     lleno (sin inventar estados nuevos del enum, ADR-020).
	currentSum, err := r.SumInventory(ctx, building)
	if err != nil {
		return err
	}
	var totalOut, consumedPhysical int64
	for _, o := range outputs {
		totalOut += o.Quantity
	}
	if hasFuel {
		consumedPhysical += pb.FuelPerBatch
	}
	if !mine {
		for _, in := range inputs {
			consumedPhysical += in.Quantity
		}
	}
	capacity := storageCapacity(pb.BaseStorage, pb.LevelCurve, pb.Level)
	if currentSum-consumedPhysical+totalOut > capacity {
		oc.stalled = reasonStorageFull
		return nil
	}

	// ── Fase de mutación (físico + contable en la MISMA tx, GDD 15.3) ──

	// Combustible: -físico y consumo contable (+world_source / -stock_free);
	// fuel_stock queda como columna espejo del inventario físico del combustible.
	if hasFuel {
		fuel := *pb.FuelProductID
		if err := r.ConsumeInventory(ctx, building, fuel, pb.FuelPerBatch, simNow); err != nil {
			return err
		}
		if err := w.postConsumption(ctx, r, owner, building, fuel, pb.FuelPerBatch, simNow); err != nil {
			return err
		}
		newFuel, err := r.GetInventoryQty(ctx, building, fuel)
		if err != nil {
			return err
		}
		if err := r.SetFuelStock(ctx, building, newFuel, simNow); err != nil {
			return err
		}
	}

	// Insumos de manufactura: -físico y consumo contable.
	if !mine {
		for _, in := range inputs {
			if err := r.ConsumeInventory(ctx, building, in.ProductID, in.Quantity, simNow); err != nil {
				return err
			}
			if err := w.postConsumption(ctx, r, owner, building, in.ProductID, in.Quantity, simNow); err != nil {
				return err
			}
		}
	}

	// Salidas: extracción (decrementa yacimiento) o manufactura; +físico y alta
	// contable (production_output: +stock_free / -world_source, ADR-022).
	for _, o := range outputs {
		if mine {
			if _, err := r.DecrementDeposit(ctx, depositByOutput[o.ProductID], o.Quantity, simNow); err != nil {
				return err
			}
			oc.extracted[o.ProductID.String()] += o.Quantity
		}
		if err := r.AddInventory(ctx, building, o.ProductID, o.Quantity, simNow); err != nil {
			return err
		}
		if err := w.postProduction(ctx, r, owner, building, o.ProductID, o.Quantity, simNow); err != nil {
			return err
		}
		oc.output[o.ProductID.String()] += o.Quantity
	}

	// Salario: SINK (transacción wage), destrucción de valor (GDD 5.5/5.7).
	if wage > 0 {
		cash, err := r.GetCashAccount(ctx, owner)
		if err != nil {
			return err
		}
		sink, err := r.GetSinkAccount(ctx)
		if err != nil {
			return fmt.Errorf("world/production: localizando la cuenta sink: %w", err)
		}
		if err := r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindWage, simNow, building,
			fmt.Sprintf("Salario de producción (%d)", wage), []entryAmount{
				{AccountID: cash.ID, Amount: -wage},
				{AccountID: sink.ID, Amount: wage},
			}); err != nil {
			return err
		}
	}

	// Avance de la cola: cierra el batch; si era el último, el lote pasa a
	// completed y se promueve la siguiente cabeza de cola a running.
	done := pb.Batch.BatchesDone + 1
	var finalStatus sqlcgen.WorldBatchStatus
	if done >= pb.Batch.BatchesQueued {
		finalStatus = statusCompleted
		if _, err := r.AdvanceBatch(ctx, pb.Batch.ID, statusCompleted, nil, simNow); err != nil {
			return err
		}
		if err := w.promoteNext(ctx, r, building, simNow); err != nil {
			return err
		}
	} else {
		finalStatus = statusRunning
		start := int64(simNow)
		if _, err := r.AdvanceBatch(ctx, pb.Batch.ID, statusRunning, &start, simNow); err != nil {
			return err
		}
	}
	oc.completed = true

	return outbox.Emit(ctx, tx, int64(simNow), AggregateBatch, pb.Batch.ID, EventBatchCompleted, BatchCompletedPayload{
		BatchID:        pb.Batch.ID.String(),
		BuildingID:     building.String(),
		RecipeID:       pb.Batch.RecipeID.String(),
		BatchesDone:    done,
		BatchesQueued:  pb.Batch.BatchesQueued,
		Status:         string(finalStatus),
		CompletedAtSim: int64(simNow),
	})
}

// pause lleva el lote a un estado de pausa del enum (paused_no_fuel/
// paused_no_workers) y emite batch.paused. No produce ni cobra.
func (w *Worker) pause(ctx context.Context, r *Repo, tx pgx.Tx, pb procBatch, status sqlcgen.WorldBatchStatus, reason string, simNow simtime.SimTime, oc *batchOutcome) error {
	if _, err := r.PauseBatch(ctx, pb.Batch.ID, status, simNow); err != nil {
		return err
	}
	oc.paused = reason
	return outbox.Emit(ctx, tx, int64(simNow), AggregateBatch, pb.Batch.ID, EventBatchPaused, BatchPausedPayload{
		BatchID:     pb.Batch.ID.String(),
		BuildingID:  pb.Batch.BuildingID.String(),
		RecipeID:    pb.Batch.RecipeID.String(),
		Reason:      reason,
		Status:      string(status),
		PausedAtSim: int64(simNow),
	})
}

// tryResume reintenta un lote pausado: si el bloqueo (combustible o salario) se
// resolvió, vuelve a running arrancando de nuevo el reloj del lote.
func (w *Worker) tryResume(ctx context.Context, r *Repo, pb procBatch, simNow simtime.SimTime, blocker string) error {
	ok := false
	switch blocker {
	case reasonNoFuel:
		if pb.FuelProductID == nil || pb.FuelPerBatch <= 0 {
			ok = true
		} else {
			avail, err := r.GetInventoryQty(ctx, pb.Batch.BuildingID, *pb.FuelProductID)
			if err != nil {
				return err
			}
			ok = avail >= pb.FuelPerBatch
		}
	case reasonNoWorkers:
		wage, err := w.computeWage(ctx, r, pb)
		if err != nil {
			return err
		}
		if wage <= 0 {
			ok = true
		} else {
			cash, err := r.GetCashAccount(ctx, pb.OwnerAccountID)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				ok = false
			case err != nil:
				return err
			default:
				ok = cash.Balance >= wage
			}
		}
	}
	if !ok {
		return nil
	}
	if _, err := r.SetBatchRunning(ctx, pb.Batch.ID, simNow); err != nil {
		return err
	}
	w.logger.Debug("world/production: lote reanudado",
		slog.String("batch_id", pb.Batch.ID.String()), slog.String("blocker", blocker))
	return nil
}

// promoteNext promueve a running la siguiente cabeza queued del edificio (avance
// de cola tras completar un lote).
func (w *Worker) promoteNext(ctx context.Context, r *Repo, building uuid.UUID, simNow simtime.SimTime) error {
	head, err := r.LockNextQueuedHead(ctx, building)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil
	case err != nil:
		return err
	}
	_, err = r.SetBatchRunning(ctx, head.ID, simNow)
	return err
}

// postConsumption asienta el consumo de stock (ADR-022, transacción
// consumption): +qty world_source(producto) / -qty stock_free(dueño, producto,
// almacén). El stock "vuelve al mundo".
func (w *Worker) postConsumption(ctx context.Context, r *Repo, owner, building, product uuid.UUID, qty int64, simNow simtime.SimTime) error {
	worldSource, err := r.EnsureWorldSourceAccount(ctx, product)
	if err != nil {
		return err
	}
	stockFree, err := r.EnsureStockFreeAccount(ctx, owner, product, building)
	if err != nil {
		return err
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindConsumption, simNow, building,
		"Consumo de producción", []entryAmount{
			{AccountID: worldSource, Amount: qty},
			{AccountID: stockFree.ID, Amount: -qty},
		})
}

// postProduction asienta el alta de stock producido/extraído (ADR-022,
// transacción production_output): +qty stock_free(dueño, producto, almacén) /
// -qty world_source(producto).
func (w *Worker) postProduction(ctx context.Context, r *Repo, owner, building, product uuid.UUID, qty int64, simNow simtime.SimTime) error {
	worldSource, err := r.EnsureWorldSourceAccount(ctx, product)
	if err != nil {
		return err
	}
	stockFree, err := r.EnsureStockFreeAccount(ctx, owner, product, building)
	if err != nil {
		return err
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindProductionOutput, simNow, building,
		"Alta de stock producido", []entryAmount{
			{AccountID: stockFree.ID, Amount: qty},
			{AccountID: worldSource, Amount: -qty},
		})
}

// computeWage calcula el salario del lote: workers_required × salario_base(ciudad
// más cercana) × factor_saturación(región) (GDD 5.7). Sin trabajadores o sin
// ciudad cercana el salario es 0. El factor de saturación por defecto es 1.0.
func (w *Worker) computeWage(ctx context.Context, r *Repo, pb procBatch) (int64, error) {
	if pb.WorkersRequired <= 0 {
		return 0, nil
	}
	base, err := r.NearestCityBaseSalary(ctx, pb.RegionID, pb.Batch.BuildingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil // sin ciudad cercana: no hay salario que cobrar (documentado)
	case err != nil:
		return 0, err
	}
	saturation := 1.0
	sat, err := r.RegionSaturation(ctx, pb.RegionID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// sin estadística regional: factor por defecto 1.0
	case err != nil:
		return 0, err
	default:
		if sat > 0 {
			saturation = sat
		}
	}
	// workers × base en int64 con guarda de overflow (math/big); × saturación en
	// punto flotante (redondeo al entero más cercano).
	workersBase := new(big.Int).Mul(big.NewInt(int64(pb.WorkersRequired)), big.NewInt(base))
	if !workersBase.IsInt64() {
		return 0, fmt.Errorf("%w: el salario base desborda int64", ErrValidation)
	}
	wage := int64(math.Round(float64(workersBase.Int64()) * saturation))
	if wage < 0 {
		wage = 0
	}
	return wage, nil
}

// ─── (3) Reconciliación física↔contable (ADR-004) ─────────────────────────────

// Reconcile compara el inventario físico contra la suma de stock_free por
// (almacén, producto) y publica el número de divergencias (gauge, esperado 0).
// Endpoint interno: no forma parte del contrato.
func (w *Worker) Reconcile(ctx context.Context) (int, error) {
	disc, err := w.repo.ListStockDiscrepancies(ctx, reconcileScanLimit)
	if err != nil {
		return 0, err
	}
	w.reconciliationGap.Set(float64(len(disc)))
	for _, d := range disc {
		w.logger.Error("world/production: divergencia de reconciliación física↔contable",
			slog.String("building_id", d.BuildingID.String()),
			slog.String("product_id", d.ProductID.String()),
			slog.Int64("physical", d.Physical),
			slog.Int64("ledger", d.Ledger))
	}
	return len(disc), nil
}

// ─── Utilidades ───────────────────────────────────────────────────────────────

// sleepJitter espera d ± hasta 25% de jitter y devuelve false si el contexto se
// cancela antes (desincroniza instancias concurrentes del motor).
func sleepJitter(ctx context.Context, d time.Duration) bool {
	jitter := time.Duration(0)
	if d > 0 {
		span := int64(d / 4)
		if span > 0 {
			nBig, err := crand.Int(crand.Reader, big.NewInt(2*span+1))
			if err == nil {
				jitter = time.Duration(nBig.Int64() - span)
			}
		}
	}
	t := time.NewTimer(d + jitter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
