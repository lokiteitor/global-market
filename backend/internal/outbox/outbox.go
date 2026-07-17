// Package outbox implementa el transactional outbox del backend (SAD/ADR-008,
// esquema outbox de la migración 0006): los módulos emiten eventos de dominio
// EN LA MISMA transacción que el cambio de estado que los causa (Emit) y los
// consumidores lógicos los procesan por polling con cursor propio
// (Consumer.Run), avanzando el cursor EN LA MISMA transacción en la que el
// handler aplica sus efectos. El resultado es entrega exactly-once por
// consumidor: o los efectos del handler y el avance del cursor se confirman
// juntos, o ninguno de los dos.
//
// Publicar nunca puede divergir del estado que lo causó: si la transacción
// del emisor se revierte, el evento desaparece con ella.
//
// Las métricas del módulo (ii_outbox_events_emitted_total,
// ii_outbox_events_processed_total, ii_outbox_consumer_lag) se registran en
// el registry de cada binario con RegisterMetrics.
package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Event es un evento de dominio leído del outbox, tal y como lo recibe el
// handler de un consumidor.
type Event struct {
	// Seq es el orden total de publicación (BIGINT IDENTITY de la tabla).
	Seq int64
	// EventID identifica el evento de forma única (UUIDv7, ADR-018).
	EventID uuid.UUID
	// AggregateType es el tipo de la entidad emisora ('contract',
	// 'publication', 'acceptance', ...).
	AggregateType string
	// AggregateID es la entidad de dominio que emitió el evento.
	AggregateID uuid.UUID
	// EventType es el tipo del evento ('contract.settled',
	// 'publication.created', ...).
	EventType string
	// Payload es el cuerpo JSON del evento tal cual se emitió.
	Payload json.RawMessage
	// SimTimeAt es el sim-time del mundo en el momento de la emisión
	// (dominio sim_time, segundos desde el génesis).
	SimTimeAt int64
	// CreatedAt es el wall-clock de inserción (auditoría).
	CreatedAt time.Time
}

// sqlInsertEvent inserta el evento en la transacción del emisor. seq lo
// asigna la IDENTITY de la tabla (orden total de polling, 0006_outbox).
const sqlInsertEvent = `
INSERT INTO outbox.events (event_id, aggregate_type, aggregate_id, event_type, payload, sim_time_at)
VALUES ($1, $2, $3, $4, $5, $6)`

// Emit inserta un evento en el outbox DENTRO de la transacción del llamante
// (patrón transactional outbox): el evento solo existe si esa transacción
// confirma; si se revierte, el evento desaparece con ella.
//
// El event_id (UUIDv7) se genera en la aplicación (ADR-018). El payload se
// serializa con json.Marshal (usa json.RawMessage para JSON ya serializado);
// por el contrato de la API, dinero y stock viajan SIEMPRE como string en el
// payload, jamás como float.
func Emit(ctx context.Context, tx pgx.Tx, simTime int64, aggregateType string, aggregateID uuid.UUID, eventType string, payload any) error {
	if strings.TrimSpace(aggregateType) == "" {
		return errors.New("outbox: aggregateType vacío")
	}
	if aggregateID == uuid.Nil {
		return errors.New("outbox: aggregateID nulo")
	}
	if strings.TrimSpace(eventType) == "" {
		return errors.New("outbox: eventType vacío")
	}
	if simTime < 0 {
		return fmt.Errorf("outbox: simTime negativo %d (dominio sim_time)", simTime)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: serializando el payload de %s: %w", eventType, err)
	}
	eventID, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("outbox: generando UUIDv7: %w", err)
	}
	if tx == nil {
		return errors.New("outbox: Emit requiere la transacción del llamante (tx nil)")
	}
	if _, err := tx.Exec(ctx, sqlInsertEvent, eventID, aggregateType, aggregateID, eventType, body, simTime); err != nil {
		return fmt.Errorf("outbox: insertando el evento %s: %w", eventType, err)
	}
	eventsEmitted.WithLabelValues(eventType).Inc()
	return nil
}
