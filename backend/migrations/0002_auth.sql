-- =============================================================================
-- Imperio Industrial — 0002_auth.sql
-- Esquema auth: identidad, sesiones y perfiles de bot.
-- Propiedad: Gateway TS (Fastify + Drizzle). Jugadores y bots comparten el
-- mismo modelo de cuenta (GDD 18.1 #1); ciudades y banco central son cuentas
-- de sistema sin canal privilegiado (Arquitectura §9).
-- Origen: specs/schemas/01_auth.sql, adaptado a PostgreSQL 18 (IDs uuid con
-- uuidv7() nativa en lugar del antiguo dominio ulid_id con prefijos).
-- =============================================================================

CREATE TYPE auth.account_kind AS ENUM (
    'human',    -- jugador humano (corporación)
    'bot',      -- bot de producción (Bot Orchestration Service)
    'city',     -- ciudad como cuenta de mercado (agente decisor: Economy Balancer)
    'system'    -- banco central, cuentas operativas del sistema
);

CREATE TYPE auth.account_status AS ENUM (
    'active',
    'suspended',
    'retired'   -- bot retirado / cuenta liquidada por el ciclo de embargo
);

CREATE TYPE auth.bot_archetype AS ENUM (
    'primary_producer',        -- extrae recursos y vende materia prima
    'industrial_transformer',  -- compra insumos, produce bienes intermedios/finales
    'arbitrageur',             -- arbitraje de precios interregional
    'freighter'                -- servicios de flete (CCRI-Flete)
);

-- 1. accounts — corporaciones: humanos, bots, ciudades y sistema
CREATE TABLE auth.accounts (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    kind          auth.account_kind NOT NULL,
    name          TEXT NOT NULL,
    status        auth.account_status NOT NULL DEFAULT 'active',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ux_accounts_name ON auth.accounts (lower(name));
CREATE INDEX ix_accounts_kind ON auth.accounts (kind) WHERE status = 'active';

-- 2. sessions — sesiones de cliente (wall-clock: única capa donde el tiempo
--    real es legítimo como regla; GDD 1.1)
CREATE TABLE auth.sessions (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    account_id    uuid NOT NULL REFERENCES auth.accounts(id),
    token_hash    TEXT NOT NULL,
    client_info   JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at    TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX ux_sessions_token ON auth.sessions (token_hash);
CREATE INDEX ix_sessions_account ON auth.sessions (account_id);
CREATE INDEX ix_sessions_expiry ON auth.sessions (expires_at);

-- 3. bot_profiles — parámetros de comportamiento de la población de bots
--    (densidad dinámica gestionada por el Bot Orchestration Service, GDD 13.4)
CREATE TABLE auth.bot_profiles (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    account_id     uuid NOT NULL UNIQUE REFERENCES auth.accounts(id),
    archetype      auth.bot_archetype NOT NULL,
    behavior       JSONB NOT NULL DEFAULT '{}',   -- umbrales, agresividad, región foco
    density_weight NUMERIC NOT NULL DEFAULT 1.0 CHECK (density_weight >= 0),
    active         BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_bot_profiles_archetype ON auth.bot_profiles (archetype) WHERE active;

-- 4. account_credentials — credenciales de autenticación por cuenta.
--    Tabla omitida en la spec original y necesaria para POST /auth/sessions:
--    el gateway compara el secreto presentado (hash sha256 hex, nivel dev)
--    con secret_hash para emitir la sesión. Una fila por cuenta; las cuentas
--    sin credenciales (bots, ciudades, sistema) no pueden abrir sesión.
CREATE TABLE auth.account_credentials (
    account_id  uuid PRIMARY KEY REFERENCES auth.accounts(id),
    secret_hash text NOT NULL,   -- sha256 hex del secreto (nivel dev)
    updated_at  timestamptz NOT NULL DEFAULT now()
);
