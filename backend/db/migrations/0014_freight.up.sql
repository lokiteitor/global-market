-- =============================================================================
-- 0014_freight — CCRI-Flete (GDD 5.3.2, Incremento 8, Fase 2): activa el
-- SEGUNDO tipo de contrato. La maquinaria del tablón (ventana de sorteo,
-- aceptación parcial, liquidación pro-rata) se REUTILIZA de las publicaciones
-- de bienes; esta migración añade solo lo que el flete necesita sobre el
-- esquema ya definido en 0004_ledger:
--
--   1. ledger.publications.declared_value — el valor declarado de la carga de
--      una solicitud de flete (base de la garantía del transportista). La
--      publicación de flete la crea el CARGADOR con su escrow (precio del flete)
--      bloqueado como una compra; declared_value viaja hasta el contrato.
--   2. ledger.freight_deliveries — idempotencia de la liquidación por
--      (freight_contract_id, shipment_id): un cargamento de flete llega una vez.
--   3. ledger.confirm_freight — al servirse una aceptación, mueve el escrow del
--      cargador y la garantía del transportista a las cuentas espejo del
--      freight_contract y ASIENTA LA CUSTODIA (stock_free del cargador →
--      custody del contrato), todo atómico. El transportista lleva la carga
--      físicamente pero el ledger le impide venderla: está en 'custody', no en
--      su 'stock_free'.
--   4. ledger.settle_freight_prorata — liquidación pro-rata contra la entrega:
--      la custodia va al cargador donde la mercancía está físicamente; el
--      transportista cobra el flete y recupera su garantía por lo entregado a
--      tiempo; lo no entregado reembolsa el flete al cargador y reparte la
--      garantía entre compensación al cargador y sink.
--   5. índices de barrido del flete.
--
-- Sin cambio de diseño (GDD 5.3.2 ya lo especifica). Dinero/stock int64.
-- =============================================================================

-- 1. declared_value de la publicación de flete ------------------------------
ALTER TABLE ledger.publications
    ADD COLUMN declared_value money_amount
        CHECK (declared_value IS NULL OR declared_value > 0);

COMMENT ON COLUMN ledger.publications.declared_value IS
    'CCRI-Flete: valor declarado de la carga, base de la garantía del transportista (NULL salvo kind=freight).';

-- Una publicación de flete la crea el CARGADOR: bloquea su escrow (precio del
-- flete, como una compra) y declara el valor de la carga; product_id y
-- origin/destination identifican qué carga y de dónde a dónde.
ALTER TABLE ledger.publications
    ADD CONSTRAINT ck_publications_freight CHECK (
        kind <> 'freight' OR (
            product_id IS NOT NULL
            AND escrow_account_id IS NOT NULL
            AND declared_value IS NOT NULL
            AND origin_node_id IS NOT NULL
            AND destination_node_id IS NOT NULL
        )
    );

-- 2. Idempotencia de la entrega de flete ------------------------------------
CREATE TABLE ledger.freight_deliveries (
    freight_contract_id  uuid NOT NULL REFERENCES ledger.freight_contracts(id),
    shipment_id          uuid NOT NULL,   -- world.shipments (FK cross-schema, ya existente)
    quantity             stock_qty NOT NULL CHECK (quantity > 0),
    delivered_at_sim     sim_time NOT NULL,
    on_time              BOOLEAN NOT NULL,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (freight_contract_id, shipment_id)
);

-- 3. Barrido de fletes vencidos (deadline pasado, aún activos) ---------------
CREATE INDEX ix_freight_due ON ledger.freight_contracts (deadline_sim)
    WHERE status = 'active';

-- -----------------------------------------------------------------------------
-- 4. ledger.confirm_freight — confirmación atómica del CCRI-Flete.
--    Al servirse la aceptación (cierre del sorteo), mueve:
--      - escrow del cargador (precio del flete servido): publicación → contrato;
--      - garantía del transportista (proporcional al valor declarado servido):
--        aceptación → contrato;
--      - CUSTODIA: stock_free del cargador en el almacén de origen → custody del
--        contrato (la carga deja de ser vendible por nadie salvo la liquidación).
--    Todo-o-nada: los triggers del ledger garantizan que o se asientan las tres
--    parejas o ninguna. Los IDs (UUIDv7) los genera la aplicación (ADR-018).
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.confirm_freight(
    p_tx_id             uuid,
    p_freight_id        uuid,
    p_sim_time          sim_time,
    p_quantity          stock_qty,      -- unidades de carga que entran en custodia
    p_freight_price     money_amount,   -- precio del flete servido (a mover del escrow)
    p_guarantee         money_amount,   -- garantía del transportista servida (a mover)
    p_from_escrow       uuid,           -- escrow de la publicación (cargador)
    p_from_guarantee    uuid,           -- garantía de la aceptación (transportista)
    p_from_stock_free   uuid,           -- stock_free del cargador en el origen
    p_to_escrow         uuid,           -- escrow espejo del freight_contract
    p_to_guarantee      uuid,           -- garantía espejo del freight_contract
    p_to_custody        uuid,           -- custodia espejo del freight_contract
    p_entry_ids         uuid[]          -- 6 IDs de partida pre-generados
) RETURNS void AS $$
BEGIN
    INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
    VALUES (p_tx_id, 'custody_load', p_sim_time, p_freight_id,
            'Confirmación CCRI-Flete: escrow + garantía + carga en custodia');

    INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES
    -- Escrow del cargador (precio del flete): publicación → contrato
        (p_entry_ids[1], p_tx_id, p_from_escrow,     -p_freight_price),
        (p_entry_ids[2], p_tx_id, p_to_escrow,        p_freight_price),
    -- Garantía del transportista: aceptación → contrato
        (p_entry_ids[3], p_tx_id, p_from_guarantee,  -p_guarantee),
        (p_entry_ids[4], p_tx_id, p_to_guarantee,     p_guarantee),
    -- Custodia: stock libre del cargador → custody del contrato (no vendible)
        (p_entry_ids[5], p_tx_id, p_from_stock_free, -p_quantity),
        (p_entry_ids[6], p_tx_id, p_to_custody,       p_quantity);
END;
$$ LANGUAGE plpgsql;

-- -----------------------------------------------------------------------------
-- 5. ledger.settle_freight_prorata — liquidación pro-rata del flete.
--    La mercancía en custodia (v_total) va ÍNTEGRA al stock libre del cargador
--    en su ubicación FÍSICA ACTUAL (p_shipper_goods): el destino si llegó, o el
--    origen si se libera in situ por vencimiento (nada se teletransporta). El
--    pago se reparte por lo entregado A TIEMPO (p_delivered):
--      - flete ganado = freight_price * delivered/total → transportista;
--        el resto (no entregado a tiempo) reembolsa al cargador;
--      - garantía cumplida = garantía * delivered/total → transportista;
--        la faltante se reparte compensación al cargador (p_compensation_bp) y sink.
--    p_delivered = total en la entrega a tiempo; 0 en el fallo. Todo-o-nada.
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION ledger.settle_freight_prorata(
    p_tx_id            uuid,
    p_freight_id       uuid,
    p_sim_time         sim_time,
    p_delivered        stock_qty,   -- unidades entregadas A TIEMPO (base del pago)
    p_shipper_cash     uuid,        -- caja del cargador
    p_carrier_cash     uuid,        -- caja del transportista
    p_sink_account     uuid,        -- sink del banco central
    p_shipper_goods    uuid,        -- stock_free del cargador donde la carga está físicamente
    p_compensation_bp  INT,         -- parte de la garantía faltante que compensa al cargador
    p_entry_ids        uuid[]       -- hasta 9 IDs de partida pre-generados
) RETURNS void AS $$
DECLARE
    fc              ledger.freight_contracts%ROWTYPE;
    v_total         BIGINT;   -- carga total en custodia
    v_guar_total    BIGINT;   -- garantía total del transportista en el contrato
    v_freight_earn  BIGINT;
    v_freight_ref   BIGINT;
    v_guar_filled   BIGINT;
    v_guar_missing  BIGINT;
    v_comp          BIGINT;
    v_sink          BIGINT;
    v_i             INT := 1;
BEGIN
    SELECT * INTO fc FROM ledger.freight_contracts WHERE id = p_freight_id FOR UPDATE;
    IF fc.status <> 'active' THEN
        RAISE EXCEPTION 'ledger: contrato de flete % no está activo', p_freight_id;
    END IF;

    SELECT balance INTO v_total     FROM ledger.accounts WHERE id = fc.custody_account_id;
    SELECT balance INTO v_guar_total FROM ledger.accounts WHERE id = fc.carrier_guarantee_account_id;
    IF v_total <= 0 THEN
        RAISE EXCEPTION 'ledger: la custodia del flete % está vacía', p_freight_id;
    END IF;
    IF p_delivered > v_total THEN
        p_delivered := v_total;   -- defensivo: nunca se paga por más de lo cargado
    END IF;

    v_freight_earn := (fc.freight_price * p_delivered) / v_total;
    v_freight_ref  := fc.freight_price - v_freight_earn;
    v_guar_filled  := (v_guar_total * p_delivered) / v_total;
    v_guar_missing := v_guar_total - v_guar_filled;
    v_comp         := (v_guar_missing * p_compensation_bp) / 10000;
    v_sink         := v_guar_missing - v_comp;

    INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
    VALUES (p_tx_id, 'custody_release', p_sim_time, p_freight_id,
            format('Liquidación CCRI-Flete: entregado %s/%s', p_delivered, v_total));

    -- Custodia → cargador (donde la carga está físicamente ahora)
    INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES
        (p_entry_ids[v_i],   p_tx_id, fc.custody_account_id, -v_total),
        (p_entry_ids[v_i+1], p_tx_id, p_shipper_goods,        v_total);
    v_i := v_i + 2;

    -- Precio del flete: escrow → transportista (ganado) + cargador (reembolso)
    INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
        VALUES (p_entry_ids[v_i], p_tx_id, fc.escrow_account_id, -fc.freight_price);
    v_i := v_i + 1;
    IF v_freight_earn > 0 THEN
        INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
            VALUES (p_entry_ids[v_i], p_tx_id, p_carrier_cash, v_freight_earn);
        v_i := v_i + 1;
    END IF;
    IF v_freight_ref > 0 THEN
        INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
            VALUES (p_entry_ids[v_i], p_tx_id, p_shipper_cash, v_freight_ref);
        v_i := v_i + 1;
    END IF;

    -- Garantía del transportista: cumplida → transportista; faltante → comp/sink
    INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
        VALUES (p_entry_ids[v_i], p_tx_id, fc.carrier_guarantee_account_id, -v_guar_total);
    v_i := v_i + 1;
    IF v_guar_filled > 0 THEN
        INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
            VALUES (p_entry_ids[v_i], p_tx_id, p_carrier_cash, v_guar_filled);
        v_i := v_i + 1;
    END IF;
    IF v_comp > 0 THEN
        INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
            VALUES (p_entry_ids[v_i], p_tx_id, p_shipper_cash, v_comp);
        v_i := v_i + 1;
    END IF;
    IF v_sink > 0 THEN
        INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
            VALUES (p_entry_ids[v_i], p_tx_id, p_sink_account, v_sink);
        v_i := v_i + 1;
    END IF;

    UPDATE ledger.freight_contracts
       SET status = CASE WHEN p_delivered = 0 THEN 'failed'::ledger.contract_status
                         ELSE 'settled'::ledger.contract_status END,
           fill_bp = (p_delivered * 10000) / v_total,
           settled_at_sim = p_sim_time,
           updated_at = now()
     WHERE id = p_freight_id;
END;
$$ LANGUAGE plpgsql;
