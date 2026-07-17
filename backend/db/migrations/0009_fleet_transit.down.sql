-- =============================================================================
-- Imperio Industrial — 0009_fleet_transit (down)
-- Revierte 0009 en orden inverso: elimina los índices de barrido del motor de
-- tránsito y las dos columnas añadidas a world.shipments. Las columnas son
-- NULLABLE y sin datos estructurales fuera del contexto fleet, por lo que su
-- retirada es limpia (los cargamentos in situ del contracts nunca las poblaron).
-- =============================================================================

DROP FUNCTION IF EXISTS world.segment_travel_seconds(jsonb);

DROP INDEX IF EXISTS world.ix_shipments_destination;
DROP INDEX IF EXISTS world.ix_vehicles_broken;
DROP INDEX IF EXISTS world.ix_vehicles_in_transit;

ALTER TABLE world.shipments
    DROP COLUMN IF EXISTS deadline_sim,
    DROP COLUMN IF EXISTS destination_node_id;
