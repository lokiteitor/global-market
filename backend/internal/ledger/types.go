package ledger

import (
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// AccountKind es el tipo de una cuenta del ledger (enum ledger.account_kind;
// schema LedgerAccountKind del contrato).
type AccountKind string

// Valores de AccountKind.
const (
	AccountKindCash          AccountKind = "cash"
	AccountKindEscrow        AccountKind = "escrow"
	AccountKindGuarantee     AccountKind = "guarantee"
	AccountKindStockFree     AccountKind = "stock_free"
	AccountKindStockReserved AccountKind = "stock_reserved"
	AccountKindCustody       AccountKind = "custody"
	AccountKindSink          AccountKind = "sink"
	AccountKindEmission      AccountKind = "emission"
)

// Valid indica si el valor pertenece al enum del contrato/BD.
func (k AccountKind) Valid() bool {
	switch k {
	case AccountKindCash, AccountKindEscrow, AccountKindGuarantee,
		AccountKindStockFree, AccountKindStockReserved, AccountKindCustody,
		AccountKindSink, AccountKindEmission:
		return true
	}
	return false
}

// TransactionKind es el tipo de un asiento (enum ledger.transaction_kind;
// campo transaction_kind del schema LedgerEntry del contrato).
type TransactionKind string

// Valores de TransactionKind.
const (
	TransactionKindSeedCapital          TransactionKind = "seed_capital"
	TransactionKindBotCapitalization    TransactionKind = "bot_capitalization"
	TransactionKindBotRetirement        TransactionKind = "bot_retirement"
	TransactionKindPublicationLock      TransactionKind = "publication_lock"
	TransactionKindPublicationRelease   TransactionKind = "publication_release"
	TransactionKindAcceptanceLock       TransactionKind = "acceptance_lock"
	TransactionKindContractConfirmation TransactionKind = "contract_confirmation"
	TransactionKindDeliverySettlement   TransactionKind = "delivery_settlement"
	TransactionKindCustodyLoad          TransactionKind = "custody_load"
	TransactionKindCustodyRelease       TransactionKind = "custody_release"
	TransactionKindProductionOutput     TransactionKind = "production_output"
	TransactionKindConsumption          TransactionKind = "consumption"
	TransactionKindWage                 TransactionKind = "wage"
	TransactionKindMaintenance          TransactionKind = "maintenance"
	TransactionKindTax                  TransactionKind = "tax"
	TransactionKindCanon                TransactionKind = "canon"
	TransactionKindTransfer             TransactionKind = "transfer"
	TransactionKindAuction              TransactionKind = "auction"
	TransactionKindReconciliation       TransactionKind = "reconciliation"
)

// Account es una cuenta del ledger de doble entrada (schema LedgerAccount).
// Balance es dinero en unidades menores o stock en unidad mínima: SIEMPRE
// int64 en Go y string en el JSON del contrato (nunca floats).
type Account struct {
	ID                  uuid.UUID
	Kind                AccountKind
	OwnerAccountID      *uuid.UUID // NULL en cuentas puras de sistema
	ProductID           *uuid.UUID // presente en cuentas de stock
	WarehouseBuildingID *uuid.UUID // presente en stock_free
	ReferenceID         *uuid.UUID // publicación/contrato de la cuenta espejo
	Balance             int64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// Entry es una partida del extracto de una cuenta, con los campos de su
// cabecera de asiento que exige el schema LedgerEntry del contrato.
type Entry struct {
	ID              uuid.UUID
	TransactionID   uuid.UUID
	AccountID       uuid.UUID
	Amount          int64
	TransactionKind TransactionKind
	ReferenceID     *uuid.UUID
	Description     *string
	SimTimeAt       simtime.SimTime
	CreatedAt       time.Time
}

// EntryInput es una partida de un asiento a asentar con PostTransaction.
// El signo de Amount es el cargo/abono; nunca cero.
type EntryInput struct {
	AccountID uuid.UUID
	Amount    int64
}

// AccountFilter son los filtros y la paginación de ListAccounts
// (query params del contrato: kind, product_id, cursor, limit).
type AccountFilter struct {
	// Kind filtra por tipo de cuenta; vacío = todos. Debe ser un valor
	// válido de AccountKind.
	Kind AccountKind
	// ProductID filtra cuentas de stock por producto; nil = todos.
	ProductID *uuid.UUID
	// Cursor es el cursor opaco devuelto en meta.next_cursor; vacío = primera página.
	Cursor string
	// Limit es el tamaño máximo de página (1..MaxPageLimit); 0 = DefaultPageLimit.
	Limit int
}

// EntryFilter son los filtros y la paginación de ListEntries
// (query params del contrato: from_sim, to_sim, cursor, limit).
type EntryFilter struct {
	// FromSim es el sim-time mínimo del asiento (inclusive); nil = sin mínimo.
	FromSim *simtime.SimTime
	// ToSim es el sim-time máximo del asiento (inclusive); nil = sin máximo.
	ToSim *simtime.SimTime
	// Cursor es el cursor opaco devuelto en meta.next_cursor; vacío = primera página.
	Cursor string
	// Limit es el tamaño máximo de página (1..MaxPageLimit); 0 = DefaultPageLimit.
	Limit int
}
