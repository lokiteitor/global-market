package ledger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Códigos de error del contrato emitidos por este módulo (además de los de
// la plataforma httpx).
const (
	codeUnauthorized     = "UNAUTHORIZED"
	codeNotResourceOwner = "NOT_RESOURCE_OWNER"
)

// Identity resuelve la cuenta autenticada de una petición. La define este
// módulo (no importa auth: SAD §7, sin imports cruzados entre bounded
// contexts) y la implementa el composition root con el middleware de sesión.
type Identity interface {
	// AccountID devuelve la corporación autenticada del contexto de la
	// petición y false si no hay sesión válida.
	AccountID(ctx context.Context) (uuid.UUID, bool)
}

// MetaSource construye los metadatos comunes (schema Meta del contrato:
// sim_time actual del mundo) de toda respuesta exitosa. Lo implementa el
// composition root con el reloj de simulación.
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// Reader es la superficie de lectura del módulo que consumen los handlers;
// la implementa *Service.
type Reader interface {
	ListAccounts(ctx context.Context, owner uuid.UUID, f AccountFilter) ([]Account, string, error)
	ListEntries(ctx context.Context, requester, ledgerAccountID uuid.UUID, f EntryFilter) ([]Entry, string, error)
}

var _ Reader = (*Service)(nil)

// Handlers sirve los endpoints /ledger/* del contrato OpenAPI.
type Handlers struct {
	reader   Reader
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del módulo ledger.
func NewHandlers(reader Reader, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	return &Handlers{reader: reader, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway.
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /ledger/accounts", h.listAccounts)
	mux.HandleFunc("GET /ledger/accounts/{ledgerAccountId}/entries", h.listEntries)
}

// ─── GET /ledger/accounts ───────────────────────────────────────────────────

func (h *Handlers) listAccounts(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "sesión ausente o expirada", nil)
		return
	}

	q := r.URL.Query()
	filter := AccountFilter{Cursor: q.Get("cursor")}

	if kind := q.Get("kind"); kind != "" {
		filter.Kind = AccountKind(kind)
		if !filter.Kind.Valid() {
			writeValidationError(w, "kind", "no es un tipo de cuenta del ledger")
			return
		}
	}
	if raw := q.Get("product_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeValidationError(w, "product_id", "no es un UUID válido")
			return
		}
		filter.ProductID = &id
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	accounts, next, err := h.reader.ListAccounts(r.Context(), owner, filter)
	if err != nil {
		h.writeListError(w, r, err, "listando cuentas del ledger")
		return
	}

	data := make([]accountJSON, len(accounts))
	for i, a := range accounts {
		data[i] = toAccountJSON(a)
	}
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, http.StatusOK, data, meta)
}

// ─── GET /ledger/accounts/{ledgerAccountId}/entries ─────────────────────────

func (h *Handlers) listEntries(w http.ResponseWriter, r *http.Request) {
	requester, ok := h.identity.AccountID(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "sesión ausente o expirada", nil)
		return
	}

	// Un id de ruta que no es UUID no puede resolver a ninguna entidad: 404
	// (el contrato no define 400 para el path).
	accountID, err := uuid.Parse(r.PathValue("ledgerAccountId"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la cuenta del ledger no existe", nil)
		return
	}

	q := r.URL.Query()
	filter := EntryFilter{Cursor: q.Get("cursor")}

	if filter.FromSim, err = parseSimTime(q, "from_sim"); err != nil {
		writeValidationError(w, "from_sim", err.Error())
		return
	}
	if filter.ToSim, err = parseSimTime(q, "to_sim"); err != nil {
		writeValidationError(w, "to_sim", err.Error())
		return
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	entries, next, err := h.reader.ListEntries(r.Context(), requester, accountID, filter)
	if err != nil {
		h.writeListError(w, r, err, "listando el extracto del ledger")
		return
	}

	data := make([]entryJSON, len(entries))
	for i, e := range entries {
		data[i] = toEntryJSON(e)
	}
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, http.StatusOK, data, meta)
}

// ─── Mapeo de errores y parsing ─────────────────────────────────────────────

// writeListError mapea los errores tipados del servicio a los códigos del
// contrato; lo no reconocido es un 500 INTERNAL logueado con request id.
func (h *Handlers) writeListError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	switch {
	case errors.Is(err, ErrAccountNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la cuenta del ledger no existe", nil)
	case errors.Is(err, ErrNotOwner):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, "la cuenta del ledger pertenece a otra corporación", nil)
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	default:
		logging.WithRequestID(h.logger, httpx.RequestIDFromContext(r.Context())).LogAttrs(
			r.Context(), slog.LevelError, "error "+doing,
			slog.String("error", err.Error()),
		)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "error interno del servidor", nil)
	}
}

// writeValidationError responde 400 VALIDATION_ERROR con el campo culpable.
func writeValidationError(w http.ResponseWriter, field, reason string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
		fmt.Sprintf("parámetro %s inválido: %s", field, reason),
		map[string]any{"field": field})
}

// parseLimit interpreta el query param limit del contrato (entero 1..200;
// ausente = 0, el servicio aplica el default 50).
func parseLimit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("debe ser un entero")
	}
	if n < 1 || n > MaxPageLimit {
		return 0, fmt.Errorf("debe estar entre 1 y %d", MaxPageLimit)
	}
	return n, nil
}

// parseSimTime interpreta un query param de sim-time (int64 >= 0; ausente = nil).
func parseSimTime(q url.Values, name string) (*simtime.SimTime, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, errors.New("debe ser un entero de sim-time")
	}
	if n < 0 {
		return nil, errors.New("debe ser >= 0")
	}
	t := simtime.SimTime(n)
	return &t, nil
}

// ─── DTOs del contrato (snake_case; dinero/stock como string) ───────────────

// accountJSON es el schema LedgerAccount del contrato.
type accountJSON struct {
	ID                  string    `json:"id"`
	Kind                string    `json:"kind"`
	OwnerAccountID      string    `json:"owner_account_id,omitempty"`
	ProductID           string    `json:"product_id,omitempty"`
	WarehouseBuildingID string    `json:"warehouse_building_id,omitempty"`
	ReferenceID         string    `json:"reference_id,omitempty"`
	Balance             string    `json:"balance"`
	UpdatedAt           time.Time `json:"updated_at"`
	CreatedAt           time.Time `json:"created_at"`
}

func toAccountJSON(a Account) accountJSON {
	return accountJSON{
		ID:                  a.ID.String(),
		Kind:                string(a.Kind),
		OwnerAccountID:      uuidOrEmpty(a.OwnerAccountID),
		ProductID:           uuidOrEmpty(a.ProductID),
		WarehouseBuildingID: uuidOrEmpty(a.WarehouseBuildingID),
		ReferenceID:         uuidOrEmpty(a.ReferenceID),
		Balance:             strconv.FormatInt(a.Balance, 10),
		UpdatedAt:           a.UpdatedAt,
		CreatedAt:           a.CreatedAt,
	}
}

// entryJSON es el schema LedgerEntry del contrato.
type entryJSON struct {
	ID              string    `json:"id"`
	TransactionID   string    `json:"transaction_id"`
	AccountID       string    `json:"account_id"`
	Amount          string    `json:"amount"`
	TransactionKind string    `json:"transaction_kind"`
	ReferenceID     string    `json:"reference_id,omitempty"`
	Description     string    `json:"description,omitempty"`
	SimTimeAt       int64     `json:"sim_time_at"`
	CreatedAt       time.Time `json:"created_at"`
}

func toEntryJSON(e Entry) entryJSON {
	j := entryJSON{
		ID:              e.ID.String(),
		TransactionID:   e.TransactionID.String(),
		AccountID:       e.AccountID.String(),
		Amount:          strconv.FormatInt(e.Amount, 10),
		TransactionKind: string(e.TransactionKind),
		ReferenceID:     uuidOrEmpty(e.ReferenceID),
		SimTimeAt:       int64(e.SimTimeAt),
		CreatedAt:       e.CreatedAt,
	}
	if e.Description != nil {
		j.Description = *e.Description
	}
	return j
}

// uuidOrEmpty serializa un UUID opcional ("" se omite con omitempty).
func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
