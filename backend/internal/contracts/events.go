package contracts

import (
	"strconv"

	"github.com/google/uuid"
)

// Tipos de agregado y de evento que este bounded context emite por el outbox
// transaccional (SAD/ADR-008). Los eventos de contrato (confirmed, delivered,
// settled) y publication.expired los emiten los workers del incremento sobre
// esta misma base; las constantes fijan aquí el contrato completo de eventos
// del módulo.
const (
	AggregatePublication = "publication"
	AggregateAcceptance  = "acceptance"
	AggregateContract    = "contract"

	EventPublicationCreated   = "publication.created"
	EventPublicationCancelled = "publication.cancelled"
	EventPublicationExpired   = "publication.expired"
	EventAcceptanceRegistered = "acceptance.registered"
	EventAcceptanceResolved   = "acceptance.resolved"
	EventContractConfirmed    = "contract.confirmed"
	EventContractDelivered    = "contract.delivered"
	EventContractSettled      = "contract.settled"
	// EventContractExpiredUndelivered lo emite el barrido de vencimiento cuando
	// un contrato vence con cantidad SIN entregar. Lo consume world (motor de
	// tránsito) para DETENER los cargamentos aún en tránsito de ese contrato y
	// liberarlos in situ en su ubicación física actual (GDD 7.1/5.3: nada se
	// teletransporta). La contabilidad la cierra contracts (settle pro-rata); el
	// movimiento físico lo hace world — integración SOLO por el outbox (SAD §7).
	EventContractExpiredUndelivered = "contract.expired_undelivered"
)

// Payloads JSON de los eventos. Por el contrato de la API, dinero y stock
// viajan SIEMPRE como string de punto fijo, jamás como float; los sim-time
// como enteros. Los campos opcionales (según kind) se omiten vacíos.

// PublicationCreatedPayload es el payload de publication.created.
type PublicationCreatedPayload struct {
	PublicationID      string `json:"publication_id"`
	Kind               string `json:"kind"`
	Channel            string `json:"channel"`
	PublisherAccountID string `json:"publisher_account_id"`
	ProductID          string `json:"product_id,omitempty"`
	QuantityTotal      string `json:"quantity_total"`
	UnitPrice          string `json:"unit_price"`
	MinLot             string `json:"min_lot"`
	OriginNodeID       string `json:"origin_node_id,omitempty"`
	DestinationNodeID  string `json:"destination_node_id,omitempty"`
	DeliverySimSeconds int64  `json:"delivery_sim_seconds"`
	PublishedAtSim     int64  `json:"published_at_sim"`
}

// PublicationCancelledPayload es el payload de publication.cancelled.
// ReleasedAcceptances cuenta las aceptaciones pending_draw liberadas junto
// con la publicación.
type PublicationCancelledPayload struct {
	PublicationID       string `json:"publication_id"`
	Kind                string `json:"kind"`
	QuantityRemaining   string `json:"quantity_remaining"`
	ReleasedAcceptances int    `json:"released_acceptances"`
	CancelledAtSim      int64  `json:"cancelled_at_sim"`
}

// PublicationExpiredPayload es el payload de publication.expired (lo emite el
// sweep de TTL del incremento: II_PUBLICATION_TTL_SIM_SECONDS).
type PublicationExpiredPayload struct {
	PublicationID     string `json:"publication_id"`
	Kind              string `json:"kind"`
	QuantityRemaining string `json:"quantity_remaining"`
	ExpiredAtSim      int64  `json:"expired_at_sim"`
}

// AcceptanceRegisteredPayload es el payload de acceptance.registered.
type AcceptanceRegisteredPayload struct {
	AcceptanceID      string `json:"acceptance_id"`
	PublicationID     string `json:"publication_id"`
	AcceptorAccountID string `json:"acceptor_account_id"`
	Quantity          string `json:"quantity"`
	RegisteredAtSim   int64  `json:"registered_at_sim"`
}

// AcceptanceResolvedPayload es el payload de acceptance.resolved: el
// resultado del sorteo (served, con la cantidad servida y el contrato) o la
// liberación (released, por sorteo perdido o cancelación de la publicación).
type AcceptanceResolvedPayload struct {
	AcceptanceID      string `json:"acceptance_id"`
	PublicationID     string `json:"publication_id"`
	AcceptorAccountID string `json:"acceptor_account_id"`
	Status            string `json:"status"` // served | released
	QuantityServed    string `json:"quantity_served"`
	ContractID        string `json:"contract_id,omitempty"`
	ResolvedAtSim     int64  `json:"resolved_at_sim"`
}

// ContractConfirmedPayload es el payload de contract.confirmed: el contrato
// nace del sorteo con su bloqueo triple ya asentado. Su forma es el contrato de
// integración FIJO CCRI↔Logística (SAD §7): contract_id, kind (buy|sell),
// buyer/seller, product_id, quantity, origin/destination_node_id, deadline_sim y
// confirmed_at_sim — el consumidor world "shipment_creator" materializa el
// cargamento a partir de ellos. publication_id, channel y unit_price son extras
// informativos. Kind distingue las compras cross-node (generan cargamento) de
// las ventas in situ (origin==destination, sin transporte).
type ContractConfirmedPayload struct {
	ContractID        string `json:"contract_id"`
	Kind              string `json:"kind"` // buy | sell (kind de la publicación de origen)
	PublicationID     string `json:"publication_id,omitempty"`
	Channel           string `json:"channel"`
	BuyerAccountID    string `json:"buyer_account_id"`
	SellerAccountID   string `json:"seller_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	UnitPrice         string `json:"unit_price"`
	OriginNodeID      string `json:"origin_node_id"`
	DestinationNodeID string `json:"destination_node_id"`
	DeadlineSim       int64  `json:"deadline_sim"`
	ConfirmedAtSim    int64  `json:"confirmed_at_sim"`
}

// ContractDeliveredPayload es el payload de contract.delivered: una llegada
// física parcial confirmada (en la retirada in situ, la entrega íntegra).
type ContractDeliveredPayload struct {
	ContractID        string `json:"contract_id"`
	DeliveryID        string `json:"delivery_id"`
	ShipmentID        string `json:"shipment_id"`
	Quantity          string `json:"quantity"`
	QuantityDelivered string `json:"quantity_delivered"`
	DeliveredAtSim    int64  `json:"delivered_at_sim"`
	OnTime            bool   `json:"on_time"`
}

// ContractSettledPayload es el payload de contract.settled, documentado en el
// plan del incremento (lo emite el worker de liquidación sobre esta base).
type ContractSettledPayload struct {
	ContractID          string `json:"contract_id"`
	ProductID           string `json:"product_id"`
	DestinationRegionID string `json:"destination_region_id"`
	UnitPrice           string `json:"unit_price"`
	QuantityAgreed      string `json:"quantity_agreed"`
	QuantityDelivered   string `json:"quantity_delivered"`
	FillBP              int    `json:"fill_bp"`
	SettledAtSim        int64  `json:"settled_at_sim"`
	Status              string `json:"status"` // settled | failed
}

// ContractExpiredUndeliveredPayload es el payload de contract.expired_undelivered:
// el contrato venció con undelivered_quantity unidades sin entregar. Lo consume
// world para detener y liberar in situ los cargamentos aún en tránsito de ese
// contrato (su ubicación física actual). contracts ya cerró la contabilidad
// (settle pro-rata liberó el stock reservado no entregado en el ledger); este
// evento coordina el lado FÍSICO sin cruzar la frontera de contexto.
type ContractExpiredUndeliveredPayload struct {
	ContractID          string `json:"contract_id"`
	UndeliveredQuantity string `json:"undelivered_quantity"`
	ExpiredAtSim        int64  `json:"expired_at_sim"`
}

// fixed serializa un importe/cantidad de punto fijo como string del contrato.
func fixed(v int64) string { return strconv.FormatInt(v, 10) }

// uuidOrEmpty serializa un uuid opcional ("" si es nil, para omitempty).
func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
