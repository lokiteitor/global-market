package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultBatchSize es el tamaño máximo de lote por defecto del polling.
const DefaultBatchSize = 100

// maxBackoff acota el backoff exponencial tras fallos consecutivos del lote.
const maxBackoff = 30 * time.Second

// Códigos SQLSTATE de contención transitoria: el lote debe reintentarse sin
// escalar el backoff (no es un fallo del handler ni de la infraestructura).
const (
	sqlstateSerializationFailure = "40001"
	sqlstateDeadlockDetected     = "40P01"
)

// Handler procesa UN evento dentro de la transacción del lote. Sus efectos
// (escrituras a través de tx) se confirman junto con el avance del cursor:
// si devuelve error, el lote ENTERO se revierte —ningún efecto parcial— y se
// reintenta con backoff. El handler debe ser por tanto determinista respecto
// al evento: se puede volver a invocar con el mismo evento tras un rollback.
type Handler func(ctx context.Context, tx pgx.Tx, ev Event) error

// Consumer es un consumidor lógico del outbox con cursor propio en
// outbox.consumer_cursors (equivalente barato a un consumer group de Kafka).
// Varios procesos con el mismo nombre comparten cursor y se serializan con
// el lock de su fila: a lo sumo uno procesa un lote a la vez.
type Consumer struct {
	pool       *pgxpool.Pool
	name       string
	eventTypes []string
	batchSize  int
	logger     *slog.Logger
}

// ConsumerOption ajusta la configuración opcional del consumidor.
type ConsumerOption func(*Consumer)

// WithBatchSize fija el tamaño máximo del lote de polling (por defecto
// DefaultBatchSize). Valores <= 0 hacen fallar Run.
func WithBatchSize(n int) ConsumerOption {
	return func(c *Consumer) { c.batchSize = n }
}

// WithLogger fija el logger del consumidor (slog.Default() si no se indica).
func WithLogger(l *slog.Logger) ConsumerOption {
	return func(c *Consumer) {
		if l != nil {
			c.logger = l
		}
	}
}

// NewConsumer construye el consumidor lógico name, interesado en los tipos de
// evento eventTypes. El cursor se registra on-demand en el primer polling
// (INSERT ... ON CONFLICT sobre outbox.consumer_cursors), que además deja la
// SUSCRIPCIÓN declarada en la fila (event_types, 0016): sin ella el retraso de
// un consumidor no se puede medir desde fuera del proceso. No hay alta previa.
// La configuración se valida en Run: una configuración rota impide arrancar el
// bucle, no la construcción.
func NewConsumer(pool *pgxpool.Pool, name string, eventTypes []string, opts ...ConsumerOption) *Consumer {
	c := &Consumer{
		pool:       pool,
		name:       name,
		eventTypes: append([]string(nil), eventTypes...),
		batchSize:  DefaultBatchSize,
		logger:     slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Run ejecuta el bucle de polling hasta que ctx se cancele (devuelve nil en
// el apagado limpio; error solo ante configuración inválida). Cada iteración
// procesa UN lote en UNA transacción SERIALIZABLE: registra/bloquea el
// cursor (FOR UPDATE), lee hasta batchSize eventos con seq > cursor de los
// tipos suscritos en orden de seq, invoca el handler evento a evento dentro
// de esa transacción y avanza el cursor ANTES del COMMIT — exactly-once por
// consumidor. Si el handler (o el COMMIT) falla, el lote entero se revierte
// y se reintenta: con el intervalo base ante contención transitoria (40001/
// 40P01) y con backoff exponencial (cap 30s) ante cualquier otro fallo. Un
// lote lleno encadena el siguiente polling sin esperar el intervalo
// (drenaje).
func (c *Consumer) Run(ctx context.Context, interval time.Duration, handler Handler) error {
	if err := c.validate(interval, handler); err != nil {
		return err
	}
	logger := c.logger.With(slog.String("consumer", c.name))
	logger.Info("outbox: consumidor iniciado",
		slog.Any("event_types", c.eventTypes),
		slog.Int("batch_size", c.batchSize),
		slog.Duration("interval", interval))

	backoff := min(interval, maxBackoff)
	for {
		n, err := c.poll(ctx, handler)
		switch {
		case ctx.Err() != nil:
			logger.Info("outbox: consumidor detenido")
			return nil
		case isTransientTxFailure(err):
			logger.Debug("outbox: contención transitoria; se reintenta el lote",
				slog.Any("error", err))
			if !sleep(ctx, interval) {
				logger.Info("outbox: consumidor detenido")
				return nil
			}
			continue
		case err != nil:
			logger.Warn("outbox: lote revertido; se reintenta con backoff",
				slog.Any("error", err),
				slog.Duration("backoff", backoff))
			if !sleep(ctx, backoff) {
				logger.Info("outbox: consumidor detenido")
				return nil
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = min(interval, maxBackoff)
		if n > 0 {
			logger.Debug("outbox: lote confirmado", slog.Int("events", n))
		}
		if n >= c.batchSize {
			continue // lote lleno: drenar sin esperar el intervalo
		}
		if !sleep(ctx, interval) {
			logger.Info("outbox: consumidor detenido")
			return nil
		}
	}
}

// validate comprueba la configuración del consumidor antes de arrancar el
// bucle. Una configuración rota debe impedir el arranque.
func (c *Consumer) validate(interval time.Duration, handler Handler) error {
	if c.pool == nil {
		return errors.New("outbox: el consumidor requiere un pool de BD")
	}
	if strings.TrimSpace(c.name) == "" {
		return errors.New("outbox: el consumidor requiere un nombre")
	}
	if len(c.eventTypes) == 0 {
		return fmt.Errorf("outbox: el consumidor %s no declara tipos de evento", c.name)
	}
	for _, et := range c.eventTypes {
		if strings.TrimSpace(et) == "" {
			return fmt.Errorf("outbox: el consumidor %s declara un tipo de evento vacío", c.name)
		}
	}
	if c.batchSize <= 0 {
		return fmt.Errorf("outbox: tamaño de lote inválido %d (debe ser > 0)", c.batchSize)
	}
	if interval <= 0 {
		return fmt.Errorf("outbox: intervalo de polling inválido %s (debe ser > 0)", interval)
	}
	if handler == nil {
		return fmt.Errorf("outbox: el consumidor %s requiere un handler", c.name)
	}
	return nil
}

// lagProbeLimit acota la medición del retraso durante el DRENAJE: por encima
// de este número de eventos pendientes el gauge satura (la lectura operativa
// —«backlog grande»— es la misma) y ponerse al día no se vuelve cuadrático.
// La magnitud exacta de un backlog mayor la dan el probe de stress y
// ii_outbox_lag_observed.
const lagProbeLimit = 10_000

// SQL del polling. El cursor con FOR UPDATE serializa instancias concurrentes
// del mismo consumidor; el índice ix_outbox_type_seq soporta el filtro por
// tipo + orden por seq (0006_outbox).
const (
	// sqlEnsureCursor registra el cursor on-demand y mantiene al día la
	// SUSCRIPCIÓN declarada en la fila (0016_outbox_consumer_interest): es lo
	// que permite medir el retraso REAL de este consumidor —eventos de SUS
	// tipos por encima de su cursor— en vez de compararlo con la cabecera
	// global del outbox, que un consumidor de eventos raros nunca alcanza. El
	// UPDATE solo se ejecuta si la suscripción cambió: en régimen estacionario
	// el polling no escribe esta fila.
	sqlEnsureCursor = `
INSERT INTO outbox.consumer_cursors (consumer_name, event_types)
VALUES ($1, $2)
ON CONFLICT (consumer_name) DO UPDATE
   SET event_types = EXCLUDED.event_types, updated_at = now()
 WHERE consumer_cursors.event_types IS DISTINCT FROM EXCLUDED.event_types`

	sqlLockCursor = `
SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = $1 FOR UPDATE`

	sqlReadBatch = `
SELECT seq, event_id, aggregate_type, aggregate_id, event_type, payload, sim_time_at, created_at
FROM outbox.events
WHERE seq > $1 AND event_type = ANY($2)
ORDER BY seq
LIMIT $3`

	sqlAdvanceCursor = `
UPDATE outbox.consumer_cursors SET last_seq = $2, updated_at = now() WHERE consumer_name = $1`

	// sqlPendingCount cuenta los eventos DE LOS TIPOS SUSCRITOS que quedan por
	// encima del cursor (retraso real del consumidor), acotado a $3.
	sqlPendingCount = `
SELECT count(*) FROM (
  SELECT 1 FROM outbox.events
   WHERE seq > $1 AND event_type = ANY($2)
   LIMIT $3
) pending`
)

// poll procesa UN lote y devuelve cuántos eventos confirmó. Todo ocurre en
// una única transacción SERIALIZABLE (regla de oro: los handlers pueden mover
// valor): cursor bloqueado, handler por evento y UPDATE del cursor; el COMMIT
// confirma efectos y cursor a la vez. Cualquier error revierte el lote
// completo — el llamante (Run) reintenta.
func (c *Consumer) poll(ctx context.Context, handler Handler) (int, error) {
	tx, err := c.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, fmt.Errorf("outbox: abriendo la transacción del lote: %w", err)
	}
	// Rollback tras Commit devuelve ErrTxClosed: inocuo.
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	// Registro on-demand del consumidor (con su suscripción) y lock de su cursor.
	if _, err := tx.Exec(ctx, sqlEnsureCursor, c.name, c.eventTypes); err != nil {
		return 0, fmt.Errorf("outbox: registrando el consumidor %s: %w", c.name, err)
	}
	var cursor int64
	if err := tx.QueryRow(ctx, sqlLockCursor, c.name).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("outbox: leyendo el cursor de %s: %w", c.name, err)
	}

	events, err := readBatch(ctx, tx, cursor, c.eventTypes, c.batchSize)
	if err != nil {
		return 0, fmt.Errorf("outbox: leyendo el lote de %s: %w", c.name, err)
	}

	for _, ev := range events {
		if err := handler(ctx, tx, ev); err != nil {
			return 0, fmt.Errorf("outbox: handler de %s falló en seq %d (%s): %w",
				c.name, ev.Seq, ev.EventType, err)
		}
	}

	newCursor := cursor
	if len(events) > 0 {
		newCursor = events[len(events)-1].Seq
		if _, err := tx.Exec(ctx, sqlAdvanceCursor, c.name, newCursor); err != nil {
			return 0, fmt.Errorf("outbox: avanzando el cursor de %s: %w", c.name, err)
		}
	}

	// Retraso REAL tras el lote: eventos DE LOS TIPOS SUSCRITOS por encima del
	// cursor, en el snapshot de esta transacción. NO se compara con la cabecera
	// global del outbox: los eventos ajenos no son trabajo de este consumidor y
	// contarlos daba un retraso fantasma que crecía con la historia del mundo.
	// Si el lote no llenó el LIMIT, la propia lectura ya demostró que no queda
	// ninguno: 0 sin consultar. Solo el drenaje paga la cuenta, acotada.
	var pending int64
	if len(events) >= c.batchSize {
		if err := tx.QueryRow(ctx, sqlPendingCount, newCursor, c.eventTypes, lagProbeLimit).Scan(&pending); err != nil {
			return 0, fmt.Errorf("outbox: midiendo el retraso de %s: %w", c.name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("outbox: confirmando el lote de %s: %w", c.name, err)
	}

	if len(events) > 0 {
		eventsProcessed.WithLabelValues(c.name).Add(float64(len(events)))
	}
	consumerLag.WithLabelValues(c.name).Set(float64(pending))
	return len(events), nil
}

// readBatch materializa el lote completo antes de invocar los handlers: la
// transacción usa una sola conexión y el cursor de filas debe cerrarse antes
// de ejecutar más queries sobre ella.
func readBatch(ctx context.Context, tx pgx.Tx, cursor int64, types []string, limit int) ([]Event, error) {
	rows, err := tx.Query(ctx, sqlReadBatch, cursor, types, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.Seq, &ev.EventID, &ev.AggregateType, &ev.AggregateID,
			&ev.EventType, &ev.Payload, &ev.SimTimeAt, &ev.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// isTransientTxFailure reconoce la contención transitoria de PostgreSQL
// (fallo de serialización o deadlock): reintentable de inmediato.
func isTransientTxFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		(pgErr.Code == sqlstateSerializationFailure || pgErr.Code == sqlstateDeadlockDetected)
}

// sleep espera d y devuelve false si el contexto se cancela antes.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
