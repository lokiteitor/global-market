-- =============================================================================
-- Imperio Industrial — 0001_init (up)
-- Extensiones, esquemas por dominio y dominios de tipos comunes.
-- Referencia: GDD/SAD §17 y Arquitectura §4.1 (una sola instancia de
-- PostgreSQL 18, esquemas separados por dominio); ADR-018 (UUIDv7 nativo:
-- desaparece el dominio ulid_id y los CHECK de prefijo por tipo).
-- =============================================================================

-- Extensiones (misma instalación de PostgreSQL)
CREATE EXTENSION IF NOT EXISTS postgis;          -- estado espacial del mundo (planar SRID 0, ADR-019)
-- TimescaleDB NO se instala en Fases 0-1: solo si el volumen medido lo justifica (GDD 17.1)

-- Esquemas por dominio
CREATE SCHEMA IF NOT EXISTS auth;       -- identidad, credenciales y sesiones (gateway Go, ADR-017)
CREATE SCHEMA IF NOT EXISTS world;      -- estado físico del mundo + PostGIS (motor Go / sqlc)
CREATE SCHEMA IF NOT EXISTS ledger;     -- dinero, stock comprometible, contratos (ACID estricta)
CREATE SCHEMA IF NOT EXISTS analytics;  -- agregados: OHLC, indicadores macro
CREATE SCHEMA IF NOT EXISTS outbox;     -- mensajería entre módulos (outbox + polling)

-- -----------------------------------------------------------------------------
-- Dominios de tipos comunes
-- (los identificadores son uuid nativo, generados con uuidv7(); ADR-018)
-- -----------------------------------------------------------------------------

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
