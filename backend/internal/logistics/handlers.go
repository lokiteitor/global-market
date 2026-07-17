package logistics

import (
	"context"
	"encoding/json"
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
)

// Códigos de error del contrato emitidos por este bounded context (además de
// los de la plataforma httpx). El schema Error es de código abierto: los 403/422
// usan códigos estables descriptivos.
const (
	codeUnauthorized     = "UNAUTHORIZED"
	codeNotResourceOwner = "NOT_RESOURCE_OWNER"
	codeNoRouteFound     = "NO_ROUTE_FOUND"
	maxStockDigits       = 32
)

// Identity resuelve la cuenta autenticada de una petición. La define este
// bounded context (SAD §7: sin imports cruzados) y la implementa el composition
// root con el middleware de sesión.
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
	ListNodes(ctx context.Context, f NodeFilter) ([]NetworkNode, string, error)
	ListLinks(ctx context.Context, f LinkFilter) ([]NetworkLink, string, error)
	PlanRoute(ctx context.Context, req PlanRequest) (RoutePlan, error)
	ListRoutes(ctx context.Context, owner uuid.UUID, f RouteFilter) ([]Route, string, error)
	GetRoute(ctx context.Context, owner, id uuid.UUID) (Route, error)
	CreateRoute(ctx context.Context, owner uuid.UUID, in RouteInput) (Route, error)
	UpdateRoute(ctx context.Context, owner, id uuid.UUID, upd RouteUpdate) (Route, error)
	DeleteRoute(ctx context.Context, owner, id uuid.UUID) error
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints logistics/* del contrato OpenAPI. Las lecturas
// del grafo y el route-plan son públicas (solo exigen sesión); el CRUD de rutas
// filtra por propiedad (403 sobre ruta ajena).
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del bounded context logistics.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, protegidas por sesión e idempotencia en el wiring).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /logistics/network/nodes", h.listNodes)
	mux.HandleFunc("GET /logistics/network/links", h.listLinks)
	mux.HandleFunc("POST /logistics/route-plans", h.createRoutePlan)
	mux.HandleFunc("GET /logistics/routes", h.listRoutes)
	mux.HandleFunc("POST /logistics/routes", h.createRoute)
	mux.HandleFunc("GET /logistics/routes/{routeId}", h.getRoute)
	mux.HandleFunc("PATCH /logistics/routes/{routeId}", h.updateRoute)
	mux.HandleFunc("DELETE /logistics/routes/{routeId}", h.deleteRoute)
}

// ─── GET /logistics/network/nodes ────────────────────────────────────────────

func (h *Handlers) listNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := NodeFilter{Kind: q.Get("kind"), Cursor: q.Get("cursor")}
	if filter.Kind != "" && !validNodeKind(filter.Kind) {
		writeValidationError(w, "kind", "no es una clase de nodo válida")
		return
	}
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

	nodes, next, err := h.svc.ListNodes(r.Context(), filter)
	if err != nil {
		h.writeError(w, r, err, "listando nodos del grafo")
		return
	}
	data := make([]networkNodeJSON, len(nodes))
	for i, n := range nodes {
		data[i] = toNetworkNodeJSON(n)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /logistics/network/links ────────────────────────────────────────────

func (h *Handlers) listLinks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := LinkFilter{Mode: q.Get("mode"), Cursor: q.Get("cursor")}
	if filter.Mode != "" && !validMode(filter.Mode) {
		writeValidationError(w, "mode", "no es un modo válido")
		return
	}
	if id, ok, err := optionalUUID(q, "region_id"); err != nil {
		writeValidationError(w, "region_id", "no es un UUID válido")
		return
	} else if ok {
		filter.RegionID = &id
	}
	if id, ok, err := optionalUUID(q, "from_node_id"); err != nil {
		writeValidationError(w, "from_node_id", "no es un UUID válido")
		return
	} else if ok {
		filter.FromNodeID = &id
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	links, next, err := h.svc.ListLinks(r.Context(), filter)
	if err != nil {
		h.writeError(w, r, err, "listando enlaces del grafo")
		return
	}
	data := make([]networkLinkJSON, len(links))
	for i, l := range links {
		data[i] = toNetworkLinkJSON(l)
	}
	h.writeList(w, r, data, next)
}

// ─── POST /logistics/route-plans ─────────────────────────────────────────────

func (h *Handlers) createRoutePlan(w http.ResponseWriter, r *http.Request) {
	var body routePlanRequestJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	req, verr := body.toRequest()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	plan, err := h.svc.PlanRoute(r.Context(), req)
	if err != nil {
		h.writeError(w, r, err, "calculando el route-plan")
		return
	}
	h.writeData(w, r, http.StatusOK, toRoutePlanJSON(plan), "")
}

// ─── GET /logistics/routes ───────────────────────────────────────────────────

func (h *Handlers) listRoutes(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := RouteFilter{Kind: q.Get("kind"), Cursor: q.Get("cursor")}
	if filter.Kind != "" && !validRouteKind(filter.Kind) {
		writeValidationError(w, "kind", "no es un tipo de ruta válido")
		return
	}
	if raw := q.Get("active"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			writeValidationError(w, "active", "debe ser un booleano")
			return
		}
		filter.Active = &b
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	routes, next, err := h.svc.ListRoutes(r.Context(), owner, filter)
	if err != nil {
		h.writeError(w, r, err, "listando rutas")
		return
	}
	data := make([]routeJSON, len(routes))
	for i, rt := range routes {
		data[i] = toRouteJSON(rt)
	}
	h.writeList(w, r, data, next)
}

// ─── POST /logistics/routes ──────────────────────────────────────────────────

func (h *Handlers) createRoute(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body routeCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	route, err := h.svc.CreateRoute(r.Context(), owner, in)
	if err != nil {
		h.writeError(w, r, err, "creando la ruta")
		return
	}
	h.writeData(w, r, http.StatusCreated, toRouteJSON(route), "")
}

// ─── GET /logistics/routes/{routeId} ─────────────────────────────────────────

func (h *Handlers) getRoute(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("routeId"))
	if err != nil {
		notFound(w, "la ruta no existe")
		return
	}
	route, err := h.svc.GetRoute(r.Context(), owner, id)
	if err != nil {
		h.writeError(w, r, err, "consultando la ruta")
		return
	}
	h.writeData(w, r, http.StatusOK, toRouteJSON(route), "")
}

// ─── PATCH /logistics/routes/{routeId} ───────────────────────────────────────

func (h *Handlers) updateRoute(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("routeId"))
	if err != nil {
		notFound(w, "la ruta no existe")
		return
	}
	var body routeUpdateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	upd, verr := body.toUpdate()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	route, err := h.svc.UpdateRoute(r.Context(), owner, id, upd)
	if err != nil {
		h.writeError(w, r, err, "actualizando la ruta")
		return
	}
	h.writeData(w, r, http.StatusOK, toRouteJSON(route), "")
}

// ─── DELETE /logistics/routes/{routeId} ──────────────────────────────────────

func (h *Handlers) deleteRoute(w http.ResponseWriter, r *http.Request) {
	owner, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("routeId"))
	if err != nil {
		notFound(w, "la ruta no existe")
		return
	}
	if err := h.svc.DeleteRoute(r.Context(), owner, id); err != nil {
		h.writeError(w, r, err, "eliminando la ruta")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

func (h *Handlers) writeList(w http.ResponseWriter, r *http.Request, data any, next string) {
	h.writeData(w, r, http.StatusOK, data, next)
}

// writeError mapea los errores tipados del servicio a los códigos del contrato;
// lo no reconocido es un 500 INTERNAL logueado con request id.
func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	switch {
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.Is(err, ErrNodeNotFound):
		notFound(w, "el nodo del grafo no existe")
	case errors.Is(err, ErrRouteNotFound):
		notFound(w, "la ruta no existe")
	case errors.Is(err, ErrNotRouteOwner):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
	case errors.Is(err, ErrNoRoute):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeNoRouteFound, err.Error(), nil)
	case errors.Is(err, ErrLinkNotFound),
		errors.Is(err, ErrDiscontiguousLegs),
		errors.Is(err, ErrMultimodalNoTerminal),
		errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
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

// ─── DTOs de salida del contrato ─────────────────────────────────────────────

type networkNodeJSON struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	RegionID   string          `json:"region_id"`
	BuildingID string          `json:"building_id,omitempty"`
	CityID     string          `json:"city_id,omitempty"`
	Location   json.RawMessage `json:"location"`
}

func toNetworkNodeJSON(n NetworkNode) networkNodeJSON {
	return networkNodeJSON{
		ID: n.ID.String(), Kind: n.Kind, RegionID: n.RegionID.String(),
		BuildingID: uuidPtrOrEmpty(n.BuildingID), CityID: uuidPtrOrEmpty(n.CityID),
		Location: rawGeo(n.Location),
	}
}

type linkSegmentJSON struct {
	ID            string  `json:"id"`
	RegionID      string  `json:"region_id"`
	Seq           int32   `json:"seq"`
	LengthM       int32   `json:"length_m"`
	CongestionEma float64 `json:"congestion_ema"`
	UpdatedAtSim  int64   `json:"updated_at_sim"`
}

type networkLinkJSON struct {
	ID              string            `json:"id"`
	Mode            string            `json:"mode"`
	FromNodeID      string            `json:"from_node_id"`
	ToNodeID        string            `json:"to_node_id"`
	Path            json.RawMessage   `json:"path,omitempty"`
	LengthM         int32             `json:"length_m"`
	CapacityPerHour int32             `json:"capacity_per_hour"`
	BaseSpeedKmh    int32             `json:"base_speed_kmh"`
	Segments        []linkSegmentJSON `json:"segments"`
}

func toNetworkLinkJSON(l NetworkLink) networkLinkJSON {
	segs := make([]linkSegmentJSON, len(l.Segments))
	for i, s := range l.Segments {
		segs[i] = linkSegmentJSON{
			ID: s.ID.String(), RegionID: s.RegionID.String(), Seq: s.Seq,
			LengthM: s.LengthM, CongestionEma: s.CongestionEma, UpdatedAtSim: s.UpdatedAtSim,
		}
	}
	return networkLinkJSON{
		ID: l.ID.String(), Mode: l.Mode, FromNodeID: l.FromNodeID.String(),
		ToNodeID: l.ToNodeID.String(), Path: rawGeo(l.Path), LengthM: l.LengthM,
		CapacityPerHour: l.CapacityPerHour, BaseSpeedKmh: l.BaseSpeedKmh, Segments: segs,
	}
}

type routePlanLegJSON struct {
	Seq                     int    `json:"seq"`
	LinkID                  string `json:"link_id"`
	Mode                    string `json:"mode"`
	EtaSimSeconds           int64  `json:"eta_sim_seconds"`
	TransshipmentTerminalID string `json:"transshipment_terminal_id,omitempty"`
}

type routePlanJSON struct {
	OriginNodeID       string             `json:"origin_node_id"`
	DestinationNodeID  string             `json:"destination_node_id"`
	Legs               []routePlanLegJSON `json:"legs"`
	TotalEtaSimSeconds int64              `json:"total_eta_sim_seconds"`
	EstimatedCost      string             `json:"estimated_cost,omitempty"`
}

func toRoutePlanJSON(p RoutePlan) routePlanJSON {
	legs := make([]routePlanLegJSON, len(p.Legs))
	for i, l := range p.Legs {
		legs[i] = routePlanLegJSON{
			Seq: l.Seq, LinkID: l.LinkID.String(), Mode: l.Mode, EtaSimSeconds: l.EtaSimSeconds,
			TransshipmentTerminalID: uuidPtrOrEmpty(l.TransshipmentTerminalID),
		}
	}
	out := routePlanJSON{
		OriginNodeID: p.OriginNodeID.String(), DestinationNodeID: p.DestinationNodeID.String(),
		Legs: legs, TotalEtaSimSeconds: p.TotalEtaSimSeconds,
	}
	if p.HasCost {
		out.EstimatedCost = fixed(p.EstimatedCost)
	}
	return out
}

type routeLegJSON struct {
	LegIndex int32  `json:"leg_index"`
	LinkID   string `json:"link_id"`
}

type routeJSON struct {
	ID             string         `json:"id"`
	OwnerAccountID string         `json:"owner_account_id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	Active         bool           `json:"active"`
	Legs           []routeLegJSON `json:"legs"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func toRouteJSON(rt Route) routeJSON {
	legs := make([]routeLegJSON, len(rt.Legs))
	for i, l := range rt.Legs {
		legs[i] = routeLegJSON{LegIndex: l.LegIndex, LinkID: l.LinkID.String()}
	}
	return routeJSON{
		ID: rt.ID.String(), OwnerAccountID: rt.OwnerAccountID.String(), Name: rt.Name,
		Kind: rt.Kind, Active: rt.Active, Legs: legs,
		CreatedAt: rt.CreatedAt, UpdatedAt: rt.UpdatedAt,
	}
}

// ─── DTOs de entrada del contrato ────────────────────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

type routePlanRequestJSON struct {
	OriginNodeID      string   `json:"origin_node_id"`
	DestinationNodeID string   `json:"destination_node_id"`
	Modes             []string `json:"modes"`
	Optimize          string   `json:"optimize"`
	CargoVolume       string   `json:"cargo_volume"`
}

func (b routePlanRequestJSON) toRequest() (PlanRequest, *fieldError) {
	origin, err := uuid.Parse(b.OriginNodeID)
	if err != nil {
		return PlanRequest{}, &fieldError{"origin_node_id", "no es un UUID válido"}
	}
	dest, err := uuid.Parse(b.DestinationNodeID)
	if err != nil {
		return PlanRequest{}, &fieldError{"destination_node_id", "no es un UUID válido"}
	}
	if origin == dest {
		return PlanRequest{}, &fieldError{"destination_node_id", "el origen y el destino no pueden ser el mismo nodo"}
	}
	for _, m := range b.Modes {
		if !validMode(m) {
			return PlanRequest{}, &fieldError{"modes", fmt.Sprintf("modo inválido %q", m)}
		}
	}
	optimize := b.Optimize
	if optimize == "" {
		optimize = OptimizeTime
	}
	if optimize != OptimizeTime && optimize != OptimizeCost {
		return PlanRequest{}, &fieldError{"optimize", "debe ser time o cost"}
	}
	var cargo int64
	if b.CargoVolume != "" {
		v, err := parseFixed(b.CargoVolume)
		if err != nil {
			return PlanRequest{}, &fieldError{"cargo_volume", err.Error()}
		}
		cargo = v
	}
	return PlanRequest{
		Origin: origin, Destination: dest, Modes: b.Modes,
		Optimize: optimize, CargoVolume: cargo,
	}, nil
}

type routeCreateJSON struct {
	Name string   `json:"name"`
	Kind string   `json:"kind"`
	Legs []string `json:"legs"`
}

func (b routeCreateJSON) toInput() (RouteInput, *fieldError) {
	if b.Name == "" {
		return RouteInput{}, &fieldError{"name", "requerido"}
	}
	if !validRouteKind(b.Kind) {
		return RouteInput{}, &fieldError{"kind", "debe ser fixed_line u on_demand"}
	}
	legs, ferr := parseLegs(b.Legs)
	if ferr != nil {
		return RouteInput{}, ferr
	}
	if len(legs) == 0 {
		return RouteInput{}, &fieldError{"legs", "requiere al menos un enlace"}
	}
	return RouteInput{Name: b.Name, Kind: b.Kind, Legs: legs}, nil
}

type routeUpdateJSON struct {
	Name   *string   `json:"name"`
	Active *bool     `json:"active"`
	Legs   *[]string `json:"legs"`
}

func (b routeUpdateJSON) toUpdate() (RouteUpdate, *fieldError) {
	if b.Name == nil && b.Active == nil && b.Legs == nil {
		return RouteUpdate{}, &fieldError{"body", "requiere al menos un campo (name, active o legs)"}
	}
	upd := RouteUpdate{Name: b.Name, Active: b.Active}
	if b.Legs != nil {
		legs, ferr := parseLegs(*b.Legs)
		if ferr != nil {
			return RouteUpdate{}, ferr
		}
		if len(legs) == 0 {
			return RouteUpdate{}, &fieldError{"legs", "requiere al menos un enlace"}
		}
		upd.Legs = &legs
	}
	return upd, nil
}

// parseLegs interpreta una lista de ids de enlace (UUID) preservando el orden.
func parseLegs(raw []string) ([]uuid.UUID, *fieldError) {
	legs := make([]uuid.UUID, len(raw))
	for i, s := range raw {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, &fieldError{"legs", fmt.Sprintf("el enlace %d no es un UUID válido", i)}
		}
		legs[i] = id
	}
	return legs, nil
}

// ─── Helpers de serialización ────────────────────────────────────────────────

// fixed serializa un entero de punto fijo (dinero/stock) como string (jamás
// float; invariante del contrato).
func fixed(v int64) string { return strconv.FormatInt(v, 10) }

// rawGeo embebe un GeoJSON plano (de ST_AsGeoJSON) como objeto JSON; vacío se
// omite (omitempty sobre RawMessage nil).
func rawGeo(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// uuidPtrOrEmpty serializa un UUID opcional ("" se omite con omitempty).
func uuidPtrOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// parseFixed interpreta una cantidad de punto fijo (string de dígitos) a int64,
// rechazando floats, signos y desbordamiento.
func parseFixed(raw string) (int64, error) {
	if len(raw) > maxStockDigits {
		return 0, errors.New("cantidad demasiado larga")
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
