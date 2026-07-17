package market

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/market/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
)

// ConsumerName es el nombre lógico del consumidor del outbox que agrega las
// velas OHLC. Fija su cursor propio en outbox.consumer_cursors: procesar es
// exactly-once por consumidor (la vela y el avance del cursor se confirman en
// la misma transacción).
const ConsumerName = "ohlc_aggregator"

// eventContractSettled es el único tipo de evento que consume el agregador.
// Se declara aquí (no se importa contracts) para no cruzar bounded contexts:
// el contrato entre módulos es el nombre del evento y su payload JSON, no el
// código Go que lo emite.
const eventContractSettled = "contract.settled"

// Estados terminales del contrato en el payload de contract.settled.
const (
	statusSettled = "settled"
	statusFailed  = "failed"
)

// settledPayload es el payload documentado de contract.settled (contrato entre
// agentes del Incremento 1). Dinero y stock viajan como string de punto fijo,
// jamás float; los sim-time como enteros.
type settledPayload struct {
	ContractID          string `json:"contract_id"`
	ProductID           string `json:"product_id"`
	DestinationRegionID string `json:"destination_region_id"`
	UnitPrice           string `json:"unit_price"`
	QuantityAgreed      string `json:"quantity_agreed"`
	QuantityDelivered   string `json:"quantity_delivered"`
	FillBP              int    `json:"fill_bp"`
	SettledAtSim        int64  `json:"settled_at_sim"`
	Status              string `json:"status"`
}

// Aggregator construye el historial OHLC (GDD 5.2) a partir de los eventos
// contract.settled del outbox: velas SOLO de contratos efectivamente
// liquidados con entrega positiva. Es el handler de un Consumer del outbox y
// hace su UPSERT DENTRO de la transacción del lote, de modo que la vela y el
// avance del cursor se confirman juntos (exactly-once por consumidor).
type Aggregator struct {
	bucketSimSecs int64
	metrics       *Metrics
	logger        *slog.Logger
}

// NewAggregator construye el agregador. opts.OhlcBucketSimSeconds fija el
// tamaño del bucket; metrics y logger pueden ser nil (sin instrumentar / logger
// por defecto). Una configuración inválida devuelve error: no arrancar.
func NewAggregator(opts Options, metrics *Metrics, logger *slog.Logger) (*Aggregator, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Aggregator{
		bucketSimSecs: opts.OhlcBucketSimSeconds,
		metrics:       metrics,
		logger:        logger.With(slog.String("module", "market"), slog.String("consumer", ConsumerName)),
	}, nil
}

// NewConsumer construye el Consumer del outbox del agregador, suscrito a
// contract.settled con el ConsumerName y su cursor propio. El llamante lo
// arranca con Run(ctx, interval, agg.Handle).
func (a *Aggregator) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	return outbox.NewConsumer(pool, ConsumerName, []string{eventContractSettled}, opts...)
}

// Handle procesa UN evento contract.settled dentro de la transacción del lote
// (firma outbox.Handler). Ignora los contratos fallidos (status=failed) y las
// entregas nulas (quantity_delivered=0): no producen precio de mercado. Para
// las entregas efectivas calcula el bucket y hace UPSERT de la vela en tx. Un
// payload que no se puede interpretar devuelve error: el lote se revierte y se
// reintenta — nunca se avanza el cursor sobre un evento no procesado.
func (a *Aggregator) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventContractSettled {
		// El Consumer solo entrega el tipo suscrito; defensa ante un cambio de
		// suscripción que no debería llegar hasta aquí.
		return nil
	}

	var p settledPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		return fmt.Errorf("market: payload de %s (seq %d) ilegible: %w", ev.EventType, ev.Seq, err)
	}

	// Solo los contratos liquidados con entrega positiva forman precio (GDD
	// 5.2: velas de contratos efectivamente cerrados). failed y entrega 0 se
	// ignoran avanzando el cursor.
	if p.Status == statusFailed {
		return nil
	}
	if p.Status != statusSettled {
		return fmt.Errorf("market: status inesperado %q en %s (seq %d)", p.Status, ev.EventType, ev.Seq)
	}

	delivered, err := parseFixed(p.QuantityDelivered)
	if err != nil {
		return fmt.Errorf("market: quantity_delivered inválido en %s (seq %d): %w", ev.EventType, ev.Seq, err)
	}
	if delivered == 0 {
		return nil
	}

	unitPrice, err := parseFixed(p.UnitPrice)
	if err != nil {
		return fmt.Errorf("market: unit_price inválido en %s (seq %d): %w", ev.EventType, ev.Seq, err)
	}
	productID, err := uuid.Parse(p.ProductID)
	if err != nil {
		return fmt.Errorf("market: product_id inválido en %s (seq %d): %w", ev.EventType, ev.Seq, err)
	}
	regionID, err := uuid.Parse(p.DestinationRegionID)
	if err != nil {
		return fmt.Errorf("market: destination_region_id inválido en %s (seq %d): %w", ev.EventType, ev.Seq, err)
	}
	if p.SettledAtSim < 0 {
		return fmt.Errorf("market: settled_at_sim negativo %d en %s (seq %d)", p.SettledAtSim, ev.EventType, ev.Seq)
	}

	bucketStart := bucketStart(p.SettledAtSim, a.bucketSimSecs)

	// UPSERT en la MISMA transacción que el consumidor usa para avanzar su
	// cursor: la vela y el cursor se confirman juntos (exactly-once).
	q := sqlcgen.New(tx)
	if err := q.UpsertOhlcCandle(ctx, sqlcgen.UpsertOhlcCandleParams{
		ProductID:         productID,
		RegionID:          regionID,
		BucketStartSim:    bucketStart,
		BucketSimSecs:     a.bucketSimSecs,
		UnitPrice:         unitPrice,
		QuantityDelivered: delivered,
	}); err != nil {
		return fmt.Errorf("market: UPSERT de la vela OHLC (producto %s, región %s, bucket %d): %w",
			productID, regionID, bucketStart, err)
	}

	a.metrics.incCandleUpserted()
	a.logger.LogAttrs(ctx, slog.LevelDebug, "vela OHLC agregada",
		slog.Int64("seq", ev.Seq),
		slog.String("product_id", productID.String()),
		slog.String("region_id", regionID.String()),
		slog.Int64("bucket_start_sim", bucketStart),
		slog.Int64("unit_price", unitPrice),
		slog.Int64("quantity_delivered", delivered))
	return nil
}

// bucketStart calcula el inicio del bucket en sim-time:
// floor(settledAtSim / bucketSecs) * bucketSecs. settledAtSim y bucketSecs son
// no negativos (dominio sim_time y configuración validada > 0): la división
// entera de Go trunca hacia cero, que para no negativos es floor.
func bucketStart(settledAtSim, bucketSecs int64) int64 {
	return (settledAtSim / bucketSecs) * bucketSecs
}

// parseFixed interpreta un importe/cantidad de punto fijo (string ^[0-9]+$ del
// contrato) como int64. Rechaza el vacío, el signo y el desbordamiento.
func parseFixed(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("valor de punto fijo vacío")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q no es un entero de punto fijo de 64 bits: %w", s, err)
	}
	if n < 0 {
		return 0, fmt.Errorf("%q es negativo", s)
	}
	return n, nil
}
