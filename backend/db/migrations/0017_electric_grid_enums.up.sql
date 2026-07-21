-- migrate:no-transaction
-- =============================================================================
-- Imperio Industrial — 0017_electric_grid_enums (up)
-- Red eléctrica regional (GDD 5.8, Fase 3; ADR-025), parte 1 de 2: extensión
-- de enums existentes + índice parcial que referencia el valor nuevo.
--
--   1. world.batch_status + 'paused_no_power': pausa por falta de suministro
--      eléctrico (recorte del spot, insolvencia o desconexión — 2º escalón de
--      GDD 5.9). Estado NUEVO y no reutilización de paused_no_fuel porque el
--      remedio del jugador es distinto (asegurar suministro regional: subir
--      puja, construir generación o líneas vs. traer combustible por
--      logística) y el recorte rotatorio debe ser observable como tal.
--   2. ledger.transaction_kind + 'power_spot': asiento multi-parte del mercado
--      spot (cajas de consumidores → cajas de generadores al precio de cierre
--      uniforme). Kind propio y no 'transfer' para que el flujo eléctrico sea
--      auditable/monitorizable por el Balancer como primer orden.
--   3. Recrea el índice parcial ix_batches_building incluyendo el estado nuevo
--      (el barrido de producción también visita paused_no_power).
--
-- Directiva no-transaction (patrón 0008): ALTER TYPE ... ADD VALUE no permite
-- usar el valor en la misma transacción (PG18) y el índice de (3) lo
-- referencia. Cada sentencia va en autocommit y es re-ejecutable
-- (IF NOT EXISTS / DROP+CREATE emparejados) para recuperarse de un fallo
-- parcial relanzando. Las tablas y tipos nuevos van en 0018_electric_grid
-- (transaccional y atómica; además sqlc no cataloga DDL dentro de DO-blocks).
-- =============================================================================

-- ── 1-2. Valores nuevos de enum ──────────────────────────────────────────────

ALTER TYPE world.batch_status ADD VALUE IF NOT EXISTS 'paused_no_power';
ALTER TYPE ledger.transaction_kind ADD VALUE IF NOT EXISTS 'power_spot';

-- ── 3. Índice parcial del barrido de producción, con el estado nuevo ─────────

DROP INDEX IF EXISTS world.ix_batches_building;
CREATE INDEX IF NOT EXISTS ix_batches_building
    ON world.production_batches (building_id, queue_position)
    WHERE status IN ('queued', 'running', 'paused_no_fuel', 'paused_no_workers', 'paused_no_power');
