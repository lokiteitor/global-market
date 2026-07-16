-- =============================================================================
-- Imperio Industrial — 03_ledger.sql
-- Esquema ledger: dinero, stock comprometible, tablón, contratos CCRI y
-- CCRI-Flete. Fuente de verdad del VALOR económico (Arquitectura §11.1).
--
-- REGLA DE ORO (GDD 18.3, ADR-005): toda invariante de dinero/stock vive en
-- la base de datos — transacciones SERIALIZABLE, constraints y funciones SQL
-- todo-o-nada. El código de aplicación (Contract Service, Go) orquesta; la
-- base garantiza. Un bug de aplicación no puede romper la contabilidad.
--
-- Modelo: ledger de DOBLE ENTRADA. El inventario comprometible se modela como
-- cuentas del mismo ledger que el dinero (partidas por producto + almacén,
-- cuentas espejo de reserva por contrato), de modo que el bloqueo triple del
-- CCRI (stock reservado + garantía monetaria + escrow) es UNA única
-- transacción ACID local — sin 2PC ni sagas (GDD 15.3, ADR-004).
--
-- Identificadores: uuid con DEFAULT uuidv7(), UUIDv7 nativo de PostgreSQL 18.
-- Fuente ejecutable: backend/migrations/0004_ledger.sql (aplicación manual vía make db-migrate).
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------

CREATE TYPE ledger.account_kind AS ENUM (
    'cash',            -- saldo líquido de una corporación (nunca negativo: sin deuda, GDD 5.9)
    'escrow',          -- pago del comprador retenido por el banco central
    'guarantee',       -- garantía monetaria del vendedor/transportista (10% fijo, decisión #27)
    'stock_free',      -- stock comprometible disponible (por producto + almacén)
    'stock_reserved',  -- stock reservado/congelado por publicación o contrato
    'custody',         -- mercancía en custodia de un CCRI-Flete (el transportista no puede venderla)
    'sink',            -- destrucción de valor: sanciones, impuestos, canon (GDD 5.5)
    'emission'         -- contrapartida de emisión del banco central (única cuenta con saldo negativo)
);

CREATE TYPE ledger.transaction_kind AS ENUM (
    'seed_capital',        -- capital semilla de jugador nuevo (emisión explícita)
    'bot_capitalization',  -- alta de bot = emisión asentada (ADR-010)
    'bot_retirement',      -- retiro de bot = absorción
    'publication_lock',    -- bloqueo de garantía propia al publicar
    'publication_release', -- cancelación/no servido en sorteo: garantía liberada
    'acceptance_lock',     -- bloqueo de garantía del aceptante durante la ventana
    'contract_confirmation', -- bloqueo triple atómico (GDD 5.3 paso 3)
    'delivery_settlement', -- liquidación pro-rata (GDD 5.3 paso 6)
    'custody_load',        -- carga en custodia de flete
    'custody_release',     -- entrega/liberación de custodia de flete
    'production_output',   -- alta de stock producido
    'consumption',         -- baja de stock consumido (insumos, combustible, ciudades)
    'wage',                -- salarios (sink parcial vía NPCs)
    'maintenance',         -- mantenimiento de edificios/vehículos (sink)
    'tax',                 -- impuestos y aduanas (sink)
    'canon',               -- canon de concesión (sink)
    'transfer',            -- transferencia genérica entre cuentas
    'auction',             -- subasta de embargo vía CCRI (GDD 11.2)
    'reconciliation'       -- ajuste auditado de reconciliación física↔contable
);

CREATE TYPE ledger.publication_kind AS ENUM (
    'sell',      -- oferta de venta: stock congelado + garantía monetaria del vendedor
    'buy',       -- solicitud de compra: 100% del pago en escrow
    'freight'    -- oferta/solicitud de flete (CCRI-Flete, Fase 2)
);

CREATE TYPE ledger.publication_status AS ENUM (
    'draw_window',   -- ventana de sorteo inicial abierta (30-60 s reales)
    'open',          -- madura: publicada, cantidad restante disponible
    'micro_window',  -- micro-ventana (15-30 s) abierta por una aceptación posterior
    'exhausted',     -- cantidad agotada
    'cancelled',     -- cancelada por el publicador (fuera del cooldown)
    'expired'        -- plazo vencido sin aceptación
);

CREATE TYPE ledger.acceptance_status AS ENUM (
    'pending_draw',  -- esperando el cierre de la ventana y el sorteo
    'served',        -- servida (total o parcialmente) en el orden sorteado
    'released'       -- no servida: su garantía se libera
);

CREATE TYPE ledger.contract_status AS ENUM (
    'active',    -- confirmado; en ejecución logística
    'settled',   -- liquidado (fill 100% o pro-rata al vencer el plazo)
    'failed'     -- fill 0% al vencer el plazo
);

CREATE TYPE ledger.contract_channel AS ENUM (
    'board',     -- tablón abierto global
    'private'    -- negociación directa 1:1 (mismas garantías y liquidación)
);

-- -----------------------------------------------------------------------------
-- Núcleo contable
-- -----------------------------------------------------------------------------

-- 1. accounts — cuentas del ledger. Una cuenta contiene UN activo:
--    dinero (product_id IS NULL) o stock de un producto (product_id NOT NULL).
CREATE TABLE ledger.accounts (
    id                     uuid PRIMARY KEY DEFAULT uuidv7(),
    kind                   ledger.account_kind NOT NULL,
    owner_account_id       uuid REFERENCES auth.accounts(id), -- NULL para cuentas puras de sistema
    product_id             uuid REFERENCES world.products(id),
    warehouse_building_id  uuid REFERENCES world.buildings(id), -- almacén, para cuentas de stock
    reference_id           uuid,   -- publicación/contrato/flete que motiva la cuenta espejo
    balance                BIGINT NOT NULL DEFAULT 0,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- No-negatividad: solo la cuenta de emisión del banco central puede ser negativa.
    CONSTRAINT ck_accounts_non_negative CHECK (balance >= 0 OR kind = 'emission'),
    -- Cuentas monetarias no llevan producto ni almacén; las de stock exigen producto.
    -- Las cuentas 'emission' pueden ser monetarias (product_id NULL: masa monetaria
    -- emitida) o de génesis de stock por producto (product_id NOT NULL: contrapartida
    -- de producción/consumo físico — sin ella la doble entrada por activo no cierra).
    CONSTRAINT ck_accounts_asset CHECK (
        (kind IN ('cash','escrow','guarantee','sink') AND product_id IS NULL AND warehouse_building_id IS NULL)
        OR
        (kind IN ('stock_free','stock_reserved','custody') AND product_id IS NOT NULL)
        OR
        (kind = 'emission' AND warehouse_building_id IS NULL)
    )
);

-- Una sola cuenta de caja por corporación
CREATE UNIQUE INDEX ux_accounts_cash ON ledger.accounts (owner_account_id)
    WHERE kind = 'cash';
-- Una sola cuenta de stock libre por (dueño, producto, almacén)
CREATE UNIQUE INDEX ux_accounts_stock_free
    ON ledger.accounts (owner_account_id, product_id, warehouse_building_id)
    WHERE kind = 'stock_free';
-- Una sola cuenta de emisión monetaria (banco central) y una de génesis por producto
CREATE UNIQUE INDEX ux_accounts_emission_money ON ledger.accounts ((true))
    WHERE kind = 'emission' AND product_id IS NULL;
CREATE UNIQUE INDEX ux_accounts_emission_stock ON ledger.accounts (product_id)
    WHERE kind = 'emission' AND product_id IS NOT NULL;
CREATE INDEX ix_accounts_owner ON ledger.accounts (owner_account_id);
CREATE INDEX ix_accounts_reference ON ledger.accounts (reference_id) WHERE reference_id IS NOT NULL;

-- 2. transactions — cabecera de asiento (agrupación atómica de partidas)
CREATE TABLE ledger.transactions (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    kind          ledger.transaction_kind NOT NULL,
    sim_time_at   sim_time NOT NULL,
    reference_id  uuid,          -- contrato, publicación, edificio... (auditoría cruzada)
    description   TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_transactions_reference ON ledger.transactions (reference_id)
    WHERE reference_id IS NOT NULL;
CREATE INDEX ix_transactions_sim_time ON ledger.transactions (sim_time_at);
CREATE INDEX ix_transactions_kind_time ON ledger.transactions (kind, created_at);

-- 3. entries — partidas de doble entrada. INMUTABLES una vez asentadas.
CREATE TABLE ledger.entries (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    transaction_id  uuid NOT NULL REFERENCES ledger.transactions(id),
    account_id      uuid NOT NULL REFERENCES ledger.accounts(id),
    amount          BIGINT NOT NULL CHECK (amount <> 0),  -- signo: cargo/abono
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_entries_transaction ON ledger.entries (transaction_id);
CREATE INDEX ix_entries_account ON ledger.entries (account_id, created_at);

-- -----------------------------------------------------------------------------
-- Invariantes del ledger (triggers) — la base garantiza, la aplicación orquesta
-- -----------------------------------------------------------------------------

-- (a) Cada partida actualiza el saldo de su cuenta; el CHECK de no-negatividad
--     de ledger.accounts aborta la transacción entera si un saldo quedara < 0.
CREATE OR REPLACE FUNCTION ledger.apply_entry_balance() RETURNS trigger AS $$
BEGIN
    UPDATE ledger.accounts
       SET balance = balance + NEW.amount,
           updated_at = now()
     WHERE id = NEW.account_id;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_entries_apply_balance
    AFTER INSERT ON ledger.entries
    FOR EACH ROW EXECUTE FUNCTION ledger.apply_entry_balance();

-- (b) Doble entrada balanceada: al confirmar la transacción SQL, la suma de
--     partidas de cada asiento debe ser cero POR ACTIVO (dinero, o cada
--     producto). Constraint trigger diferido: se evalúa en el COMMIT.
--     Nota: product_id es uuid; se castea a text para agrupar junto al
--     marcador 'MONEY' de las cuentas monetarias.
CREATE OR REPLACE FUNCTION ledger.assert_transaction_balanced() RETURNS trigger AS $$
DECLARE
    v_unbalanced BIGINT;
BEGIN
    SELECT count(*) INTO v_unbalanced
    FROM (
        SELECT COALESCE(a.product_id::text, 'MONEY') AS asset, SUM(e.amount) AS total
        FROM ledger.entries e
        JOIN ledger.accounts a ON a.id = e.account_id
        WHERE e.transaction_id = NEW.transaction_id
        GROUP BY COALESCE(a.product_id::text, 'MONEY')
    ) sums
    WHERE sums.total <> 0;

    IF v_unbalanced > 0 THEN
        RAISE EXCEPTION 'ledger: transaccion % no balanceada (doble entrada violada)',
            NEW.transaction_id;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_entries_balanced
    AFTER INSERT ON ledger.entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger.assert_transaction_balanced();

-- (c) Inmutabilidad: las partidas y cabeceras asentadas no se editan ni borran.
--     Toda corrección es un asiento nuevo de tipo 'reconciliation'.
CREATE OR REPLACE FUNCTION ledger.forbid_mutation() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'ledger: % es inmutable (append-only)', TG_TABLE_NAME;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_entries_immutable
    BEFORE UPDATE OR DELETE ON ledger.entries
    FOR EACH ROW EXECUTE FUNCTION ledger.forbid_mutation();

CREATE TRIGGER trg_transactions_immutable
    BEFORE UPDATE OR DELETE ON ledger.transactions
    FOR EACH ROW EXECUTE FUNCTION ledger.forbid_mutation();

-- -----------------------------------------------------------------------------
-- Tablón de contratos (global, único, interregional — GDD 5.3.1)
-- -----------------------------------------------------------------------------

-- 4. publications — publicaciones del tablón. Invariante por construcción:
--    toda publicación visible es ejecutable al 100% — su garantía íntegra
--    quedó bloqueada AL PUBLICAR (una garantía por publicación, ADR-014).
CREATE TABLE ledger.publications (
    id                        uuid PRIMARY KEY DEFAULT uuidv7(),
    kind                      ledger.publication_kind NOT NULL,
    publisher_account_id      uuid NOT NULL REFERENCES auth.accounts(id),
    channel                   ledger.contract_channel NOT NULL DEFAULT 'board',
    counterparty_account_id   uuid REFERENCES auth.accounts(id), -- solo canal 'private'
    product_id                uuid REFERENCES world.products(id), -- NULL en fletes
    quantity_total            stock_qty NOT NULL CHECK (quantity_total > 0),
    quantity_remaining        stock_qty NOT NULL CHECK (quantity_remaining >= 0),
    unit_price                money_amount NOT NULL CHECK (unit_price > 0),
    min_lot                   stock_qty NOT NULL DEFAULT 1 CHECK (min_lot > 0), -- lote mínimo de aceptación
    origin_node_id            uuid REFERENCES world.network_nodes(id),
    destination_node_id       uuid REFERENCES world.network_nodes(id),
    delivery_sim_seconds      sim_time NOT NULL,  -- plazo de entrega pactado (sim-time)
    status                    ledger.publication_status NOT NULL DEFAULT 'draw_window',
    -- La ventana de sorteo y el cooldown anti-parpadeo son de las pocas
    -- mecánicas definidas en TIEMPO REAL (30-60 s / 15-30 s; ADR-011)
    window_closes_at          TIMESTAMPTZ,
    cancel_cooldown_until     TIMESTAMPTZ,
    -- Cuentas espejo de la garantía propia, bloqueada desde la publicación:
    stock_reserve_account_id  uuid REFERENCES ledger.accounts(id), -- venta: stock congelado
    guarantee_account_id      uuid REFERENCES ledger.accounts(id), -- venta/flete: garantía monetaria
    escrow_account_id         uuid REFERENCES ledger.accounts(id), -- compra/flete: pago retenido
    published_at_sim          sim_time NOT NULL,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (quantity_remaining <= quantity_total),
    CHECK (channel <> 'private' OR counterparty_account_id IS NOT NULL),
    -- Garantías según el lado que publica (GDD 5.3):
    CHECK (kind <> 'sell' OR (product_id IS NOT NULL
           AND stock_reserve_account_id IS NOT NULL AND guarantee_account_id IS NOT NULL
           AND origin_node_id IS NOT NULL)),
    CHECK (kind <> 'buy'  OR (product_id IS NOT NULL
           AND escrow_account_id IS NOT NULL AND destination_node_id IS NOT NULL)),
    CHECK (kind <> 'freight' OR (origin_node_id IS NOT NULL AND destination_node_id IS NOT NULL))
);

-- Consultas del tablón (pull con filtros: producto, ubicación, precio, plazo)
CREATE INDEX ix_publications_board
    ON ledger.publications (product_id, unit_price)
    WHERE status IN ('draw_window','open','micro_window') AND channel = 'board';
CREATE INDEX ix_publications_publisher ON ledger.publications (publisher_account_id);
CREATE INDEX ix_publications_window ON ledger.publications (window_closes_at)
    WHERE status IN ('draw_window','micro_window');

-- 5. publication_acceptances — aceptaciones concurrentes de la ventana de
--    sorteo: al cierre se sortea el orden (draw_order) y se sirven hasta
--    agotar; la latencia no otorga ventaja (ADR-011).
CREATE TABLE ledger.publication_acceptances (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    publication_id        uuid NOT NULL REFERENCES ledger.publications(id),
    acceptor_account_id   uuid NOT NULL REFERENCES auth.accounts(id),
    quantity              stock_qty NOT NULL CHECK (quantity > 0),
    quantity_served       stock_qty NOT NULL DEFAULT 0 CHECK (quantity_served >= 0),
    status                ledger.acceptance_status NOT NULL DEFAULT 'pending_draw',
    draw_order            INT,     -- asignado por el sorteo al cerrar la ventana
    -- Garantía del aceptante, bloqueada al aceptar y liberada si no es servido:
    stock_reserve_account_id uuid REFERENCES ledger.accounts(id),
    guarantee_account_id     uuid REFERENCES ledger.accounts(id),
    escrow_account_id        uuid REFERENCES ledger.accounts(id),
    accepted_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at           TIMESTAMPTZ,
    CHECK (quantity_served <= quantity),
    CHECK (status = 'pending_draw' OR draw_order IS NOT NULL)
);

CREATE INDEX ix_acceptances_publication
    ON ledger.publication_acceptances (publication_id, status);
CREATE INDEX ix_acceptances_acceptor ON ledger.publication_acceptances (acceptor_account_id);

-- -----------------------------------------------------------------------------
-- Contratos
-- -----------------------------------------------------------------------------

-- 6. contracts — CCRI de bienes (GDD 5.3). Nace con el bloqueo triple ya
--    asentado (transacción 'contract_confirmation'); las cuentas espejo del
--    contrato son la prueba contable de sus garantías.
CREATE TABLE ledger.contracts (
    id                        uuid PRIMARY KEY DEFAULT uuidv7(),
    publication_id            uuid REFERENCES ledger.publications(id), -- NULL: negociación directa
    channel                   ledger.contract_channel NOT NULL,
    buyer_account_id          uuid NOT NULL REFERENCES auth.accounts(id),
    seller_account_id         uuid NOT NULL REFERENCES auth.accounts(id),
    product_id                uuid NOT NULL REFERENCES world.products(id),
    quantity_agreed           stock_qty NOT NULL CHECK (quantity_agreed > 0),
    quantity_delivered        stock_qty NOT NULL DEFAULT 0 CHECK (quantity_delivered >= 0),
    unit_price                money_amount NOT NULL CHECK (unit_price > 0),
    origin_node_id            uuid NOT NULL REFERENCES world.network_nodes(id),
    destination_node_id       uuid NOT NULL REFERENCES world.network_nodes(id),
    deadline_sim              sim_time NOT NULL,   -- vencimiento en sim-time
    status                    ledger.contract_status NOT NULL DEFAULT 'active',
    fill_bp                   INT CHECK (fill_bp BETWEEN 0 AND 10000), -- % entregado a tiempo, en puntos básicos
    -- Bloqueo triple (cuentas espejo del contrato):
    stock_reserve_account_id  uuid NOT NULL REFERENCES ledger.accounts(id),
    seller_guarantee_account_id uuid NOT NULL REFERENCES ledger.accounts(id),
    escrow_account_id         uuid NOT NULL REFERENCES ledger.accounts(id),
    confirmed_at_sim          sim_time NOT NULL,
    settled_at_sim            sim_time,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (quantity_delivered <= quantity_agreed),
    CHECK (buyer_account_id <> seller_account_id),
    CHECK (status = 'active' OR (fill_bp IS NOT NULL AND settled_at_sim IS NOT NULL))
);

CREATE INDEX ix_contracts_buyer ON ledger.contracts (buyer_account_id, status);
CREATE INDEX ix_contracts_seller ON ledger.contracts (seller_account_id, status);
CREATE INDEX ix_contracts_deadline ON ledger.contracts (deadline_sim) WHERE status = 'active';
CREATE INDEX ix_contracts_settled ON ledger.contracts (product_id, settled_at_sim)
    WHERE status <> 'active';  -- fuente de las velas OHLC (analytics)

-- 7. contract_deliveries — verificación de entrega ACUMULATIVA: el shard
--    confirma cada llegada física parcial (GDD 5.3 paso 5)
CREATE TABLE ledger.contract_deliveries (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    contract_id       uuid NOT NULL REFERENCES ledger.contracts(id),
    shipment_id       uuid NOT NULL,   -- world.shipments (FK cross-schema, abajo)
    quantity          stock_qty NOT NULL CHECK (quantity > 0),
    delivered_at_sim  sim_time NOT NULL,
    on_time           BOOLEAN NOT NULL,   -- dentro del plazo pactado
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_deliveries_contract ON ledger.contract_deliveries (contract_id);

-- 8. freight_contracts — CCRI-Flete (GDD 5.3.2, Fase 2): custodia asentada
--    en el ledger; el transportista lleva la carga físicamente pero no puede
--    venderla (la cuenta 'custody' lo impide contablemente).
CREATE TABLE ledger.freight_contracts (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    publication_id              uuid REFERENCES ledger.publications(id),
    channel                     ledger.contract_channel NOT NULL,
    shipper_account_id          uuid NOT NULL REFERENCES auth.accounts(id), -- cargador (dueño de la mercancía)
    carrier_account_id          uuid NOT NULL REFERENCES auth.accounts(id), -- transportista
    origin_node_id              uuid NOT NULL REFERENCES world.network_nodes(id),
    destination_node_id         uuid NOT NULL REFERENCES world.network_nodes(id),
    freight_price               money_amount NOT NULL CHECK (freight_price > 0),
    declared_value              money_amount NOT NULL CHECK (declared_value > 0), -- base de la garantía del transportista
    deadline_sim                sim_time NOT NULL,
    status                      ledger.contract_status NOT NULL DEFAULT 'active',
    fill_bp                     INT CHECK (fill_bp BETWEEN 0 AND 10000),
    escrow_account_id           uuid NOT NULL REFERENCES ledger.accounts(id), -- precio del flete (cargador)
    carrier_guarantee_account_id uuid NOT NULL REFERENCES ledger.accounts(id),
    custody_account_id          uuid NOT NULL REFERENCES ledger.accounts(id), -- mercancía en custodia
    confirmed_at_sim            sim_time NOT NULL,
    settled_at_sim              sim_time,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (shipper_account_id <> carrier_account_id)
);

CREATE INDEX ix_freight_shipper ON ledger.freight_contracts (shipper_account_id, status);
CREATE INDEX ix_freight_carrier ON ledger.freight_contracts (carrier_account_id, status);

-- -----------------------------------------------------------------------------
-- FKs cross-schema diferidas (world → ledger), ahora que ambos existen
-- -----------------------------------------------------------------------------

ALTER TABLE world.shipments
    ADD CONSTRAINT fk_shipments_contract
        FOREIGN KEY (contract_id) REFERENCES ledger.contracts(id),
    ADD CONSTRAINT fk_shipments_freight
        FOREIGN KEY (freight_contract_id) REFERENCES ledger.freight_contracts(id);

ALTER TABLE ledger.contract_deliveries
    ADD CONSTRAINT fk_deliveries_shipment
        FOREIGN KEY (shipment_id) REFERENCES world.shipments(id);

-- -----------------------------------------------------------------------------
-- Funciones todo-o-nada (extracto). El Contract Service las invoca dentro de
-- transacciones SERIALIZABLE; si cualquier paso falla, no queda NINGUNA
-- garantía a medio bloquear (GDD §10 de Arquitectura: 500 nunca deja estado parcial).
-- -----------------------------------------------------------------------------

-- Bloqueo triple atómico del CCRI (GDD 5.3 paso 3): mueve las garantías ya
-- bloqueadas de la publicación/aceptación a las cuentas espejo del contrato.
-- Los IDs de transacción y contrato (UUIDv7) los genera y pasa la capa de
-- aplicación; los IDs de partida se generan en la base con uuidv7()
-- (DEFAULT de ledger.entries.id).
CREATE OR REPLACE FUNCTION ledger.confirm_contract(
    p_tx_id        uuid,
    p_contract_id  uuid,
    p_sim_time     sim_time,
    p_quantity     stock_qty,
    p_unit_price   money_amount,
    p_from_stock_account     uuid,  -- stock reservado de la publicación de venta
    p_from_guarantee_account uuid,  -- garantía monetaria del vendedor (en publicación/aceptación)
    p_from_escrow_account    uuid,  -- escrow del comprador (en publicación/aceptación)
    p_to_stock_account       uuid,  -- cuenta espejo de stock del contrato
    p_to_guarantee_account   uuid,  -- cuenta espejo de garantía del contrato
    p_to_escrow_account      uuid   -- cuenta espejo de escrow del contrato
) RETURNS void AS $$
DECLARE
    v_value     BIGINT := p_quantity * p_unit_price;
    v_guarantee BIGINT := (v_value * 10) / 100;   -- garantía fija del 10% (decisión #27)
BEGIN
    INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
    VALUES (p_tx_id, 'contract_confirmation', p_sim_time, p_contract_id,
            'Bloqueo triple CCRI: stock + garantía + escrow');

    -- Stock reservado: publicación → contrato
    INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
        (p_tx_id, p_from_stock_account,     -p_quantity),
        (p_tx_id, p_to_stock_account,        p_quantity),
    -- Escrow del comprador: 100% del pago
        (p_tx_id, p_from_escrow_account,    -v_value),
        (p_tx_id, p_to_escrow_account,       v_value);
    -- Garantía monetaria del vendedor: proporcional a la cantidad aceptada.
    -- Puede ser 0 en contratos pequeños (valor < 10): el CHECK amount <> 0
    -- prohíbe partidas nulas, así que solo se asienta si hay importe.
    IF v_guarantee <> 0 THEN
        INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
            (p_tx_id, p_from_guarantee_account, -v_guarantee),
            (p_tx_id, p_to_guarantee_account,    v_guarantee);
    END IF;
    -- Los triggers (balance por cuenta, no-negatividad, doble entrada diferida)
    -- garantizan que o se asientan las tres partidas o ninguna.
END;
$$ LANGUAGE plpgsql;

-- Liquidación pro-rata al completarse la cantidad o vencer el plazo
-- (GDD 5.3 paso 6). La cantidad NO entregada se penaliza: el escrow
-- proporcional vuelve al comprador; la garantía proporcional del vendedor se
-- reparte entre compensación al comprador y sink del banco central. La
-- liberación FÍSICA del stock no entregado (en su ubicación actual) la asienta
-- el shard con una transacción separada de tipo 'publication_release'.
-- Los IDs de partida se generan en la base con uuidv7() (DEFAULT de
-- ledger.entries.id).
CREATE OR REPLACE FUNCTION ledger.settle_contract_prorata(
    p_tx_id            uuid,
    p_contract_id      uuid,
    p_sim_time         sim_time,
    p_seller_cash      uuid,   -- caja del vendedor
    p_buyer_cash       uuid,   -- caja del comprador
    p_buyer_stock      uuid,   -- stock libre del comprador en destino
    p_sink_account     uuid,   -- cuenta sink del banco central
    p_seller_stock_release uuid, -- stock libre del vendedor en la ubicación física actual
    p_compensation_bp  INT     -- parte de la garantía que compensa al comprador (resto: sink)
) RETURNS void AS $$
DECLARE
    c               ledger.contracts%ROWTYPE;
    v_value_total   BIGINT;
    v_value_filled  BIGINT;
    v_value_missing BIGINT;
    v_guar_total    BIGINT;
    v_guar_filled   BIGINT;
    v_guar_missing  BIGINT;
    v_comp          BIGINT;
    v_qty_missing   BIGINT;
BEGIN
    SELECT * INTO c FROM ledger.contracts WHERE id = p_contract_id FOR UPDATE;
    IF c.status <> 'active' THEN
        RAISE EXCEPTION 'ledger: contrato % no está activo', p_contract_id;
    END IF;

    v_value_total   := c.quantity_agreed * c.unit_price;
    v_value_filled  := c.quantity_delivered * c.unit_price;
    v_value_missing := v_value_total - v_value_filled;
    v_guar_total    := (v_value_total * 10) / 100;
    v_guar_filled   := (v_guar_total * c.quantity_delivered) / c.quantity_agreed;
    v_guar_missing  := v_guar_total - v_guar_filled;
    v_comp          := (v_guar_missing * p_compensation_bp) / 10000;
    v_qty_missing   := c.quantity_agreed - c.quantity_delivered;

    INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
    VALUES (p_tx_id, 'delivery_settlement', p_sim_time, p_contract_id,
            format('Liquidación pro-rata: fill %s/%s', c.quantity_delivered, c.quantity_agreed));

    -- Lo entregado a tiempo: stock al comprador, pago y garantía proporcional al vendedor
    IF c.quantity_delivered > 0 THEN
        INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
            (p_tx_id, c.stock_reserve_account_id,   -c.quantity_delivered),
            (p_tx_id, p_buyer_stock,                 c.quantity_delivered),
            (p_tx_id, c.escrow_account_id,          -v_value_filled),
            (p_tx_id, p_seller_cash,                 v_value_filled);
        -- La garantía proporcional puede redondear a 0 (contratos pequeños o
        -- fills bajos): el CHECK amount <> 0 prohíbe partidas nulas.
        IF v_guar_filled <> 0 THEN
            INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
                (p_tx_id, c.seller_guarantee_account_id, -v_guar_filled),
                (p_tx_id, p_seller_cash,                 v_guar_filled);
        END IF;
    END IF;

    -- Lo faltante: escrow al comprador; garantía repartida compensación/sink;
    -- stock no entregado liberado como stock libre EN SU UBICACIÓN FÍSICA ACTUAL
    IF v_qty_missing > 0 THEN
        INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
            (p_tx_id, c.escrow_account_id,           -v_value_missing),
            (p_tx_id, p_buyer_cash,                   v_value_missing),
            (p_tx_id, c.stock_reserve_account_id,    -v_qty_missing),
            (p_tx_id, p_seller_stock_release,         v_qty_missing);
        -- Reparto de la garantía incumplida. v_comp o la parte del sink pueden
        -- ser 0 por división entera (el residuo va SIEMPRE al sink); el CHECK
        -- amount <> 0 obliga a asentar solo las partidas con importe.
        IF v_guar_missing <> 0 THEN
            INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
                (p_tx_id, c.seller_guarantee_account_id, -v_guar_missing);
            IF v_comp <> 0 THEN
                INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
                    (p_tx_id, p_buyer_cash, v_comp);
            END IF;
            IF v_guar_missing - v_comp <> 0 THEN
                INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES
                    (p_tx_id, p_sink_account, v_guar_missing - v_comp);
            END IF;
        END IF;
    END IF;

    UPDATE ledger.contracts
       SET status = CASE WHEN c.quantity_delivered = 0 THEN 'failed'::ledger.contract_status
                         ELSE 'settled'::ledger.contract_status END,
           fill_bp = (c.quantity_delivered * 10000) / c.quantity_agreed,
           settled_at_sim = p_sim_time,
           updated_at = now()
     WHERE id = p_contract_id;
END;
$$ LANGUAGE plpgsql;
