-- =============================================================================
-- Imperio Industrial — 04_analytics.sql
-- Esquema analytics: agregados permanentes (nunca se borran, crecen lento) y
-- estadísticas de mercado. Escrito por el job Analytics (batch de baja
-- prioridad, nunca compite con Persistence — Arquitectura §5.1).
-- Fuente: contratos LIQUIDADOS, no órdenes vivas (GDD 5.2).
-- TimescaleDB solo si el volumen medido lo justifica; a la escala de Fases
-- 0-1 basta un GROUP BY por bucket (GDD 17.1).
-- Identificadores: uuid, referencias a claves UUIDv7 nativas de PostgreSQL 18.
-- Fuente ejecutable: backend/migrations/0005_analytics.sql (aplicación manual vía make db-migrate).
-- =============================================================================

-- 1. market_ohlc — velas OHLC por producto y región, construidas a partir de
--    contratos efectivamente cerrados; referencia de precio para todos (GDD 5.2/5.4)
CREATE TABLE analytics.market_ohlc (
    product_id        uuid NOT NULL REFERENCES world.products(id),
    region_id         uuid NOT NULL REFERENCES world.regions(id),
    bucket_start_sim  sim_time NOT NULL,        -- bucket en sim-time (p. ej. 1 día de juego)
    bucket_sim_secs   BIGINT NOT NULL CHECK (bucket_sim_secs > 0),
    open_price        money_amount NOT NULL,
    high_price        money_amount NOT NULL,
    low_price         money_amount NOT NULL,
    close_price       money_amount NOT NULL,
    volume            stock_qty NOT NULL CHECK (volume >= 0),
    contract_count    INT NOT NULL CHECK (contract_count >= 0),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (product_id, region_id, bucket_start_sim),
    CHECK (low_price <= open_price AND low_price <= close_price
           AND high_price >= open_price AND high_price >= close_price)
);

CREATE INDEX ix_ohlc_region_time ON analytics.market_ohlc (region_id, bucket_start_sim DESC);

-- 2. city_snapshots — evolución de ciudades (nivel, población, índice de
--    suministro): agregado permanente, objetivo estratégico observable (GDD 5.6)
CREATE TABLE analytics.city_snapshots (
    city_id           uuid NOT NULL REFERENCES world.cities(id),
    bucket_start_sim  sim_time NOT NULL,
    level             INT NOT NULL,
    population        BIGINT NOT NULL,
    supply_index      NUMERIC NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (city_id, bucket_start_sim)
);

-- 3. region_stats — estadísticas regionales para el Economy Balancer:
--    saturación industrial (fórmula laboral 5.7), actividad, fiscalidad
CREATE TABLE analytics.region_stats (
    region_id              uuid NOT NULL REFERENCES world.regions(id),
    bucket_start_sim       sim_time NOT NULL,
    industrial_occupation  NUMERIC NOT NULL,   -- factor_saturación laboral (GDD 5.7)
    active_buildings       INT NOT NULL,
    contracts_settled      INT NOT NULL,
    trade_volume           money_amount NOT NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (region_id, bucket_start_sim)
);

-- 4. economy_indicators — métricas macro de primer nivel (Arquitectura §6.2):
--    masa monetaria vs. PIB simulado; ritmo de agotamiento global de recursos
--    con proyección 6-12 meses para planificar expansiones de mapa (GDD 10/20)
CREATE TABLE analytics.economy_indicators (
    bucket_start_sim        sim_time PRIMARY KEY,
    money_supply            money_amount NOT NULL,  -- masa monetaria total (suma de cuentas cash/escrow/guarantee)
    simulated_gdp           money_amount NOT NULL,  -- valor de contratos liquidados en el bucket
    emission_total          money_amount NOT NULL,  -- faucets: capitalización de bots, capital semilla
    absorption_total        money_amount NOT NULL,  -- sinks: sanciones, impuestos, canon, retiros
    active_bot_count        INT NOT NULL,
    active_human_count      INT NOT NULL,
    global_depletion_rate   NUMERIC NOT NULL,       -- ritmo de agotamiento de minerales finitos
    depletion_projection    JSONB NOT NULL DEFAULT '{}', -- proyección 6-12 meses por recurso
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
