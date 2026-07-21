package production

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
)

// Códigos de error del contrato emitidos por este subpaquete (además de los de
// la plataforma httpx). El schema Error es de código abierto (type: string).
const (
	codeUnauthorized           = "UNAUTHORIZED"
	codeNotResourceOwner       = "NOT_RESOURCE_OWNER"
	codeBuildingNotOperational = "BUILDING_NOT_OPERATIONAL"
	codeRecipeNotSupported     = "RECIPE_NOT_SUPPORTED"
	codeBatchNotCancellable    = "BATCH_NOT_CANCELLABLE"
)

// Identity resuelve la cuenta autenticada de una petición (la implementa el
// composition root; SAD §7: sin imports cruzados entre bounded contexts).
type Identity interface {
	AccountID(ctx context.Context) (uuid.UUID, bool)
}

// MetaSource construye los metadatos comunes (schema Meta) de toda respuesta.
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// API es la superficie del servicio que consumen los handlers; la implementa
// *Service.
type API interface {
	ListBatches(ctx context.Context, owner, buildingID uuid.UUID, f BatchFilter) ([]Batch, string, error)
	QueueBatches(ctx context.Context, owner, buildingID uuid.UUID, in BatchInput) (Batch, error)
	CancelBatch(ctx context.Context, owner, batchID uuid.UUID) (Batch, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints world/*production-batches del contrato OpenAPI
// v1.2.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del subpaquete de producción.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/buildings/{buildingId}/production-batches", h.listBatches)
	mux.HandleFunc("POST /world/buildings/{buildingId}/production-batches", h.queueBatches)
	mux.HandleFunc("DELETE /world/production-batches/{batchId}", h.cancelBatch)
}

// ─── GET /world/buildings/{id}/production-batches ─────────────────────────────

func (h *Handlers) listBatches(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	buildingID, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	q := r.URL.Query()
	filter := BatchFilter{Status: q.Get("status"), Cursor: q.Get("cursor")}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	batches, next, err := h.svc.ListBatches(r.Context(), owner, buildingID, filter)
	if err != nil {
		h.writeError(w, r, err, "listando lotes")
		return
	}
	data := make([]batchJSON, len(batches))
	for i, b := range batches {
		data[i] = toBatchJSON(b)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── POST /world/buildings/{id}/production-batches ────────────────────────────

func (h *Handlers) queueBatches(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	buildingID, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	var body batchCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	b, err := h.svc.QueueBatches(r.Context(), owner, buildingID, in)
	if err != nil {
		h.writeError(w, r, err, "encolando lotes")
		return
	}
	h.writeData(w, r, http.StatusCreated, toBatchJSON(b), "")
}

// ─── DELETE /world/production-batches/{batchId} ───────────────────────────────

func (h *Handlers) cancelBatch(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	batchID, err := uuid.Parse(r.PathValue("batchId"))
	if err != nil {
		notFound(w, "el lote no existe")
		return
	}
	b, err := h.svc.CancelBatch(r.Context(), owner, batchID)
	if err != nil {
		h.writeError(w, r, err, "cancelando el lote")
		return
	}
	h.writeData(w, r, http.StatusOK, toBatchJSON(b), "")
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	switch {
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.Is(err, ErrBuildingNotOperational):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeBuildingNotOperational, err.Error(), nil)
	case errors.Is(err, ErrRecipeNotSupported):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeRecipeNotSupported, err.Error(), nil)
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrBatchNotFound):
		notFound(w, "el lote no existe")
	case errors.Is(err, ErrBuildingNotFound):
		notFound(w, "el edificio no existe")
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrBatchNotCancellable):
		httpx.WriteError(w, http.StatusConflict, codeBatchNotCancellable, err.Error(), nil)
	default:
		// Petición abortada por el cliente o plazo agotado: no es un fallo
		// del servicio y no debe contarse como 5xx ni loguearse como ERROR.
		if httpx.WriteClientGone(w, r, h.logger, err, doing) {
			return
		}
		logging.WithRequestID(h.logger, httpx.RequestIDFromContext(r.Context())).LogAttrs(
			r.Context(), slog.LevelError, "error "+doing, slog.String("error", err.Error()))
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "error interno del servidor", nil)
	}
}

func unauthorized(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized, "sesión ausente o expirada", nil)
}

func notFound(w http.ResponseWriter, msg string) {
	httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, msg, nil)
}

func writeValidationError(w http.ResponseWriter, field, reason string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
		fmt.Sprintf("parámetro %s inválido: %s", field, reason),
		map[string]any{"field": field})
}

// ─── Parsing de query params ─────────────────────────────────────────────────

func parseLimit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("debe ser un entero")
	}
	if n < 1 || int32(n) > MaxPageLimit {
		return 0, fmt.Errorf("debe estar entre 1 y %d", MaxPageLimit)
	}
	return n, nil
}

// ─── DTOs del contrato ───────────────────────────────────────────────────────

type batchJSON struct {
	ID            string   `json:"id"`
	BuildingID    string   `json:"building_id"`
	RecipeID      string   `json:"recipe_id"`
	BatchesQueued int32    `json:"batches_queued"`
	BatchesDone   int32    `json:"batches_done"`
	Status        string   `json:"status"`
	QueuePosition int32    `json:"queue_position"`
	StartedAtSim  *int64   `json:"started_at_sim,omitempty"`
	ProgressPct   *float64 `json:"progress_pct,omitempty"`
	EtaSim        *int64   `json:"eta_sim,omitempty"`
}

func toBatchJSON(b Batch) batchJSON {
	return batchJSON{
		ID:            b.ID.String(),
		BuildingID:    b.BuildingID.String(),
		RecipeID:      b.RecipeID.String(),
		BatchesQueued: b.BatchesQueued,
		BatchesDone:   b.BatchesDone,
		Status:        b.Status,
		QueuePosition: b.QueuePosition,
		StartedAtSim:  b.StartedAtSim,
		ProgressPct:   b.ProgressPct,
		EtaSim:        b.EtaSim,
	}
}

// ─── DTOs de entrada ─────────────────────────────────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

type batchCreateJSON struct {
	RecipeID      string `json:"recipe_id"`
	BatchesQueued *int32 `json:"batches_queued"`
	QueuePosition *int32 `json:"queue_position"`
}

func (b batchCreateJSON) toInput() (BatchInput, *fieldError) {
	recipeID, err := uuid.Parse(b.RecipeID)
	if err != nil {
		return BatchInput{}, &fieldError{"recipe_id", "no es un UUID válido"}
	}
	if b.BatchesQueued == nil {
		return BatchInput{}, &fieldError{"batches_queued", "requerido"}
	}
	if *b.BatchesQueued < 1 {
		return BatchInput{}, &fieldError{"batches_queued", "debe ser >= 1"}
	}
	if b.QueuePosition != nil && *b.QueuePosition < 0 {
		return BatchInput{}, &fieldError{"queue_position", "debe ser >= 0"}
	}
	return BatchInput{
		RecipeID:      recipeID,
		BatchesQueued: *b.BatchesQueued,
		QueuePosition: b.QueuePosition,
	}, nil
}
