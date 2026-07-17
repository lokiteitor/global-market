-- =============================================================================
-- Imperio Industrial — 0006_outbox (down)
-- Revierte el esquema outbox en orden inverso a su creación.
-- =============================================================================

DROP TABLE IF EXISTS outbox.consumer_cursors;
DROP TABLE IF EXISTS outbox.events;
