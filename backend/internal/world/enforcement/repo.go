package enforcement

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Alias de los estados de los enums (legibilidad).
const (
	buildingOperational = sqlcgen.WorldBuildingStatusOperational
	buildingDamaged     = sqlcgen.WorldBuildingStatusDamaged
	buildingAbandoned   = sqlcgen.WorldBuildingStatusAbandoned
	buildingSeized      = sqlcgen.WorldBuildingStatusSeized
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). No abre transacciones — el motor
// decide el ámbito transaccional y deriva un Repo con WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db)}
}

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx)}
}

// ─── (1) Mantenimiento de edificios ───────────────────────────────────────────

// ListBuildingsDueMaintenance lista los edificios con mantenimiento vencido.
func (r *Repo) ListBuildingsDueMaintenance(ctx context.Context, paidBefore int64, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListBuildingsDueMaintenance(ctx, sqlcgen.ListBuildingsDueMaintenanceParams{PaidBefore: paidBefore, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando edificios con mantenimiento vencido: %w", err)
	}
	return ids, nil
}

// buildingMaint es la vista de un edificio bloqueado para mantenimiento.
type buildingMaint struct {
	ID              uuid.UUID
	OwnerAccountID  uuid.UUID
	RegionID        uuid.UUID
	ConcessionID    uuid.UUID
	Status          string
	ConditionPct    int32
	PaidUntilSim    int64
	MaintenanceCost int64
}

// LockBuildingForMaintenance bloquea un edificio vencido; pgx.ErrNoRows si ya no
// aplica o lo tomó otra instancia.
func (r *Repo) LockBuildingForMaintenance(ctx context.Context, id uuid.UUID, paidBefore int64) (buildingMaint, error) {
	row, err := r.q.LockBuildingForMaintenance(ctx, sqlcgen.LockBuildingForMaintenanceParams{ID: id, PaidBefore: paidBefore})
	if err != nil {
		return buildingMaint{}, err
	}
	return buildingMaint{
		ID:              row.ID,
		OwnerAccountID:  row.OwnerAccountID,
		RegionID:        row.RegionID,
		ConcessionID:    row.ConcessionID,
		Status:          string(row.Status),
		ConditionPct:    row.ConditionPct,
		PaidUntilSim:    row.MaintenancePaidUntilSim,
		MaintenanceCost: row.MaintenanceCost,
	}, nil
}

// UpdateBuildingMaintenance asienta el resultado del barrido (marcador, condición
// y estado).
func (r *Repo) UpdateBuildingMaintenance(ctx context.Context, id uuid.UUID, paidUntil int64, condition int32, status sqlcgen.WorldBuildingStatus, simNow simtime.SimTime) error {
	if err := r.q.UpdateBuildingMaintenance(ctx, sqlcgen.UpdateBuildingMaintenanceParams{
		MaintenancePaidUntilSim: paidUntil,
		ConditionPct:            condition,
		Status:                  status,
		UpdatedAtSim:            int64(simNow),
		ID:                      id,
	}); err != nil {
		return fmt.Errorf("world/enforcement: actualizando mantenimiento de %s: %w", id, err)
	}
	return nil
}

// ─── (1b) Mantenimiento de flota ──────────────────────────────────────────────

// ListVehiclesDueMaintenance lista los vehículos con opex vencido.
func (r *Repo) ListVehiclesDueMaintenance(ctx context.Context, paidBefore int64, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListVehiclesDueMaintenance(ctx, sqlcgen.ListVehiclesDueMaintenanceParams{PaidBefore: paidBefore, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando vehículos con opex vencido: %w", err)
	}
	return ids, nil
}

// vehicleMaint es la vista de un vehículo bloqueado para opex.
type vehicleMaint struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	PaidUntilSim   int64
	OpexPerDay     int64
}

// LockVehicleForMaintenance bloquea un vehículo vencido; pgx.ErrNoRows si ya no
// aplica o lo tomó otra instancia.
func (r *Repo) LockVehicleForMaintenance(ctx context.Context, id uuid.UUID, paidBefore int64) (vehicleMaint, error) {
	row, err := r.q.LockVehicleForMaintenance(ctx, sqlcgen.LockVehicleForMaintenanceParams{ID: id, PaidBefore: paidBefore})
	if err != nil {
		return vehicleMaint{}, err
	}
	return vehicleMaint{
		ID:             row.ID,
		OwnerAccountID: row.OwnerAccountID,
		PaidUntilSim:   row.MaintenancePaidUntilSim,
		OpexPerDay:     row.OperatingCostPerDay,
	}, nil
}

// SetVehicleMaintenancePaid avanza el marcador de opex del vehículo.
func (r *Repo) SetVehicleMaintenancePaid(ctx context.Context, id uuid.UUID, paidUntil int64, simNow simtime.SimTime) error {
	if err := r.q.SetVehicleMaintenancePaid(ctx, sqlcgen.SetVehicleMaintenancePaidParams{
		MaintenancePaidUntilSim: paidUntil,
		UpdatedAtSim:            int64(simNow),
		ID:                      id,
	}); err != nil {
		return fmt.Errorf("world/enforcement: actualizando opex de %s: %w", id, err)
	}
	return nil
}

// ─── (2) Canon de concesión ───────────────────────────────────────────────────

// ListConcessionsDueCanon lista las concesiones activas con el periodo vencido.
func (r *Repo) ListConcessionsDueCanon(ctx context.Context, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListConcessionsDueCanon(ctx, sqlcgen.ListConcessionsDueCanonParams{SimNow: int64(simNow), PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando concesiones con canon vencido: %w", err)
	}
	return ids, nil
}

// concessionCanon es la vista de una concesión bloqueada para el canon.
type concessionCanon struct {
	ID              uuid.UUID
	RegionID        uuid.UUID
	HolderAccountID uuid.UUID
	CanonAmount     int64
	PeriodSimDays   int32
	ExpiresAtSim    int64
}

// LockConcessionForCanon bloquea una concesión activa vencida; pgx.ErrNoRows si
// ya no aplica o la tomó otra instancia.
func (r *Repo) LockConcessionForCanon(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (concessionCanon, error) {
	row, err := r.q.LockConcessionForCanon(ctx, sqlcgen.LockConcessionForCanonParams{ID: id, SimNow: int64(simNow)})
	if err != nil {
		return concessionCanon{}, err
	}
	return concessionCanon{
		ID:              row.ID,
		RegionID:        row.RegionID,
		HolderAccountID: row.HolderAccountID,
		CanonAmount:     row.CanonAmount,
		PeriodSimDays:   row.PeriodSimDays,
		ExpiresAtSim:    row.ExpiresAtSim,
	}, nil
}

// ExtendConcession renueva el periodo (canon vigente ya cobrado al sink).
func (r *Repo) ExtendConcession(ctx context.Context, id uuid.UUID, extendSimSeconds int64) error {
	if err := r.q.ExtendConcession(ctx, sqlcgen.ExtendConcessionParams{ExtendSimSeconds: extendSimSeconds, ID: id}); err != nil {
		return fmt.Errorf("world/enforcement: renovando la concesión %s: %w", id, err)
	}
	return nil
}

// MarkConcessionDelinquent marca la concesión morosa fijando el vencimiento de la
// gracia (si no lo tenía).
func (r *Repo) MarkConcessionDelinquent(ctx context.Context, id uuid.UUID, graceUntil int64) error {
	g := graceUntil
	if err := r.q.MarkConcessionDelinquent(ctx, sqlcgen.MarkConcessionDelinquentParams{GraceUntilSim: &g, ID: id}); err != nil {
		return fmt.Errorf("world/enforcement: marcando morosa la concesión %s: %w", id, err)
	}
	return nil
}

// ListDelinquentDueGrace lista las concesiones morosas con la gracia vencida.
func (r *Repo) ListDelinquentDueGrace(ctx context.Context, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	s := int64(simNow)
	ids, err := r.q.ListDelinquentDueGrace(ctx, sqlcgen.ListDelinquentDueGraceParams{SimNow: &s, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando morosas con gracia vencida: %w", err)
	}
	return ids, nil
}

// LockConcessionForGrace bloquea una concesión morosa con la gracia vencida;
// pgx.ErrNoRows si ya no aplica o la tomó otra instancia.
func (r *Repo) LockConcessionForGrace(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (uuid.UUID, error) {
	s := int64(simNow)
	return r.q.LockConcessionForGrace(ctx, sqlcgen.LockConcessionForGraceParams{ID: id, SimNow: &s})
}

// MarkConcessionGrace marca la concesión para embargo (grace).
func (r *Repo) MarkConcessionGrace(ctx context.Context, id uuid.UUID) error {
	if err := r.q.MarkConcessionGrace(ctx, id); err != nil {
		return fmt.Errorf("world/enforcement: marcando en gracia la concesión %s: %w", id, err)
	}
	return nil
}

// ─── (3) Embargo / reclamo ────────────────────────────────────────────────────

// ListConcessionsInGrace lista las concesiones marcadas para embargo.
func (r *Repo) ListConcessionsInGrace(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListConcessionsInGrace(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando concesiones en gracia: %w", err)
	}
	return ids, nil
}

// ListConcessionsToEmbargoByAbandon lista las concesiones con un edificio
// abandonado cuya gracia venció.
func (r *Repo) ListConcessionsToEmbargoByAbandon(ctx context.Context, graceBefore int64, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListConcessionsToEmbargoByAbandon(ctx, sqlcgen.ListConcessionsToEmbargoByAbandonParams{GraceBefore: graceBefore, PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando concesiones a embargar por abandono: %w", err)
	}
	return ids, nil
}

// concessionEmbargo es la vista de una concesión bloqueada para embargar.
type concessionEmbargo struct {
	ID              uuid.UUID
	RegionID        uuid.UUID
	HolderAccountID uuid.UUID
	Status          string
}

// LockConcessionForEmbargo bloquea la concesión a embargar (idempotente: filtra
// reverted); pgx.ErrNoRows si ya está revertida o la tomó otra instancia.
func (r *Repo) LockConcessionForEmbargo(ctx context.Context, id uuid.UUID) (concessionEmbargo, error) {
	row, err := r.q.LockConcessionForEmbargo(ctx, id)
	if err != nil {
		return concessionEmbargo{}, err
	}
	return concessionEmbargo{ID: row.ID, RegionID: row.RegionID, HolderAccountID: row.HolderAccountID, Status: string(row.Status)}, nil
}

// buildingToSeize es un edificio bloqueado sobre la concesión a embargar.
type buildingToSeize struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	RegionID       uuid.UUID
	Status         string
}

// ListBuildingsOnConcessionForSeize bloquea (FOR UPDATE) los edificios no
// embargados de la concesión.
func (r *Repo) ListBuildingsOnConcessionForSeize(ctx context.Context, concessionID uuid.UUID) ([]buildingToSeize, error) {
	rows, err := r.q.ListBuildingsOnConcessionForSeize(ctx, concessionID)
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: bloqueando edificios de la concesión %s: %w", concessionID, err)
	}
	out := make([]buildingToSeize, len(rows))
	for i, row := range rows {
		out[i] = buildingToSeize{ID: row.ID, OwnerAccountID: row.OwnerAccountID, RegionID: row.RegionID, Status: string(row.Status)}
	}
	return out, nil
}

// GetBuildingNodeID devuelve el nodo logístico del edificio; pgx.ErrNoRows si no
// tiene nodo.
func (r *Repo) GetBuildingNodeID(ctx context.Context, buildingID uuid.UUID) (uuid.UUID, error) {
	b := buildingID
	return r.q.GetBuildingNodeID(ctx, &b)
}

// stockLine es una línea de stock libre de un edificio (producto, almacén, saldo).
type stockLine struct {
	ProductID   uuid.UUID
	WarehouseID uuid.UUID
	Balance     int64
}

// ListBuildingStockFree lista el stock libre del edificio (por almacén/producto).
func (r *Repo) ListBuildingStockFree(ctx context.Context, buildingID uuid.UUID) ([]stockLine, error) {
	b := buildingID
	rows, err := r.q.ListBuildingStockFree(ctx, &b)
	if err != nil {
		return nil, fmt.Errorf("world/enforcement: listando stock libre de %s: %w", buildingID, err)
	}
	out := make([]stockLine, 0, len(rows))
	for _, row := range rows {
		line := stockLine{Balance: row.Balance}
		if row.ProductID != nil {
			line.ProductID = *row.ProductID
		}
		if row.WarehouseBuildingID != nil {
			line.WarehouseID = *row.WarehouseBuildingID
		}
		out = append(out, line)
	}
	return out, nil
}

// SeizeBuilding congela el edificio (status = 'seized').
func (r *Repo) SeizeBuilding(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) error {
	if err := r.q.SeizeBuilding(ctx, sqlcgen.SeizeBuildingParams{UpdatedAtSim: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/enforcement: embargando el edificio %s: %w", id, err)
	}
	return nil
}

// RevertConcession revierte el suelo al sistema.
func (r *Repo) RevertConcession(ctx context.Context, id uuid.UUID) error {
	if err := r.q.RevertConcession(ctx, id); err != nil {
		return fmt.Errorf("world/enforcement: revirtiendo la concesión %s: %w", id, err)
	}
	return nil
}

// PauseRunningBatchesForBuilding para la producción del edificio (running →
// paused_no_workers).
func (r *Repo) PauseRunningBatchesForBuilding(ctx context.Context, buildingID uuid.UUID, simNow simtime.SimTime) error {
	if err := r.q.PauseRunningBatchesForBuilding(ctx, sqlcgen.PauseRunningBatchesForBuildingParams{UpdatedAtSim: int64(simNow), BuildingID: buildingID}); err != nil {
		return fmt.Errorf("world/enforcement: parando la producción de %s: %w", buildingID, err)
	}
	return nil
}

// ─── Soporte de ledger (mismo patrón que land/production) ─────────────────────

// ledgerAccount es la vista mínima de una cuenta del ledger (id y saldo).
type ledgerAccount struct {
	ID      uuid.UUID
	Balance int64
}

// GetCashAccount devuelve la caja de una corporación; pgx.ErrNoRows si no existe.
func (r *Repo) GetCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	o := owner
	row, err := r.q.GetCashAccount(ctx, &o)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// GetSinkAccount devuelve la cuenta sink del banco central; pgx.ErrNoRows si el
// seed no la creó.
func (r *Repo) GetSinkAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetSinkAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo. Los triggers de 0004 aplican los saldos y garantizan la
// no-negatividad y la doble entrada por activo. Los IDs (UUIDv7) los genera la
// aplicación (ADR-018).
func (r *Repo) PostLedgerTransaction(ctx context.Context, kind sqlcgen.LedgerTransactionKind, simAt simtime.SimTime, reference uuid.UUID, description string, entries []entryAmount) error {
	txID, err := newUUIDv7()
	if err != nil {
		return err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	ref := reference
	if err := r.q.InsertLedgerTransaction(ctx, sqlcgen.InsertLedgerTransactionParams{
		ID:          txID,
		Kind:        kind,
		SimTimeAt:   int64(simAt),
		ReferenceID: &ref,
		Description: desc,
	}); err != nil {
		return fmt.Errorf("world/enforcement: asentando la cabecera %s de %s: %w", kind, reference, err)
	}
	for _, e := range entries {
		entryID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := r.q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{
			ID:            entryID,
			TransactionID: txID,
			AccountID:     e.AccountID,
			Amount:        e.Amount,
		}); err != nil {
			return fmt.Errorf("world/enforcement: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/enforcement: generando UUIDv7: %w", err)
	}
	return id, nil
}
