-- =============================================================================
-- Imperio Industrial — 0001_init (down)
-- Revierte dominios, esquemas y extensión en orden inverso a su creación.
-- Se ejecuta cuando las migraciones posteriores ya fueron revertidas: los
-- esquemas deben estar vacíos (sin CASCADE deliberadamente, como salvaguarda).
-- =============================================================================

DROP DOMAIN IF EXISTS stock_qty;
DROP DOMAIN IF EXISTS money_amount;
DROP DOMAIN IF EXISTS sim_time;

DROP SCHEMA IF EXISTS outbox;
DROP SCHEMA IF EXISTS analytics;
DROP SCHEMA IF EXISTS ledger;
DROP SCHEMA IF EXISTS world;
DROP SCHEMA IF EXISTS auth;

DROP EXTENSION IF EXISTS postgis;
