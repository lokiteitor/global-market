-- =============================================================================
-- Imperio Industrial — 0015_transship_queue (up)
-- Incremento 8 (LOGÍSTICA COMO SERVICIO): COLA DE TRANSBORDO con prioridad por
-- slots de terminal (GDD 7.3). El TransitWorker sirve la cola de una terminal en
-- orden de PRIORIDAD (dueños con slot vigente primero, por priority_tier
-- ascendente) y el resto FIFO por orden de llegada; el tiempo de servicio deriva
-- de world.terminals.transshipment_per_hour.
--
-- transship_ready_at_sim marca el instante en que la terminal TERMINA de
-- transbordar el cargamento (queda LISTO para el siguiente tramo). NULL = el
-- cargamento aún NO ha sido servido por la cola (recién llegado, encolado). Al
-- despachar el siguiente tramo la puerta de tiempo de transbordo usa este valor
-- (posición real en la cola) en vez de recomputarlo de forma aislada; si es NULL
-- (la cola aún no lo sirvió, o un fixture directo) se recae en el cálculo previo
-- (updated_at_sim + tiempo de transbordo) — retrocompatible.
--
-- El servicio de la cola reescribe SOLO esta columna; updated_at_sim conserva el
-- instante de llegada a la terminal (clave FIFO). Al re-despachar o re-transbordar
-- el cargamento, transship_ready_at_sim vuelve a NULL (cola fresca por terminal).
-- =============================================================================

ALTER TABLE world.shipments
    ADD COLUMN transship_ready_at_sim sim_time;

-- Índice parcial para el barrido de la cola: cargamentos encolados (at_terminal,
-- aún sin servir) por nodo de terminal.
CREATE INDEX ix_shipments_transship_pending
    ON world.shipments (at_node_id)
    WHERE status = 'at_terminal' AND transship_ready_at_sim IS NULL;
