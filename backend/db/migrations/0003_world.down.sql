-- =============================================================================
-- Imperio Industrial — 0003_world (down)
-- Revierte el esquema world en orden inverso a su creación.
-- Requiere que 0004_ledger ya esté revertida (FKs cross-schema).
-- =============================================================================

DROP TABLE IF EXISTS world.sim_clock;
DROP TABLE IF EXISTS world.shard_snapshots;
DROP TABLE IF EXISTS world.shipments;
DROP TABLE IF EXISTS world.vehicles;
DROP TABLE IF EXISTS world.route_legs;
DROP TABLE IF EXISTS world.routes;
DROP TABLE IF EXISTS world.vehicle_types;
DROP TABLE IF EXISTS world.terminal_slots;
DROP TABLE IF EXISTS world.terminals;
DROP TABLE IF EXISTS world.link_segments;
DROP TABLE IF EXISTS world.network_links;
DROP TABLE IF EXISTS world.network_nodes;
DROP TABLE IF EXISTS world.production_batches;
DROP TABLE IF EXISTS world.building_inventories;
DROP TABLE IF EXISTS world.buildings;
DROP TABLE IF EXISTS world.concession_transfers;
DROP TABLE IF EXISTS world.land_concessions;
DROP TABLE IF EXISTS world.city_demand;
DROP TABLE IF EXISTS world.cities;
DROP TABLE IF EXISTS world.resource_deposits;
DROP TABLE IF EXISTS world.recipe_ingredients;
DROP TABLE IF EXISTS world.recipes;
DROP TABLE IF EXISTS world.building_types;
DROP TABLE IF EXISTS world.products;
DROP TABLE IF EXISTS world.regions;

DROP TYPE IF EXISTS world.shipment_status;
DROP TYPE IF EXISTS world.route_kind;
DROP TYPE IF EXISTS world.vehicle_status;
DROP TYPE IF EXISTS world.link_mode;
DROP TYPE IF EXISTS world.node_kind;
DROP TYPE IF EXISTS world.concession_status;
DROP TYPE IF EXISTS world.batch_status;
DROP TYPE IF EXISTS world.building_status;
DROP TYPE IF EXISTS world.ingredient_role;
DROP TYPE IF EXISTS world.product_class;
DROP TYPE IF EXISTS world.biome;
