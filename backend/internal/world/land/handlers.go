package land

import (
	"context"
	"encoding/json"
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
// la plataforma httpx). El schema Error del contrato es de código abierto
// (type: string): los 409 usan códigos estables descriptivos.
const (
	codeUnauthorized       = "UNAUTHORIZED"
	codeNotResourceOwner   = "NOT_RESOURCE_OWNER"
	codeInsufficientFunds  = "INSUFFICIENT_FUNDS"
	codeConcessionOverlap  = "CONCESSION_OVERLAP"
	codeConcessionReverted = "CONCESSION_REVERTED"
	maxMoneyDigits         = 32
)

// Identity resuelve la cuenta autenticada de una petición. La define este
// subpaquete (SAD §7: sin imports cruzados entre bounded contexts) y la
// implementa el composition root con el middleware de sesión.
type Identity interface {
	AccountID(ctx context.Context) (uuid.UUID, bool)
}

// MetaSource construye los metadatos comunes (schema Meta) de toda respuesta
// exitosa. Lo implementa el composition root con el reloj de simulación.
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// API es la superficie del servicio que consumen los handlers; la implementa
// *Service.
type API interface {
	ListConcessions(ctx context.Context, holder uuid.UUID, f ConcessionFilter) ([]Concession, string, error)
	GetConcession(ctx context.Context, holder, id uuid.UUID) (Concession, error)
	CreateConcession(ctx context.Context, holder uuid.UUID, in ConcessionInput) (Concession, error)
	RenewConcession(ctx context.Context, holder, id uuid.UUID) (Concession, error)
	TransferConcession(ctx context.Context, seller uuid.UUID, in TransferInput) (ConcessionTransfer, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints world/concessions* y world/concession-transfers
// del contrato OpenAPI v1.2.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del subpaquete de suelo.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/concessions", h.listConcessions)
	mux.HandleFunc("POST /world/concessions", h.createConcession)
	mux.HandleFunc("GET /world/concessions/{concessionId}", h.getConcession)
	mux.HandleFunc("POST /world/concessions/{concessionId}/renew", h.renewConcession)
	mux.HandleFunc("POST /world/concession-transfers", h.createTransfer)
}

// ─── GET /world/concessions ──────────────────────────────────────────────────

func (h *Handlers) listConcessions(w http.ResponseWriter, r *http.Request) {
	holder, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := ConcessionFilter{Status: q.Get("status"), Cursor: q.Get("cursor")}
	if id, ok, err := optionalUUID(q, "region_id"); err != nil {
		writeValidationError(w, "region_id", "no es un UUID válido")
		return
	} else if ok {
		filter.RegionID = &id
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	concessions, next, err := h.svc.ListConcessions(r.Context(), holder, filter)
	if err != nil {
		h.writeError(w, r, err, "listando concesiones")
		return
	}
	data := make([]concessionJSON, len(concessions))
	for i, c := range concessions {
		data[i] = toConcessionJSON(c)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── POST /world/concessions ─────────────────────────────────────────────────

func (h *Handlers) createConcession(w http.ResponseWriter, r *http.Request) {
	holder, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body concessionCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	c, err := h.svc.CreateConcession(r.Context(), holder, in)
	if err != nil {
		h.writeError(w, r, err, "otorgando la concesión")
		return
	}
	h.writeData(w, r, http.StatusCreated, toConcessionJSON(c), "")
}

// ─── GET /world/concessions/{id} ─────────────────────────────────────────────

func (h *Handlers) getConcession(w http.ResponseWriter, r *http.Request) {
	holder, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("concessionId"))
	if err != nil {
		notFound(w, "la concesión no existe")
		return
	}
	c, err := h.svc.GetConcession(r.Context(), holder, id)
	if err != nil {
		h.writeError(w, r, err, "consultando la concesión")
		return
	}
	h.writeData(w, r, http.StatusOK, toConcessionJSON(c), "")
}

// ─── POST /world/concessions/{id}/renew ──────────────────────────────────────

func (h *Handlers) renewConcession(w http.ResponseWriter, r *http.Request) {
	holder, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("concessionId"))
	if err != nil {
		notFound(w, "la concesión no existe")
		return
	}
	c, err := h.svc.RenewConcession(r.Context(), holder, id)
	if err != nil {
		h.writeError(w, r, err, "renovando la concesión")
		return
	}
	h.writeData(w, r, http.StatusOK, toConcessionJSON(c), "")
}

// ─── POST /world/concession-transfers ────────────────────────────────────────

func (h *Handlers) createTransfer(w http.ResponseWriter, r *http.Request) {
	seller, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body transferCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	t, err := h.svc.TransferConcession(r.Context(), seller, in)
	if err != nil {
		h.writeError(w, r, err, "traspasando la concesión")
		return
	}
	h.writeData(w, r, http.StatusCreated, toTransferJSON(t), "")
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	var fundsErr *FundsError
	switch {
	case errors.As(err, &fundsErr):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientFunds, fundsErr.Error(),
			map[string]any{"required": fixed(fundsErr.Required), "available": fixed(fundsErr.Available)})
	case errors.Is(err, ErrInsufficientFunds):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientFunds, err.Error(), nil)
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrConcessionNotFound):
		notFound(w, "la concesión no existe")
	case errors.Is(err, ErrNotHolder):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrParcelOverlap):
		httpx.WriteError(w, http.StatusConflict, codeConcessionOverlap, err.Error(), nil)
	case errors.Is(err, ErrConcessionReverted):
		httpx.WriteError(w, http.StatusConflict, codeConcessionReverted, err.Error(), nil)
	default:
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

func optionalUUID(q url.Values, name string) (uuid.UUID, bool, error) {
	raw := q.Get(name)
	if raw == "" {
		return uuid.UUID{}, false, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, false, err
	}
	return id, true, nil
}

// ─── DTOs del contrato ───────────────────────────────────────────────────────

type concessionJSON struct {
	ID              string          `json:"id"`
	RegionID        string          `json:"region_id"`
	HolderAccountID string          `json:"holder_account_id"`
	Parcel          json.RawMessage `json:"parcel,omitempty"`
	CanonAmount     string          `json:"canon_amount"`
	PeriodSimDays   int32           `json:"period_sim_days"`
	ExpiresAtSim    int64           `json:"expires_at_sim"`
	Status          string          `json:"status"`
	GrantedAtSim    int64           `json:"granted_at_sim"`
}

func toConcessionJSON(c Concession) concessionJSON {
	return concessionJSON{
		ID:              c.ID.String(),
		RegionID:        c.RegionID.String(),
		HolderAccountID: c.HolderAccountID.String(),
		Parcel:          rawGeo(c.Parcel),
		CanonAmount:     fixed(c.CanonAmount),
		PeriodSimDays:   c.PeriodSimDays,
		ExpiresAtSim:    c.ExpiresAtSim,
		Status:          c.Status,
		GrantedAtSim:    c.GrantedAtSim,
	}
}

type transferJSON struct {
	ID            string `json:"id"`
	ConcessionID  string `json:"concession_id"`
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	Price         string `json:"price"`
	SystemFee     string `json:"system_fee"`
	OccurredAtSim int64  `json:"occurred_at_sim"`
}

func toTransferJSON(t ConcessionTransfer) transferJSON {
	return transferJSON{
		ID:            t.ID.String(),
		ConcessionID:  t.ConcessionID.String(),
		FromAccountID: t.FromAccountID.String(),
		ToAccountID:   t.ToAccountID.String(),
		Price:         fixed(t.Price),
		SystemFee:     fixed(t.SystemFee),
		OccurredAtSim: t.OccurredAtSim,
	}
}

// ─── DTOs de entrada ─────────────────────────────────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

type concessionCreateJSON struct {
	RegionID string          `json:"region_id"`
	Parcel   json.RawMessage `json:"parcel"`
}

func (b concessionCreateJSON) toInput() (ConcessionInput, *fieldError) {
	regionID, err := uuid.Parse(b.RegionID)
	if err != nil {
		return ConcessionInput{}, &fieldError{"region_id", "no es un UUID válido"}
	}
	if err := validatePolygon(b.Parcel); err != nil {
		return ConcessionInput{}, &fieldError{"parcel", err.Error()}
	}
	return ConcessionInput{RegionID: regionID, Parcel: []byte(b.Parcel)}, nil
}

type transferCreateJSON struct {
	ConcessionID string `json:"concession_id"`
	ToAccountID  string `json:"to_account_id"`
	Price        string `json:"price"`
}

func (b transferCreateJSON) toInput() (TransferInput, *fieldError) {
	concessionID, err := uuid.Parse(b.ConcessionID)
	if err != nil {
		return TransferInput{}, &fieldError{"concession_id", "no es un UUID válido"}
	}
	toID, err := uuid.Parse(b.ToAccountID)
	if err != nil {
		return TransferInput{}, &fieldError{"to_account_id", "no es un UUID válido"}
	}
	price, err := parseFixed(b.Price)
	if err != nil {
		return TransferInput{}, &fieldError{"price", err.Error()}
	}
	return TransferInput{ConcessionID: concessionID, ToAccountID: toID, Price: price}, nil
}

// ─── Helpers de serialización y validación ───────────────────────────────────

// rawGeo embebe un GeoJSON plano (de ST_AsGeoJSON) como objeto JSON; vacío se
// omite (omitempty sobre RawMessage nil).
func rawGeo(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// parseFixed interpreta un importe de punto fijo (string de dígitos) a int64,
// rechazando floats, signos y desbordamiento.
func parseFixed(raw string) (int64, error) {
	if raw == "" {
		return 0, errors.New("requerido")
	}
	if len(raw) > maxMoneyDigits {
		return 0, errors.New("importe demasiado largo")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("debe ser un entero de punto fijo (string de dígitos, sin decimales)")
	}
	if n < 0 {
		return 0, errors.New("debe ser >= 0")
	}
	return n, nil
}

// validatePolygon valida la forma de un GeoPolygon del contrato (type Polygon,
// anillos cerrados de >= 4 vértices con coordenadas planas [x_m, y_m]) antes de
// proyectarlo en la BD, para responder 400 en lugar de un 500 de PostGIS.
func validatePolygon(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("requerido")
	}
	var g struct {
		Type        string        `json:"type"`
		Coordinates [][][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return errors.New("no es un objeto GeoJSON válido")
	}
	if g.Type != "Polygon" {
		return errors.New("type debe ser Polygon")
	}
	if len(g.Coordinates) == 0 {
		return errors.New("coordinates no puede estar vacío")
	}
	for _, ring := range g.Coordinates {
		if len(ring) < 4 {
			return errors.New("cada anillo requiere al menos 4 vértices (cerrado)")
		}
		first, last := ring[0], ring[len(ring)-1]
		if len(first) < 2 || len(last) < 2 || first[0] != last[0] || first[1] != last[1] {
			return errors.New("el anillo debe estar cerrado (primer y último vértice iguales)")
		}
	}
	return nil
}
