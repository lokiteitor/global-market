-- =============================================================================
-- Imperio Industrial — 05_outbox.sql
-- Esquema outbox: mensajería asíncrona entre módulos en Fases 0-1
-- (outbox table + polling sobre PostgreSQL; ADR-008). Kafka con schema
-- registry solo en Fase 2+ y solo si el volumen medido lo exige.
--
-- Patrón transactional outbox: el módulo emisor inserta el evento EN LA MISMA
-- transacción que su cambio de estado; los consumidores (Notification Gateway,
-- módulos del motor) hacen polling por cursor. Publicar nunca puede divergir
-- del estado que lo causó.
-- =============================================================================

CREATE TABLE outbox.events (
    seq             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, -- orden total de polling
    event_id        ulid_id NOT NULL UNIQUE CHECK (event_id LIKE 'evt_%'),
    aggregate_type  TEXT NOT NULL,       -- 'contract', 'vehicle', 'building', 'city', ...
    aggregate_id    ulid_id NOT NULL,    -- entidad de dominio que emite el evento
    event_type      TEXT NOT NULL,       -- 'contract.settled', 'vehicle.arrived', 'batch.completed', ...
    payload         JSONB NOT NULL,
    sim_time_at     sim_time NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Polling incremental por consumidor (cursor > seq); el filtro por tipo
-- soporta el interest management del Notification Gateway.
CREATE INDEX ix_outbox_type_seq ON outbox.events (event_type, seq);
CREATE INDEX ix_outbox_aggregate ON outbox.events (aggregate_type, aggregate_id);

-- Cursores de consumo: cada consumidor lógico avanza su propio offset
-- (equivalente barato a los consumer groups de Kafka).
CREATE TABLE outbox.consumer_cursors (
    consumer_name   TEXT PRIMARY KEY,    -- 'notification_gateway', 'sim_engine', 'balancer', ...
    last_seq        BIGINT NOT NULL DEFAULT 0,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Retención: los eventos consumidos por todos los cursores se purgan
-- periódicamente (job de limpieza en la ventana de mantenimiento diaria).
