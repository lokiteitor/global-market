-- =============================================================================
-- Imperio Industrial — 00_init.sql
-- Extensiones, esquemas por dominio y dominios de tipos comunes.
-- Referencia: GDD/SAD v1.2 §17 y Arquitectura §4.1 (una sola instancia de
-- PostgreSQL, esquemas separados por dominio).
-- =============================================================================

-- Extensiones (misma instalación de PostgreSQL)
CREATE EXTENSION IF NOT EXISTS postgis;          -- estado espacial del mundo
-- TimescaleDB NO se instala en Fases 0-1: solo si el volumen medido lo justifica (GDD 17.1)

-- Esquemas por dominio
CREATE SCHEMA IF NOT EXISTS auth;       -- identidad y sesiones (gateway TS / Drizzle)
CREATE SCHEMA IF NOT EXISTS world;      -- estado físico del mundo + PostGIS (motor Go / sqlc)
CREATE SCHEMA IF NOT EXISTS ledger;     -- dinero, stock comprometible, contratos (ACID estricta)
CREATE SCHEMA IF NOT EXISTS analytics;  -- agregados: OHLC, indicadores macro
CREATE SCHEMA IF NOT EXISTS outbox;     -- mensajería entre módulos (outbox + polling)

-- -----------------------------------------------------------------------------
-- Dominios de tipos comunes
-- -----------------------------------------------------------------------------

-- Identificador ULID con espacio de nombres por tipo: 'veh_', 'ctr_', 'crg_', ...
-- Únicos globalmente e independientes del esquema donde residan (GDD 17.2).
-- Alfabeto Crockford base32 (sin I, L, O, U).
CREATE DOMAIN ulid_id AS TEXT
    CHECK (VALUE ~ '^[a-z]{2,4}_[0-9A-HJKMNP-TV-Z]{26}$');

-- Sim-time: único reloj lógico del dominio (GDD 1.1). Segundos de sim-time
-- desde el génesis del mundo. Todo plazo de juego se almacena en sim-time;
-- el wall-clock (TIMESTAMPTZ) solo se usa para sesiones, auditoría y
-- mecánicas explícitamente definidas en tiempo real (ventana de sorteo).
CREATE DOMAIN sim_time AS BIGINT
    CHECK (VALUE >= 0);

-- Dinero: enteros en unidades menores (punto fijo). Nunca floats (invariante
-- del ledger, GDD 1.1 / 18.3). El signo lo aporta cada asiento, no el tipo.
CREATE DOMAIN money_amount AS BIGINT;

-- Cantidad de stock: enteros en la unidad mínima del producto. Nunca floats.
CREATE DOMAIN stock_qty AS BIGINT;
