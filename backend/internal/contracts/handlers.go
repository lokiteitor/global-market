package contracts

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
)

// Códigos de error del contrato emitidos por este módulo (además de los de la
// plataforma httpx).
const (
	codeUnauthorized           = "UNAUTHORIZED"
	codeNotResourceOwner       = "NOT_RESOURCE_OWNER"
	codeInsufficientCollateral = "INSUFFICIENT_COLLATERAL"
	codePublicationExhausted   = "PUBLICATION_EXHAUSTED"
	codeCancelCooldownActive   = "CANCEL_COOLDOWN_ACTIVE"
	codeBelowMinLot            = "BELOW_MIN_LOT"
)

// maxMoneyStockDigits acota la longitud de un importe/cantidad de punto fijo
// del cuerpo (defensa antes de math/big: rechaza entradas absurdas sin
// procesarlas).
const maxMoneyStockDigits = 32

// Identity resuelve la cuenta autenticada de una petición. La define este
// módulo (SAD §7: sin imports cruzados entre bounded contexts) y la implementa
// el composition root con el middleware de sesión.
type Identity interface {
	AccountID(ctx context.Context) (uuid.UUID, bool)
}

// MetaSource construye los metadatos comunes (schema Meta del contrato) de
// toda respuesta exitosa. Lo implementa el composition root con el reloj de
// simulación.
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// API es la superficie del servicio que consumen los handlers; la implementa
// *Service.
type API interface {
	QueryBoard(ctx context.Context, f BoardFilter) ([]Publication, string, error)
	CreatePublication(ctx context.Context, publisher uuid.UUID, in PublicationInput) (Publication, error)
	GetPublication(ctx context.Context, viewer, id uuid.UUID) (Publication, error)
	CancelPublication(ctx context.Context, publisher, id uuid.UUID) (Publication, error)
	Accept(ctx context.Context, acceptor, publicationID uuid.UUID, in AcceptInput) (Acceptance, error)
	GetAcceptance(ctx context.Context, viewer, id uuid.UUID) (Acceptance, error)
	ResolveAcceptanceContract(ctx context.Context, a Acceptance) (*uuid.UUID, error)
	ListContracts(ctx context.Context, account uuid.UUID, f ContractFilter) ([]Contract, string, error)
	GetContract(ctx context.Context, viewer, id uuid.UUID) (Contract, error)
	ListContractDeliveries(ctx context.Context, viewer, contractID uuid.UUID) ([]ContractDelivery, error)
	ListFreightContracts(ctx context.Context, account uuid.UUID, f FreightContractFilter) ([]FreightContract, string, error)
	GetFreightContract(ctx context.Context, viewer, id uuid.UUID) (FreightContract, error)
}

var _ API = (*Service)(nil)

// Handlers sirve los endpoints /contracts/* del contrato OpenAPI v1.2.0.
type Handlers struct {
	svc      API
	identity Identity
	meta     MetaSource
	logger   *slog.Logger
}

// NewHandlers construye los handlers del módulo contracts.
func NewHandlers(svc API, identity Identity, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{svc: svc, identity: identity, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: el
// composition root las monta bajo APIPrefix, protegidas por sesión).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /contracts/board", h.queryBoard)
	mux.HandleFunc("POST /contracts/publications", h.createPublication)
	mux.HandleFunc("GET /contracts/publications/{publicationId}", h.getPublication)
	mux.HandleFunc("DELETE /contracts/publications/{publicationId}", h.cancelPublication)
	mux.HandleFunc("POST /contracts/publications/{publicationId}/acceptances", h.acceptPublication)
	mux.HandleFunc("GET /contracts/acceptances/{acceptanceId}", h.getAcceptance)
	mux.HandleFunc("GET /contracts/contracts", h.listContracts)
	mux.HandleFunc("GET /contracts/contracts/{contractId}", h.getContract)
	mux.HandleFunc("GET /contracts/contracts/{contractId}/deliveries", h.listDeliveries)
	mux.HandleFunc("GET /contracts/freight-contracts", h.listFreightContracts)
	mux.HandleFunc("GET /contracts/freight-contracts/{freightContractId}", h.getFreightContract)
}

// ─── GET /contracts/board ────────────────────────────────────────────────────

func (h *Handlers) queryBoard(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.identity.AccountID(r.Context()); !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := BoardFilter{Cursor: q.Get("cursor"), Sort: BoardSort(q.Get("sort"))}

	if kind := q.Get("kind"); kind != "" {
		filter.Kind = PublicationKind(kind)
	}
	var err error
	if filter.ProductID, err = optionalUUID(q, "product_id"); err != nil {
		writeValidationError(w, "product_id", err.Error())
		return
	}
	if filter.OriginRegionID, err = optionalUUID(q, "origin_region_id"); err != nil {
		writeValidationError(w, "origin_region_id", err.Error())
		return
	}
	if filter.DestinationRegionID, err = optionalUUID(q, "destination_region_id"); err != nil {
		writeValidationError(w, "destination_region_id", err.Error())
		return
	}
	if filter.MinUnitPrice, err = optionalFixed(q, "min_unit_price"); err != nil {
		writeValidationError(w, "min_unit_price", err.Error())
		return
	}
	if filter.MaxUnitPrice, err = optionalFixed(q, "max_unit_price"); err != nil {
		writeValidationError(w, "max_unit_price", err.Error())
		return
	}
	if filter.MinQuantityRemaining, err = optionalFixed(q, "min_quantity_remaining"); err != nil {
		writeValidationError(w, "min_quantity_remaining", err.Error())
		return
	}
	if filter.MaxDeliverySimSeconds, err = optionalSimTime(q, "max_delivery_sim_seconds"); err != nil {
		writeValidationError(w, "max_delivery_sim_seconds", err.Error())
		return
	}
	if filter.Limit, err = parseLimit(q); err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}

	pubs, next, err := h.svc.QueryBoard(r.Context(), filter)
	if err != nil {
		h.writeError(w, r, err, "consultando el tablón")
		return
	}
	data := make([]publicationJSON, len(pubs))
	for i, p := range pubs {
		data[i] = toPublicationJSON(p)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── POST /contracts/publications ────────────────────────────────────────────

func (h *Handlers) createPublication(w http.ResponseWriter, r *http.Request) {
	publisher, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	var body publicationCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return // httpx ya respondió 400
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	pub, err := h.svc.CreatePublication(r.Context(), publisher, in)
	if err != nil {
		h.writeError(w, r, err, "creando la publicación")
		return
	}
	h.writeData(w, r, http.StatusCreated, toPublicationJSON(pub), "")
}

// ─── GET /contracts/publications/{id} ────────────────────────────────────────

func (h *Handlers) getPublication(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("publicationId"))
	if err != nil {
		notFound(w, "la publicación no existe")
		return
	}
	pub, err := h.svc.GetPublication(r.Context(), viewer, id)
	if err != nil {
		h.writeError(w, r, err, "consultando la publicación")
		return
	}
	h.writeData(w, r, http.StatusOK, toPublicationJSON(pub), "")
}

// ─── DELETE /contracts/publications/{id} ─────────────────────────────────────

func (h *Handlers) cancelPublication(w http.ResponseWriter, r *http.Request) {
	publisher, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("publicationId"))
	if err != nil {
		notFound(w, "la publicación no existe")
		return
	}
	pub, err := h.svc.CancelPublication(r.Context(), publisher, id)
	if err != nil {
		h.writeError(w, r, err, "cancelando la publicación")
		return
	}
	h.writeData(w, r, http.StatusOK, toPublicationJSON(pub), "")
}

// ─── POST /contracts/publications/{id}/acceptances ───────────────────────────

func (h *Handlers) acceptPublication(w http.ResponseWriter, r *http.Request) {
	acceptor, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("publicationId"))
	if err != nil {
		notFound(w, "la publicación no existe")
		return
	}
	var body acceptanceCreateJSON
	if err := httpx.ReadJSON(w, r, &body, 0); err != nil {
		return
	}
	in, verr := body.toInput()
	if verr != nil {
		writeValidationError(w, verr.field, verr.reason)
		return
	}
	acc, err := h.svc.Accept(r.Context(), acceptor, id, in)
	if err != nil {
		h.writeError(w, r, err, "aceptando la publicación")
		return
	}
	h.writeData(w, r, http.StatusCreated, toAcceptanceJSON(acc, nil), "")
}

// ─── GET /contracts/acceptances/{id} ─────────────────────────────────────────

func (h *Handlers) getAcceptance(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("acceptanceId"))
	if err != nil {
		notFound(w, "la aceptación no existe")
		return
	}
	acc, err := h.svc.GetAcceptance(r.Context(), viewer, id)
	if err != nil {
		h.writeError(w, r, err, "consultando la aceptación")
		return
	}
	contractID, err := h.svc.ResolveAcceptanceContract(r.Context(), acc)
	if err != nil {
		h.writeError(w, r, err, "resolviendo el contrato de la aceptación")
		return
	}
	h.writeData(w, r, http.StatusOK, toAcceptanceJSON(acc, contractID), "")
}

// ─── GET /contracts/contracts ────────────────────────────────────────────────

func (h *Handlers) listContracts(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := ContractFilter{
		Role:   ContractRole(q.Get("role")),
		Status: ContractStatus(q.Get("status")),
		Cursor: q.Get("cursor"),
	}
	var err error
	if filter.ProductID, err = optionalUUID(q, "product_id"); err != nil {
		writeValidationError(w, "product_id", err.Error())
		return
	}
	if filter.Limit, err = parseLimit(q); err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	contracts, next, err := h.svc.ListContracts(r.Context(), account, filter)
	if err != nil {
		h.writeError(w, r, err, "listando contratos")
		return
	}
	data := make([]contractJSON, len(contracts))
	for i, c := range contracts {
		data[i] = toContractJSON(c)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

// ─── GET /contracts/contracts/{id} ───────────────────────────────────────────

func (h *Handlers) getContract(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("contractId"))
	if err != nil {
		notFound(w, "el contrato no existe")
		return
	}
	c, err := h.svc.GetContract(r.Context(), viewer, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el contrato")
		return
	}
	h.writeData(w, r, http.StatusOK, toContractJSON(c), "")
}

// ─── GET /contracts/contracts/{id}/deliveries ────────────────────────────────

func (h *Handlers) listDeliveries(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("contractId"))
	if err != nil {
		notFound(w, "el contrato no existe")
		return
	}
	deliveries, err := h.svc.ListContractDeliveries(r.Context(), viewer, id)
	if err != nil {
		h.writeError(w, r, err, "listando las entregas del contrato")
		return
	}
	data := make([]deliveryJSON, len(deliveries))
	for i, d := range deliveries {
		data[i] = toDeliveryJSON(d)
	}
	h.writeData(w, r, http.StatusOK, data, "")
}

// ─── GET /contracts/freight-contracts (CCRI-Flete, GDD 5.3.2) ────────────────

func (h *Handlers) listFreightContracts(w http.ResponseWriter, r *http.Request) {
	account, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	q := r.URL.Query()
	filter := FreightContractFilter{
		Role:   FreightRole(q.Get("role")),
		Status: ContractStatus(q.Get("status")),
		Cursor: q.Get("cursor"),
	}
	var err error
	if filter.Limit, err = parseLimit(q); err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	freights, next, err := h.svc.ListFreightContracts(r.Context(), account, filter)
	if err != nil {
		h.writeError(w, r, err, "listando contratos de flete")
		return
	}
	data := make([]freightContractJSON, len(freights))
	for i, fc := range freights {
		data[i] = toFreightContractJSON(fc)
	}
	h.writeData(w, r, http.StatusOK, data, next)
}

func (h *Handlers) getFreightContract(w http.ResponseWriter, r *http.Request) {
	viewer, ok := h.identity.AccountID(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	id, err := uuid.Parse(r.PathValue("freightContractId"))
	if err != nil {
		notFound(w, "el contrato de flete no existe")
		return
	}
	fc, err := h.svc.GetFreightContract(r.Context(), viewer, id)
	if err != nil {
		h.writeError(w, r, err, "consultando el contrato de flete")
		return
	}
	h.writeData(w, r, http.StatusOK, toFreightContractJSON(fc), "")
}

// ─── Escritura de respuestas y mapeo de errores ──────────────────────────────

func (h *Handlers) writeData(w http.ResponseWriter, r *http.Request, status int, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, status, data, meta)
}

// writeError mapea los errores tipados del servicio a los códigos estables del
// contrato; lo no reconocido es un 500 INTERNAL logueado con request id.
func (h *Handlers) writeError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	var collErr *CollateralError
	var cdErr *CooldownError
	var lotErr *MinLotError
	switch {
	case errors.As(err, &collErr):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientCollateral,
			collErr.Error(), map[string]any{
				"resource":  collErr.Resource,
				"required":  fixed(collErr.Required),
				"available": fixed(collErr.Available),
			})
	case errors.As(err, &cdErr):
		httpx.WriteError(w, http.StatusConflict, codeCancelCooldownActive, cdErr.Error(),
			map[string]any{"cancel_cooldown_until": cdErr.Until.UTC().Format(time.RFC3339)})
	case errors.As(err, &lotErr):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeBelowMinLot, lotErr.Error(),
			map[string]any{"min_lot": fixed(lotErr.MinLot), "quantity_remaining": fixed(lotErr.QuantityRemaining)})
	case errors.Is(err, ErrFreightPhase2):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError,
			"CCRI-Flete se activa en Fase 2", nil)
	case errors.Is(err, ErrInsufficientCollateral):
		httpx.WriteError(w, http.StatusUnprocessableEntity, codeInsufficientCollateral, err.Error(), nil)
	case errors.Is(err, ErrOverflow), errors.Is(err, ErrValidation):
		httpx.WriteError(w, http.StatusUnprocessableEntity, httpx.CodeValidationError, err.Error(), nil)
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	case errors.Is(err, ErrPublicationNotFound):
		notFound(w, "la publicación no existe")
	case errors.Is(err, ErrAcceptanceNotFound):
		notFound(w, "la aceptación no existe")
	case errors.Is(err, ErrContractNotFound):
		notFound(w, "el contrato no existe")
	case errors.Is(err, ErrFreightContractNotFound):
		notFound(w, "el contrato de flete no existe")
	case errors.Is(err, ErrPublicationExhausted):
		httpx.WriteError(w, http.StatusConflict, codePublicationExhausted, err.Error(), nil)
	case errors.Is(err, ErrNotPublisher), errors.Is(err, ErrNotParty),
		errors.Is(err, ErrNotAcceptor), errors.Is(err, ErrNotNodeOwner),
		errors.Is(err, ErrNotContractParty), errors.Is(err, ErrNotFreightParty):
		httpx.WriteError(w, http.StatusForbidden, codeNotResourceOwner, err.Error(), nil)
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
	if n < 1 || n > MaxPageLimit {
		return 0, fmt.Errorf("debe estar entre 1 y %d", MaxPageLimit)
	}
	return n, nil
}

func optionalUUID(q url.Values, name string) (*uuid.UUID, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errors.New("no es un UUID válido")
	}
	return &id, nil
}

func optionalFixed(q url.Values, name string) (*int64, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	v, err := parseFixed(raw)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func optionalSimTime(q url.Values, name string) (*int64, error) {
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
	return &n, nil
}

// parseFixed interpreta un importe/cantidad de punto fijo (string de dígitos,
// contrato v1.2.0) a int64, rechazando floats, signos y desbordamiento.
func parseFixed(raw string) (int64, error) {
	if len(raw) > maxMoneyStockDigits {
		return 0, errors.New("importe/cantidad demasiado largo")
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, errors.New("debe ser un entero de punto fijo (string de dígitos, sin decimales)")
	}
	return n, nil
}

// ─── DTOs del contrato (snake_case; dinero/stock como string) ────────────────

type publicationJSON struct {
	ID                    string     `json:"id"`
	Kind                  string     `json:"kind"`
	PublisherAccountID    string     `json:"publisher_account_id"`
	Channel               string     `json:"channel"`
	CounterpartyAccountID string     `json:"counterparty_account_id,omitempty"`
	ProductID             string     `json:"product_id,omitempty"`
	QuantityTotal         string     `json:"quantity_total"`
	QuantityRemaining     string     `json:"quantity_remaining"`
	UnitPrice             string     `json:"unit_price"`
	MinLot                string     `json:"min_lot"`
	OriginNodeID          string     `json:"origin_node_id,omitempty"`
	DestinationNodeID     string     `json:"destination_node_id,omitempty"`
	DeliverySimSeconds    int64      `json:"delivery_sim_seconds"`
	Status                string     `json:"status"`
	WindowClosesAt        *time.Time `json:"window_closes_at,omitempty"`
	CancelCooldownUntil   *time.Time `json:"cancel_cooldown_until,omitempty"`
	PublishedAtSim        int64      `json:"published_at_sim"`
	CreatedAt             time.Time  `json:"created_at"`
}

func toPublicationJSON(p Publication) publicationJSON {
	return publicationJSON{
		ID:                    p.ID.String(),
		Kind:                  string(p.Kind),
		PublisherAccountID:    p.PublisherAccountID.String(),
		Channel:               string(p.Channel),
		CounterpartyAccountID: uuidOrEmpty(p.CounterpartyAccountID),
		ProductID:             uuidOrEmpty(p.ProductID),
		QuantityTotal:         fixed(p.QuantityTotal),
		QuantityRemaining:     fixed(p.QuantityRemaining),
		UnitPrice:             fixed(p.UnitPrice),
		MinLot:                fixed(p.MinLot),
		OriginNodeID:          uuidOrEmpty(p.OriginNodeID),
		DestinationNodeID:     uuidOrEmpty(p.DestinationNodeID),
		DeliverySimSeconds:    int64(p.DeliverySimSeconds),
		Status:                string(p.Status),
		WindowClosesAt:        p.WindowClosesAt,
		CancelCooldownUntil:   p.CancelCooldownUntil,
		PublishedAtSim:        int64(p.PublishedAtSim),
		CreatedAt:             p.CreatedAt,
	}
}

type acceptanceJSON struct {
	ID                string     `json:"id"`
	PublicationID     string     `json:"publication_id"`
	AcceptorAccountID string     `json:"acceptor_account_id"`
	Quantity          string     `json:"quantity"`
	QuantityServed    string     `json:"quantity_served"`
	Status            string     `json:"status"`
	DrawOrder         *int32     `json:"draw_order,omitempty"`
	ContractID        string     `json:"contract_id,omitempty"`
	AcceptedAt        time.Time  `json:"accepted_at"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
}

func toAcceptanceJSON(a Acceptance, contractID *uuid.UUID) acceptanceJSON {
	return acceptanceJSON{
		ID:                a.ID.String(),
		PublicationID:     a.PublicationID.String(),
		AcceptorAccountID: a.AcceptorAccountID.String(),
		Quantity:          fixed(a.Quantity),
		QuantityServed:    fixed(a.QuantityServed),
		Status:            string(a.Status),
		DrawOrder:         a.DrawOrder,
		ContractID:        uuidOrEmpty(contractID),
		AcceptedAt:        a.AcceptedAt,
		ResolvedAt:        a.ResolvedAt,
	}
}

type contractJSON struct {
	ID                       string    `json:"id"`
	PublicationID            string    `json:"publication_id,omitempty"`
	Channel                  string    `json:"channel"`
	BuyerAccountID           string    `json:"buyer_account_id"`
	SellerAccountID          string    `json:"seller_account_id"`
	ProductID                string    `json:"product_id"`
	QuantityAgreed           string    `json:"quantity_agreed"`
	QuantityDelivered        string    `json:"quantity_delivered"`
	UnitPrice                string    `json:"unit_price"`
	OriginNodeID             string    `json:"origin_node_id"`
	DestinationNodeID        string    `json:"destination_node_id"`
	DeadlineSim              int64     `json:"deadline_sim"`
	Status                   string    `json:"status"`
	FillBP                   *int32    `json:"fill_bp,omitempty"`
	StockReserveAccountID    string    `json:"stock_reserve_account_id"`
	SellerGuaranteeAccountID string    `json:"seller_guarantee_account_id"`
	EscrowAccountID          string    `json:"escrow_account_id"`
	ConfirmedAtSim           int64     `json:"confirmed_at_sim"`
	SettledAtSim             *int64    `json:"settled_at_sim,omitempty"`
	CreatedAt                time.Time `json:"created_at"`
}

func toContractJSON(c Contract) contractJSON {
	var settled *int64
	if c.SettledAtSim != nil {
		v := int64(*c.SettledAtSim)
		settled = &v
	}
	return contractJSON{
		ID:                       c.ID.String(),
		PublicationID:            uuidOrEmpty(c.PublicationID),
		Channel:                  string(c.Channel),
		BuyerAccountID:           c.BuyerAccountID.String(),
		SellerAccountID:          c.SellerAccountID.String(),
		ProductID:                c.ProductID.String(),
		QuantityAgreed:           fixed(c.QuantityAgreed),
		QuantityDelivered:        fixed(c.QuantityDelivered),
		UnitPrice:                fixed(c.UnitPrice),
		OriginNodeID:             c.OriginNodeID.String(),
		DestinationNodeID:        c.DestinationNodeID.String(),
		DeadlineSim:              int64(c.DeadlineSim),
		Status:                   string(c.Status),
		FillBP:                   c.FillBP,
		StockReserveAccountID:    c.StockReserveAccountID.String(),
		SellerGuaranteeAccountID: c.SellerGuaranteeAccountID.String(),
		EscrowAccountID:          c.EscrowAccountID.String(),
		ConfirmedAtSim:           int64(c.ConfirmedAtSim),
		SettledAtSim:             settled,
		CreatedAt:                c.CreatedAt,
	}
}

type deliveryJSON struct {
	ID             string `json:"id"`
	ContractID     string `json:"contract_id"`
	ShipmentID     string `json:"shipment_id"`
	Quantity       string `json:"quantity"`
	DeliveredAtSim int64  `json:"delivered_at_sim"`
	OnTime         bool   `json:"on_time"`
}

func toDeliveryJSON(d ContractDelivery) deliveryJSON {
	return deliveryJSON{
		ID:             d.ID.String(),
		ContractID:     d.ContractID.String(),
		ShipmentID:     d.ShipmentID.String(),
		Quantity:       fixed(d.Quantity),
		DeliveredAtSim: int64(d.DeliveredAtSim),
		OnTime:         d.OnTime,
	}
}

// freightContractJSON es el schema FreightContract (CCRI-Flete, GDD 5.3.2).
type freightContractJSON struct {
	ID                        string    `json:"id"`
	PublicationID             string    `json:"publication_id,omitempty"`
	Channel                   string    `json:"channel"`
	ShipperAccountID          string    `json:"shipper_account_id"`
	CarrierAccountID          string    `json:"carrier_account_id"`
	OriginNodeID              string    `json:"origin_node_id"`
	DestinationNodeID         string    `json:"destination_node_id"`
	FreightPrice              string    `json:"freight_price"`
	DeclaredValue             string    `json:"declared_value"`
	DeadlineSim               int64     `json:"deadline_sim"`
	Status                    string    `json:"status"`
	FillBP                    *int32    `json:"fill_bp,omitempty"`
	EscrowAccountID           string    `json:"escrow_account_id"`
	CarrierGuaranteeAccountID string    `json:"carrier_guarantee_account_id"`
	CustodyAccountID          string    `json:"custody_account_id"`
	ConfirmedAtSim            int64     `json:"confirmed_at_sim"`
	SettledAtSim              *int64    `json:"settled_at_sim,omitempty"`
	CreatedAt                 time.Time `json:"created_at"`
}

func toFreightContractJSON(fc FreightContract) freightContractJSON {
	var settled *int64
	if fc.SettledAtSim != nil {
		v := int64(*fc.SettledAtSim)
		settled = &v
	}
	return freightContractJSON{
		ID:                        fc.ID.String(),
		PublicationID:             uuidOrEmpty(fc.PublicationID),
		Channel:                   string(fc.Channel),
		ShipperAccountID:          fc.ShipperAccountID.String(),
		CarrierAccountID:          fc.CarrierAccountID.String(),
		OriginNodeID:              fc.OriginNodeID.String(),
		DestinationNodeID:         fc.DestinationNodeID.String(),
		FreightPrice:              fixed(fc.FreightPrice),
		DeclaredValue:             fixed(fc.DeclaredValue),
		DeadlineSim:               int64(fc.DeadlineSim),
		Status:                    string(fc.Status),
		FillBP:                    fc.FillBP,
		EscrowAccountID:           fc.EscrowAccountID.String(),
		CarrierGuaranteeAccountID: fc.CarrierGuaranteeAccountID.String(),
		CustodyAccountID:          fc.CustodyAccountID.String(),
		ConfirmedAtSim:            int64(fc.ConfirmedAtSim),
		SettledAtSim:              settled,
		CreatedAt:                 fc.CreatedAt,
	}
}

// ─── DTOs de entrada (cuerpo de las peticiones) ──────────────────────────────

// fieldError localiza un campo de cuerpo inválido (→ 400 VALIDATION_ERROR).
type fieldError struct {
	field  string
	reason string
}

func (e *fieldError) Error() string { return e.field + ": " + e.reason }

type publicationCreateJSON struct {
	Kind                  string  `json:"kind"`
	Channel               string  `json:"channel"`
	CounterpartyAccountID *string `json:"counterparty_account_id"`
	ProductID             *string `json:"product_id"`
	QuantityTotal         string  `json:"quantity_total"`
	UnitPrice             string  `json:"unit_price"`
	MinLot                *string `json:"min_lot"`
	OriginNodeID          *string `json:"origin_node_id"`
	DestinationNodeID     *string `json:"destination_node_id"`
	DeliverySimSeconds    *int64  `json:"delivery_sim_seconds"`
	DeclaredValue         *string `json:"declared_value"`
}

func (b publicationCreateJSON) toInput() (PublicationInput, *fieldError) {
	in := PublicationInput{
		Kind:    PublicationKind(b.Kind),
		Channel: Channel(b.Channel),
	}
	var err error
	if in.QuantityTotal, err = parseFixed(b.QuantityTotal); err != nil {
		return PublicationInput{}, &fieldError{"quantity_total", err.Error()}
	}
	if in.UnitPrice, err = parseFixed(b.UnitPrice); err != nil {
		return PublicationInput{}, &fieldError{"unit_price", err.Error()}
	}
	if b.MinLot != nil && *b.MinLot != "" {
		if in.MinLot, err = parseFixed(*b.MinLot); err != nil {
			return PublicationInput{}, &fieldError{"min_lot", err.Error()}
		}
	}
	if b.DeliverySimSeconds != nil {
		in.DeliverySimSeconds = *b.DeliverySimSeconds
	}
	if b.DeclaredValue != nil && *b.DeclaredValue != "" {
		if in.DeclaredValue, err = parseFixed(*b.DeclaredValue); err != nil {
			return PublicationInput{}, &fieldError{"declared_value", err.Error()}
		}
	}
	if in.CounterpartyAccountID, err = bodyUUID(b.CounterpartyAccountID); err != nil {
		return PublicationInput{}, &fieldError{"counterparty_account_id", err.Error()}
	}
	if in.ProductID, err = bodyUUID(b.ProductID); err != nil {
		return PublicationInput{}, &fieldError{"product_id", err.Error()}
	}
	if in.OriginNodeID, err = bodyUUID(b.OriginNodeID); err != nil {
		return PublicationInput{}, &fieldError{"origin_node_id", err.Error()}
	}
	if in.DestinationNodeID, err = bodyUUID(b.DestinationNodeID); err != nil {
		return PublicationInput{}, &fieldError{"destination_node_id", err.Error()}
	}
	return in, nil
}

type acceptanceCreateJSON struct {
	Quantity     string  `json:"quantity"`
	OriginNodeID *string `json:"origin_node_id"`
}

func (b acceptanceCreateJSON) toInput() (AcceptInput, *fieldError) {
	in := AcceptInput{}
	var err error
	if in.Quantity, err = parseFixed(b.Quantity); err != nil {
		return AcceptInput{}, &fieldError{"quantity", err.Error()}
	}
	if in.OriginNodeID, err = bodyUUID(b.OriginNodeID); err != nil {
		return AcceptInput{}, &fieldError{"origin_node_id", err.Error()}
	}
	return in, nil
}

// bodyUUID interpreta un uuid opcional del cuerpo (nil o "" → nil).
func bodyUUID(raw *string) (*uuid.UUID, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(*raw)
	if err != nil {
		return nil, errors.New("no es un UUID válido")
	}
	return &id, nil
}
