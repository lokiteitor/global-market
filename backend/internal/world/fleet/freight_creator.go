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

// ConsumerFreightShipmentCreator es el nombre lógico del consumidor del outbox que
// materializa el cargamento del cargador desde freight.confirmed (cursor propio).
const ConsumerFreightShipmentCreator = "freight_shipment_creator"

// eventFreightConfirmed es el tipo del evento que emite el Contract Service al
// confirmar un flete. Se declara aquí como string (world NUNCA importa
// internal/contracts: integración SOLO por el outbox, SAD §7).
const eventFreightConfirmed = "freight.confirmed"

// FreightShipmentCreator implementa el consumidor world "freight_shipment_creator":
// consume freight.confirmed y crea el cargamento del CARGADOR (owner=shipper,
// freight_contract_id) en el nodo de origen, DESCONTANDO el inventario físico del
// almacén (la carga deja el almacén; contablemente ya está en custodia, la asentó
// el Contract Service al confirmar). El TRANSPORTISTA lo despacha después en su
// vehículo. Exactly-once por el cursor; el handler es reejecutable (idempotente por
// freight_contract_id).
type FreightShipmentCreator struct {
	baseRepo *Repo
	logger   *slog.Logger

	created prometheus.Counter
	skipped prometheus.Counter
}

// NewFreightShipmentCreator construye el creador de cargamentos de flete. reg
// registra sus métricas (nil las deja sin instrumentar: tests).
func NewFreightShipmentCreator(logger *slog.Logger, reg prometheus.Registerer) *FreightShipmentCreator {
	if logger == nil {
		logger = slog.Default()
	}
	c := &FreightShipmentCreator{
		logger: logger,
		created: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_freight_shipments_created_total",
			Help: "Total de cargamentos de flete creados desde freight.confirmed.",
		}),
		skipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_freight_shipments_created_skipped_total",
			Help: "Total de freight.confirmed ignorados por el freight_shipment_creator (ya materializado, sin almacén o sin stock).",
		}),
	}
	if reg != nil {
		reg.MustRegister(c.created, c.skipped)
	}
	return c
}

// NewConsumer construye el consumidor lógico del outbox para freight.confirmed.
func (c *FreightShipmentCreator) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	c.baseRepo = NewRepo(pool)
	return outbox.NewConsumer(pool, ConsumerFreightShipmentCreator, []string{eventFreightConfirmed}, opts...)
}

// freightConfirmedEvent es el payload que consume el freight_shipment_creator (los
// campos FIJOS del contrato de integración CCRI-Flete↔Logística).
type freightConfirmedEvent struct {
	FreightContractID string `json:"freight_contract_id"`
	ShipperAccountID  string `json:"shipper_account_id"`
	ProductID         string `json:"product_id"`
	Quantity          string `json:"quantity"`
	OriginNodeID      string `json:"origin_node_id"`
	DestinationNodeID string `json:"destination_node_id"`
	DeadlineSim       int64  `json:"deadline_sim"`
}

// Handle procesa un freight.confirmed dentro de la transacción del lote (los
// efectos se confirman con el avance del cursor: exactly-once).
func (c *FreightShipmentCreator) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventFreightConfirmed {
		return nil
	}
	var e freightConfirmedEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("world/fleet: leyendo freight.confirmed (seq %d): %w", ev.Seq, err)
	}
	freightID, err := uuid.Parse(e.FreightContractID)
	if err != nil {
		return fmt.Errorf("world/fleet: freight_contract_id inválido en freight.confirmed (seq %d): %w", ev.Seq, err)
	}
	shipper, err := uuid.Parse(e.ShipperAccountID)
	if err != nil {
		return fmt.Errorf("world/fleet: shipper_account_id inválido en freight.confirmed %s: %w", freightID, err)
	}
	origin, err := uuid.Parse(e.OriginNodeID)
	if err != nil {
		return fmt.Errorf("world/fleet: origin_node_id inválido en freight.confirmed %s: %w", freightID, err)
	}
	dest, err := uuid.Parse(e.DestinationNodeID)
	if err != nil {
		return fmt.Errorf("world/fleet: destination_node_id inválido en freight.confirmed %s: %w", freightID, err)
	}
	product, err := uuid.Parse(e.ProductID)
	if err != nil {
		return fmt.Errorf("world/fleet: product_id inválido en freight.confirmed %s: %w", freightID, err)
	}
	quantity, err := strconv.ParseInt(e.Quantity, 10, 64)
	if err != nil || quantity <= 0 {
		return fmt.Errorf("world/fleet: quantity inválida en freight.confirmed %s: %q", freightID, e.Quantity)
	}

	r := c.baseRepo.WithTx(tx)

	if exists, err := r.ShipmentExistsForFreightContract(ctx, freightID); err != nil {
		return err
	} else if exists {
		c.skipped.Inc()
		return nil
	}

	node, err := r.GetNode(ctx, origin)
	if err != nil {
		return fmt.Errorf("world/fleet: consultando el nodo de origen %s del flete %s: %w", origin, freightID, err)
	}
	if node.BuildingID == nil {
		c.logger.Warn("world/fleet: nodo de origen del flete sin almacén; no se materializa el cargamento",
			slog.String("freight_contract_id", freightID.String()), slog.String("origin_node_id", origin.String()))
		c.skipped.Inc()
		return nil
	}
	building := *node.BuildingID

	avail, err := r.GetInventoryQty(ctx, building, product)
	if err != nil {
		return err
	}
	if avail < quantity {
		c.logger.Error("world/fleet: stock físico insuficiente para materializar el cargamento del flete",
			slog.String("freight_contract_id", freightID.String()), slog.String("building_id", building.String()),
			slog.String("product_id", product.String()), slog.Int64("available", avail), slog.Int64("required", quantity))
		c.skipped.Inc()
		return nil
	}

	simNow := simtime.SimTime(ev.SimTimeAt)
	shipmentID, err := newUUIDv7()
	if err != nil {
		return err
	}
	if _, err := r.InsertFreightShipmentInWarehouse(ctx, insertFreightShipmentParams{
		ID: shipmentID, Owner: shipper, Product: product, Quantity: quantity, FreightID: freightID,
		AtNode: origin, Destination: dest, Deadline: simtime.SimTime(e.DeadlineSim), SimNow: simNow,
	}); err != nil {
		return err
	}
	// La carga deja el almacén físicamente (ya está en custodia contable).
	if err := r.ConsumeInventory(ctx, building, product, quantity, simNow); err != nil {
		return err
	}

	if err := outbox.Emit(ctx, tx, ev.SimTimeAt, AggregateShipment, shipmentID, EventShipmentCreated, ShipmentCreatedPayload{
		ShipmentID: shipmentID.String(), FreightContractID: freightID.String(), OwnerAccountID: shipper.String(),
		ProductID: product.String(), Quantity: fixed(quantity), OriginNodeID: origin.String(),
		DestinationNodeID: dest.String(), DeadlineSim: e.DeadlineSim, CreatedAtSim: ev.SimTimeAt,
	}); err != nil {
		return err
	}

	c.created.Inc()
	c.logger.Info("cargamento de flete creado desde freight.confirmed",
		slog.String("shipment_id", shipmentID.String()), slog.String("freight_contract_id", freightID.String()),
		slog.String("shipper", shipper.String()), slog.String("product_id", product.String()),
		slog.Int64("quantity", quantity), slog.String("origin_node_id", origin.String()),
		slog.String("destination_node_id", dest.String()))
	return nil
}
