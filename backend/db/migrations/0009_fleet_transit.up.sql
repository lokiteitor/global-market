-- =============================================================================
-- Imperio Industrial — 0009_fleet_transit (up)
-- Incremento 3 (LOGÍSTICA FÍSICA, Fase 1: terrestre). Soporte de la ejecución
-- logística del CCRI en el contexto World Simulation (internal/world/fleet): el
-- motor de tránsito mueve físicamente los cargamentos por el grafo vial.
--
-- El esquema base del grafo, la flota y los cargamentos ya existe (0003_world:
-- network_nodes/links/link_segments, vehicle_types, routes/route_legs, vehicles,
-- shipments). Esta migración solo AÑADE a world.shipments los dos datos del
-- CONTRATO de origen que el motor de tránsito necesita para validar el despacho
-- y para confirmar la entrega SIN cruzar al bounded context contracts (la
-- frontera entre contextos es de código Go; world y contracts se integran solo
-- por el outbox de eventos, SAD §7 / ADR-006):
--
--   * destination_node_id — el nodo destino del contrato al que debe llegar el
--     cargamento (la ruta de despacho debe terminar en él; su llegada FÍSICA es
--     lo que confirma la entrega y emite shipment.arrived).
--   * deadline_sim — el vencimiento del contrato en sim-time (informativo para
--     el motor; la puntualidad la decide el consumidor contracts al recibir
--     shipment.arrived).
--
-- Ambas las puebla el consumidor world "shipment_creator" al crear el cargamento
-- desde contract.confirmed (las recibe en el payload del evento). Son NULLABLE:
-- los cargamentos que crea el propio contracts para la retirada in situ
-- (released_in_situ, origin==destination) no se despachan nunca y las dejan sin
-- poblar.
--
-- Índices de barrido del motor de tránsito (event-driven, GDD 1.1): los
-- vehículos in_transit vencidos por sim-time y los broken cuya reparación venció.
-- =============================================================================

ALTER TABLE world.shipments
    ADD COLUMN destination_node_id uuid REFERENCES world.network_nodes(id),
    ADD COLUMN deadline_sim        sim_time;

-- Barrido de segmentos vencidos: el motor lista los vehículos in_transit y
-- decide su vencimiento por (segment_entered_sim + tiempo_de_viaje) <= simNow.
CREATE INDEX ix_vehicles_in_transit ON world.vehicles (segment_entered_sim)
    WHERE status = 'in_transit';

-- Reanudación de averías: el barrido lista los broken cuya reparación venció
-- (repair_until_sim <= simNow) para re-entrar al mismo segmento (GDD 7.3).
CREATE INDEX ix_vehicles_broken ON world.vehicles (repair_until_sim)
    WHERE status = 'broken';

-- Confirmación de entrega: al llegar un vehículo a un nodo, el motor busca los
-- cargamentos a bordo con destino ese nodo.
CREATE INDEX ix_shipments_destination ON world.shipments (destination_node_id)
    WHERE destination_node_id IS NOT NULL;

-- -----------------------------------------------------------------------------
-- Tiempo de viaje de un segmento (fórmula VINCULANTE del motor de tránsito,
-- Fase 1 terrestre). Fuente ÚNICA en SQL de la fórmula, compartida por la
-- derivación analítica de la posición (GET vehicle) y por el barrido de
-- segmentos vencidos del motor. El código Go del motor replica esta MISMA
-- fórmula (documentado en internal/world/fleet).
--
--   factor         = 1 / congestion_ema   (>1 = más lento)
--   t_viaje (h)    = ceil( (length_m/1000) / (base_speed_kmh * factor) )
--                  = ceil( length_km * congestion_ema / base_speed_kmh )
--   t_viaje (seg)  = t_viaje(h) * 3600
--
-- advance_fn es el JSONB persistido al entrar al segmento
-- ({base_speed_kmh, congestion_ema, length_m, dir}); la congestión es la
-- SNAPSHOT del momento de entrada (la llegada no se recalcula al variar la
-- congestión: sólo los hitos escriben, GDD 1.1/7.3). IMMUTABLE: depende solo de
-- su argumento.
CREATE FUNCTION world.segment_travel_seconds(p_advance_fn jsonb) RETURNS bigint AS $$
    SELECT (CEIL(
        ((p_advance_fn->>'length_m')::float8 / 1000.0)
        * (p_advance_fn->>'congestion_ema')::float8
        / NULLIF((p_advance_fn->>'base_speed_kmh')::float8, 0)
    ) * 3600)::bigint;
$$ LANGUAGE sql IMMUTABLE;
