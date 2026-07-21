-- =============================================================================
-- Imperio Industrial — 0011_enforcement (down)
-- Revierte 0011 en orden inverso: elimina los índices de barrido de la cascada
-- de insolvencia y las tres columnas añadidas. Todas son columnas de estado del
-- motor de enforcement (mantenimiento/canon/embargo), sin dependencias externas:
-- su retirada es limpia. Un edificio 'seized' o una concesión 'reverted' quedan
-- en ese estado terminal del enum (0003_world), pero ya no habrá barrido que los
-- procese — coherente (el motor desaparece con la migración).
-- =============================================================================

DROP INDEX IF EXISTS world.ix_vehicles_maintenance_due;
DROP INDEX IF EXISTS world.ix_concessions_pending_seizure;
DROP INDEX IF EXISTS world.ix_concessions_grace;
DROP INDEX IF EXISTS world.ix_buildings_concession;
DROP INDEX IF EXISTS world.ix_buildings_abandoned;
DROP INDEX IF EXISTS world.ix_buildings_maintenance_due;

ALTER TABLE world.land_concessions
    DROP COLUMN IF EXISTS grace_until_sim;

ALTER TABLE world.vehicles
    DROP COLUMN IF EXISTS maintenance_paid_until_sim;

ALTER TABLE world.buildings
    DROP COLUMN IF EXISTS maintenance_paid_until_sim;
