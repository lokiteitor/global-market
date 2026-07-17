-- =============================================================================
-- Imperio Industrial — 0010_delivery_idempotency (up)
-- Incremento 3 (LOGÍSTICA FÍSICA, Fase 1): integración de ENTREGA en el
-- Contract Service (internal/contracts). El consumidor "delivery_confirmer"
-- consume shipment.arrived (hito físico emitido por world) y asienta la entrega
-- del CCRI (ledger.contract_deliveries), acumula quantity_delivered a tiempo y
-- liquida al completarse (GDD 5.3 pasos 5-6).
--
-- El outbox ya garantiza entrega exactly-once por consumidor (cursor propio),
-- pero la entrega se refuerza con una idempotencia ESTRUCTURAL: cada cargamento
-- (world.shipments) llega FÍSICAMENTE a su destino una sola vez, de modo que su
-- entrega debe contarse una sola vez. El índice único por shipment_id habilita
-- el INSERT ... ON CONFLICT (shipment_id) DO NOTHING del delivery_confirmer:
-- reprocesar el mismo shipment.arrived (reintento del lote, redespliegue) no
-- duplica la partida ni la cantidad entregada.
--
-- shipment_id es único por cargamento (un cargamento pertenece a un único
-- contrato y se entrega una vez): la unicidad por shipment_id es la restricción
-- más ajustada y correcta (no hace falta (contract_id, shipment_id), pues
-- shipment_id ya es globalmente único). La retirada in situ (origin==destination)
-- crea un cargamento fresco por contrato y una única entrega: nunca colisiona.
-- =============================================================================

CREATE UNIQUE INDEX IF NOT EXISTS ux_contract_deliveries_shipment
    ON ledger.contract_deliveries (shipment_id);
