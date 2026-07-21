package contracts

import (
	"context"
	"encoding/json"
	"errors"
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

// ConsumerDeliveryConfirmer es el nombre lógico del consumidor del outbox que
// confirma las entregas del CCRI desde shipment.arrived (cursor propio en
// outbox.consumer_cursors).
const ConsumerDeliveryConfirmer = "delivery_confirmer"

// eventShipmentArrived es el tipo del evento que emite world (motor de tránsito)
// cuando un cargamento llega FÍSICAMENTE a su nodo de destino. Se declara aquí
// como string: contracts NUNCA importa internal/world — la integración es SOLO
// por el outbox (SAD §7 / ADR-006).
const eventShipmentArrived = "shipment.arrived"

// DeliveryConfirmer implementa el consumidor contracts "delivery_confirmer"
// (GDD 5.3 pasos 5-6): consume shipment.arrived, asienta la entrega del contrato
// (ledger.contract_deliveries), acumula quantity_delivered A TIEMPO y —al
// completar la cantidad pactada— liquida ya con ledger.settle_contract_prorata.
// La entrega es exactly-once por el cursor del outbox y, además, idempotente por
// shipment_id (índice único): reprocesar el mismo hito no cuenta dos veces.
type DeliveryConfirmer struct {
	svc    *Service
	logger *slog.Logger

	delivered   prometheus.Counter
	late        prometheus.Counter
	settled     prometheus.Counter
	duplicated  prometheus.Counter
	afterSettle prometheus.Counter
}

// NewDeliveryConfirmer construye el confirmador de entregas sobre el Service del
// módulo (reutiliza su pool, repo, opciones y la liquidación pro-rata). reg
// registra sus métricas (nil las deja sin instrumentar: tests). logger nil usa
// el del servicio.
func NewDeliveryConfirmer(svc *Service, logger *slog.Logger, reg prometheus.Registerer) (*DeliveryConfirmer, error) {
	if svc == nil {
		return nil, errors.New("contracts: el delivery_confirmer requiere un Service")
	}
	if logger == nil {
		logger = svc.logger
	}
	c := &DeliveryConfirmer{
		svc:    svc,
		logger: logger,
		delivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contract_deliveries_confirmed_total",
			Help: "Total de entregas físicas del CCRI confirmadas desde shipment.arrived.",
		}),
		late: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contract_deliveries_late_total",
			Help: "Total de entregas llegadas fuera de plazo (no cuentan para el pago).",
		}),
		settled: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contract_deliveries_settled_total",
			Help: "Total de contratos liquidados al completarse la entrega (fill 100%).",
		}),
		duplicated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contract_deliveries_duplicate_total",
			Help: "Total de shipment.arrived reprocesados (entrega ya asentada; ignorados por idempotencia).",
		}),
		afterSettle: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contract_deliveries_after_settle_total",
			Help: "Total de shipment.arrived recibidos cuando el contrato ya no está activo (registrados, sin liquidar de nuevo).",
		}),
	}
	if reg != nil {
		reg.MustRegister(c.delivered, c.late, c.settled, c.duplicated, c.afterSettle)
	}
	return c, nil
}

// NewConsumer construye el consumidor lógico del outbox para shipment.arrived.
// Lo arranca el engine con Handle como handler.
func (c *DeliveryConfirmer) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	return outbox.NewConsumer(pool, ConsumerDeliveryConfirmer, []string{eventShipmentArrived}, opts...)
}

// shipmentArrivedEvent es el payload FIJO de shipment.arrived (contrato de
// integración CCRI↔Logística). quantity y sim-time viajan como en todo el
// outbox: importes/cantidades como string de punto fijo, sim-time como entero.
type shipmentArrivedEvent struct {
	ShipmentID        string `json:"shipment_id"`
	ContractID        string `json:"contract_id"`
	Quantity          string `json:"quantity"`
	DestinationNodeID string `json:"destination_node_id"`
	ArrivedAtSim      int64  `json:"arrived_at_sim"`
}

// Handle procesa un shipment.arrived dentro de la transacción del lote del
// consumidor (los efectos se confirman con el avance del cursor: exactly-once).
// El contrato se bloquea FOR UPDATE (se serializa con el barrido de vencimiento,
// que lo toma con SKIP LOCKED): así una entrada tardía que corra a la par del
// vencimiento no liquida dos veces.
func (c *DeliveryConfirmer) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventShipmentArrived {
		return nil
	}
	var e shipmentArrivedEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("contracts: leyendo shipment.arrived (seq %d): %w", ev.Seq, err)
	}
	// Un cargamento SOLO de flete (sin contract_id de bienes) no lo liquida este
	// consumidor: lo hace el freight_settler por su freight_contract_id. Un
	// cargamento con ambos ids (composición plena flete↔venta) dispara ambos.
	if e.ContractID == "" {
		return nil
	}
	contractID, err := uuid.Parse(e.ContractID)
	if err != nil {
		return fmt.Errorf("contracts: contract_id inválido en shipment.arrived (seq %d): %w", ev.Seq, err)
	}
	shipmentID, err := uuid.Parse(e.ShipmentID)
	if err != nil {
		return fmt.Errorf("contracts: shipment_id inválido en shipment.arrived %s: %w", contractID, err)
	}
	quantity, err := strconv.ParseInt(e.Quantity, 10, 64)
	if err != nil || quantity <= 0 {
		return fmt.Errorf("contracts: quantity inválida en shipment.arrived %s: %q", contractID, e.Quantity)
	}
	arrivedAtSim := simtime.SimTime(e.ArrivedAtSim)

	r := c.svc.repo.WithTx(tx)

	contract, err := r.GetContractForUpdate(ctx, contractID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// El cargamento referencia un contrato inexistente: inconsistencia de
		// datos irrecuperable. Se registra y se omite (no bloquear el cursor).
		c.logger.Error("contracts: shipment.arrived de un contrato inexistente; entrega omitida",
			slog.String("contract_id", contractID.String()), slog.String("shipment_id", shipmentID.String()))
		return nil
	case err != nil:
		return fmt.Errorf("contracts: bloqueando el contrato %s de la entrega: %w", contractID, err)
	}

	onTime := arrivedAtSim <= contract.DeadlineSim

	deliveryID, err := newUUIDv7()
	if err != nil {
		return err
	}
	delivery, inserted, err := r.InsertContractDeliveryIfNew(ctx, deliveryID, contractID, shipmentID, quantity, arrivedAtSim, onTime)
	if err != nil {
		return err
	}
	if !inserted {
		// Idempotencia: el cargamento ya tenía su entrega asentada (reintento del
		// lote o redespliegue). No se cuenta ni se emite de nuevo.
		c.duplicated.Inc()
		c.logger.Debug("contracts: shipment.arrived ya procesado; entrega no duplicada",
			slog.String("contract_id", contractID.String()), slog.String("shipment_id", shipmentID.String()))
		return nil
	}

	// Contrato ya no activo (lo liquidó antes el barrido de vencimiento o una
	// entrega previa que completó la cantidad): la entrega se registra para
	// auditoría, pero NO se acumula ni se liquida de nuevo (settle_contract_prorata
	// exige 'active'). El lado físico (liberación in situ del cargamento) ya lo
	// resolvió world con contract.expired_undelivered.
	if contract.Status != ContractActive {
		c.afterSettle.Inc()
		c.logger.Info("contracts: entrega recibida con el contrato ya liquidado; registrada sin recontar",
			slog.String("contract_id", contractID.String()), slog.String("shipment_id", shipmentID.String()),
			slog.String("status", string(contract.Status)), slog.Bool("on_time", onTime))
		return nil
	}

	// Solo lo entregado A TIEMPO cuenta para el pago (GDD 5.3 paso 6). El tope a
	// quantity_agreed es defensivo (respeta el CHECK quantity_delivered <=
	// quantity_agreed ante cualquier suma de cargamentos que lo excediera).
	if onTime {
		newDelivered := contract.QuantityDelivered + quantity
		if newDelivered > contract.QuantityAgreed {
			newDelivered = contract.QuantityAgreed
		}
		updated, err := r.SetContractQuantityDelivered(ctx, contractID, newDelivered)
		if err != nil {
			return err
		}
		contract = updated
	} else {
		c.late.Inc()
	}
	c.delivered.Inc()

	if err := outbox.Emit(ctx, tx, int64(arrivedAtSim), AggregateContract, contractID, EventContractDelivered, ContractDeliveredPayload{
		ContractID:        contractID.String(),
		DeliveryID:        delivery.ID.String(),
		ShipmentID:        shipmentID.String(),
		Quantity:          fixed(quantity),
		QuantityDelivered: fixed(contract.QuantityDelivered),
		DeliveredAtSim:    int64(arrivedAtSim),
		OnTime:            onTime,
	}); err != nil {
		return err
	}

	c.logger.Info("entrega del CCRI confirmada desde shipment.arrived",
		slog.String("contract_id", contractID.String()), slog.String("shipment_id", shipmentID.String()),
		slog.Int64("quantity", quantity), slog.Int64("quantity_delivered", contract.QuantityDelivered),
		slog.Bool("on_time", onTime))

	// Cantidad pactada completada a tiempo: liquida ya (fill 100% ⇒ settled).
	if onTime && contract.QuantityDelivered >= contract.QuantityAgreed {
		if _, err := c.svc.settleAndEmit(ctx, r, tx, contract, arrivedAtSim); err != nil {
			return err
		}
		c.settled.Inc()
		c.logger.Info("contrato liquidado al completarse la entrega",
			slog.String("contract_id", contractID.String()),
			slog.Int64("quantity_delivered", contract.QuantityDelivered))
	}
	return nil
}
