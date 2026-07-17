-- =============================================================================
-- Imperio Industrial — 0002_auth (down)
-- Revierte el esquema auth en orden inverso a su creación.
-- =============================================================================

DROP TABLE IF EXISTS auth.bot_profiles;
DROP TABLE IF EXISTS auth.sessions;
DROP TABLE IF EXISTS auth.account_credentials;
DROP TABLE IF EXISTS auth.accounts;

DROP TYPE IF EXISTS auth.bot_archetype;
DROP TYPE IF EXISTS auth.account_status;
DROP TYPE IF EXISTS auth.account_kind;
