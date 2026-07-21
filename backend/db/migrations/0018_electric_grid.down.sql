-- =============================================================================
-- Imperio Industrial — 0018_electric_grid (down)
-- Revierte 0018 en orden inverso a su creación: columnas, tablas (sus índices
-- caen con ellas) y tipos.
--
-- GUARDAS EXPLÍCITAS ANTES DE DESTRUIR (las mismas condiciones que guarda el
-- down de 0017, comprobadas AQUÍ porque un `migrate down` ejecuta este fichero
-- primero: si fallaran después, el mundo quedaría a medio revertir con el
-- plano físico de la red ya destruido e irrecuperable). Este down FALLA si:
--   a) existen asientos power_spot en ledger.transactions — el ledger es
--      append-only: no pueden borrarse; revertir exige decisión del operador;
--   b) existen lotes en estado paused_no_power — el motor previo no sabe
--      reanudarlos (y sin la red ya no habría suministro que los despierte);
--   c) existen recetas con power_per_hour > 0 — al perder la columna quedarían
--      como recetas SIN coste energético alguno (dominarían estrictamente a
--      sus equivalentes de combustible); el operador debe eliminarlas antes.
-- =============================================================================

DO $$
DECLARE
    v_n bigint;
BEGIN
    SELECT count(*) INTO v_n FROM ledger.transactions WHERE kind = 'power_spot';
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0018_electric_grid (down): existen %s asiento(s) power_spot en ledger.transactions', v_n),
            HINT = 'El ledger es append-only: los asientos del spot no pueden borrarse; revertir exige una decisión del operador.';
    END IF;

    SELECT count(*) INTO v_n FROM world.production_batches WHERE status = 'paused_no_power';
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0018_electric_grid (down): existen %s lote(s) en paused_no_power', v_n),
            HINT = 'Reanuda el suministro o cancela esos lotes antes de revertir; el motor previo a 0017 no conoce ese estado.';
    END IF;

    SELECT count(*) INTO v_n FROM world.recipes WHERE power_per_hour > 0;
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0018_electric_grid (down): existen %s receta(s) eléctricas (power_per_hour > 0)', v_n),
            HINT = 'Elimina las recetas eléctricas (p. ej. smelt_steel_electric) antes de revertir: sin la columna quedarían sin coste energético alguno.';
    END IF;
END
$$;

-- ── 3. Columnas ──────────────────────────────────────────────────────────────

ALTER TABLE world.buildings DROP COLUMN powered_rate;
ALTER TABLE world.buildings DROP COLUMN last_curtailed_at_sim;
ALTER TABLE world.buildings DROP COLUMN powered_until_sim;
ALTER TABLE world.recipes DROP COLUMN power_per_hour;

-- ── 2. Tablas ────────────────────────────────────────────────────────────────

DROP TABLE world.power_dispatches;
DROP TABLE world.power_spot_ticks;
DROP TABLE world.power_bids;
DROP TABLE world.power_offers;
DROP TABLE world.power_plant_types;
DROP TABLE world.power_lines;

-- ── 1. Tipos ─────────────────────────────────────────────────────────────────

DROP TYPE world.power_role;
DROP TYPE world.power_line_status;
