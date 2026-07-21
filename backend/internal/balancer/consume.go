package balancer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// ConsumerName es el nombre lógico del consumidor del outbox que consume las
// entregas urbanas. Fija su cursor propio en outbox.consumer_cursors: procesar
// es exactly-once por consumidor (el consumo, la actualización de la curva y el
// avance del cursor se confirman en la misma transacción). Es un consumidor
// DISTINTO del agregador OHLC: ambos consumen contract.settled con su cursor.
const ConsumerName = "city_consumer"

// eventContractSettled es el único tipo que consume el Balancer. Se declara aquí
// (no se importa contracts) para no cruzar la frontera de servicio: el contrato
// entre agentes es el nombre del evento y su payload, no el código Go que lo
// emite.
const eventContractSettled = "contract.settled"

// Estados terminales del contrato en el payload de contract.settled.
const (
	statusSettled = "settled"
	statusFailed  = "failed"
)

// settledPayload es el subconjunto del payload de contract.settled que el
// consumer necesita (contract_id + status). Los datos de la entrega (comprador,
// producto, destino, cantidad) se releen del contrato: el evento no lleva el
// comprador ni el nodo destino, y la fuente autoritativa es ledger.contracts.
type settledPayload struct {
	ContractID string `json:"contract_id"`
	Status     string `json:"status"`
}

// Consumer consume las entregas urbanas (contract.settled cuyo comprador es una
// ciudad): CONSUME lo entregado (city stock_free → world_source, transacción
// consumption, ADR-022; y descuenta el inventario físico del centro de
// distribución para no romper la reconciliación físico↔contable) y alimenta la
// curva de demanda (recent_supply para el EMA) y el crecimiento (supply_index,
// ponderado por variedad). Así la ciudad es sumidero final real (GDD 5.6) y no
// acumula inventario. Es el handler de un Consumer del outbox.
type Consumer struct {
	repo    *Repo
	opts    Options
	metrics *Metrics
	logger  *slog.Logger
}

// NewConsumer construye el consumer de entregas urbanas. metrics/logger pueden
// ser nil (sin instrumentar / logger por defecto). Options inválidas devuelven
// error: no arrancar.
func NewConsumer(opts Options, metrics *Metrics, logger *slog.Logger) (*Consumer, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{
		repo:    NewRepo(nil), // se re-liga a la transacción del lote en Handle
		opts:    opts,
		metrics: metrics,
		logger:  logger.With(slog.String("module", "balancer"), slog.String("consumer", ConsumerName)),
	}, nil
}

// NewOutboxConsumer construye el Consumer del outbox suscrito a contract.settled
// con el ConsumerName y su cursor propio. El llamante lo arranca con
// Run(ctx, interval, c.Handle).
func (c *Consumer) NewOutboxConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	return outbox.NewConsumer(pool, ConsumerName, []string{eventContractSettled}, opts...)
}

// Handle procesa UN evento contract.settled dentro de la transacción del lote
// (firma outbox.Handler). Ignora los contratos fallidos, las entregas nulas y
// los compradores que no son ciudades (avanzando el cursor). Para una entrega
// urbana efectiva consume el stock y alimenta la curva/crecimiento, todo en tx.
func (c *Consumer) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventContractSettled {
		return nil // el Consumer solo entrega el tipo suscrito; defensa
	}
	var p settledPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("balancer: payload de %s (seq %d) ilegible: %w", ev.EventType, ev.Seq, err)
	}
	if p.Status == statusFailed {
		return nil // fill 0%: nada entregado que consumir
	}
	if p.Status != statusSettled {
		return fmt.Errorf("balancer: status inesperado %q en %s (seq %d)", p.Status, ev.EventType, ev.Seq)
	}
	contractID, err := uuid.Parse(p.ContractID)
	if err != nil {
		return fmt.Errorf("balancer: contract_id inválido en %s (seq %d): %w", ev.EventType, ev.Seq, err)
	}

	r := c.repo.WithTx(tx)
	ct, err := r.GetContractForConsume(ctx, contractID)
	if err != nil {
		return fmt.Errorf("balancer: releyendo el contrato %s (seq %d): %w", contractID, ev.Seq, err)
	}
	isCity, err := r.IsCityAccount(ctx, ct.BuyerAccountID)
	if err != nil {
		return err
	}
	if !isCity {
		return nil // compra de un jugador/bot, no de una ciudad: no es sumidero urbano
	}
	if ct.QuantityDelivered <= 0 {
		return nil // sin entrega (fill 0% ya cubierto por status; defensa)
	}

	node, err := r.GetNodeBuilding(ctx, ct.DestinationNodeID)
	if err != nil {
		return fmt.Errorf("balancer: nodo destino %s del contrato %s (seq %d): %w", ct.DestinationNodeID, contractID, ev.Seq, err)
	}
	if node.BuildingID == nil {
		return fmt.Errorf("balancer: el nodo destino %s del contrato %s no tiene edificio (centro de distribución)", ct.DestinationNodeID, contractID)
	}
	if node.CityID == nil {
		return fmt.Errorf("balancer: el nodo destino %s del contrato %s no está ligado a una ciudad", ct.DestinationNodeID, contractID)
	}
	buildingID := *node.BuildingID
	cityID := *node.CityID
	simNow := simtime.SimTime(ev.SimTimeAt)
	qty := ct.QuantityDelivered

	// (1) Consumo contable: +qty world_source / −qty city stock_free (ADR-022): el
	// stock "vuelve al mundo"/se destruye. La ciudad es sumidero final.
	stockFree, err := r.GetStockFreeAccount(ctx, ct.BuyerAccountID, ct.ProductID, buildingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("balancer: la ciudad %s no tiene stock_free de %s en su centro de distribución %s (contrato %s)",
			ct.BuyerAccountID, ct.ProductID, buildingID, contractID)
	case err != nil:
		return err
	}
	worldSource, err := r.EnsureWorldSourceAccount(ctx, ct.ProductID)
	if err != nil {
		return err
	}
	if _, err := r.PostLedgerTransaction(ctx, txKindConsumption, simNow, contractID,
		fmt.Sprintf("Consumo final de ciudad: %d de %s", qty, ct.ProductID),
		[]entryAmount{
			{AccountID: worldSource, Amount: qty},
			{AccountID: stockFree.ID, Amount: -qty},
		}); err != nil {
		return err
	}

	// (2) Consumo físico: descuenta el inventario del centro de distribución
	// (mantiene físico↔contable en sincronía, ADR-004).
	if err := r.ConsumeBuildingInventory(ctx, buildingID, ct.ProductID, qty, simNow); err != nil {
		return err
	}

	// (3) Alimenta el EMA de oferta: recent_supply += qty. El acumulado previo
	// (resultante − qty) == 0 significa PRIMER suministro del producto en la
	// ventana → producto "nuevo" → bono de variedad en el supply_index.
	variety := false
	if resulting, found, err := r.AddRecentSupply(ctx, cityID, ct.ProductID, qty); err != nil {
		return err
	} else if found {
		variety = resulting-qty == 0
	}

	// (4) Índice de suministro histórico (crecimiento de ciudad, GDD 5.6),
	// ponderado por variedad (producto nuevo suma más).
	delta := float64(qty)
	if variety {
		delta *= 1 + float64(c.opts.VarietyBonusPct)/100
	}
	if _, err := r.AddCitySupplyIndex(ctx, cityID, delta); err != nil {
		return err
	}

	c.metrics.addConsumed(ct.ProductID.String(), qty)
	c.logger.LogAttrs(ctx, slog.LevelDebug, "entrega urbana consumida",
		slog.Int64("seq", ev.Seq),
		slog.String("city_id", cityID.String()),
		slog.String("product_id", ct.ProductID.String()),
		slog.Int64("quantity", qty),
		slog.Bool("variety", variety))
	return nil
}
