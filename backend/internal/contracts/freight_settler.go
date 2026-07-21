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

// ConsumerFreightSettler es el nombre lógico del consumidor del outbox que
// liquida los fletes desde shipment.arrived (cursor propio en
// outbox.consumer_cursors, independiente del delivery_confirmer de bienes).
const ConsumerFreightSettler = "freight_settler"

// FreightSettler implementa el consumidor contracts "freight_settler" (GDD 5.3.2):
// consume shipment.arrived de cargamentos con freight_contract_id y liquida el
// flete pro-rata (la custodia va al cargador en el destino, donde la carga llegó
// físicamente; el transportista cobra el flete y recupera la garantía por lo
// entregado A TIEMPO; una entrega tardía no paga y penaliza la garantía). Es
// exactly-once por el cursor del outbox y, además, idempotente por
// (freight_contract_id, shipment_id).
type FreightSettler struct {
	svc    *Service
	logger *slog.Logger

	settled     *prometheus.CounterVec
	late        prometheus.Counter
	duplicated  prometheus.Counter
	afterSettle prometheus.Counter
}

// NewFreightSettler construye el liquidador de fletes sobre el Service del módulo
// (reutiliza su pool, repo, opciones y la liquidación pro-rata del flete). reg
// registra sus métricas (nil las deja sin instrumentar: tests).
func NewFreightSettler(svc *Service, logger *slog.Logger, reg prometheus.Registerer) (*FreightSettler, error) {
	if svc == nil {
		return nil, errors.New("contracts: el freight_settler requiere un Service")
	}
	if logger == nil {
		logger = svc.logger
	}
	c := &FreightSettler{
		svc:    svc,
		logger: logger,
		settled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_freight_deliveries_settled_total",
			Help: "Total de fletes liquidados desde shipment.arrived, por estado final (settled|failed).",
		}, []string{"status"}),
		late: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_freight_deliveries_late_total",
			Help: "Total de entregas de flete llegadas fuera de plazo (no pagan al transportista).",
		}),
		duplicated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_freight_deliveries_duplicate_total",
			Help: "Total de shipment.arrived de flete reprocesados (ignorados por idempotencia).",
		}),
		afterSettle: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_freight_deliveries_after_settle_total",
			Help: "Total de shipment.arrived de flete recibidos con el flete ya liquidado (registrados sin recontar).",
		}),
	}
	if reg != nil {
		reg.MustRegister(c.settled, c.late, c.duplicated, c.afterSettle)
	}
	return c, nil
}

// NewConsumer construye el consumidor lógico del outbox para shipment.arrived.
func (c *FreightSettler) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	return outbox.NewConsumer(pool, ConsumerFreightSettler, []string{eventShipmentArrived}, opts...)
}

// freightArrivedEvent es la vista del payload shipment.arrived que el
// freight_settler necesita: solo procesa los que llevan freight_contract_id.
type freightArrivedEvent struct {
	ShipmentID        string `json:"shipment_id"`
	FreightContractID string `json:"freight_contract_id"`
	Quantity          string `json:"quantity"`
	DestinationNodeID string `json:"destination_node_id"`
	ArrivedAtSim      int64  `json:"arrived_at_sim"`
}

// Handle procesa un shipment.arrived de flete dentro de la transacción del lote
// (exactly-once por el cursor). El flete se bloquea FOR UPDATE (se serializa con
// el barrido de vencimiento, que lo toma con SKIP LOCKED): una entrega que corra
// a la par del vencimiento no liquida dos veces.
func (c *FreightSettler) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventShipmentArrived {
		return nil
	}
	var e freightArrivedEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("contracts: leyendo shipment.arrived de flete (seq %d): %w", ev.Seq, err)
	}
	// Un cargamento SOLO de bienes (sin freight_contract_id) lo liquida el
	// delivery_confirmer, no este consumidor.
	if e.FreightContractID == "" {
		return nil
	}
	freightID, err := uuid.Parse(e.FreightContractID)
	if err != nil {
		return fmt.Errorf("contracts: freight_contract_id inválido en shipment.arrived (seq %d): %w", ev.Seq, err)
	}
	shipmentID, err := uuid.Parse(e.ShipmentID)
	if err != nil {
		return fmt.Errorf("contracts: shipment_id inválido en shipment.arrived de flete %s: %w", freightID, err)
	}
	quantity, err := strconv.ParseInt(e.Quantity, 10, 64)
	if err != nil || quantity <= 0 {
		return fmt.Errorf("contracts: quantity inválida en shipment.arrived de flete %s: %q", freightID, e.Quantity)
	}
	arrivedAtSim := simtime.SimTime(e.ArrivedAtSim)

	r := c.svc.repo.WithTx(tx)

	fc, err := r.GetFreightContractForUpdate(ctx, freightID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		c.logger.Error("contracts: shipment.arrived de un flete inexistente; entrega omitida",
			slog.String("freight_contract_id", freightID.String()), slog.String("shipment_id", shipmentID.String()))
		return nil
	case err != nil:
		return fmt.Errorf("contracts: bloqueando el flete %s de la entrega: %w", freightID, err)
	}

	onTime := arrivedAtSim <= fc.DeadlineSim
	inserted, err := r.InsertFreightDeliveryIfNew(ctx, freightID, shipmentID, quantity, arrivedAtSim, onTime)
	if err != nil {
		return err
	}
	if !inserted {
		c.duplicated.Inc()
		c.logger.Debug("contracts: shipment.arrived de flete ya procesado; no duplicado",
			slog.String("freight_contract_id", freightID.String()), slog.String("shipment_id", shipmentID.String()))
		return nil
	}

	// Flete ya no activo (lo falló antes el barrido de vencimiento): la entrega se
	// registra para auditoría, sin liquidar de nuevo.
	if fc.Status != ContractActive {
		c.afterSettle.Inc()
		c.logger.Info("contracts: entrega de flete recibida con el flete ya liquidado; registrada sin recontar",
			slog.String("freight_contract_id", freightID.String()), slog.String("shipment_id", shipmentID.String()),
			slog.String("status", string(fc.Status)))
		return nil
	}

	// La carga llegó FÍSICAMENTE al destino: la custodia va al cargador allí. El
	// pago se basa en lo entregado A TIEMPO (tardío ⇒ 0, no paga y penaliza la
	// garantía, pero el cargador recibe igualmente su mercancía en el destino).
	delivered := quantity
	if !onTime {
		delivered = 0
		c.late.Inc()
	}
	out, err := c.svc.settleFreightAndEmit(ctx, r, tx, fc, delivered, fc.DestinationNodeID, arrivedAtSim)
	if err != nil {
		return err
	}
	c.settled.WithLabelValues(string(out.Status)).Inc()
	c.svc.logFreightSettled(out, delivered, "entrega")
	return nil
}
