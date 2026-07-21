-- =============================================================================
-- Imperio Industrial — 0012_system_liquidation (up)
-- Incremento 6a: SUBASTA PÚBLICA del stock embargado (GDD 11.2, cierre de la
-- cascada de insolvencia). El motor de world/enforcement emite building.seized;
-- el consumidor contracts "system_liquidator" mueve el stock libre embargado a
-- la cuenta stock_free del banco central (transacción 'auction', doble entrada
-- por producto) y lo publica en el tablón como oferta sell del sistema (mismo
-- camino que cualquier venta del CCRI). Cuando se vende, los proceeds los cobra
-- el banco central (efecto sink/absorción: el moroso no tiene deuda residual —
-- su caja se agotó en la cascada).
--
-- Esta migración solo AÑADE la tabla de idempotencia del liquidador: un embargo
-- (building.seized) se subasta UNA sola vez. El outbox ya garantiza exactly-once
-- por cursor; esta tabla es la defensa en profundidad por building_id (un mismo
-- embargo re-emitido o un redespliegue no re-subastan). No lleva FK a
-- world.buildings: es un registro de auditoría/idempotencia del contexto de
-- contratos, no una proyección del mundo (la frontera es de código Go).
--
-- GRANTs: coherentes con 0007 (ii_engine escribe el ledger, ii_gateway/
-- ii_analytics leen). El ALTER DEFAULT PRIVILEGES de 0007 ya cubriría la tabla
-- nueva; se conceden además de forma explícita para no depender del rol que
-- ejecute la migración.
-- =============================================================================

CREATE TABLE ledger.system_liquidations (
    building_id       uuid PRIMARY KEY,          -- edificio embargado (clave de idempotencia)
    seized_at_sim     sim_time NOT NULL,         -- sim-time del embargo (del payload building.seized)
    liquidated_at_sim sim_time NOT NULL,         -- sim-time en que el liquidador procesó la subasta
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE ledger.system_liquidations IS
    'Idempotencia de la subasta pública del stock embargado (Incremento 6a, GDD 11.2): un building.seized se liquida una sola vez.';

GRANT ALL    ON TABLE ledger.system_liquidations TO ii_engine;
GRANT SELECT ON TABLE ledger.system_liquidations TO ii_gateway;
GRANT SELECT ON TABLE ledger.system_liquidations TO ii_analytics;
