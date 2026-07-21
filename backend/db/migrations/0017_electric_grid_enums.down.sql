-- =============================================================================
-- Imperio Industrial — 0017_electric_grid_enums (down)
-- Restaura el índice parcial original de production_batches (0003).
--
-- LÍMITE DE POSTGRESQL: un valor de enum NO puede eliminarse (no existe
-- ALTER TYPE ... DROP VALUE). 'paused_no_power' (world.batch_status) y
-- 'power_spot' (ledger.transaction_kind) quedan en sus enums tras esta
-- reversión — inofensivos una vez que ninguna fila los usa.
--
-- Este down revierte lo reversible y FALLA EXPLÍCITAMENTE si:
--   a) existen asientos power_spot en ledger.transactions — el ledger es
--      append-only (trg_transactions_immutable): no pueden borrarse; revertir
--      exige decisión del operador, nunca de una migración;
--   b) existen lotes en estado paused_no_power — el índice parcial original
--      no los cubre y el motor previo a 0017 no sabe reanudarlos; el operador
--      debe resolverlos antes (reanudar el suministro o cancelar los lotes).
-- =============================================================================

DO $$
DECLARE
    v_n bigint;
BEGIN
    SELECT count(*) INTO v_n FROM ledger.transactions WHERE kind = 'power_spot';
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0017_electric_grid_enums (down): existen %s asiento(s) power_spot en ledger.transactions', v_n),
            HINT = 'El ledger es append-only: los asientos del spot no pueden borrarse; revertir exige una decisión del operador.';
    END IF;

    SELECT count(*) INTO v_n FROM world.production_batches WHERE status = 'paused_no_power';
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0017_electric_grid_enums (down): existen %s lote(s) en paused_no_power', v_n),
            HINT = 'Reanuda el suministro o cancela esos lotes antes de revertir; el motor previo a 0017 no conoce ese estado.';
    END IF;
END
$$;

-- Índice parcial original de 0003 (la guarda de arriba garantiza que no queda
-- ninguna fila fuera del predicado restaurado).

DROP INDEX IF EXISTS world.ix_batches_building;
CREATE INDEX IF NOT EXISTS ix_batches_building
    ON world.production_batches (building_id, queue_position)
    WHERE status IN ('queued', 'running', 'paused_no_fuel', 'paused_no_workers');
