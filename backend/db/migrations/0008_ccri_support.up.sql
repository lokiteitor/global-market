-- migrate:no-transaction
-- =============================================================================
-- Imperio Industrial — 0008_ccri_support (up)
-- Soporte del núcleo CCRI (Incremento 1):
--   1. ADR-022: nuevo ledger.account_kind 'world_source' — contrapartida
--      física del mundo para production_output/consumption. Cuenta de stock
--      (product_id NOT NULL, una por producto, titular: banco central) y la
--      ÚNICA cuenta de stock que puede ser negativa (simétrica a 'emission'
--      para el dinero: su saldo negativo = stock neto emitido al mundo).
--   2. public.idempotency_keys: almacén de respuestas para la cabecera
--      Idempotency-Key del contrato v1.2.0 (reintentos seguros de comandos
--      que mueven valor: misma clave ⇒ misma respuesta, nunca doble
--      ejecución).
--   3. GRANTs coherentes con 0007 (ii_gateway opera idempotency_keys).
--
-- Directiva no-transaction: ALTER TYPE ... ADD VALUE puede ejecutarse dentro
-- de una transacción en PG18, pero el valor nuevo NO puede usarse en la misma
-- transacción — y los CHECK recreados abajo lo referencian. Cada sentencia va
-- en autocommit; todas son re-ejecutables (IF EXISTS / IF NOT EXISTS o
-- drop+add emparejados) para que un fallo parcial se recupere relanzando.
-- =============================================================================

-- ── 1. ADR-022: kind 'world_source' ──────────────────────────────────────────

ALTER TYPE ledger.account_kind ADD VALUE IF NOT EXISTS 'world_source';

-- No-negatividad: emission (dinero) y world_source (stock) son las dos únicas
-- cuentas fiat del banco central que pueden quedar en negativo.
-- Recreación con NOT VALID + VALIDATE: el ADD no escanea la tabla bajo el
-- lock ACCESS EXCLUSIVE; el VALIDATE posterior solo toma SHARE UPDATE
-- EXCLUSIVE. La nueva condición es estrictamente más permisiva que la
-- anterior, así que VALIDATE no puede fallar sobre datos existentes.
ALTER TABLE ledger.accounts DROP CONSTRAINT IF EXISTS ck_accounts_non_negative;
ALTER TABLE ledger.accounts ADD CONSTRAINT ck_accounts_non_negative
    CHECK (balance >= 0 OR kind IN ('emission', 'world_source')) NOT VALID;
ALTER TABLE ledger.accounts VALIDATE CONSTRAINT ck_accounts_non_negative;

-- Clasificación del activo: world_source es cuenta de STOCK (product_id NOT
-- NULL); al ser la contrapartida global del mundo no está ligada a almacén.
ALTER TABLE ledger.accounts DROP CONSTRAINT IF EXISTS ck_accounts_asset;
ALTER TABLE ledger.accounts ADD CONSTRAINT ck_accounts_asset
    CHECK (
        (kind IN ('cash','escrow','guarantee','sink','emission') AND product_id IS NULL AND warehouse_building_id IS NULL)
        OR
        (kind IN ('stock_free','stock_reserved','custody','world_source') AND product_id IS NOT NULL)
    ) NOT VALID;
ALTER TABLE ledger.accounts VALIDATE CONSTRAINT ck_accounts_asset;

-- ── 2. Idempotencia de comandos (contrato v1.2.0, cabecera Idempotency-Key) ──

-- Respuesta almacenada del primer intento de un comando mutante. La clave la
-- aporta el cliente (uuid) y se acota por cuenta autenticada: dos cuentas no
-- pueden colisionar ni leerse las respuestas entre sí. Solo se persisten
-- respuestas con status < 500 (un error interno debe poder reintentarse de
-- verdad). Vive en public: es infraestructura transversal de la API, no
-- pertenece a ningún dominio (mismo criterio que schema_migrations).
CREATE TABLE IF NOT EXISTS public.idempotency_keys (
    key             uuid        NOT NULL,
    account_id      uuid        NOT NULL REFERENCES auth.accounts(id),
    method          text        NOT NULL,   -- método y ruta del primer intento
    path            text        NOT NULL,   --   (observabilidad y auditoría)
    response_status int         NOT NULL,
    content_type    text        NOT NULL,
    response_body   bytea       NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key, account_id)
);

-- Purga por antigüedad: las claves son útiles solo durante la ventana de
-- reintento del cliente; un job de limpieza en la ventana de mantenimiento
-- diaria (ADR-003) borrará por created_at (mismo criterio de retención que
-- outbox.events). El índice sirve ese DELETE por rango.
CREATE INDEX IF NOT EXISTS ix_idempotency_keys_created_at
    ON public.idempotency_keys (created_at);

-- ── 3. GRANTs (mínimo privilegio, coherentes con 0007) ───────────────────────

-- El gateway lee, inserta y purga claves de idempotencia. El schema public ya
-- concede USAGE a PUBLIC por defecto en PG18; no hace falta GRANT de schema.
GRANT ALL ON TABLE public.idempotency_keys TO ii_gateway;
