// Package contracts implementa el bounded context del CCRI (GDD 5.3/5.3.1):
// el ciclo de publicación y aceptación del tablón global — publicar con la
// garantía propia bloqueada íntegramente en el mismo acto (ADR-014), consultar
// el tablón con filtros y paginación keyset, cancelar respetando el cooldown
// anti-parpadeo y aceptar dentro de la ventana de sorteo (ADR-011).
//
// Toda operación que mueve valor corre en UNA transacción SERIALIZABLE
// (platform/db.RunSerializable) que asienta a la vez el cambio de estado, las
// partidas del ledger y el evento del outbox. Las invariantes de dinero/stock
// viven en la base (0004_ledger: triggers de saldo, doble entrada diferida,
// no-negatividad, inmutabilidad): este módulo orquesta, la base garantiza.
//
// Dinero y stock son SIEMPRE int64 de punto fijo (string en el JSON del
// contrato, jamás floats). El acceso a datos es código generado por sqlc
// (ADR-020) en el subpaquete sqlcgen, a partir de queries/contracts.sql.
package contracts

import (
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// PublicationKind es el tipo de publicación (enum ledger.publication_kind;
// schema PublicationKind del contrato).
type PublicationKind string

// Valores de PublicationKind.
const (
	KindSell    PublicationKind = "sell"
	KindBuy     PublicationKind = "buy"
	KindFreight PublicationKind = "freight" // CCRI-Flete: se activa en Fase 2
)

// Valid indica si el valor pertenece al enum del contrato/BD.
func (k PublicationKind) Valid() bool {
	switch k {
	case KindSell, KindBuy, KindFreight:
		return true
	}
	return false
}

// Channel es el canal de descubrimiento de una publicación (enum
// ledger.contract_channel): tablón abierto o negociación privada 1:1 con las
// mismas garantías y liquidación.
type Channel string

// Valores de Channel.
const (
	ChannelBoard   Channel = "board"
	ChannelPrivate Channel = "private"
)

// Valid indica si el valor pertenece al enum del contrato/BD.
func (c Channel) Valid() bool {
	return c == ChannelBoard || c == ChannelPrivate
}

// PublicationStatus es el estado de una publicación (enum
// ledger.publication_status).
type PublicationStatus string

// Valores de PublicationStatus.
const (
	StatusDrawWindow  PublicationStatus = "draw_window"
	StatusOpen        PublicationStatus = "open"
	StatusMicroWindow PublicationStatus = "micro_window"
	StatusExhausted   PublicationStatus = "exhausted"
	StatusCancelled   PublicationStatus = "cancelled"
	StatusExpired     PublicationStatus = "expired"
)

// Acceptable indica si la publicación admite aceptaciones en este estado
// (los tres estados visibles del tablón).
func (s PublicationStatus) Acceptable() bool {
	return s == StatusDrawWindow || s == StatusOpen || s == StatusMicroWindow
}

// AcceptanceStatus es el estado de una aceptación (enum
// ledger.acceptance_status).
type AcceptanceStatus string

// Valores de AcceptanceStatus.
const (
	AcceptancePendingDraw AcceptanceStatus = "pending_draw"
	AcceptanceServed      AcceptanceStatus = "served"
	AcceptanceReleased    AcceptanceStatus = "released"
)

// Publication es una publicación del tablón (schema Publication del
// contrato). Toda publicación visible es ejecutable al 100%: su garantía
// íntegra quedó bloqueada al publicar, en las cuentas espejo referenciadas.
type Publication struct {
	ID                    uuid.UUID
	Kind                  PublicationKind
	PublisherAccountID    uuid.UUID
	Channel               Channel
	CounterpartyAccountID *uuid.UUID // contraparte fija en canal private
	ProductID             *uuid.UUID
	QuantityTotal         int64
	QuantityRemaining     int64
	UnitPrice             int64
	MinLot                int64
	OriginNodeID          *uuid.UUID // sell: almacén con el stock congelado
	DestinationNodeID     *uuid.UUID // buy: destino de la entrega
	DeliverySimSeconds    simtime.SimTime
	Status                PublicationStatus
	WindowClosesAt        *time.Time // cierre de la ventana de sorteo/micro-ventana (wall-clock de la BD)
	CancelCooldownUntil   *time.Time // fin del cooldown anti-parpadeo (wall-clock de la BD)
	StockReserveAccountID *uuid.UUID // espejo sell: stock congelado
	GuaranteeAccountID    *uuid.UUID // espejo sell: garantía monetaria del 10%
	EscrowAccountID       *uuid.UUID // espejo buy/freight: 100% del pago retenido
	DeclaredValue         *int64     // freight: valor declarado de la carga (base de la garantía del transportista)
	PublishedAtSim        simtime.SimTime
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// Acceptance es una aceptación de una publicación (schema Acceptance del
// contrato), pendiente del sorteo de su ventana o ya resuelta.
type Acceptance struct {
	ID                    uuid.UUID
	PublicationID         uuid.UUID
	AcceptorAccountID     uuid.UUID
	Quantity              int64
	QuantityServed        int64
	Status                AcceptanceStatus
	DrawOrder             *int32     // asignado al resolverse (sorteo o liberación)
	StockReserveAccountID *uuid.UUID // espejo del aceptante-vendedor (publicación buy)
	GuaranteeAccountID    *uuid.UUID // espejo del aceptante-vendedor (publicación buy)
	EscrowAccountID       *uuid.UUID // espejo del aceptante-comprador (publicación sell)
	AcceptedAt            time.Time
	ResolvedAt            *time.Time
}

// ContractStatus es el estado de un contrato CCRI (enum
// ledger.contract_status).
type ContractStatus string

// Valores de ContractStatus.
const (
	ContractActive  ContractStatus = "active"  // confirmado; en ejecución logística
	ContractSettled ContractStatus = "settled" // liquidado (fill 100% o pro-rata)
	ContractFailed  ContractStatus = "failed"  // fill 0% al vencer el plazo
)

// Valid indica si el valor pertenece al enum del contrato/BD.
func (s ContractStatus) Valid() bool {
	switch s {
	case ContractActive, ContractSettled, ContractFailed:
		return true
	}
	return false
}

// ContractRole es el rol de la corporación autenticada en el filtro de
// contratos (query param role del contrato).
type ContractRole string

// Valores de ContractRole.
const (
	RoleBuyer  ContractRole = "buyer"
	RoleSeller ContractRole = "seller"
)

// Valid indica si el valor pertenece al enum del contrato.
func (r ContractRole) Valid() bool {
	return r == RoleBuyer || r == RoleSeller
}

// Contract es un CCRI de bienes (schema Contract del contrato): la unidad
// económica atómica del juego, nacida con el bloqueo triple ya asentado. Si
// destination_node_id == origin_node_id (retirada in situ, siempre en
// contratos nacidos de sell) se entrega y liquida al confirmarse; si difieren
// (nacidos de buy) la entrega exige transporte físico antes de deadline_sim.
type Contract struct {
	ID                       uuid.UUID
	PublicationID            *uuid.UUID // NULL en negociación directa (Fase 2)
	Channel                  Channel
	BuyerAccountID           uuid.UUID
	SellerAccountID          uuid.UUID
	ProductID                uuid.UUID
	QuantityAgreed           int64
	QuantityDelivered        int64
	UnitPrice                int64
	OriginNodeID             uuid.UUID
	DestinationNodeID        uuid.UUID
	DeadlineSim              simtime.SimTime
	Status                   ContractStatus
	FillBP                   *int32 // % entregado a tiempo (puntos básicos); presente al liquidar
	StockReserveAccountID    uuid.UUID
	SellerGuaranteeAccountID uuid.UUID
	EscrowAccountID          uuid.UUID
	ConfirmedAtSim           simtime.SimTime
	SettledAtSim             *simtime.SimTime
	CreatedAt                time.Time
}

// IsParty indica si account es comprador o vendedor del contrato (la
// autorización de GetContract/ListContractDeliveries: solo las partes).
func (c Contract) IsParty(account uuid.UUID) bool {
	return account == c.BuyerAccountID || account == c.SellerAccountID
}

// ContractDelivery es una entrega parcial confirmada de un contrato (schema
// ContractDelivery): cada llegada física al nodo de destino dentro del plazo.
type ContractDelivery struct {
	ID             uuid.UUID
	ContractID     uuid.UUID
	ShipmentID     uuid.UUID
	Quantity       int64
	DeliveredAtSim simtime.SimTime
	OnTime         bool
}

// ContractFilter son los filtros y la paginación de ListContracts (query
// params del contrato /contracts/contracts).
type ContractFilter struct {
	Role      ContractRole // vacío = ambos roles
	Status    ContractStatus
	ProductID *uuid.UUID
	Cursor    string
	Limit     int
}

// PublicationInput son los parámetros de CreatePublication (schema
// PublicationCreate del contrato). Cantidades y precio en int64 punto fijo,
// ya parseados por el handler.
type PublicationInput struct {
	Kind                  PublicationKind
	Channel               Channel    // vacío = board
	CounterpartyAccountID *uuid.UUID // obligatorio en canal private
	ProductID             *uuid.UUID
	QuantityTotal         int64
	UnitPrice             int64
	MinLot                int64 // 0 = 1 (default del contrato)
	OriginNodeID          *uuid.UUID
	DestinationNodeID     *uuid.UUID
	DeliverySimSeconds    int64
	// DeclaredValue es el valor declarado de la carga en una solicitud de flete
	// (kind=freight): base de la garantía del transportista. Obligatorio y > 0
	// en freight; ignorado en sell/buy.
	DeclaredValue int64
}

// AcceptInput son los parámetros de Accept (schema AcceptanceCreate).
type AcceptInput struct {
	Quantity int64
	// OriginNodeID es el almacén propio del vendedor-aceptante del que sale
	// el stock: obligatorio al aceptar publicaciones buy, ignorado en sell
	// (la entrega es in situ en el origen de la publicación).
	OriginNodeID *uuid.UUID
}

// BoardSort es el orden de la consulta del tablón (parámetro sort del
// contrato).
type BoardSort string

// Valores de BoardSort. deadline_asc ordena por el plazo de entrega pactado
// (delivery_sim_seconds ascendente: primero lo más urgente).
const (
	SortUnitPriceAsc    BoardSort = "unit_price_asc" // default del contrato
	SortUnitPriceDesc   BoardSort = "unit_price_desc"
	SortPublishedAtDesc BoardSort = "published_at_desc"
	SortDeadlineAsc     BoardSort = "deadline_asc"
)

// Valid indica si el valor pertenece al enum del contrato.
func (s BoardSort) Valid() bool {
	switch s {
	case SortUnitPriceAsc, SortUnitPriceDesc, SortPublishedAtDesc, SortDeadlineAsc:
		return true
	}
	return false
}

// ─── CCRI-Flete (GDD 5.3.2) ──────────────────────────────────────────────────

// FreightContract es un contrato de flete (schema FreightContract): el cargador
// paga el precio del flete a escrow y el transportista deposita una garantía
// proporcional al valor declarado; la carga viaja en una cuenta de CUSTODIA a
// nombre del contrato (el transportista la lleva pero no puede venderla). Nace
// activo al servirse la aceptación y se liquida pro-rata contra la entrega.
type FreightContract struct {
	ID                        uuid.UUID
	PublicationID             *uuid.UUID
	Channel                   Channel
	ShipperAccountID          uuid.UUID // cargador (dueño de la mercancía)
	CarrierAccountID          uuid.UUID // transportista
	OriginNodeID              uuid.UUID
	DestinationNodeID         uuid.UUID
	FreightPrice              int64 // precio del flete de este contrato (escrow del cargador)
	DeclaredValue             int64 // valor declarado de la carga de este contrato
	DeadlineSim               simtime.SimTime
	Status                    ContractStatus
	FillBP                    *int32
	EscrowAccountID           uuid.UUID
	CarrierGuaranteeAccountID uuid.UUID
	CustodyAccountID          uuid.UUID
	ConfirmedAtSim            simtime.SimTime
	SettledAtSim              *simtime.SimTime
	CreatedAt                 time.Time
}

// IsParty indica si account es el cargador o el transportista del flete (la
// autorización de GetFreightContract: solo las partes).
func (f FreightContract) IsParty(account uuid.UUID) bool {
	return account == f.ShipperAccountID || account == f.CarrierAccountID
}

// FreightRole es el rol de la corporación autenticada en el filtro de fletes
// (query param role de /contracts/freight-contracts).
type FreightRole string

// Valores de FreightRole.
const (
	RoleShipper FreightRole = "shipper"
	RoleCarrier FreightRole = "carrier"
)

// Valid indica si el valor pertenece al enum del contrato.
func (r FreightRole) Valid() bool { return r == RoleShipper || r == RoleCarrier }

// FreightContractFilter son los filtros y la paginación de ListFreightContracts.
type FreightContractFilter struct {
	Role   FreightRole
	Status ContractStatus
	Cursor string
	Limit  int
}

// BoardFilter son los filtros, el orden y la paginación de QueryBoard
// (query params del contrato /contracts/board).
type BoardFilter struct {
	Kind                  PublicationKind // vacío = todos
	ProductID             *uuid.UUID
	OriginRegionID        *uuid.UUID // región del nodo de origen
	DestinationRegionID   *uuid.UUID // región del nodo de destino
	MinUnitPrice          *int64
	MaxUnitPrice          *int64
	MinQuantityRemaining  *int64
	MaxDeliverySimSeconds *int64
	Sort                  BoardSort // vacío = unit_price_asc
	Cursor                string    // cursor opaco de meta.next_cursor; vacío = primera página
	Limit                 int       // 0 = DefaultPageLimit; máximo MaxPageLimit
}
