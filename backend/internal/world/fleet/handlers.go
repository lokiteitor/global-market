package fleet

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
// la plataforma httpx). El schema Error es de código abierto (type: string).
const (
	codeUnauthorized            = "UNAUTHORIZED"
	codeNotResourceOwner        = "NOT_RESOURCE_OWNER"
	codeVehicleSealed           = "VEHICLE_SEALED"
	codeInsufficientFunds       = "INSUFFICIENT_FUNDS"
	codeVehicleNotIdle          = "VEHICLE_NOT_IDLE"
	codeShipmentNotDispatchable = "SHIPMENT_NOT_DISPATCHABLE"
	codeSlotHeld                = "SLOT_HELD"
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
	ListVehicleTypes(ctx context.Context, f VehicleTypeFilter) ([]VehicleType, string, error)
	ListVehicles(ctx context.Context, owner uuid.UUID, f VehicleFilter) ([]Vehicle, string, error)
	GetVehicle(ctx context.Context, owner, id uuid.UUID) (Vehicle, error)
	PurchaseVehicle(ctx context.Context, owner uuid.UUID, in VehiclePurchase) (Vehicle, error)
	UpdateVehicle(ctx context.Context, owner, id uuid.UUID, in VehicleUpdate) (Vehicle, error)
	RepositionVehicle(ctx context.Context, owner, id uuid.UUID, in VehicleReposition) (Vehicle, error)
	ListShipments(ctx context.Context, owner uuid.UUID, f ShipmentFilter) ([]Shipment, string, error)
	GetShipment(ctx context.Context, owner, id uuid.UUID) (Shipment, error)
	DispatchShipment(ctx context.Context, owner, shipmentID uuid.UUID, in ShipmentDispatch) (Shipment, error)
	GetTerminal(ctx context.Context, id uuid.UUID) (Terminal, error)
	ListTerminalSlots(ctx context.Context, terminalID uuid.UUID, onlyAvailable bool) ([]TerminalSlot, error)
	PurchaseSlot(ctx context.Context, buyer, slotID uuid.UUID) (TerminalSlot, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints world fleet/shipments del contrato v1.5.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del subpaquete de flota.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/vehicle-types", h.listVehicleTypes)
	mux.HandleFunc("GET /world/vehicles", h.listVehicles)
	mux.HandleFunc("POST /world/vehicles", h.purchaseVehicle)
	mux.HandleFunc("GET /world/vehicles/{vehicleId}", h.getVehicle)
	mux.HandleFunc("PATCH /world/vehicles/{vehicleId}", h.updateVehicle)
	mux.HandleFunc("POST /world/vehicles/{vehicleId}/reposition", h.repositionVehicle)
	mux.HandleFunc("GET /world/shipments", h.listShipments)
	mux.HandleFunc("GET /world/shipments/{shipmentId}", h.getShipment)
	mux.HandleFunc("POST /world/shipments/{shipmentId}/dispatch", h.dispatchShipment)
	mux.HandleFunc("GET /world/terminals/{terminalId}", h.getTerminal)
	mux.HandleFunc("GET /world/terminals/{terminalId}/slots", h.listTerminalSlots)
	mux.HandleFunc("POST /world/terminal-slots/{slotId}/purchase", h.purchaseSlot)
}

// ─── GET /world/vehicle-types ─────────────────────────────────────────────────

func (h *Handlers) listVehicleTypes(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	types, next, err := h.svc.ListVehicleTypes(r.Context(), VehicleTypeFilter{Mode: q.Get("mode"), Cursor: q.Get("cursor"), Limit: limit})
	if err != nil {
		h.writeError(w, r, err, "listando tipos de vehículo")
		return
	}
	data := make([]vehicleTypeJSON, len(types))
	for i, t := range types {
		data[i] = toVehicleTypeJSON(t)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── GET /world/vehicles ──────────────────────────────────────────────────────

func (h *Handlers) listVehicles(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	f := VehicleFilter{Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: limit}
	if raw := q.Get("route_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			writeValidationError(w, "route_id", "no es un UUID válido")
			return
		}
		f.RouteID = &id
	}
	vehicles, next, err := h.svc.ListVehicles(r.Context(), owner, f)
	if err != nil {
		h.writeError(w, r, err, "listando vehículos")
		return
	}
	data := make([]vehicleJSON, len(vehicles))
	for i, v := range vehicles {
		data[i] = toVehicleJSON(v)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── POST /world/vehicles ─────────────────────────────────────────────────────

func (h *Handlers) purchaseVehicle(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body vehiclePurchaseJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	v, err := h.svc.PurchaseVehicle(r.Context(), owner, in)
	if err != nil {
		h.writeError(w, r, err, "comprando vehículo")
		return
	}
	h.writeData(w, r, http.StatusCreated, toVehicleJSON(v), "")
}

// ─── GET /world/vehicles/{id} ─────────────────────────────────────────────────

func (h *Handlers) getVehicle(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("vehicleId"))
	if err != nil {
		notFound(w, "el vehículo no existe")
		return
	}
	v, err := h.svc.GetVehicle(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el vehículo")
		return
	}
	h.writeData(w, r, http.StatusOK, toVehicleJSON(v), "")
}

// ─── PATCH /world/vehicles/{id} ───────────────────────────────────────────────

func (h *Handlers) updateVehicle(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("vehicleId"))
	if err != nil {
		notFound(w, "el vehículo no existe")
		return
	}
	var body vehicleUpdateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	v, err := h.svc.UpdateVehicle(r.Context(), owner, id, in)
	if err != nil {
		h.writeError(w, r, err, "actualizando el vehículo")
		return
	}
	h.writeData(w, r, http.StatusOK, toVehicleJSON(v), "")
}

// ─── POST /world/vehicles/{id}/reposition ─────────────────────────────────────

func (h *Handlers) repositionVehicle(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("vehicleId"))
	if err != nil {
		notFound(w, "el vehículo no existe")
		return
	}
	var body vehicleRepositionJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	v, err := h.svc.RepositionVehicle(r.Context(), owner, id, in)
	if err != nil {
		h.writeError(w, r, err, "reposicionando el vehículo en vacío")
		return
	}
	h.writeData(w, r, http.StatusOK, toVehicleJSON(v), "")
}

// ─── GET /world/shipments ─────────────────────────────────────────────────────

func (h *Handlers) listShipments(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	f := ShipmentFilter{Status: q.Get("status"), Cursor: q.Get("cursor"), Limit: limit}
	if raw := q.Get("contract_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			writeValidationError(w, "contract_id", "no es un UUID válido")
			return
		}
		f.ContractID = &id
	}
	if raw := q.Get("freight_contract_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			writeValidationError(w, "freight_contract_id", "no es un UUID válido")
			return
		}
		f.FreightContractID = &id
	}
	if raw := q.Get("vehicle_id"); raw != "" {
		id, perr := uuid.Parse(raw)
		if perr != nil {
			writeValidationError(w, "vehicle_id", "no es un UUID válido")
			return
		}
		f.VehicleID = &id
	}
	shipments, next, err := h.svc.ListShipments(r.Context(), owner, f)
	if err != nil {
		h.writeError(w, r, err, "listando cargamentos")
		return
	}
	data := make([]shipmentJSON, len(shipments))
	for i, sh := range shipments {
		data[i] = toShipmentJSON(sh)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── GET /world/shipments/{id} ────────────────────────────────────────────────

func (h *Handlers) getShipment(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("shipmentId"))
	if err != nil {
		notFound(w, "el cargamento no existe")
		return
	}
	sh, err := h.svc.GetShipment(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el cargamento")
		return
	}
	h.writeData(w, r, http.StatusOK, toShipmentJSON(sh), "")
}

// ─── POST /world/shipments/{id}/dispatch ──────────────────────────────────────

func (h *Handlers) dispatchShipment(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("shipmentId"))
	if err != nil {
		notFound(w, "el cargamento no existe")
		return
	}
	var body shipmentDispatchJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	sh, err := h.svc.DispatchShipment(r.Context(), owner, id, in)
	if err != nil {
		h.writeError(w, r, err, "despachando el cargamento")
		return
	}
	h.writeData(w, r, http.StatusOK, toShipmentJSON(sh), "")
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

// ─── GET /world/terminals/{terminalId} ────────────────────────────────────────

func (h *Handlers) getTerminal(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("terminalId"))
	if err != nil {
		notFound(w, "la terminal no existe")
		return
	}
	t, err := h.svc.GetTerminal(r.Context(), id)
	if err != nil {
		h.writeError(w, r, err, "consultando la terminal")
		return
	}
	h.writeData(w, r, http.StatusOK, toTerminalJSON(t), "")
}

// ─── GET /world/terminals/{terminalId}/slots ──────────────────────────────────

func (h *Handlers) listTerminalSlots(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("terminalId"))
	if err != nil {
		notFound(w, "la terminal no existe")
		return
	}
	onlyAvailable := r.URL.Query().Get("only_available") == "true"
	slots, err := h.svc.ListTerminalSlots(r.Context(), id, onlyAvailable)
	if err != nil {
		h.writeError(w, r, err, "listando slots de la terminal")
		return
	}
	data := make([]terminalSlotJSON, len(slots))
	for i, s := range slots {
		data[i] = toTerminalSlotJSON(s)
	}
	h.writeData(w, r, http.StatusOK, data, "")
}

// ─── POST /world/terminal-slots/{slotId}/purchase ─────────────────────────────

func (h *Handlers) purchaseSlot(w http.ResponseWriter, r *http.Request) {
	buyer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("slotId"))
	if err != nil {
		notFound(w, "el slot no existe")
		return
	}
	slot, err := h.svc.PurchaseSlot(r.Context(), buyer, id)
	if err != nil {
		h.writeError(w, r, err, "comprando el slot de prioridad")
		return
	}
	h.writeData(w, r, http.StatusOK, toTerminalSlotJSON(slot), "")
}

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	var funds *FundsError
	switch {
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.As(err, &funds):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientFunds, funds.Error(),
			map[string]any{"required": strconv.FormatInt(funds.Required, 10), "available": strconv.FormatInt(funds.Available, 10)})
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrVehicleNotFound):
		notFound(w, "el vehículo no existe")
	case errors.Is(err, ErrShipmentNotFound):
		notFound(w, "el cargamento no existe")
	case errors.Is(err, ErrNotFound):
		notFound(w, err.Error())
	case errors.Is(err, ErrVehicleSealed):
		httpx.WriteError(w, http.StatusForbidden, codeVehicleSealed, err.Error(), nil)
	case errors.Is(err, ErrForbidden):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrVehicleNotIdle):
		httpx.WriteError(w, http.StatusConflict, codeVehicleNotIdle, err.Error(), nil)
	case errors.Is(err, ErrShipmentNotDispatchable):
		httpx.WriteError(w, http.StatusConflict, codeShipmentNotDispatchable, err.Error(), nil)
	case errors.Is(err, ErrSlotHeld):
		httpx.WriteError(w, http.StatusConflict, codeSlotHeld, err.Error(), nil)
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

// ─── DTOs de salida ──────────────────────────────────────────────────────────

type vehicleTypeJSON struct {
	ID                  string `json:"id"`
	Code                string `json:"code"`
	Name                string `json:"name"`
	Mode                string `json:"mode"`
	CargoCapacity       string `json:"cargo_capacity"`
	SpeedKmh            int32  `json:"speed_kmh"`
	FuelProductID       string `json:"fuel_product_id"`
	FuelPer100km        string `json:"fuel_per_100km"`
	AutonomyKm          int32  `json:"autonomy_km"`
	PurchasePrice       string `json:"purchase_price"`
	OperatingCostPerDay string `json:"operating_cost_per_day"`
}

func toVehicleTypeJSON(t VehicleType) vehicleTypeJSON {
	return vehicleTypeJSON{
		ID: t.ID.String(), Code: t.Code, Name: t.Name, Mode: t.Mode,
		CargoCapacity: fixed(t.CargoCapacity), SpeedKmh: t.SpeedKmh, FuelProductID: t.FuelProductID.String(),
		FuelPer100km: fixed(t.FuelPer100km), AutonomyKm: t.AutonomyKm,
		PurchasePrice: fixed(t.PurchasePrice), OperatingCostPerDay: fixed(t.OperatingCostPerDay),
	}
}

type positionJSON struct {
	AtNodeID           string          `json:"at_node_id,omitempty"`
	OnSegmentID        string          `json:"on_segment_id,omitempty"`
	SegmentProgressPct *float64        `json:"segment_progress_pct,omitempty"`
	Location           json.RawMessage `json:"location,omitempty"`
}

type vehicleJSON struct {
	ID             string       `json:"id"`
	VehicleTypeID  string       `json:"vehicle_type_id"`
	OwnerAccountID string       `json:"owner_account_id"`
	Status         string       `json:"status"`
	WearPct        int32        `json:"wear_pct"`
	Fuel           string       `json:"fuel"`
	RouteID        string       `json:"route_id,omitempty"`
	RouteLegIndex  *int32       `json:"route_leg_index,omitempty"`
	Position       positionJSON `json:"position"`
	RepairUntilSim *int64       `json:"repair_until_sim,omitempty"`
	UpdatedAtSim   int64        `json:"updated_at_sim"`
}

func toVehicleJSON(v Vehicle) vehicleJSON {
	pos := positionJSON{SegmentProgressPct: v.Position.SegmentProgressPct}
	if v.Position.AtNodeID != nil {
		pos.AtNodeID = v.Position.AtNodeID.String()
	}
	if v.Position.OnSegmentID != nil {
		pos.OnSegmentID = v.Position.OnSegmentID.String()
	}
	if v.Position.Location != "" {
		pos.Location = json.RawMessage(v.Position.Location)
	}
	return vehicleJSON{
		ID: v.ID.String(), VehicleTypeID: v.VehicleTypeID.String(), OwnerAccountID: v.OwnerAccountID.String(),
		Status: v.Status, WearPct: v.WearPct, Fuel: fixed(v.Fuel),
		RouteID: uuidOrEmpty(v.RouteID), RouteLegIndex: v.RouteLegIndex, Position: pos,
		RepairUntilSim: v.RepairUntilSim, UpdatedAtSim: v.UpdatedAtSim,
	}
}

type shipmentJSON struct {
	ID                string `json:"id"`
	OwnerAccountID    string `json:"owner_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	ContractID        string `json:"contract_id,omitempty"`
	FreightContractID string `json:"freight_contract_id,omitempty"`
	VehicleID         string `json:"vehicle_id,omitempty"`
	AtNodeID          string `json:"at_node_id,omitempty"`
	Status            string `json:"status"`
	UpdatedAtSim      int64  `json:"updated_at_sim"`
}

func toShipmentJSON(sh Shipment) shipmentJSON {
	return shipmentJSON{
		ID: sh.ID.String(), OwnerAccountID: sh.OwnerAccountID.String(), ProductID: sh.ProductID.String(),
		Quantity: fixed(sh.Quantity), ContractID: uuidOrEmpty(sh.ContractID), FreightContractID: uuidOrEmpty(sh.FreightContractID),
		VehicleID: uuidOrEmpty(sh.VehicleID), AtNodeID: uuidOrEmpty(sh.AtNodeID), Status: sh.Status, UpdatedAtSim: sh.UpdatedAtSim,
	}
}

// ─── DTOs de entrada ─────────────────────────────────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

type vehiclePurchaseJSON struct {
	VehicleTypeID  string `json:"vehicle_type_id"`
	DeliveryNodeID string `json:"delivery_node_id"`
}

func (b vehiclePurchaseJSON) toInput() (VehiclePurchase, *fieldError) {
	vt, err := uuid.Parse(b.VehicleTypeID)
	if err != nil {
		return VehiclePurchase{}, &fieldError{"vehicle_type_id", "no es un UUID válido"}
	}
	node, err := uuid.Parse(b.DeliveryNodeID)
	if err != nil {
		return VehiclePurchase{}, &fieldError{"delivery_node_id", "no es un UUID válido"}
	}
	return VehiclePurchase{VehicleTypeID: vt, DeliveryNodeID: node}, nil
}

type vehicleUpdateJSON struct {
	RouteID             json.RawMessage `json:"route_id"`
	ScheduleMaintenance *bool           `json:"schedule_maintenance"`
}

func (b vehicleUpdateJSON) toInput() (VehicleUpdate, *fieldError) {
	var in VehicleUpdate
	if b.RouteID != nil {
		in.SetRoute = true
		// route_id admite null (retirar) o un UUID (asignar).
		if string(b.RouteID) != "null" {
			var raw string
			if err := json.Unmarshal(b.RouteID, &raw); err != nil {
				return VehicleUpdate{}, &fieldError{"route_id", "debe ser un UUID o null"}
			}
			id, err := uuid.Parse(raw)
			if err != nil {
				return VehicleUpdate{}, &fieldError{"route_id", "no es un UUID válido"}
			}
			in.RouteID = &id
		}
	}
	if b.ScheduleMaintenance != nil {
		in.ScheduleMaintenance = *b.ScheduleMaintenance
	}
	if !in.SetRoute && !in.ScheduleMaintenance {
		return VehicleUpdate{}, &fieldError{"body", "requiere route_id o schedule_maintenance"}
	}
	return in, nil
}

type vehicleRepositionJSON struct {
	RouteID string `json:"route_id"`
}

func (b vehicleRepositionJSON) toInput() (VehicleReposition, *fieldError) {
	route, err := uuid.Parse(b.RouteID)
	if err != nil {
		return VehicleReposition{}, &fieldError{"route_id", "no es un UUID válido"}
	}
	return VehicleReposition{RouteID: route}, nil
}

type shipmentDispatchJSON struct {
	VehicleID string `json:"vehicle_id"`
	RouteID   string `json:"route_id"`
}

func (b shipmentDispatchJSON) toInput() (ShipmentDispatch, *fieldError) {
	vehicle, err := uuid.Parse(b.VehicleID)
	if err != nil {
		return ShipmentDispatch{}, &fieldError{"vehicle_id", "no es un UUID válido"}
	}
	route, err := uuid.Parse(b.RouteID)
	if err != nil {
		return ShipmentDispatch{}, &fieldError{"route_id", "no es un UUID válido"}
	}
	return ShipmentDispatch{VehicleID: vehicle, RouteID: route}, nil
}

// ─── DTOs de terminales y slots (schemas Terminal / TerminalSlot) ─────────────

type terminalJSON struct {
	ID                   string `json:"id"`
	NodeID               string `json:"node_id"`
	OwnerAccountID       string `json:"owner_account_id"`
	TransshipmentPerHour int32  `json:"transshipment_per_hour"`
	QueueLength          int32  `json:"queue_length"`
	UpdatedAtSim         int64  `json:"updated_at_sim"`
}

func toTerminalJSON(t Terminal) terminalJSON {
	return terminalJSON{
		ID: t.ID.String(), NodeID: t.NodeID.String(), OwnerAccountID: t.OwnerAccountID.String(),
		TransshipmentPerHour: t.TransshipmentPerHour, QueueLength: t.QueueLength, UpdatedAtSim: t.UpdatedAtSim,
	}
}

type terminalSlotJSON struct {
	ID              string `json:"id"`
	TerminalID      string `json:"terminal_id"`
	PriorityTier    int32  `json:"priority_tier"`
	Price           string `json:"price"`
	HolderAccountID string `json:"holder_account_id,omitempty"`
	ValidUntilSim   *int64 `json:"valid_until_sim,omitempty"`
}

func toTerminalSlotJSON(s TerminalSlot) terminalSlotJSON {
	out := terminalSlotJSON{
		ID: s.ID.String(), TerminalID: s.TerminalID.String(), PriorityTier: s.PriorityTier,
		Price: strconv.FormatInt(s.Price, 10), ValidUntilSim: s.ValidUntilSim,
	}
	if s.HolderAccountID != nil {
		out.HolderAccountID = s.HolderAccountID.String()
	}
	return out
}
