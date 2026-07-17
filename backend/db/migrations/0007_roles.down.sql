-- =============================================================================
-- Imperio Industrial — 0007_roles (down)
-- Revoca privilegios (incluidos los privilegios por defecto y las membresías,
-- vía DROP OWNED en la base actual) y elimina los roles de grupo.
-- Nota: DROP OWNED actúa sobre la base de datos actual; los roles de grupo no
-- reciben privilegios en ninguna otra base de este proyecto.
-- =============================================================================

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_analytics') THEN
        DROP OWNED BY ii_analytics;
        DROP ROLE ii_analytics;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_engine') THEN
        DROP OWNED BY ii_engine;
        DROP ROLE ii_engine;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'ii_gateway') THEN
        DROP OWNED BY ii_gateway;
        DROP ROLE ii_gateway;
    END IF;
END
$$;
