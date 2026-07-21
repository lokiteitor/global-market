-- =============================================================================
-- Imperio Industrial — 0016_outbox_consumer_interest (down)
-- =============================================================================

ALTER TABLE outbox.consumer_cursors
    DROP COLUMN IF EXISTS event_types;
