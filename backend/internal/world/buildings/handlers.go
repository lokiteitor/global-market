package buildings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
)

// Códigos de error del contrato emitidos por este subpaquete (además de los de
// la plataforma httpx). El schema Error es de código abierto (type: string): el
// 409 de nivel máximo usa un código estable descriptivo.
const (
	codeUnauthorized      = "UNAUTHORIZED"
	codeNotResourceOwner  = "NOT_RESOURCE_OWNER"
	codeInsufficientFunds = "INSUFFICIENT_FUNDS"
	codePlacementInvalid  = "PLACEMENT_INVALID"
	codeMaxLevelReached   = "MAX_LEVEL_REACHED"
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
	ListBuildings(ctx context.Context, owner uuid.UUID, f BuildingFilter) ([]Building, string, error)
	GetBuilding(ctx context.Context, owner, id uuid.UUID) (Building, error)
	CreateBuilding(ctx context.Context, owner uuid.UUID, in BuildingInput) (Building, error)
	UpdateBuilding(ctx context.Context, owner, id uuid.UUID, in BuildingUpdateInput) (Building, error)
	UpgradeBuilding(ctx context.Context, owner, id uuid.UUID) (Building, error)
	ListInventory(ctx context.Context, owner, id uuid.UUID) ([]InventoryItem, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints world/buildings* del contrato OpenAPI v1.2.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del subpaquete de edificios.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/buildings", h.listBuildings)
	mux.HandleFunc("POST /world/buildings", h.createBuilding)
	mux.HandleFunc("GET /world/buildings/{buildingId}", h.getBuilding)
	mux.HandleFunc("PATCH /world/buildings/{buildingId}", h.updateBuilding)
	mux.HandleFunc("POST /world/buildings/{buildingId}/upgrade", h.upgradeBuilding)
	mux.HandleFunc("GET /world/buildings/{buildingId}/inventory", h.getInventory)
}

// ─── GET /world/buildings ────────────────────────────────────────────────────

func (h *Handlers) listBuildings(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := BuildingFilter{Status: q.Get("status"), Cursor: q.Get("cursor")}
	if id, ok, err := optionalUUID(q, "region_id"); err != nil {
		writeValidationError(w, "region_id", "no es un UUID válido")
		return
	} else if ok {
		filter.RegionID = &id
	}
	if id, ok, err := optionalUUID(q, "building_type_id"); err != nil {
		writeValidationError(w, "building_type_id", "no es un UUID válido")
		return
	} else if ok {
		filter.BuildingTypeID = &id
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	buildings, next, err := h.svc.ListBuildings(r.Context(), owner, filter)
	if err != nil {
		h.writeError(w, r, err, "listando edificios")
		return
	}
	data := make([]buildingJSON, len(buildings))
	for i, b := range buildings {
		data[i] = toBuildingJSON(b)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── POST /world/buildings ───────────────────────────────────────────────────

func (h *Handlers) createBuilding(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body buildingCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	b, err := h.svc.CreateBuilding(r.Context(), owner, in)
	if err != nil {
		h.writeError(w, r, err, "construyendo el edificio")
		return
	}
	h.writeData(w, r, http.StatusCreated, toBuildingJSON(b), "")
}

// ─── GET /world/buildings/{id} ───────────────────────────────────────────────

func (h *Handlers) getBuilding(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	b, err := h.svc.GetBuilding(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el edificio")
		return
	}
	h.writeData(w, r, http.StatusOK, toBuildingJSON(b), "")
}

// ─── PATCH /world/buildings/{id} ─────────────────────────────────────────────

func (h *Handlers) updateBuilding(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	var body buildingUpdateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	b, err := h.svc.UpdateBuilding(r.Context(), owner, id, in)
	if err != nil {
		h.writeError(w, r, err, "actualizando el edificio")
		return
	}
	h.writeData(w, r, http.StatusOK, toBuildingJSON(b), "")
}

// ─── POST /world/buildings/{id}/upgrade ──────────────────────────────────────

func (h *Handlers) upgradeBuilding(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	b, err := h.svc.UpgradeBuilding(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "mejorando el edificio")
		return
	}
	h.writeData(w, r, http.StatusOK, toBuildingJSON(b), "")
}

// ─── GET /world/buildings/{id}/inventory ─────────────────────────────────────

func (h *Handlers) getInventory(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("buildingId"))
	if err != nil {
		notFound(w, "el edificio no existe")
		return
	}
	items, err := h.svc.ListInventory(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el inventario")
		return
	}
	data := make([]inventoryItemJSON, len(items))
	for i, it := range items {
		data[i] = toInventoryItemJSON(it)
	}
	h.writeData(w, r, http.StatusOK, data, "")
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

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
	case errors.Is(err, ErrInsufficientFunds):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientFunds, err.Error(), nil)
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrBuildingNotFound):
		notFound(w, "el edificio no existe")
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrMaxLevelReached):
		httpx.WriteError(w, http.StatusConflict, codeMaxLevelReached, err.Error(), nil)
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

// ─── DTOs del contrato ───────────────────────────────────────────────────────

type buildingJSON struct {
	ID             string          `json:"id"`
	OwnerAccountID string          `json:"owner_account_id"`
	RegionID       string          `json:"region_id"`
	ConcessionID   string          `json:"concession_id"`
	BuildingTypeID string          `json:"building_type_id"`
	Footprint      json.RawMessage `json:"footprint,omitempty"`
	Level          int32           `json:"level"`
	Status         string          `json:"status"`
	ActiveRecipeID string          `json:"active_recipe_id,omitempty"`
	ConditionPct   int32           `json:"condition_pct"`
	FuelStock      string          `json:"fuel_stock"`
	UpdatedAtSim   int64           `json:"updated_at_sim"`
	CreatedAt      time.Time       `json:"created_at"`
}

func toBuildingJSON(b Building) buildingJSON {
	return buildingJSON{
		ID:             b.ID.String(),
		OwnerAccountID: b.OwnerAccountID.String(),
		RegionID:       b.RegionID.String(),
		ConcessionID:   b.ConcessionID.String(),
		BuildingTypeID: b.BuildingTypeID.String(),
		Footprint:      rawGeo(b.Footprint),
		Level:          b.Level,
		Status:         b.Status,
		ActiveRecipeID: uuidOrEmpty(b.ActiveRecipeID),
		ConditionPct:   b.ConditionPct,
		FuelStock:      fixed(b.FuelStock),
		UpdatedAtSim:   b.UpdatedAtSim,
		CreatedAt:      b.CreatedAt,
	}
}

type inventoryItemJSON struct {
	BuildingID   string `json:"building_id"`
	ProductID    string `json:"product_id"`
	Quantity     string `json:"quantity"`
	UpdatedAtSim int64  `json:"updated_at_sim"`
}

func toInventoryItemJSON(it InventoryItem) inventoryItemJSON {
	return inventoryItemJSON{
		BuildingID:   it.BuildingID.String(),
		ProductID:    it.ProductID.String(),
		Quantity:     fixed(it.Quantity),
		UpdatedAtSim: it.UpdatedAtSim,
	}
}

// ─── DTOs de entrada ─────────────────────────────────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

type buildingCreateJSON struct {
	BuildingTypeID string          `json:"building_type_id"`
	ConcessionID   string          `json:"concession_id"`
	Footprint      json.RawMessage `json:"footprint"`
}

func (b buildingCreateJSON) toInput() (BuildingInput, *fieldError) {
	buildingTypeID, err := uuid.Parse(b.BuildingTypeID)
	if err != nil {
		return BuildingInput{}, &fieldError{"building_type_id", "no es un UUID válido"}
	}
	concessionID, err := uuid.Parse(b.ConcessionID)
	if err != nil {
		return BuildingInput{}, &fieldError{"concession_id", "no es un UUID válido"}
	}
	if err := validatePolygon(b.Footprint); err != nil {
		return BuildingInput{}, &fieldError{"footprint", err.Error()}
	}
	return BuildingInput{
		BuildingTypeID: buildingTypeID,
		ConcessionID:   concessionID,
		Footprint:      []byte(b.Footprint),
	}, nil
}

type buildingUpdateJSON struct {
	// ActiveRecipeID es json.RawMessage para distinguir ausente (nil) de null
	// ("null", detener línea) de un UUID.
	ActiveRecipeID   json.RawMessage `json:"active_recipe_id"`
	StartMaintenance *bool           `json:"start_maintenance"`
}

func (b buildingUpdateJSON) toInput() (BuildingUpdateInput, *fieldError) {
	var in BuildingUpdateInput
	if b.ActiveRecipeID != nil {
		in.SetRecipe = true
		if strings.TrimSpace(string(b.ActiveRecipeID)) != "null" {
			var raw string
			if err := json.Unmarshal(b.ActiveRecipeID, &raw); err != nil {
				return BuildingUpdateInput{}, &fieldError{"active_recipe_id", "debe ser un UUID o null"}
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				return BuildingUpdateInput{}, &fieldError{"active_recipe_id", "no es un UUID válido"}
			}
			in.RecipeID = &id
		}
	}
	if b.StartMaintenance != nil {
		in.StartMaintenance = *b.StartMaintenance
	}
	// minProperties: 1 — al menos un campo presente.
	if b.ActiveRecipeID == nil && b.StartMaintenance == nil {
		return BuildingUpdateInput{}, &fieldError{"body", "se requiere active_recipe_id o start_maintenance"}
	}
	return in, nil
}

// ─── Helpers de serialización y validación ───────────────────────────────────

// rawGeo embebe un GeoJSON plano (de ST_AsGeoJSON) como objeto JSON; vacío se
// omite.
func rawGeo(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// validatePolygon valida la forma de un GeoPolygon del contrato antes de
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
