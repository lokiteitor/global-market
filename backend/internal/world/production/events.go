package production

// Tipos de agregado y de evento que world/production emite por el outbox
// transaccional (SAD/ADR-008), en la MISMA tx que el cambio de estado. Las
// constantes de building.* se declaran aquí (y no se importan de world/buildings)
// para no acoplar subpaquetes: el barrido de construcción del motor vive en
// producción y también emite building.constructed.
const (
	AggregateBatch    = "production_batch"
	AggregateBuilding = "building"

	EventBatchQueued         = "batch.queued"
	EventBatchCompleted      = "batch.completed"
	EventBatchPaused         = "batch.paused"
	EventBatchCancelled      = "batch.cancelled"
	EventBuildingConstructed = "building.constructed"
)

// Motivos de pausa/parada (label de métrica y campo reason del evento
// batch.paused). no_fuel/no_workers son estados persistidos del enum
// world.batch_status; no_inputs/no_deposit/storage_full son paradas sin estado
// nuevo (el lote permanece running y se reintenta) — ver worker.go.
const (
	reasonNoFuel      = "no_fuel"
	reasonNoWorkers   = "no_workers"
	reasonNoPower     = "no_power"
	reasonNoInputs    = "no_inputs"
	reasonNoDeposit   = "no_deposit"
	reasonStorageFull = "storage_full"
)

// Payloads JSON de los eventos. Dinero/stock viajan SIEMPRE como string de punto
// fijo, jamás como float; los sim-time como enteros. Los campos opcionales se
// omiten vacíos.

// BatchQueuedPayload es el payload de batch.queued.
type BatchQueuedPayload struct {
	BatchID       string `json:"batch_id"`
	BuildingID    string `json:"building_id"`
	RecipeID      string `json:"recipe_id"`
	BatchesQueued int32  `json:"batches_queued"`
	QueuePosition int32  `json:"queue_position"`
	Status        string `json:"status"`
	QueuedAtSim   int64  `json:"queued_at_sim"`
}

// BatchCompletedPayload es el payload de batch.completed (un batch cerrado; el
// estado es running si quedan batches en el lote, o completed si fue el último).
type BatchCompletedPayload struct {
	BatchID        string `json:"batch_id"`
	BuildingID     string `json:"building_id"`
	RecipeID       string `json:"recipe_id"`
	BatchesDone    int32  `json:"batches_done"`
	BatchesQueued  int32  `json:"batches_queued"`
	Status         string `json:"status"`
	CompletedAtSim int64  `json:"completed_at_sim"`
}

// BatchPausedPayload es el payload de batch.paused (paused_no_fuel /
// paused_no_workers).
type BatchPausedPayload struct {
	BatchID     string `json:"batch_id"`
	BuildingID  string `json:"building_id"`
	RecipeID    string `json:"recipe_id"`
	Reason      string `json:"reason"`
	Status      string `json:"status"`
	PausedAtSim int64  `json:"paused_at_sim"`
}

// BatchCancelledPayload es el payload de batch.cancelled.
type BatchCancelledPayload struct {
	BatchID        string `json:"batch_id"`
	BuildingID     string `json:"building_id"`
	BatchesDone    int32  `json:"batches_done"`
	BatchesQueued  int32  `json:"batches_queued"`
	CancelledAtSim int64  `json:"cancelled_at_sim"`
}

// BuildingConstructedPayload es el payload de building.constructed (el motor
// completa la construcción diferida).
type BuildingConstructedPayload struct {
	BuildingID       string `json:"building_id"`
	OwnerAccountID   string `json:"owner_account_id"`
	RegionID         string `json:"region_id"`
	BuildingTypeID   string `json:"building_type_id"`
	ConstructedAtSim int64  `json:"constructed_at_sim"`
}
