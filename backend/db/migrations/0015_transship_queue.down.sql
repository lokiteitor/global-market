-- =============================================================================
-- Imperio Industrial — 0015_transship_queue (down)
-- =============================================================================

DROP INDEX IF EXISTS world.ix_shipments_transship_pending;

ALTER TABLE world.shipments
    DROP COLUMN IF EXISTS transship_ready_at_sim;
