-- =============================================================================
-- Imperio Industrial — 0007_roles (up)
-- Roles de grupo NOLOGIN con mínimo privilegio por servicio (Arquitectura §9,
-- DB doc "credenciales por servicio/esquema"):
--   ii_gateway   — gateway Go: dueño lógico de auth; lectura de ledger, world
--                  y outbox (consultas de la API pública).
--   ii_engine    — motor Go (shards, Contract Service, Balancer): escribe
--                  world, ledger y outbox; lee auth.
--   ii_analytics — job Analytics: escribe analytics; lee ledger y world.
-- Los usuarios LOGIN (svc_gateway/svc_engine/svc_analytics) los crea el init
-- de Docker en dev, u operaciones en otros entornos; aquí solo se les concede
-- la membresía si existen. Los roles son globales del clúster: creación
-- idempotente con IF NOT EXISTS.
-- =============================================================================

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_gateway') THEN
        CREATE ROLE ii_gateway NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_engine') THEN
        CREATE ROLE ii_engine NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_analytics') THEN
        CREATE ROLE ii_analytics NOLOGIN;
    END IF;
END
$$;

-- ── ii_gateway ───────────────────────────────────────────────────────────────
GRANT USAGE ON SCHEMA auth, ledger, world, outbox TO ii_gateway;
GRANT ALL ON ALL TABLES IN SCHEMA auth TO ii_gateway;
GRANT ALL ON ALL SEQUENCES IN SCHEMA auth TO ii_gateway;
GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO ii_gateway;
GRANT SELECT ON ALL TABLES IN SCHEMA world TO ii_gateway;
GRANT SELECT ON ALL TABLES IN SCHEMA outbox TO ii_gateway;

-- ── ii_engine ────────────────────────────────────────────────────────────────
GRANT USAGE ON SCHEMA world, ledger, outbox, auth TO ii_engine;
GRANT ALL ON ALL TABLES IN SCHEMA world TO ii_engine;
GRANT ALL ON ALL SEQUENCES IN SCHEMA world TO ii_engine;
GRANT ALL ON ALL TABLES IN SCHEMA ledger TO ii_engine;
GRANT ALL ON ALL SEQUENCES IN SCHEMA ledger TO ii_engine;
GRANT ALL ON ALL TABLES IN SCHEMA outbox TO ii_engine;
GRANT ALL ON ALL SEQUENCES IN SCHEMA outbox TO ii_engine;   -- identity de outbox.events
GRANT SELECT ON ALL TABLES IN SCHEMA auth TO ii_engine;

-- ── ii_analytics ─────────────────────────────────────────────────────────────
GRANT USAGE ON SCHEMA analytics, ledger, world TO ii_analytics;
GRANT ALL ON ALL TABLES IN SCHEMA analytics TO ii_analytics;
GRANT ALL ON ALL SEQUENCES IN SCHEMA analytics TO ii_analytics;
GRANT SELECT ON ALL TABLES IN SCHEMA ledger TO ii_analytics;
GRANT SELECT ON ALL TABLES IN SCHEMA world TO ii_analytics;

-- ── Privilegios por defecto para tablas/secuencias FUTURAS creadas por el rol
--    que aplica las migraciones (el mismo que ejecuta este script) ────────────
ALTER DEFAULT PRIVILEGES IN SCHEMA auth
    GRANT ALL ON TABLES TO ii_gateway;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth
    GRANT ALL ON SEQUENCES TO ii_gateway;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger
    GRANT SELECT ON TABLES TO ii_gateway;
ALTER DEFAULT PRIVILEGES IN SCHEMA world
    GRANT SELECT ON TABLES TO ii_gateway;
ALTER DEFAULT PRIVILEGES IN SCHEMA outbox
    GRANT SELECT ON TABLES TO ii_gateway;

ALTER DEFAULT PRIVILEGES IN SCHEMA world
    GRANT ALL ON TABLES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA world
    GRANT ALL ON SEQUENCES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger
    GRANT ALL ON TABLES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger
    GRANT ALL ON SEQUENCES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA outbox
    GRANT ALL ON TABLES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA outbox
    GRANT ALL ON SEQUENCES TO ii_engine;
ALTER DEFAULT PRIVILEGES IN SCHEMA auth
    GRANT SELECT ON TABLES TO ii_engine;

ALTER DEFAULT PRIVILEGES IN SCHEMA analytics
    GRANT ALL ON TABLES TO ii_analytics;
ALTER DEFAULT PRIVILEGES IN SCHEMA analytics
    GRANT ALL ON SEQUENCES TO ii_analytics;
ALTER DEFAULT PRIVILEGES IN SCHEMA ledger
    GRANT SELECT ON TABLES TO ii_analytics;
ALTER DEFAULT PRIVILEGES IN SCHEMA world
    GRANT SELECT ON TABLES TO ii_analytics;

-- ── Membresías de los usuarios LOGIN de servicio, si existen ────────────────
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_gateway') THEN
        GRANT ii_gateway TO svc_gateway;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_engine') THEN
        GRANT ii_engine TO svc_engine;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_analytics') THEN
        GRANT ii_analytics TO svc_analytics;
    END IF;
END
$$;
