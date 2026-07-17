package land

import (
	"strconv"

	"github.com/google/uuid"
)

// Tipos de agregado y de evento que world/land emite por el outbox
// transaccional (SAD/ADR-008), en la misma tx que el cambio de estado.
const (
	AggregateConcession = "concession"

	EventConcessionGranted     = "concession.granted"
	EventConcessionRenewed     = "concession.renewed"
	EventConcessionTransferred = "concession.transferred"
)

// Payloads JSON de los eventos. Dinero viaja SIEMPRE como string de punto fijo,
// jamás como float; los sim-time como enteros.

// ConcessionGrantedPayload es el payload de concession.granted.
type ConcessionGrantedPayload struct {
	ConcessionID    string `json:"concession_id"`
	RegionID        string `json:"region_id"`
	HolderAccountID string `json:"holder_account_id"`
	CanonAmount     string `json:"canon_amount"`
	ExpiresAtSim    int64  `json:"expires_at_sim"`
	GrantedAtSim    int64  `json:"granted_at_sim"`
}

// ConcessionRenewedPayload es el payload de concession.renewed.
type ConcessionRenewedPayload struct {
	ConcessionID    string `json:"concession_id"`
	HolderAccountID string `json:"holder_account_id"`
	CanonAmount     string `json:"canon_amount"`
	ExpiresAtSim    int64  `json:"expires_at_sim"`
	RenewedAtSim    int64  `json:"renewed_at_sim"`
}

// ConcessionTransferredPayload es el payload de concession.transferred.
type ConcessionTransferredPayload struct {
	TransferID    string `json:"transfer_id"`
	ConcessionID  string `json:"concession_id"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	Price         string `json:"price"`
	SystemFee     string `json:"system_fee"`
	OccurredAtSim int64  `json:"occurred_at_sim"`
}

// fixed serializa un importe de punto fijo como string del contrato.
func fixed(v int64) string { return strconv.FormatInt(v, 10) }

// uuidOrEmpty serializa un uuid opcional ("" si es nil, para omitempty).
func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
