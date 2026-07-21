package enforcement

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
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
	sweepBuildingMaintenance = "building_maintenance"
	sweepVehicleMaintenance  = "vehicle_maintenance"
	sweepCanon               = "canon"
	sweepEmbargo             = "embargo"
)

// SimSource entrega el sim-time actual del mundo (inyectado: en el engine es el
// reloj de simulación; en los tests, un reloj fijo avanzado por SQL).
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Worker es el MOTOR de la cascada de insolvencia (Incremento 6a): barre el
// mantenimiento de edificios/vehículos (degradación por impago), el canon de las
// concesiones (delinquent → grace) y el embargo (grace/abandono → seized +
// reverted). Cada entidad se procesa en SU PROPIA transacción SERIALIZABLE,
// bloqueada con FOR UPDATE SKIP LOCKED, de modo que varias instancias pueden
// correr en paralelo sin pisarse.
type Worker struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   WorkerOptions
	logger *slog.Logger

	maintenanceCharged    prometheus.Counter
	buildingsDegraded     prometheus.Counter
	buildingsAbandoned    prometheus.Counter
	canonCharged          prometheus.Counter
	concessionsDelinquent prometheus.Counter
	concessionsReverted   prometheus.Counter
	buildingsSeized       prometheus.Counter
	sweepDuration         *prometheus.HistogramVec
}

// NewWorker construye el motor sobre el pool compartido. reg registra sus
// métricas (nil las deja sin instrumentar: tests); logger nil usa slog.Default.
func NewWorker(pool *pgxpool.Pool, sim SimSource, opts WorkerOptions, logger *slog.Logger, reg prometheus.Registerer) (*Worker, error) {
	if pool == nil {
		return nil, errors.New("world/enforcement: el worker requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("world/enforcement: el worker requiere un SimSource")
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
		maintenanceCharged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_maintenance_charged_total",
			Help: "Importe total cobrado como mantenimiento (edificios + flota) al sink.",
		}),
		buildingsDegraded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_degraded_total",
			Help: "Total de edificios degradados por mantenimiento impagado (por barrido).",
		}),
		buildingsAbandoned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_abandoned_total",
			Help: "Total de edificios que cruzaron a 'abandoned' por degradación.",
		}),
		canonCharged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_canon_charged_total",
			Help: "Importe total cobrado como canon de renovación al sink.",
		}),
		concessionsDelinquent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_concessions_delinquent_total",
			Help: "Total de concesiones que pasaron a 'delinquent' por canon impagado.",
		}),
		concessionsReverted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_concessions_reverted_total",
			Help: "Total de concesiones revertidas al sistema por embargo.",
		}),
		buildingsSeized: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_seized_total",
			Help: "Total de edificios embargados (congelados a 'seized').",
		}),
		sweepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ii_enforcement_sweep_duration_seconds",
			Help:    "Duración de cada barrido del motor de enforcement, por tipo.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sweep"}),
	}
	if reg != nil {
		reg.MustRegister(w.maintenanceCharged, w.buildingsDegraded, w.buildingsAbandoned,
			w.canonCharged, w.concessionsDelinquent, w.concessionsReverted, w.buildingsSeized,
			w.sweepDuration)
	}
	return w, nil
}

// Run ejecuta el bucle del motor hasta que ctx se cancele (nil al apagado
// limpio). El barrido de mantenimiento + canon corre a II_MAINTENANCE_INTERVAL y
// el de embargo a II_ENFORCEMENT_INTERVAL; el bucle base tiquea al menor de los
// dos y dispara cada barrido cuando su propio periodo venció.
func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("world/enforcement: motor iniciado",
		slog.Duration("maintenance_interval", w.opts.MaintenanceInterval),
		slog.Duration("enforcement_interval", w.opts.EnforcementInterval),
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Int("degrade_pct_per_sim_day", int(w.opts.DegradePctPerSimDay)),
		slog.Int("abandon_condition_pct", int(w.opts.AbandonConditionPct)),
		slog.Int64("seize_grace_sim_seconds", w.opts.SeizeGraceSimSeconds))

	base := w.opts.EnforcementInterval
	if w.opts.MaintenanceInterval < base {
		base = w.opts.MaintenanceInterval
	}
	lastMaintenance := time.Time{} // época: el primer tick dispara ambos barridos
	lastEnforcement := time.Time{}
	for {
		if time.Since(lastMaintenance) >= w.opts.MaintenanceInterval {
			w.RunMaintenanceOnce(ctx)
			lastMaintenance = time.Now()
		}
		if time.Since(lastEnforcement) >= w.opts.EnforcementInterval {
			w.RunEnforcementOnce(ctx)
			lastEnforcement = time.Now()
		}
		if !sleepJitter(ctx, base) {
			w.logger.Info("world/enforcement: motor detenido")
			return nil
		}
	}
}

// RunMaintenanceOnce ejecuta una pasada de los barridos de mantenimiento (edificios
// + flota) y canon. Aislado para los tests, que controlan el disparo.
func (w *Worker) RunMaintenanceOnce(ctx context.Context) {
	w.runSweep(ctx, sweepBuildingMaintenance, w.SweepBuildingMaintenance)
	w.runSweep(ctx, sweepVehicleMaintenance, w.SweepVehicleMaintenance)
	w.runSweep(ctx, sweepCanon, w.SweepCanon)
}

// RunEnforcementOnce ejecuta una pasada del barrido de embargo. Aislado para los
// tests.
func (w *Worker) RunEnforcementOnce(ctx context.Context) {
	w.runSweep(ctx, sweepEmbargo, w.SweepEmbargo)
}

// runSweep cronometra un barrido y registra su duración y un error global.
func (w *Worker) runSweep(ctx context.Context, name string, fn func(context.Context) (int, error)) {
	start := time.Now()
	n, err := fn(ctx)
	w.sweepDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	if err != nil {
		w.logger.Warn("world/enforcement: barrido con error al listar candidatos",
			slog.String("sweep", name), slog.Any("error", err))
		return
	}
	if n > 0 {
		w.logger.Debug("world/enforcement: barrido completado",
			slog.String("sweep", name), slog.Int("procesados", n))
	}
}

// ─── (1) Mantenimiento de edificios ───────────────────────────────────────────

// maintOutcome acumula los efectos de procesar un edificio para volcarlos a las
// métricas UNA sola vez tras el COMMIT (un reintento de serialización re-ejecuta
// el cuerpo; solo el commit definitivo cuenta).
type maintOutcome struct {
	charged   int64
	degraded  bool
	abandoned bool
}

// SweepBuildingMaintenance cobra el mantenimiento vencido de los edificios
// operativos/dañados y degrada/abandona los que no lo cubren.
func (w *Worker) SweepBuildingMaintenance(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	paidBefore := int64(simNow) - simtime.SimDay
	ids, err := w.repo.ListBuildingsDueMaintenance(ctx, paidBefore, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		if err := w.processBuildingMaintenance(ctx, id, paidBefore); err != nil {
			w.logger.Warn("world/enforcement: fallo en mantenimiento de un edificio",
				slog.String("building_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

// processBuildingMaintenance procesa un edificio en su propia transacción.
func (w *Worker) processBuildingMaintenance(ctx context.Context, id uuid.UUID, paidBefore int64) error {
	var oc *maintOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = &maintOutcome{}
		r := w.repo.WithTx(tx)
		b, err := r.LockBuildingForMaintenance(ctx, id, paidBefore)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // ya procesado o tomado por otra instancia
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		daysDue := (int64(simNow) - b.PaidUntilSim) / simtime.SimDay
		if daysDue <= 0 {
			return nil // sin día-sim completo vencido (guarda defensiva)
		}
		cost := b.MaintenanceCost * daysDue

		avail, cashID, err := w.cashAvailable(ctx, r, b.OwnerAccountID)
		if err != nil {
			return err
		}

		var newPaid int64
		var newCond int32
		var newStatus sqlcgen.WorldBuildingStatus
		var charged int64

		if avail >= cost {
			// Cubre todo: avanza el marcador y RECUPERA condición.
			charged = cost
			newPaid = b.PaidUntilSim + daysDue*simtime.SimDay
			newCond = clampCondition(int64(b.ConditionPct) + int64(recoverPctPerSimDay)*daysDue)
			if newCond >= 100 {
				newStatus = buildingOperational
			} else {
				newStatus = sqlcgen.WorldBuildingStatus(b.Status)
			}
		} else {
			// No cubre: cobra los días que pueda; DEGRADA los impagados.
			var daysPaid int64
			if b.MaintenanceCost > 0 {
				daysPaid = avail / b.MaintenanceCost
			}
			if daysPaid > daysDue {
				daysPaid = daysDue
			}
			charged = daysPaid * b.MaintenanceCost
			daysUnpaid := daysDue - daysPaid
			newPaid = b.PaidUntilSim + daysDue*simtime.SimDay
			newCond = clampCondition(int64(b.ConditionPct) - int64(w.opts.DegradePctPerSimDay)*daysUnpaid)
			if daysUnpaid > 0 {
				oc.degraded = true
			}
			if newCond <= w.opts.AbandonConditionPct {
				newStatus = buildingAbandoned
				newPaid = int64(simNow) // arranca el conteo de gracia en el instante del abandono
				oc.abandoned = true
			} else {
				newStatus = buildingDamaged
			}
		}

		// Cobro al sink (transacción maintenance): SOLO lo cobrado (nunca deja la
		// caja en negativo; charged <= avail por construcción).
		if charged > 0 {
			if err := w.chargeToSink(ctx, r, sqlcgen.LedgerTransactionKindMaintenance, cashID, charged, b.ID, simNow,
				fmt.Sprintf("Mantenimiento de edificio (%d)", charged)); err != nil {
				return err
			}
			oc.charged = charged
		}

		if err := r.UpdateBuildingMaintenance(ctx, b.ID, newPaid, newCond, newStatus, simNow); err != nil {
			return err
		}
		// Un edificio abandonado para su producción (GDD 5.9).
		if oc.abandoned {
			if err := r.PauseRunningBatchesForBuilding(ctx, b.ID, simNow); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && oc != nil {
		w.flushMaint(oc)
	}
	return err
}

func (w *Worker) flushMaint(oc *maintOutcome) {
	if oc.charged > 0 {
		w.maintenanceCharged.Add(float64(oc.charged))
	}
	if oc.degraded {
		w.buildingsDegraded.Inc()
	}
	if oc.abandoned {
		w.buildingsAbandoned.Inc()
	}
}

// ─── (1b) Mantenimiento de flota ──────────────────────────────────────────────

// SweepVehicleMaintenance cobra el opex vencido de los vehículos (solo drena
// caja; sin condición: la avería/desgaste los maneja el motor de tránsito).
func (w *Worker) SweepVehicleMaintenance(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	paidBefore := int64(simNow) - simtime.SimDay
	ids, err := w.repo.ListVehiclesDueMaintenance(ctx, paidBefore, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		if err := w.processVehicleMaintenance(ctx, id, paidBefore); err != nil {
			w.logger.Warn("world/enforcement: fallo en opex de un vehículo",
				slog.String("vehicle_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

func (w *Worker) processVehicleMaintenance(ctx context.Context, id uuid.UUID, paidBefore int64) error {
	var charged int64
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		charged = 0
		r := w.repo.WithTx(tx)
		v, err := r.LockVehicleForMaintenance(ctx, id, paidBefore)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		daysDue := (int64(simNow) - v.PaidUntilSim) / simtime.SimDay
		if daysDue <= 0 {
			return nil
		}
		avail, cashID, err := w.cashAvailable(ctx, r, v.OwnerAccountID)
		if err != nil {
			return err
		}
		daysPaid := daysDue
		if v.OpexPerDay > 0 {
			if affordable := avail / v.OpexPerDay; affordable < daysPaid {
				daysPaid = affordable
			}
		}
		amount := daysPaid * v.OpexPerDay
		if amount > 0 {
			if err := w.chargeToSink(ctx, r, sqlcgen.LedgerTransactionKindMaintenance, cashID, amount, v.ID, simNow,
				fmt.Sprintf("Opex de vehículo (%d)", amount)); err != nil {
				return err
			}
			charged = amount
		}
		// Avanza el marcador por TODOS los días vencidos: los impagados se condonan
		// (sin deuda, GDD 5.9).
		newPaid := v.PaidUntilSim + daysDue*simtime.SimDay
		return r.SetVehicleMaintenancePaid(ctx, v.ID, newPaid, simNow)
	})
	if err == nil && charged > 0 {
		w.maintenanceCharged.Add(float64(charged))
	}
	return err
}

// ─── (2) Canon de concesión ───────────────────────────────────────────────────

// canonOutcome acumula los efectos de procesar el canon de una concesión.
type canonOutcome struct {
	canonCharged int64
	delinquent   bool
}

// SweepCanon renueva las concesiones vencidas cobrando el canon (o las marca
// morosas) y promueve las morosas con la gracia agotada a 'grace'.
func (w *Worker) SweepCanon(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	limit := int32(w.opts.BatchSize) //nolint:gosec // acotado por Validate

	dueIDs, err := w.repo.ListConcessionsDueCanon(ctx, simNow, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range dueIDs {
		if err := w.processConcessionCanon(ctx, id); err != nil {
			w.logger.Warn("world/enforcement: fallo en canon de una concesión",
				slog.String("concession_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}

	// delinquent → grace (gracia agotada).
	graceIDs, err := w.repo.ListDelinquentDueGrace(ctx, simNow, limit)
	if err != nil {
		return processed, err
	}
	for _, id := range graceIDs {
		if err := w.processConcessionGrace(ctx, id); err != nil {
			w.logger.Warn("world/enforcement: fallo marcando gracia de una concesión",
				slog.String("concession_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

func (w *Worker) processConcessionCanon(ctx context.Context, id uuid.UUID) error {
	var oc *canonOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = &canonOutcome{}
		r := w.repo.WithTx(tx)
		c, err := r.LockConcessionForCanon(ctx, id, w.sim.Now(ctx))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		avail, cashID, err := w.cashAvailable(ctx, r, c.HolderAccountID)
		if err != nil {
			return err
		}
		if avail >= c.CanonAmount {
			// Cobra el canon vigente y renueva el periodo.
			if err := w.chargeToSink(ctx, r, sqlcgen.LedgerTransactionKindCanon, cashID, c.CanonAmount, c.ID, simNow,
				fmt.Sprintf("Canon de renovación (%d)", c.CanonAmount)); err != nil {
				return err
			}
			oc.canonCharged = c.CanonAmount
			return r.ExtendConcession(ctx, c.ID, int64(c.PeriodSimDays)*simtime.SimDay)
		}
		// Impago: morosa + arranca la gracia.
		graceUntil := int64(simNow) + w.opts.SeizeGraceSimSeconds
		if err := r.MarkConcessionDelinquent(ctx, c.ID, graceUntil); err != nil {
			return err
		}
		oc.delinquent = true
		return nil
	})
	if err == nil && oc != nil {
		if oc.canonCharged > 0 {
			w.canonCharged.Add(float64(oc.canonCharged))
		}
		if oc.delinquent {
			w.concessionsDelinquent.Inc()
		}
	}
	return err
}

func (w *Worker) processConcessionGrace(ctx context.Context, id uuid.UUID) error {
	return db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		r := w.repo.WithTx(tx)
		if _, err := r.LockConcessionForGrace(ctx, id, w.sim.Now(ctx)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		return r.MarkConcessionGrace(ctx, id)
	})
}

// ─── (3) Embargo / reclamo ────────────────────────────────────────────────────

// embargoOutcome acumula los efectos de embargar una concesión.
type embargoOutcome struct {
	seized   int
	reverted bool
}

// SweepEmbargo embarga las concesiones marcadas para ello (rama canon: 'grace') y
// las que tienen un edificio abandonado con la gracia agotada (rama mantenimiento).
func (w *Worker) SweepEmbargo(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	limit := int32(w.opts.BatchSize) //nolint:gosec // acotado por Validate

	seen := map[uuid.UUID]struct{}{}
	var targets []uuid.UUID

	graceIDs, err := w.repo.ListConcessionsInGrace(ctx, limit)
	if err != nil {
		return 0, err
	}
	for _, id := range graceIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}

	graceBefore := int64(simNow) - w.opts.SeizeGraceSimSeconds
	abandonIDs, err := w.repo.ListConcessionsToEmbargoByAbandon(ctx, graceBefore, limit)
	if err != nil {
		return 0, err
	}
	for _, id := range abandonIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		targets = append(targets, id)
	}

	processed := 0
	for _, id := range targets {
		if err := w.embargoConcession(ctx, id); err != nil {
			w.logger.Warn("world/enforcement: fallo embargando una concesión",
				slog.String("concession_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

// embargoConcession revierte una concesión y congela TODOS sus edificios en una
// sola transacción, emitiendo building.seized por cada edificio (con su stock
// libre y su nodo de origen) y concession.reverted. Idempotente: se salta las
// concesiones ya revertidas y los edificios ya embargados.
func (w *Worker) embargoConcession(ctx context.Context, id uuid.UUID) error {
	var oc *embargoOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = &embargoOutcome{}
		r := w.repo.WithTx(tx)
		c, err := r.LockConcessionForEmbargo(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // ya revertida o tomada por otra instancia
			}
			return err
		}
		simNow := w.sim.Now(ctx)

		buildings, err := r.ListBuildingsOnConcessionForSeize(ctx, id)
		if err != nil {
			return err
		}
		for _, b := range buildings {
			reason := ReasonCanonReverted
			if b.Status == string(buildingAbandoned) {
				reason = ReasonAbandoned
			}

			// Nodo logístico (retirada in situ). Puede no existir (defensivo).
			nodeID := ""
			if node, err := r.GetBuildingNodeID(ctx, b.ID); err == nil {
				nodeID = node.String()
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}

			// Stock LIBRE del edificio en el momento del embargo (no se mueve aquí).
			lines, err := r.ListBuildingStockFree(ctx, b.ID)
			if err != nil {
				return err
			}
			stock := make([]SeizedStockItem, len(lines))
			for i, l := range lines {
				stock[i] = SeizedStockItem{
					ProductID:           l.ProductID.String(),
					Quantity:            fixed(l.Balance),
					WarehouseBuildingID: l.WarehouseID.String(),
				}
			}

			if err := outbox.Emit(ctx, tx, int64(simNow), AggregateBuilding, b.ID, EventBuildingSeized, BuildingSeizedPayload{
				BuildingID:     b.ID.String(),
				OwnerAccountID: b.OwnerAccountID.String(),
				RegionID:       b.RegionID.String(),
				OriginNodeID:   nodeID,
				Reason:         reason,
				Stock:          stock,
				SeizedAtSim:    int64(simNow),
			}); err != nil {
				return err
			}
			if err := r.SeizeBuilding(ctx, b.ID, simNow); err != nil {
				return err
			}
			if err := r.PauseRunningBatchesForBuilding(ctx, b.ID, simNow); err != nil {
				return err
			}
			oc.seized++
		}

		if err := r.RevertConcession(ctx, id); err != nil {
			return err
		}
		oc.reverted = true
		return outbox.Emit(ctx, tx, int64(simNow), AggregateConcession, id, EventConcessionReverted, ConcessionRevertedPayload{
			ConcessionID:  id.String(),
			FormerHolder:  c.HolderAccountID.String(),
			RegionID:      c.RegionID.String(),
			RevertedAtSim: int64(simNow),
		})
	})
	if err == nil && oc != nil {
		w.buildingsSeized.Add(float64(oc.seized))
		if oc.reverted {
			w.concessionsReverted.Inc()
		}
	}
	return err
}

// ─── Utilidades ───────────────────────────────────────────────────────────────

// cashAvailable devuelve el saldo disponible de la caja del dueño y su id. Sin
// caja (pgx.ErrNoRows) → 0 disponible y uuid.Nil (no se intentará cobrar).
func (w *Worker) cashAvailable(ctx context.Context, r *Repo, owner uuid.UUID) (int64, uuid.UUID, error) {
	cash, err := r.GetCashAccount(ctx, owner)
	switch {
	case err == nil:
		return cash.Balance, cash.ID, nil
	case errors.Is(err, pgx.ErrNoRows):
		return 0, uuid.Nil, nil
	default:
		return 0, uuid.Nil, err
	}
}

// chargeToSink asienta un cobro cash → sink (transacción maintenance/canon). El
// importe DEBE ser > 0 y estar cubierto por la caja (el trigger del ledger aborta
// si dejara la caja en negativo).
func (w *Worker) chargeToSink(ctx context.Context, r *Repo, kind sqlcgen.LedgerTransactionKind, cashID uuid.UUID, amount int64, reference uuid.UUID, simNow simtime.SimTime, description string) error {
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("world/enforcement: localizando la cuenta sink del banco central: %w", err)
	}
	return r.PostLedgerTransaction(ctx, kind, simNow, reference, description, []entryAmount{
		{AccountID: cashID, Amount: -amount},
		{AccountID: sink.ID, Amount: amount},
	})
}

// clampCondition acota una condición calculada al rango [0, 100] del CHECK de
// world.buildings.condition_pct.
func clampCondition(v int64) int32 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return int32(v)
}

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
