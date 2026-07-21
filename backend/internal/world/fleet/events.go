package fleet

import "strconv"

// Tipos de agregado y de evento que world/fleet emite por el outbox
// transaccional (SAD/ADR-008), en la MISMA tx que el cambio de estado.
const (
	AggregateVehicle      = "vehicle"
	AggregateShipment     = "shipment"
	AggregateTerminalSlot = "terminal_slot"

	// EventSlotPurchased lo emite la compra de un slot de prioridad de terminal.
	EventSlotPurchased = "slot.purchased"

	EventVehiclePurchased = "vehicle.purchased"
	EventVehicleUpdated   = "vehicle.updated"
	EventVehicleArrived   = "vehicle.arrived"
	EventVehicleBroken    = "vehicle.broken"
	EventVehicleStranded  = "vehicle.stranded"

	EventShipmentCreated    = "shipment.created"
	EventShipmentDispatched = "shipment.dispatched"
	// EventShipmentAtTerminal es el hito de TRANSBORDO: un cargamento de una ruta
	// multimodal llegó a una terminal intermodal al final de su tramo de un modo y
	// espera (at_terminal) el despacho del siguiente tramo en un vehículo del
	// siguiente modo (GDD 7.3). Hito físico de auditoría (sin consumidor obligado).
	EventShipmentAtTerminal = "shipment.at_terminal"
	// EventShipmentArrived es el hito que consume el Contract Service para
	// confirmar la entrega del CCRI (payload FIJO, ver ShipmentArrivedPayload).
	EventShipmentArrived = "shipment.arrived"
	// EventShipmentReleased lo emite el consumidor shipment_releaser al detener y
	// liberar in situ un cargamento de un contrato vencido (GDD 7.1/5.3: nada se
	// teletransporta). Es un hito físico de auditoría (sin consumidor obligado).
	EventShipmentReleased = "shipment.released"
)

// Payloads JSON de los eventos. Dinero/stock viajan SIEMPRE como string de punto
// fijo, jamás como float; los sim-time como enteros. Los campos opcionales se
// omiten vacíos.

// VehiclePurchasedPayload es el payload de vehicle.purchased.
type VehiclePurchasedPayload struct {
	VehicleID      string `json:"vehicle_id"`
	OwnerAccountID string `json:"owner_account_id"`
	VehicleTypeID  string `json:"vehicle_type_id"`
	DeliveryNodeID string `json:"delivery_node_id"`
	PurchasePrice  string `json:"purchase_price"`
	Fuel           string `json:"fuel"`
	PurchasedAtSim int64  `json:"purchased_at_sim"`
}

// VehicleUpdatedPayload es el payload de vehicle.updated (asignación/retiro de
// ruta o mantenimiento programado).
type VehicleUpdatedPayload struct {
	VehicleID      string `json:"vehicle_id"`
	OwnerAccountID string `json:"owner_account_id"`
	Status         string `json:"status"`
	RouteID        string `json:"route_id,omitempty"`
	UpdatedAtSim   int64  `json:"updated_at_sim"`
}

// ShipmentCreatedPayload es el payload de shipment.created (el shipment_creator
// materializa el cargamento de un contrato de compra cross-node).
type ShipmentCreatedPayload struct {
	ShipmentID        string `json:"shipment_id"`
	ContractID        string `json:"contract_id,omitempty"`
	FreightContractID string `json:"freight_contract_id,omitempty"`
	OwnerAccountID    string `json:"owner_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	OriginNodeID      string `json:"origin_node_id"`
	DestinationNodeID string `json:"destination_node_id"`
	DeadlineSim       int64  `json:"deadline_sim"`
	CreatedAtSim      int64  `json:"created_at_sim"`
}

// ShipmentDispatchedPayload es el payload de shipment.dispatched.
type ShipmentDispatchedPayload struct {
	ShipmentID        string `json:"shipment_id"`
	VehicleID         string `json:"vehicle_id"`
	RouteID           string `json:"route_id"`
	ContractID        string `json:"contract_id,omitempty"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	OriginNodeID      string `json:"origin_node_id"`
	DestinationNodeID string `json:"destination_node_id"`
	DispatchedAtSim   int64  `json:"dispatched_at_sim"`
}

// VehicleArrivedPayload es el payload de vehicle.arrived (hito de llegada al
// nodo destino final de la ruta).
type VehicleArrivedPayload struct {
	VehicleID      string `json:"vehicle_id"`
	OwnerAccountID string `json:"owner_account_id"`
	NodeID         string `json:"node_id"`
	ArrivedAtSim   int64  `json:"arrived_at_sim"`
}

// VehicleBrokenPayload es el payload de vehicle.broken (avería: la carga espera
// a bordo, GDD 7.3).
type VehicleBrokenPayload struct {
	VehicleID      string `json:"vehicle_id"`
	OwnerAccountID string `json:"owner_account_id"`
	SegmentID      string `json:"segment_id"`
	RepairUntilSim int64  `json:"repair_until_sim"`
	BrokenAtSim    int64  `json:"broken_at_sim"`
}

// VehicleStrandedPayload es el payload de vehicle.stranded (defensivo: sin
// combustible, se detiene en el nodo previo).
type VehicleStrandedPayload struct {
	VehicleID      string `json:"vehicle_id"`
	OwnerAccountID string `json:"owner_account_id"`
	NodeID         string `json:"node_id"`
	StrandedAtSim  int64  `json:"stranded_at_sim"`
}

// ShipmentArrivedPayload es el payload de shipment.arrived. FIJO por el contrato
// de integración CCRI↔Logística: el Contract Service lo consume para calcular la
// puntualidad, asentar la entrega y liquidar (GDD 5.3 pasos 5-6).
type ShipmentArrivedPayload struct {
	ShipmentID        string `json:"shipment_id"`
	ContractID        string `json:"contract_id,omitempty"`
	FreightContractID string `json:"freight_contract_id,omitempty"`
	Quantity          string `json:"quantity"`
	DestinationNodeID string `json:"destination_node_id"`
	ArrivedAtSim      int64  `json:"arrived_at_sim"`
}

// ShipmentAtTerminalPayload es el payload de shipment.at_terminal (transbordo de
// una ruta multimodal: el cargamento espera en la terminal el siguiente tramo).
// transshipment_seconds es el tiempo de transbordo que debe consumir antes de que
// el siguiente despacho sea admisible (GDD 7.3).
type ShipmentAtTerminalPayload struct {
	ShipmentID           string `json:"shipment_id"`
	ContractID           string `json:"contract_id,omitempty"`
	Quantity             string `json:"quantity"`
	TerminalID           string `json:"terminal_id"`
	TerminalNodeID       string `json:"terminal_node_id"`
	DestinationNodeID    string `json:"destination_node_id,omitempty"`
	TransshipmentSeconds int64  `json:"transshipment_seconds"`
	AtTerminalAtSim      int64  `json:"at_terminal_at_sim"`
}

// ShipmentReleasedPayload es el payload de shipment.released (liberación in situ
// de un cargamento de contrato vencido, en su ubicación física actual).
type ShipmentReleasedPayload struct {
	ShipmentID        string `json:"shipment_id"`
	ContractID        string `json:"contract_id,omitempty"`
	FreightContractID string `json:"freight_contract_id,omitempty"`
	OwnerAccountID    string `json:"owner_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	NodeID            string `json:"node_id"`
	ReleasedAtSim     int64  `json:"released_at_sim"`
}

// SlotPurchasedPayload es el payload de slot.purchased (compra de un slot de
// prioridad de terminal, GDD 7.3).
type SlotPurchasedPayload struct {
	SlotID          string `json:"slot_id"`
	TerminalID      string `json:"terminal_id"`
	HolderAccountID string `json:"holder_account_id"`
	Price           string `json:"price"`
	PriorityTier    int64  `json:"priority_tier"`
	ValidUntilSim   int64  `json:"valid_until_sim"`
	PurchasedAtSim  int64  `json:"purchased_at_sim"`
}

// fixed serializa un importe/cantidad de punto fijo como string del contrato.
func fixed(v int64) string { return strconv.FormatInt(v, 10) }
