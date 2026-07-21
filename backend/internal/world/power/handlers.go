package power

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

// Códigos de error del contrato emitidos por este subpaquete.
const (
	codeUnauthorized      = "UNAUTHORIZED"
	codeNotResourceOwner  = "NOT_RESOURCE_OWNER"
	codeInsufficientFunds = "INSUFFICIENT_FUNDS"
	codePlacementInvalid  = "PLACEMENT_INVALID"
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

// API es la superficie del servicio que consumen los handlers.
type API interface {
	CreatePowerLine(ctx context.Context, owner uuid.UUID, in PowerLineInput) (PowerLine, error)
	GetPowerLine(ctx context.Context, id uuid.UUID) (PowerLine, error)
	ListPowerLines(ctx context.Context, f LineFilter) ([]PowerLine, string, error)
	SetOffer(ctx context.Context, owner, buildingID uuid.UUID, unitPrice int64) error
	SetBid(ctx context.Context, owner, buildingID uuid.UUID, unitPrice int64) error
	ListSpotTicks(ctx context.Context, region uuid.UUID, beforeSim *int64, limit int) ([]SpotTick, error)
	ListDispatches(ctx context.Context, owner, buildingID uuid.UUID, beforeSim *int64, limit int) ([]Dispatch, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints world/power* del contrato OpenAPI v1.6.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del subpaquete power.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/power-lines", h.listLines)
	mux.HandleFunc("POST /world/power-lines", h.createLine)
	mux.HandleFunc("GET /world/power-lines/{powerLineId}", h.getLine)
	mux.HandleFunc("PUT /world/power-plants/{buildingId}/offer", h.putOffer)
	mux.HandleFunc("PUT /world/buildings/{buildingId}/power-bid", h.putBid)
	mux.HandleFunc("GET /world/power/spot", h.listSpot)
	mux.HandleFunc("GET /world/power/dispatches", h.listDispatches)
}

// ─── Líneas ──────────────────────────────────────────────────────────────────

func (h *Handlers) listLines(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := LineFilter{Cursor: q.Get("cursor")}
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
	lines, next, err := h.svc.ListPowerLines(r.Context(), filter)
	if err != nil {
		h.writeError(w, r, err, "listando líneas eléctricas")
		return
	}
	out := make([]powerLineJSON, 0, len(lines))
	for _, l := range lines {
		out = append(out, toPowerLineJSON(l))
	}
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, http.StatusOK, out, meta)
}

func (h *Handlers) createLine(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body struct {
		Path json.RawMessage `json:"path"`
	}
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	line, err := h.svc.CreatePowerLine(r.Context(), owner, PowerLineInput{Path: body.Path})
	if err != nil {
		h.writeError(w, r, err, "creando la línea eléctrica")
		return
	}
	httpx.WriteData(w, http.StatusCreated, toPowerLineJSON(line), h.meta.Meta(r.Context()))
}

func (h *Handlers) getLine(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("powerLineId"))
	if err != nil {
		notFound(w, "la línea no existe")
		return
	}
	line, err := h.svc.GetPowerLine(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err, "leyendo la línea eléctrica")
		return
	}
	httpx.WriteData(w, http.StatusOK, toPowerLineJSON(line), h.meta.Meta(r.Context()))
}

// ─── Oferta y puja ───────────────────────────────────────────────────────────

func (h *Handlers) putOffer(w http.ResponseWriter, r *http.Request) {
	h.putPrice(w, r, h.svc.SetOffer)
}

func (h *Handlers) putBid(w http.ResponseWriter, r *http.Request) {
	h.putPrice(w, r, h.svc.SetBid)
}

func (h *Handlers) putPrice(w http.ResponseWriter, r *http.Request, apply func(context.Context, uuid.UUID, uuid.UUID, int64) error) {
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
	var body struct {
		UnitPrice string `json:"unit_price"`
	}
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	price, err := strconv.ParseInt(body.UnitPrice, 10, 64)
	if err != nil {
		writeValidationError(w, "unit_price", "debe ser un entero de punto fijo serializado como string")
		return
	}
	if err := apply(r.Context(), owner, buildingID, price); err != nil {
		h.writeError(w, r, err, "fijando el precio eléctrico")
		return
	}
	httpx.WriteData(w, http.StatusOK, priceJSON{
		BuildingID: buildingID.String(),
		UnitPrice:  fixed(price),
	}, h.meta.Meta(r.Context()))
}

// ─── Spot y despachos ────────────────────────────────────────────────────────

func (h *Handlers) listSpot(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	region, ok, err := optionalUUID(q, "region_id")
	if err != nil || !ok {
		writeValidationError(w, "region_id", "es obligatorio y debe ser un UUID válido")
		return
	}
	beforeSim, err := optionalInt64(q, "before_sim")
	if err != nil {
		writeValidationError(w, "before_sim", "debe ser un entero")
		return
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	ticks, err := h.svc.ListSpotTicks(r.Context(), region, beforeSim, limit)
	if err != nil {
		h.writeError(w, r, err, "listando el histórico del spot")
		return
	}
	out := make([]spotTickJSON, 0, len(ticks))
	for _, t := range ticks {
		out = append(out, toSpotTickJSON(t))
	}
	httpx.WriteData(w, http.StatusOK, out, h.meta.Meta(r.Context()))
}

func (h *Handlers) listDispatches(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	building, ok, err := optionalUUID(q, "building_id")
	if err != nil || !ok {
		writeValidationError(w, "building_id", "es obligatorio y debe ser un UUID válido")
		return
	}
	beforeSim, err := optionalInt64(q, "before_sim")
	if err != nil {
		writeValidationError(w, "before_sim", "debe ser un entero")
		return
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	items, err := h.svc.ListDispatches(r.Context(), owner, building, beforeSim, limit)
	if err != nil {
		h.writeError(w, r, err, "listando despachos eléctricos")
		return
	}
	out := make([]dispatchJSON, 0, len(items))
	for _, d := range items {
		out = append(out, toDispatchJSON(d))
	}
	httpx.WriteData(w, http.StatusOK, out, h.meta.Meta(r.Context()))
}

// ─── Errores ─────────────────────────────────────────────────────────────────

func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	var placementErr *PlacementError
	var fundsErr *FundsError
	switch {
	case errors.As(err, &placementErr):
		details := map[string]any{"rule": placementErr.Rule}
		for k, v := range placementErr.Details {
			details[k] = v
		}
		httpx.WriteError(w, http.StatusUnprocessableEntity, codePlacementInvalid, placementErr.Error(), details)
	case errors.As(err, &fundsErr):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientFunds, fundsErr.Error(),
			map[string]any{"required": fixed(fundsErr.Required), "available": fixed(fundsErr.Available)})
	case errors.Is(err, ErrInvalidCursor):
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrNotFound):
		notFound(w, err.Error())
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

func optionalInt64(q url.Values, name string) (*int64, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// ─── DTOs del contrato ───────────────────────────────────────────────────────

type powerLineJSON struct {
	ID                      string          `json:"id"`
	OwnerAccountID          string          `json:"owner_account_id"`
	RegionID                string          `json:"region_id"`
	Path                    json.RawMessage `json:"path"`
	LengthM                 int32           `json:"length_m"`
	Status                  string          `json:"status"`
	ConditionPct            int32           `json:"condition_pct"`
	MaintenancePaidUntilSim int64           `json:"maintenance_paid_until_sim"`
	UpdatedAtSim            int64           `json:"updated_at_sim"`
}

func toPowerLineJSON(l PowerLine) powerLineJSON {
	return powerLineJSON{
		ID:                      l.ID.String(),
		OwnerAccountID:          l.OwnerAccountID.String(),
		RegionID:                l.RegionID.String(),
		Path:                    json.RawMessage(l.PathGeoJSON),
		LengthM:                 l.LengthM,
		Status:                  l.Status,
		ConditionPct:            l.ConditionPct,
		MaintenancePaidUntilSim: l.MaintenancePaidUntilSim,
		UpdatedAtSim:            l.UpdatedAtSim,
	}
}

type priceJSON struct {
	BuildingID string `json:"building_id"`
	UnitPrice  string `json:"unit_price"`
}

type spotTickJSON struct {
	RegionID           string `json:"region_id"`
	TickSim            int64  `json:"tick_sim"`
	IntervalSim        int64  `json:"interval_sim"`
	ClosingPrice       string `json:"closing_price"`
	DemandUnits        string `json:"demand_units"`
	SuppliedUnits      string `json:"supplied_units"`
	CurtailedUnits     string `json:"curtailed_units"`
	CurtailedBuildings int32  `json:"curtailed_buildings"`
}

func toSpotTickJSON(t SpotTick) spotTickJSON {
	return spotTickJSON{
		RegionID:           t.RegionID.String(),
		TickSim:            t.TickSim,
		IntervalSim:        t.IntervalSim,
		ClosingPrice:       fixed(t.ClosingPrice),
		DemandUnits:        fixed(t.DemandUnits),
		SuppliedUnits:      fixed(t.SuppliedUnits),
		CurtailedUnits:     fixed(t.CurtailedUnits),
		CurtailedBuildings: t.CurtailedBuildings,
	}
}

type dispatchJSON struct {
	RegionID   string `json:"region_id"`
	TickSim    int64  `json:"tick_sim"`
	BuildingID string `json:"building_id"`
	Role       string `json:"role"`
	Units      string `json:"units"`
	UnitPrice  string `json:"unit_price"`
	Amount     string `json:"amount"`
}

func toDispatchJSON(d Dispatch) dispatchJSON {
	return dispatchJSON{
		RegionID:   d.RegionID.String(),
		TickSim:    d.TickSim,
		BuildingID: d.BuildingID.String(),
		Role:       d.Role,
		Units:      fixed(d.Units),
		UnitPrice:  fixed(d.UnitPrice),
		Amount:     fixed(d.Amount),
	}
}

// fixed serializa dinero/stock como string de punto fijo (jamás floats).
func fixed(v int64) string { return strconv.FormatInt(v, 10) }
