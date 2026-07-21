-- =============================================================================
-- Imperio Industrial — 0013_city_recent_supply (down)
-- Revierte 0013: elimina el acumulador recent_supply de world.city_demand. Es
-- un tracker interno del Balancer sin dependencias externas: su retirada es
-- limpia (la curva de demanda vuelve a alimentar supply_ema sin la ventana de
-- oferta reciente persistida).
-- =============================================================================

ALTER TABLE world.city_demand
    DROP COLUMN IF EXISTS recent_supply;
