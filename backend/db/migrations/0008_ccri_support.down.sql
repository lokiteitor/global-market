-- =============================================================================
-- Imperio Industrial — 0008_ccri_support (down)
-- Revierte 0008 en orden inverso a su creación: elimina idempotency_keys
-- (sus GRANTs caen con la tabla) y restaura los CHECK originales de
-- ledger.accounts (0004_ledger).
--
-- LÍMITE DE POSTGRESQL: un valor de enum NO puede eliminarse (no existe
-- ALTER TYPE ... DROP VALUE). El valor 'world_source' queda en
-- ledger.account_kind tras esta reversión — inofensivo: los CHECK
-- restaurados vuelven a hacer imposible crear cuentas de ese kind.
--
-- Este down revierte lo reversible y FALLA EXPLÍCITAMENTE si existen filas
-- world_source: esas cuentas violarían los CHECK originales (y su saldo
-- negativo, la no-negatividad). Revertirlas exige primero deshacer sus
-- asientos (reconciliation) y borrar las cuentas — decisión del operador,
-- nunca de una migración.
-- =============================================================================

DO $$
DECLARE
    v_n bigint;
BEGIN
    SELECT count(*) INTO v_n FROM ledger.accounts WHERE kind = 'world_source';
    IF v_n > 0 THEN
        RAISE EXCEPTION USING
            MESSAGE = format('0008_ccri_support (down): existen %s cuenta(s) world_source en ledger.accounts', v_n),
            HINT = 'Deshaz sus asientos (reconciliation) y elimina esas cuentas antes de revertir; los CHECK originales de 0004_ledger no las admiten.';
    END IF;
END
$$;

-- ── 2-3. Idempotencia: la tabla arrastra índice, PK y GRANTs ─────────────────

DROP TABLE IF EXISTS public.idempotency_keys;

-- ── 1. CHECKs originales de 0004_ledger ──────────────────────────────────────
-- (validación inline: la guarda de arriba garantiza que no hay filas
-- world_source y las demás filas ya cumplían la condición original)

ALTER TABLE ledger.accounts DROP CONSTRAINT IF EXISTS ck_accounts_non_negative;
ALTER TABLE ledger.accounts ADD CONSTRAINT ck_accounts_non_negative
    CHECK (balance >= 0 OR kind = 'emission');

ALTER TABLE ledger.accounts DROP CONSTRAINT IF EXISTS ck_accounts_asset;
ALTER TABLE ledger.accounts ADD CONSTRAINT ck_accounts_asset
    CHECK (
        (kind IN ('cash','escrow','guarantee','sink','emission') AND product_id IS NULL AND warehouse_building_id IS NULL)
        OR
        (kind IN ('stock_free','stock_reserved','custody') AND product_id IS NOT NULL)
    );
