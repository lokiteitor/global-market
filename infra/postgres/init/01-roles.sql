-- ══════════════════════════════════════════════════════════════════════════
-- Imperio Industrial — Roles de servicio de DESARROLLO
--
-- Se ejecuta una única vez en el primer arranque del contenedor
-- (docker-entrypoint-initdb.d). SOLO para entorno local: passwords iguales
-- al nombre del usuario.
--
-- La membresía a los grupos de permisos ii_* (ii_gateway, ii_engine,
-- ii_analytics, ...) la otorga la migración 0007 del runner propio
-- (ADR-020), que es quien crea los grupos y sus GRANTs sobre los esquemas
-- de dominio. Este script solo garantiza que existan los usuarios LOGIN
-- en dev; en producción los crea y gestiona operaciones, nunca este fichero.
-- ══════════════════════════════════════════════════════════════════════════

CREATE USER svc_gateway   WITH LOGIN PASSWORD 'svc_gateway';
CREATE USER svc_engine    WITH LOGIN PASSWORD 'svc_engine';
CREATE USER svc_analytics WITH LOGIN PASSWORD 'svc_analytics';
