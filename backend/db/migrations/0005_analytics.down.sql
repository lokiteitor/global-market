-- =============================================================================
-- Imperio Industrial — 0005_analytics (down)
-- Revierte el esquema analytics en orden inverso a su creación.
-- =============================================================================

DROP TABLE IF EXISTS analytics.economy_indicators;
DROP TABLE IF EXISTS analytics.region_stats;
DROP TABLE IF EXISTS analytics.city_snapshots;
DROP TABLE IF EXISTS analytics.market_ohlc;
