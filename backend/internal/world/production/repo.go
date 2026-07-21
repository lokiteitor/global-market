package production

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Alias de los estados del enum world.batch_status (legibilidad).
const (
	statusQueued          = sqlcgen.WorldBatchStatusQueued
	statusRunning         = sqlcgen.WorldBatchStatusRunning
	statusPausedNoFuel    = sqlcgen.WorldBatchStatusPausedNoFuel
	statusPausedNoWorkers = sqlcgen.WorldBatchStatusPausedNoWorkers
	statusCompleted       = sqlcgen.WorldBatchStatusCompleted
	statusCancelled       = sqlcgen.WorldBatchStatusCancelled
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). No abre transacciones — el
// servicio/motor decide el ámbito transaccional y deriva un Repo con WithTx.
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

// ─── Construcción diferida ────────────────────────────────────────────────────

// constructionRow es la vista de un edificio en construcción vencido.
type constructionRow struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	RegionID       uuid.UUID
	BuildingTypeID uuid.UUID
}

// ListDueConstructionIDs lista los edificios en construcción ya vencidos.
func (r *Repo) ListDueConstructionIDs(ctx context.Context, buildSimSeconds int64, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListDueConstructionIDs(ctx, sqlcgen.ListDueConstructionIDsParams{
		BuildSimSeconds: buildSimSeconds,
		SimNow:          int64(simNow),
		PageLimit:       limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/production: listando construcciones vencidas: %w", err)
	}
	return ids, nil
}

// LockDueConstruction bloquea un edificio en construcción vencido; pgx.ErrNoRows
// si ya lo completó o lo tomó otra instancia.
func (r *Repo) LockDueConstruction(ctx context.Context, id uuid.UUID, buildSimSeconds int64, simNow simtime.SimTime) (constructionRow, error) {
	row, err := r.q.LockDueConstruction(ctx, sqlcgen.LockDueConstructionParams{
		ID:              id,
		BuildSimSeconds: buildSimSeconds,
		SimNow:          int64(simNow),
	})
	if err != nil {
		return constructionRow{}, err
	}
	return constructionRow{ID: row.ID, OwnerAccountID: row.OwnerAccountID, RegionID: row.RegionID, BuildingTypeID: row.BuildingTypeID}, nil
}

// CompleteConstruction pasa el edificio a operational.
func (r *Repo) CompleteConstruction(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (constructionRow, error) {
	row, err := r.q.CompleteConstruction(ctx, sqlcgen.CompleteConstructionParams{ID: id, SimNow: int64(simNow)})
	if err != nil {
		return constructionRow{}, fmt.Errorf("world/production: completando la construcción de %s: %w", id, err)
	}
	return constructionRow{ID: row.ID, OwnerAccountID: row.OwnerAccountID, RegionID: row.RegionID, BuildingTypeID: row.BuildingTypeID}, nil
}

// ─── Edificio (autorización y estado, reutiliza buildings.sql) ────────────────

// buildingHead es la vista mínima del edificio para autorizar y validar el
// encolado (dueño, estado y tipo).
type buildingHead struct {
	ID             uuid.UUID
	OwnerAccountID uuid.UUID
	RegionID       uuid.UUID
	BuildingTypeID uuid.UUID
	Status         string
}

// GetBuilding devuelve la cabecera del edificio; pgx.ErrNoRows si no existe.
func (r *Repo) GetBuilding(ctx context.Context, id uuid.UUID) (buildingHead, error) {
	row, err := r.q.GetBuilding(ctx, id)
	if err != nil {
		return buildingHead{}, err
	}
	return buildingHead{
		ID:             row.ID,
		OwnerAccountID: row.OwnerAccountID,
		RegionID:       row.RegionID,
		BuildingTypeID: row.BuildingTypeID,
		Status:         string(row.Status),
	}, nil
}

// ─── Cola de producción ───────────────────────────────────────────────────────

// batchListRow es un lote con los datos para derivar el progreso analítico.
type batchListRow struct {
	Batch           Batch
	BatchSimSeconds int64
	Level           int32
	LevelCurve      []byte
}

// ListBatches lista los lotes de un edificio con filtro por estado y keyset.
func (r *Repo) ListBatches(ctx context.Context, buildingID uuid.UUID, status string, afterID *uuid.UUID, limit int32) ([]batchListRow, error) {
	rows, err := r.q.ListProductionBatches(ctx, sqlcgen.ListProductionBatchesParams{
		BuildingID: buildingID,
		Status: sqlcgen.NullWorldBatchStatus{
			WorldBatchStatus: sqlcgen.WorldBatchStatus(status),
			Valid:            status != "",
		},
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/production: listando lotes de %s: %w", buildingID, err)
	}
	out := make([]batchListRow, len(rows))
	for i, row := range rows {
		out[i] = batchListRow{
			Batch: Batch{
				ID:            row.ID,
				BuildingID:    row.BuildingID,
				RecipeID:      row.RecipeID,
				BatchesQueued: row.BatchesQueued,
				BatchesDone:   row.BatchesDone,
				Status:        string(row.Status),
				QueuePosition: row.QueuePosition,
				StartedAtSim:  row.StartedAtSim,
			},
			BatchSimSeconds: row.BatchSimSeconds,
			Level:           row.Level,
			LevelCurve:      row.LevelCurve,
		}
	}
	return out, nil
}

// batchOwnerRow es un lote con el dueño de su edificio y datos de progreso.
type batchOwnerRow struct {
	Batch           Batch
	OwnerAccountID  uuid.UUID
	BatchSimSeconds int64
	Level           int32
	LevelCurve      []byte
}

// GetBatchWithOwner devuelve un lote con el dueño de su edificio; pgx.ErrNoRows
// si no existe.
func (r *Repo) GetBatchWithOwner(ctx context.Context, id uuid.UUID) (batchOwnerRow, error) {
	row, err := r.q.GetProductionBatchWithOwner(ctx, id)
	if err != nil {
		return batchOwnerRow{}, err
	}
	return batchOwnerRow{
		Batch: Batch{
			ID:            row.ID,
			BuildingID:    row.BuildingID,
			RecipeID:      row.RecipeID,
			BatchesQueued: row.BatchesQueued,
			BatchesDone:   row.BatchesDone,
			Status:        string(row.Status),
			QueuePosition: row.QueuePosition,
			StartedAtSim:  row.StartedAtSim,
		},
		OwnerAccountID:  row.OwnerAccountID,
		BatchSimSeconds: row.BatchSimSeconds,
		Level:           row.Level,
		LevelCurve:      row.LevelCurve,
	}, nil
}

// cancelRow es la vista de un lote bloqueado para cancelar.
type cancelRow struct {
	Batch          Batch
	OwnerAccountID uuid.UUID
}

// LockBatchForCancel bloquea el lote (FOR UPDATE) y devuelve el dueño de su
// edificio; pgx.ErrNoRows si no existe.
func (r *Repo) LockBatchForCancel(ctx context.Context, id uuid.UUID) (cancelRow, error) {
	row, err := r.q.LockBatchForCancel(ctx, id)
	if err != nil {
		return cancelRow{}, err
	}
	return cancelRow{
		Batch: Batch{
			ID:            row.ID,
			BuildingID:    row.BuildingID,
			BatchesQueued: row.BatchesQueued,
			BatchesDone:   row.BatchesDone,
			Status:        string(row.Status),
			QueuePosition: row.QueuePosition,
			StartedAtSim:  row.StartedAtSim,
		},
		OwnerAccountID: row.OwnerAccountID,
	}, nil
}

// NextQueuePosition devuelve la siguiente posición libre de la cola.
func (r *Repo) NextQueuePosition(ctx context.Context, buildingID uuid.UUID) (int32, error) {
	pos, err := r.q.NextQueuePosition(ctx, buildingID)
	if err != nil {
		return 0, fmt.Errorf("world/production: calculando la posición de cola de %s: %w", buildingID, err)
	}
	return pos, nil
}

// CountActiveBatches cuenta los lotes running o pausados de un edificio.
func (r *Repo) CountActiveBatches(ctx context.Context, buildingID uuid.UUID) (int64, error) {
	n, err := r.q.CountActiveBatches(ctx, buildingID)
	if err != nil {
		return 0, fmt.Errorf("world/production: contando lotes activos de %s: %w", buildingID, err)
	}
	return n, nil
}

// insertBatchParams son los parámetros de InsertBatch.
type insertBatchParams struct {
	ID            uuid.UUID
	BuildingID    uuid.UUID
	RecipeID      uuid.UUID
	BatchesQueued int32
	QueuePosition int32
	UpdatedAtSim  simtime.SimTime
}

// InsertBatch crea un lote encolado (queued).
func (r *Repo) InsertBatch(ctx context.Context, p insertBatchParams) (Batch, error) {
	row, err := r.q.InsertProductionBatch(ctx, sqlcgen.InsertProductionBatchParams{
		ID:            p.ID,
		BuildingID:    p.BuildingID,
		RecipeID:      p.RecipeID,
		BatchesQueued: p.BatchesQueued,
		QueuePosition: p.QueuePosition,
		UpdatedAtSim:  int64(p.UpdatedAtSim),
	})
	if err != nil {
		return Batch{}, fmt.Errorf("world/production: encolando el lote %s: %w", p.ID, err)
	}
	return batchFromInsert(row), nil
}

// LockNextQueuedHead bloquea el lote queued a la cabeza de la cola de un
// edificio; pgx.ErrNoRows si no hay ninguno.
func (r *Repo) LockNextQueuedHead(ctx context.Context, buildingID uuid.UUID) (Batch, error) {
	row, err := r.q.LockNextQueuedHead(ctx, buildingID)
	if err != nil {
		return Batch{}, err
	}
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}, nil
}

// SetBatchRunning promueve/reanuda un lote a running arrancando su reloj.
func (r *Repo) SetBatchRunning(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (Batch, error) {
	sim := int64(simNow)
	row, err := r.q.SetBatchRunning(ctx, sqlcgen.SetBatchRunningParams{SimNow: &sim, ID: id})
	if err != nil {
		return Batch{}, fmt.Errorf("world/production: arrancando el lote %s: %w", id, err)
	}
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}, nil
}

// SetBatchCancelled cancela lo no producido de un lote.
func (r *Repo) SetBatchCancelled(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (Batch, error) {
	row, err := r.q.SetBatchCancelled(ctx, sqlcgen.SetBatchCancelledParams{SimNow: int64(simNow), ID: id})
	if err != nil {
		return Batch{}, fmt.Errorf("world/production: cancelando el lote %s: %w", id, err)
	}
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}, nil
}

// ─── Motor: procesado de lotes ────────────────────────────────────────────────

// ListActiveBatchIDs lista los lotes running/pausados de edificios operativos.
func (r *Repo) ListActiveBatchIDs(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListActiveBatchIDs(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("world/production: listando lotes activos: %w", err)
	}
	return ids, nil
}

// procBatch es la vista completa de un lote bloqueado para procesar.
type procBatch struct {
	Batch            Batch
	OwnerAccountID   uuid.UUID
	RegionID         uuid.UUID
	Level            int32
	FuelStock        int64
	BaseStorage      int64
	LevelCurve       []byte
	BuildingTypeCode string
	PlacementRules   []byte
	BatchSimSeconds  int64
	FuelProductID    *uuid.UUID
	FuelPerBatch     int64
	WorkersRequired  int32
}

// LockBatchForProcessing bloquea un lote (FOR UPDATE SKIP LOCKED) con todo lo
// necesario para completarlo; pgx.ErrNoRows si otra instancia lo tomó.
func (r *Repo) LockBatchForProcessing(ctx context.Context, id uuid.UUID) (procBatch, error) {
	row, err := r.q.LockBatchForProcessing(ctx, id)
	if err != nil {
		return procBatch{}, err
	}
	return procBatch{
		Batch: Batch{
			ID:            row.ID,
			BuildingID:    row.BuildingID,
			RecipeID:      row.RecipeID,
			BatchesQueued: row.BatchesQueued,
			BatchesDone:   row.BatchesDone,
			Status:        string(row.Status),
			QueuePosition: row.QueuePosition,
			StartedAtSim:  row.StartedAtSim,
		},
		OwnerAccountID:   row.OwnerAccountID,
		RegionID:         row.RegionID,
		Level:            row.Level,
		FuelStock:        row.FuelStock,
		BaseStorage:      row.BaseStorage,
		LevelCurve:       row.LevelCurve,
		BuildingTypeCode: row.BuildingTypeCode,
		PlacementRules:   row.PlacementRules,
		BatchSimSeconds:  row.BatchSimSeconds,
		FuelProductID:    row.FuelProductID,
		FuelPerBatch:     row.FuelPerBatch,
		WorkersRequired:  row.WorkersRequired,
	}, nil
}

// AdvanceBatch cierra un batch: batches_done++ con el estado y reloj del
// siguiente (running+started, o completed+NULL).
func (r *Repo) AdvanceBatch(ctx context.Context, id uuid.UUID, status sqlcgen.WorldBatchStatus, startedAtSim *int64, simNow simtime.SimTime) (Batch, error) {
	row, err := r.q.AdvanceBatch(ctx, sqlcgen.AdvanceBatchParams{
		Status:       status,
		StartedAtSim: startedAtSim,
		SimNow:       int64(simNow),
		ID:           id,
	})
	if err != nil {
		return Batch{}, fmt.Errorf("world/production: avanzando el lote %s: %w", id, err)
	}
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}, nil
}

// PauseBatch pausa un lote (paused_no_fuel/paused_no_workers).
func (r *Repo) PauseBatch(ctx context.Context, id uuid.UUID, status sqlcgen.WorldBatchStatus, simNow simtime.SimTime) (Batch, error) {
	row, err := r.q.PauseBatch(ctx, sqlcgen.PauseBatchParams{Status: status, SimNow: int64(simNow), ID: id})
	if err != nil {
		return Batch{}, fmt.Errorf("world/production: pausando el lote %s: %w", id, err)
	}
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}, nil
}

// ─── Recetas e ingredientes ───────────────────────────────────────────────────

// recipeRow es la vista de una receta para validar y producir.
type recipeRow struct {
	ID              uuid.UUID
	BuildingTypeID  uuid.UUID
	Code            string
	BatchSimSeconds int64
	FuelProductID   *uuid.UUID
	FuelPerBatch    int64
	WorkersRequired int32
	MinCityLevel    int32
}

// GetRecipe devuelve la receta con todos los campos de producción; pgx.ErrNoRows
// si no existe.
func (r *Repo) GetRecipe(ctx context.Context, id uuid.UUID) (recipeRow, error) {
	row, err := r.q.GetProductionRecipe(ctx, id)
	if err != nil {
		return recipeRow{}, err
	}
	return recipeRow{
		ID:              row.ID,
		BuildingTypeID:  row.BuildingTypeID,
		Code:            row.Code,
		BatchSimSeconds: row.BatchSimSeconds,
		FuelProductID:   row.FuelProductID,
		FuelPerBatch:    row.FuelPerBatch,
		WorkersRequired: row.WorkersRequired,
		MinCityLevel:    row.MinCityLevel,
	}, nil
}

// ingredient es un insumo (input) o producto (output) de una receta.
type ingredient struct {
	ProductID uuid.UUID
	Role      string
	Quantity  int64
}

// ListRecipeIngredients devuelve inputs y outputs de una receta (reutiliza la
// query compartida de catalog.sql).
func (r *Repo) ListRecipeIngredients(ctx context.Context, recipeID uuid.UUID) ([]ingredient, error) {
	rows, err := r.q.ListRecipeIngredients(ctx, []uuid.UUID{recipeID})
	if err != nil {
		return nil, fmt.Errorf("world/production: listando ingredientes de la receta %s: %w", recipeID, err)
	}
	out := make([]ingredient, len(rows))
	for i, row := range rows {
		out[i] = ingredient{ProductID: row.ProductID, Role: string(row.Role), Quantity: row.Quantity}
	}
	return out, nil
}

// ─── Yacimientos ──────────────────────────────────────────────────────────────

// LockNearestDeposit bloquea el yacimiento alcanzable más cercano con
// existencias; pgx.ErrNoRows si no hay ninguno.
func (r *Repo) LockNearestDeposit(ctx context.Context, buildingID, productID uuid.UUID, radiusM float64) (uuid.UUID, int64, error) {
	bid := buildingID
	row, err := r.q.LockNearestDeposit(ctx, sqlcgen.LockNearestDepositParams{
		BuildingID: &bid,
		ProductID:  productID,
		RadiusM:    radiusM,
	})
	if err != nil {
		return uuid.Nil, 0, err
	}
	return row.ID, row.RemainingAmount, nil
}

// DecrementDeposit descuenta lo extraído del yacimiento.
func (r *Repo) DecrementDeposit(ctx context.Context, id uuid.UUID, amount int64, simNow simtime.SimTime) (int64, error) {
	remaining, err := r.q.DecrementDeposit(ctx, sqlcgen.DecrementDepositParams{Amount: amount, SimNow: int64(simNow), ID: id})
	if err != nil {
		return 0, fmt.Errorf("world/production: descontando el yacimiento %s: %w", id, err)
	}
	return remaining, nil
}

// ─── Inventario físico ────────────────────────────────────────────────────────

// GetInventoryQty devuelve la cantidad física de un producto en un edificio.
func (r *Repo) GetInventoryQty(ctx context.Context, buildingID, productID uuid.UUID) (int64, error) {
	q, err := r.q.GetBuildingInventoryQty(ctx, sqlcgen.GetBuildingInventoryQtyParams{BuildingID: buildingID, ProductID: productID})
	if err != nil {
		return 0, fmt.Errorf("world/production: consultando inventario (%s, %s): %w", buildingID, productID, err)
	}
	return q, nil
}

// SumInventory devuelve el total físico almacenado en un edificio.
func (r *Repo) SumInventory(ctx context.Context, buildingID uuid.UUID) (int64, error) {
	total, err := r.q.SumBuildingInventory(ctx, buildingID)
	if err != nil {
		return 0, fmt.Errorf("world/production: sumando inventario de %s: %w", buildingID, err)
	}
	return total, nil
}

// AddInventory suma una cantidad (>= 0) al inventario físico de un producto
// (alta por producción/extracción).
func (r *Repo) AddInventory(ctx context.Context, buildingID, productID uuid.UUID, amount int64, simNow simtime.SimTime) error {
	if err := r.q.AddBuildingInventory(ctx, sqlcgen.AddBuildingInventoryParams{
		BuildingID: buildingID,
		ProductID:  productID,
		Amount:     amount,
		SimNow:     int64(simNow),
	}); err != nil {
		return fmt.Errorf("world/production: sumando inventario (%s, %s, +%d): %w", buildingID, productID, amount, err)
	}
	return nil
}

// ConsumeInventory descuenta una cantidad del inventario físico de un producto
// (la fila debe existir y cubrir la cantidad, comprobado antes).
func (r *Repo) ConsumeInventory(ctx context.Context, buildingID, productID uuid.UUID, amount int64, simNow simtime.SimTime) error {
	if err := r.q.ConsumeBuildingInventory(ctx, sqlcgen.ConsumeBuildingInventoryParams{
		BuildingID: buildingID,
		ProductID:  productID,
		Amount:     amount,
		SimNow:     int64(simNow),
	}); err != nil {
		return fmt.Errorf("world/production: consumiendo inventario (%s, %s, -%d): %w", buildingID, productID, amount, err)
	}
	return nil
}

// SetFuelStock actualiza la columna espejo fuel_stock del edificio.
func (r *Repo) SetFuelStock(ctx context.Context, id uuid.UUID, fuelStock int64, simNow simtime.SimTime) error {
	if err := r.q.SetBuildingFuelStock(ctx, sqlcgen.SetBuildingFuelStockParams{FuelStock: fuelStock, SimNow: int64(simNow), ID: id}); err != nil {
		return fmt.Errorf("world/production: actualizando fuel_stock de %s: %w", id, err)
	}
	return nil
}

// ─── Salario ──────────────────────────────────────────────────────────────────

// NearestCityBaseSalary devuelve el salario base de la ciudad más cercana;
// pgx.ErrNoRows si la región no tiene ciudades.
func (r *Repo) NearestCityBaseSalary(ctx context.Context, regionID, buildingID uuid.UUID) (int64, error) {
	bid := buildingID
	return r.q.NearestCityBaseSalary(ctx, sqlcgen.NearestCityBaseSalaryParams{RegionID: regionID, BuildingID: &bid})
}

// RegionSaturation devuelve el factor de saturación industrial regional más
// reciente; pgx.ErrNoRows si aún no hay estadística.
func (r *Repo) RegionSaturation(ctx context.Context, regionID uuid.UUID) (float64, error) {
	return r.q.RegionSaturation(ctx, regionID)
}

// ─── Soporte de ledger ────────────────────────────────────────────────────────

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

// GetStockFreeBalance devuelve el saldo COMPROMETIBLE contable de (dueño,
// producto, almacén) —la cuenta stock_free que debitan los asientos de consumo—;
// 0 si la cuenta aún no existe. Lectura PURA: a diferencia de
// EnsureStockFreeAccount no crea nada, porque la fase de comprobación del motor
// no muta. Es el plano que hay que mirar antes de consumir: el físico
// (building_inventories) incluye stock ya comprometido en stock_reserved (una
// venta publicada/aceptada no mueve la mercancía del almacén) y puede ir por
// delante del asiento durante la ventana que tolera la reconciliación.
func (r *Repo) GetStockFreeBalance(ctx context.Context, owner, product, warehouse uuid.UUID) (int64, error) {
	o, p, w := owner, product, warehouse
	row, err := r.q.GetStockFreeAccount(ctx, sqlcgen.GetStockFreeAccountParams{OwnerAccountID: &o, ProductID: &p, WarehouseBuildingID: &w})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("world/production: consultando el saldo stock_free (%s, %s, %s): %w", owner, product, warehouse, err)
	}
	return row.Balance, nil
}

// EnsureStockFreeAccount localiza (o crea on-demand) la cuenta stock_free de
// (dueño, producto, almacén).
func (r *Repo) EnsureStockFreeAccount(ctx context.Context, owner, product, warehouse uuid.UUID) (ledgerAccount, error) {
	o, p, w := owner, product, warehouse
	row, err := r.q.GetStockFreeAccount(ctx, sqlcgen.GetStockFreeAccountParams{OwnerAccountID: &o, ProductID: &p, WarehouseBuildingID: &w})
	switch {
	case err == nil:
		return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, fmt.Errorf("world/production: consultando stock_free (%s, %s, %s): %w", owner, product, warehouse, err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return ledgerAccount{}, err
	}
	created, err := r.q.CreateStockFreeAccount(ctx, sqlcgen.CreateStockFreeAccountParams{ID: id, OwnerAccountID: &o, ProductID: &p, WarehouseBuildingID: &w})
	if err != nil {
		return ledgerAccount{}, fmt.Errorf("world/production: creando stock_free (%s, %s, %s): %w", owner, product, warehouse, err)
	}
	return ledgerAccount{ID: created.ID, Balance: created.Balance}, nil
}

// EnsureWorldSourceAccount localiza (o crea on-demand) la cuenta world_source de
// un producto (contrapartida física del banco central, ADR-022). El titular de
// las nuevas se toma de una world_source existente (banco central); si aún no
// hay ninguna, nace como cuenta de sistema (owner NULL).
func (r *Repo) EnsureWorldSourceAccount(ctx context.Context, product uuid.UUID) (uuid.UUID, error) {
	p := product
	row, err := r.q.GetWorldSourceAccount(ctx, &p)
	switch {
	case err == nil:
		return row.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("world/production: consultando world_source de %s: %w", product, err)
	}
	owner, err := r.q.GetWorldSourceOwner(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("world/production: localizando el banco central: %w", err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return uuid.Nil, err
	}
	created, err := r.q.CreateWorldSourceAccount(ctx, sqlcgen.CreateWorldSourceAccountParams{ID: id, OwnerAccountID: owner, ProductID: &p})
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/production: creando world_source de %s: %w", product, err)
	}
	return created.ID, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo (los triggers de 0004 garantizan saldo, no-negatividad y doble
// entrada por activo). Los IDs (UUIDv7) los genera la aplicación (ADR-018).
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
		return fmt.Errorf("world/production: asentando la cabecera %s de %s: %w", kind, reference, err)
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
			return fmt.Errorf("world/production: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, mapLedgerError(err))
		}
	}
	return nil
}

// ─── Reconciliación ───────────────────────────────────────────────────────────

// discrepancy es una divergencia física↔contable de un (almacén, producto).
type discrepancy struct {
	BuildingID uuid.UUID
	ProductID  uuid.UUID
	Physical   int64
	Ledger     int64
}

// ListStockDiscrepancies lista las divergencias físico↔contable (esperado 0).
func (r *Repo) ListStockDiscrepancies(ctx context.Context, limit int32) ([]discrepancy, error) {
	rows, err := r.q.ListStockDiscrepancies(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("world/production: listando discrepancias de reconciliación: %w", err)
	}
	out := make([]discrepancy, len(rows))
	for i, row := range rows {
		out[i] = discrepancy{BuildingID: row.BuildingID, ProductID: row.ProductID, Physical: row.Physical, Ledger: row.Ledger}
	}
	return out, nil
}

// ─── Conversión ───────────────────────────────────────────────────────────────

func batchFromInsert(row sqlcgen.InsertProductionBatchRow) Batch {
	return Batch{
		ID:            row.ID,
		BuildingID:    row.BuildingID,
		RecipeID:      row.RecipeID,
		BatchesQueued: row.BatchesQueued,
		BatchesDone:   row.BatchesDone,
		Status:        string(row.Status),
		QueuePosition: row.QueuePosition,
		StartedAtSim:  row.StartedAtSim,
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/production: generando UUIDv7: %w", err)
	}
	return id, nil
}
