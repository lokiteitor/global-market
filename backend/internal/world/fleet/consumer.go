package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// ConsumerShipmentCreator es el nombre lógico del consumidor del outbox que crea
// cargamentos desde contract.confirmed (cursor propio en outbox.consumer_cursors).
const ConsumerShipmentCreator = "shipment_creator"

// eventContractConfirmed es el tipo del evento que emite el Contract Service al
// confirmar un contrato. Se declara aquí como string (world NUNCA importa
// internal/contracts: la integración es SOLO por el outbox, SAD §7).
const eventContractConfirmed = "contract.confirmed"

// kindBuy es el valor de kind que genera cargamento (los sell son entrega in
// situ, origin==destination, y liquidan al confirmar — no generan cargamento).
const kindBuy = "buy"

// ShipmentCreator implementa el consumidor world "shipment_creator": consume
// contract.confirmed y, SOLO para los contratos de compra cross-node, crea el
// cargamento en el nodo de origen y MUEVE el stock físico fuera del almacén del
// vendedor (el stock deja el almacén y pasa al cargamento sin dejar de estar
// reservado en el ledger — solo cambia su ubicación física, GDD 5.3 paso 4).
// Exactly-once por el cursor del outbox; el handler es reejecutable.
type ShipmentCreator struct {
	baseRepo *Repo
	logger   *slog.Logger

	created prometheus.Counter
	skipped prometheus.Counter
}

// NewShipmentCreator construye el creador de cargamentos. reg registra sus
// métricas (nil las deja sin instrumentar: tests).
func NewShipmentCreator(logger *slog.Logger, reg prometheus.Registerer) *ShipmentCreator {
	if logger == nil {
		logger = slog.Default()
	}
	c := &ShipmentCreator{
		logger: logger,
		created: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipments_created_total",
			Help: "Total de cargamentos creados desde contract.confirmed (compra cross-node).",
		}),
		skipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipments_created_skipped_total",
			Help: "Total de contract.confirmed ignorados por el shipment_creator (sell, in situ o ya materializado).",
		}),
	}
	if reg != nil {
		reg.MustRegister(c.created, c.skipped)
	}
	return c
}

// NewConsumer construye el consumidor lógico del outbox para contract.confirmed.
// Lo arranca el engine con Handle como handler.
func (c *ShipmentCreator) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	c.baseRepo = NewRepo(pool)
	return outbox.NewConsumer(pool, ConsumerShipmentCreator, []string{eventContractConfirmed}, opts...)
}

// contractConfirmedEvent es el payload que consume el shipment_creator. Los
// campos son los FIJOS del contrato de integración CCRI↔Logística. Tolera el
// nombre alternativo quantity_agreed por compatibilidad con el emisor actual.
type contractConfirmedEvent struct {
	ContractID        string `json:"contract_id"`
	Kind              string `json:"kind"`
	SellerAccountID   string `json:"seller_account_id"`
	BuyerAccountID    string `json:"buyer_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	QuantityAgreed    string `json:"quantity_agreed"`
	OriginNodeID      string `json:"origin_node_id"`
	DestinationNodeID string `json:"destination_node_id"`
	DeadlineSim       int64  `json:"deadline_sim"`
	ConfirmedAtSim    int64  `json:"confirmed_at_sim"`
}

func (e contractConfirmedEvent) quantity() (int64, error) {
	s := e.Quantity
	if s == "" {
		s = e.QuantityAgreed
	}
	return strconv.ParseInt(s, 10, 64)
}

// Handle procesa un contract.confirmed dentro de la transacción del lote del
// consumidor (los efectos se confirman con el avance del cursor: exactly-once).
func (c *ShipmentCreator) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventContractConfirmed {
		return nil
	}
	var e contractConfirmedEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("world/fleet: leyendo contract.confirmed (seq %d): %w", ev.Seq, err)
	}

	// Filtro: SOLO compras cross-node. Los sell son entrega in situ
	// (origin==destination) y ya liquidan al confirmar; si kind falta, el filtro
	// origin!=destination excluye igualmente toda entrega in situ.
	if e.Kind != "" && e.Kind != kindBuy {
		c.skipped.Inc()
		return nil
	}
	contractID, err := uuid.Parse(e.ContractID)
	if err != nil {
		return fmt.Errorf("world/fleet: contract_id inválido en contract.confirmed (seq %d): %w", ev.Seq, err)
	}
	origin, err := uuid.Parse(e.OriginNodeID)
	if err != nil {
		return fmt.Errorf("world/fleet: origin_node_id inválido en contract.confirmed %s: %w", contractID, err)
	}
	dest, err := uuid.Parse(e.DestinationNodeID)
	if err != nil {
		return fmt.Errorf("world/fleet: destination_node_id inválido en contract.confirmed %s: %w", contractID, err)
	}
	if origin == dest {
		c.skipped.Inc() // entrega in situ: sin transporte físico
		return nil
	}
	seller, err := uuid.Parse(e.SellerAccountID)
	if err != nil {
		return fmt.Errorf("world/fleet: seller_account_id inválido en contract.confirmed %s: %w", contractID, err)
	}
	product, err := uuid.Parse(e.ProductID)
	if err != nil {
		return fmt.Errorf("world/fleet: product_id inválido en contract.confirmed %s: %w", contractID, err)
	}
	quantity, err := e.quantity()
	if err != nil || quantity <= 0 {
		return fmt.Errorf("world/fleet: quantity inválida en contract.confirmed %s: %q", contractID, e.Quantity+e.QuantityAgreed)
	}

	r := c.baseRepo.WithTx(tx)

	// Idempotencia defensiva: si ya hay un cargamento para el contrato, no repetir.
	if exists, err := r.ShipmentExistsForContract(ctx, contractID); err != nil {
		return err
	} else if exists {
		c.skipped.Inc()
		return nil
	}

	// El almacén de origen (network_nodes.building_id) del vendedor: de él sale el
	// stock físico.
	node, err := r.GetNode(ctx, origin)
	if err != nil {
		return fmt.Errorf("world/fleet: consultando el nodo de origen %s del contrato %s: %w", origin, contractID, err)
	}
	if node.BuildingID == nil {
		c.logger.Warn("world/fleet: nodo de origen sin almacén; no se materializa el cargamento",
			slog.String("contract_id", contractID.String()), slog.String("origin_node_id", origin.String()))
		c.skipped.Inc()
		return nil
	}
	building := *node.BuildingID

	// El stock reservado debe estar físicamente en el almacén (lo garantiza el
	// CCRI). Si no, se registra y se omite (defensivo: no bloquear el consumidor).
	avail, err := r.GetInventoryQty(ctx, building, product)
	if err != nil {
		return err
	}
	if avail < quantity {
		c.logger.Error("world/fleet: stock físico insuficiente para materializar el cargamento del contrato",
			slog.String("contract_id", contractID.String()), slog.String("building_id", building.String()),
			slog.String("product_id", product.String()), slog.Int64("available", avail), slog.Int64("required", quantity))
		c.skipped.Inc()
		return nil
	}

	simNow := simtime.SimTime(ev.SimTimeAt)
	shipmentID, err := newUUIDv7()
	if err != nil {
		return err
	}
	if _, err := r.InsertShipmentInWarehouse(ctx, insertShipmentParams{
		ID: shipmentID, Owner: seller, Product: product, Quantity: quantity, Contract: contractID,
		AtNode: origin, Destination: dest, Deadline: simtime.SimTime(e.DeadlineSim), SimNow: simNow,
	}); err != nil {
		return err
	}
	// Mueve el stock físico fuera del almacén (el ledger stock_reserved NO cambia).
	if err := r.ConsumeInventory(ctx, building, product, quantity, simNow); err != nil {
		return err
	}

	if err := outbox.Emit(ctx, tx, ev.SimTimeAt, AggregateShipment, shipmentID, EventShipmentCreated, ShipmentCreatedPayload{
		ShipmentID: shipmentID.String(), ContractID: contractID.String(), OwnerAccountID: seller.String(),
		ProductID: product.String(), Quantity: fixed(quantity), OriginNodeID: origin.String(),
		DestinationNodeID: dest.String(), DeadlineSim: e.DeadlineSim, CreatedAtSim: ev.SimTimeAt,
	}); err != nil {
		return err
	}

	c.created.Inc()
	c.logger.Info("cargamento creado desde contract.confirmed",
		slog.String("shipment_id", shipmentID.String()), slog.String("contract_id", contractID.String()),
		slog.String("seller", seller.String()), slog.String("product_id", product.String()),
		slog.Int64("quantity", quantity), slog.String("origin_node_id", origin.String()),
		slog.String("destination_node_id", dest.String()))
	return nil
}
