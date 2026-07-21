-- =============================================================================
-- Queries sqlc — red eléctrica regional (GDD 5.8 / ADR-025, Fase 3).
-- Subpaquete internal/world/power (handlers en el gateway) y el barrido de
-- mantenimiento de líneas de internal/world/enforcement (proceso engine).
-- Comparten el sqlcgen del contexto world (la frontera de módulo es de CÓDIGO
-- Go, no de esquema). El soporte de ledger (GetCashAccount, GetSinkAccount,
-- InsertLedgerTransaction, InsertLedgerEntry) viene de land.sql.
--
-- El tick del mercado spot NO vive aquí: es del Economy Balancer (GDD 18.1),
-- con sus propias queries en internal/balancer/queries/power.sql.
-- =============================================================================

-- ═════════════════════════════════════════════════════════════════════════════
-- (1) Líneas de transmisión (alta, catálogo).
-- ═════════════════════════════════════════════════════════════════════════════

-- GetRegionContainingLine resuelve la región cuyos bounds contienen ÍNTEGRO el
-- trazado (GeoJSON LineString, SRID 0). pgx.ErrNoRows si ninguna lo contiene:
-- el trazado cruza regiones o cae fuera del mundo (las interconexiones
-- interregionales son expansión futura, GDD 22).
-- name: GetRegionContainingLine :one
SELECT id, name
FROM world.regions
WHERE ST_Contains(bounds, ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(path)::text), 0))
LIMIT 1;

-- LineLengthM mide el trazado en metros de mundo (redondeo, mínimo 1).
-- name: LineLengthM :one
SELECT GREATEST(1, round(ST_Length(ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(path)::text), 0))))::int AS length_m;

-- InsertPowerLine da de alta la línea (operational, condición 100). El
-- mantenimiento arranca liquidado hasta el alta (maintenance_paid_until_sim).
-- name: InsertPowerLine :exec
INSERT INTO world.power_lines
       (id, owner_account_id, region_id, path, length_m,
        status, condition_pct, maintenance_paid_until_sim, updated_at_sim)
VALUES (sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(region_id),
        ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(path)::text), 0), sqlc.arg(length_m),
        'operational', 100, sqlc.arg(sim_now), sqlc.arg(sim_now));

-- GetPowerLine devuelve una línea con su trazado como GeoJSON.
-- name: GetPowerLine :one
SELECT id, owner_account_id, region_id, ST_AsGeoJSON(path)::text AS path_geojson,
       length_m, status, condition_pct, maintenance_paid_until_sim, updated_at_sim
FROM world.power_lines
WHERE id = sqlc.arg(id);

-- ListPowerLines pagina el catálogo (visible para todos, como las terminales),
-- con filtro opcional por región. Keyset por id (UUIDv7).
-- name: ListPowerLines :many
SELECT id, owner_account_id, region_id, ST_AsGeoJSON(path)::text AS path_geojson,
       length_m, status, condition_pct, maintenance_paid_until_sim, updated_at_sim
FROM world.power_lines
WHERE (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id))
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id))
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- ═════════════════════════════════════════════════════════════════════════════
-- (2) Ofertas de generación y pujas de consumo.
-- ═════════════════════════════════════════════════════════════════════════════

-- GetBuildingForPower devuelve lo necesario para autorizar y validar una
-- oferta/puja: dueño, estado y si el tipo es una central (fila en
-- power_plant_types). pgx.ErrNoRows si el edificio no existe.
-- name: GetBuildingForPower :one
SELECT b.id, b.owner_account_id, b.status, b.building_type_id,
       (ppt.building_type_id IS NOT NULL)::bool AS is_power_plant
FROM world.buildings b
LEFT JOIN world.power_plant_types ppt ON ppt.building_type_id = b.building_type_id
WHERE b.id = sqlc.arg(id);

-- UpsertPowerOffer fija el precio de oferta de una central. Sin fila, la
-- central no participa del spot (ofertar es decisión del jugador).
-- name: UpsertPowerOffer :exec
INSERT INTO world.power_offers (building_id, unit_price, updated_at_sim)
VALUES (sqlc.arg(building_id), sqlc.arg(unit_price), sqlc.arg(sim_now))
ON CONFLICT (building_id) DO UPDATE
   SET unit_price = EXCLUDED.unit_price, updated_at_sim = EXCLUDED.updated_at_sim;

-- UpsertPowerBid fija la puja máxima de un consumidor (prioridad inversa del
-- recorte y techo personal; sin fila rige II_POWER_DEFAULT_BID_PRICE).
-- name: UpsertPowerBid :exec
INSERT INTO world.power_bids (building_id, unit_price, updated_at_sim)
VALUES (sqlc.arg(building_id), sqlc.arg(unit_price), sqlc.arg(sim_now))
ON CONFLICT (building_id) DO UPDATE
   SET unit_price = EXCLUDED.unit_price, updated_at_sim = EXCLUDED.updated_at_sim;

-- ═════════════════════════════════════════════════════════════════════════════
-- (3) Lecturas del contrato: histórico del spot y "mi consumo".
-- ═════════════════════════════════════════════════════════════════════════════

-- ListPowerSpotTicks devuelve los últimos ticks de una región (precio de
-- cierre, despacho y recorte), más recientes primero.
-- name: ListPowerSpotTicks :many
SELECT region_id, tick_sim, interval_sim, closing_price,
       demand_units, supplied_units, curtailed_units, curtailed_buildings
FROM world.power_spot_ticks
WHERE region_id = sqlc.arg(region_id)
  AND (sqlc.narg(before_tick_sim)::bigint IS NULL OR tick_sim < sqlc.narg(before_tick_sim))
ORDER BY tick_sim DESC
LIMIT sqlc.arg(page_limit);

-- ListPowerDispatchesForBuilding devuelve el despacho/consumo de un edificio
-- (la autorización por propiedad se resuelve antes con GetBuildingForPower).
-- name: ListPowerDispatchesForBuilding :many
SELECT region_id, tick_sim, building_id, owner_account_id, role,
       units, unit_price, amount
FROM world.power_dispatches
WHERE building_id = sqlc.arg(building_id)
  AND (sqlc.narg(before_tick_sim)::bigint IS NULL OR tick_sim < sqlc.narg(before_tick_sim))
ORDER BY tick_sim DESC
LIMIT sqlc.arg(page_limit);

-- ═════════════════════════════════════════════════════════════════════════════
-- (4) Emplazamiento: bioma de la región (regla requires_biome, ADR-025 §5).
-- ═════════════════════════════════════════════════════════════════════════════

-- GetRegionBiome devuelve el bioma de una región (regla de emplazamiento
-- requires_biome de las hidroeléctricas: "ríos/agua" se materializa hoy como
-- bioma coast; los ríos del worldgen siguen pendientes).
-- name: GetRegionBiome :one
SELECT biome FROM world.regions WHERE id = sqlc.arg(id);

-- ═════════════════════════════════════════════════════════════════════════════
-- (5) Mantenimiento de líneas (barrido de world/enforcement, patrón 0011).
-- ═════════════════════════════════════════════════════════════════════════════

-- ListPowerLinesDueMaintenance lista líneas operativas con >= 1 día-sim de
-- mantenimiento vencido (solo ids; el lock va por línea).
-- name: ListPowerLinesDueMaintenance :many
SELECT id
FROM world.power_lines
WHERE status = 'operational'
  AND maintenance_paid_until_sim <= sqlc.arg(sim_now)::bigint - 86400
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockPowerLineForMaintenance re-bloquea una línea candidata (FOR UPDATE SKIP
-- LOCKED: varias instancias barren en paralelo sin pisarse). pgx.ErrNoRows si
-- otra instancia la tomó o ya no aplica.
-- name: LockPowerLineForMaintenance :one
SELECT id, owner_account_id, region_id, length_m, status, condition_pct,
       maintenance_paid_until_sim
FROM world.power_lines
WHERE id = sqlc.arg(id)
  AND status = 'operational'
  AND maintenance_paid_until_sim <= sqlc.arg(sim_now)::bigint - 86400
FOR UPDATE SKIP LOCKED;

-- UpdatePowerLineMaintenance persiste el resultado del barrido: marcador
-- avanzado (cada día vencido se salda exactamente una vez), condición
-- recuperada o degradada, y el estado (abandoned = deja de conducir, terminal).
-- name: UpdatePowerLineMaintenance :exec
UPDATE world.power_lines
   SET maintenance_paid_until_sim = sqlc.arg(paid_until_sim),
       condition_pct = sqlc.arg(condition_pct),
       status = sqlc.arg(status)::world.power_line_status,
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);
