-- =============================================================================
-- Imperio Industrial — queries sqlc del subpaquete world/buildings (ADR-020).
-- Construcción y configuración de instalaciones (GDD 6/11): alta de edificio
-- (under_construction) con su nodo del grafo logístico, validación de
-- emplazamiento server-side, cambio de receta / mantenimiento, mejora de nivel
-- e inventario físico. Es el lado de ESCRITURA del contexto world para los
-- edificios: cada operación que mueve valor (build_cost, upgrade_cost) corre en
-- una transacción SERIALIZABLE con outbox.Emit en la misma tx y asienta como
-- SINK en ledger.* vía las queries de soporte de land.sql (COMPARTIDAS: la
-- frontera entre subpaquetes es de código Go, no de fichero SQL).
--
-- Geometrías (footprint/parcel): SRID 0 planar, metros de mundo (ADR-019). La
-- ENTRADA (footprint) llega como GeoJSON y se proyecta con
-- ST_SetSRID(ST_GeomFromGeoJSON(...), 0); la SALIDA con ST_AsGeoJSON(...)::text.
-- El nodo se ubica en el centroide del footprint (ST_Centroid).
-- =============================================================================

-- ─── Edificios: lectura ───────────────────────────────────────────────────────

-- ListBuildings devuelve los edificios de un dueño (SOLO propios) con filtros
-- opcionales por región, estado y tipo, y paginación keyset por id.
-- name: ListBuildings :many
SELECT id, owner_account_id, region_id, concession_id, building_type_id,
       ST_AsGeoJSON(footprint)::text AS footprint,
       level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim
FROM world.buildings
WHERE owner_account_id = sqlc.arg(owner_account_id)
  AND (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(status)::world.building_status IS NULL OR status = sqlc.narg(status)::world.building_status)
  AND (sqlc.narg(building_type_id)::uuid IS NULL OR building_type_id = sqlc.narg(building_type_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetBuilding devuelve un edificio por id (la autorización por dueño la aplica
-- la capa de servicio).
-- name: GetBuilding :one
SELECT id, owner_account_id, region_id, concession_id, building_type_id,
       ST_AsGeoJSON(footprint)::text AS footprint,
       level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim
FROM world.buildings
WHERE id = sqlc.arg(id);

-- GetBuildingForUpdate bloquea la fila (SELECT FOR UPDATE) para configuración y
-- mejora: las validaciones y el cobro se deciden bajo lock.
-- name: GetBuildingForUpdate :one
SELECT id, owner_account_id, region_id, concession_id, building_type_id,
       ST_AsGeoJSON(footprint)::text AS footprint,
       level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim
FROM world.buildings
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- ─── Tipos y recetas (validación) ─────────────────────────────────────────────

-- GetBuildingType devuelve el tipo de edificio (coste, reglas de emplazamiento,
-- curva de niveles y nivel máximo) para construir/mejorar; ErrNoRows si no
-- existe.
-- name: GetBuildingType :one
SELECT id, code, name, footprint_cells, max_level, base_storage,
       placement_rules, level_curve, build_cost, maintenance_cost
FROM world.building_types
WHERE id = sqlc.arg(id);

-- GetRecipe devuelve una receta (para validar que pertenece al tipo del
-- edificio y su min_city_level); ErrNoRows si no existe.
-- name: GetRecipe :one
SELECT id, building_type_id, code, name, batch_sim_seconds,
       min_city_level, changeover_seconds
FROM world.recipes
WHERE id = sqlc.arg(id);

-- ─── Emplazamiento (validación server-side, PLACEMENT_INVALID) ────────────────

-- LockConcessionForBuilding bloquea la concesión de destino y devuelve su
-- región, titular y estado (reglas (a) propiedad + status active). ErrNoRows si
-- la concesión no existe.
-- name: LockConcessionForBuilding :one
SELECT id, region_id, holder_account_id, status
FROM world.land_concessions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- FootprintWithinParcel comprueba la regla (b): el footprint cae DENTRO de la
-- parcela de la concesión (ST_Within).
-- name: FootprintWithinParcel :one
SELECT ST_Within(ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(footprint_geojson)::text), 0), parcel)::boolean AS within
FROM world.land_concessions
WHERE id = sqlc.arg(id);

-- BuildingFootprintOverlaps comprueba la regla (c): el footprint NO se solapa
-- (ST_Intersects) con el de ningún edificio existente.
-- name: BuildingFootprintOverlaps :one
SELECT EXISTS (
    SELECT 1 FROM world.buildings
    WHERE ST_Intersects(footprint, ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(footprint_geojson)::text), 0))
)::boolean AS overlaps;

-- ResourceNearby comprueba la regla placement_rules.near_resource: existe un
-- yacimiento del producto (por code) con remaining > 0 dentro del radio
-- (ST_DWithin) desde el centroide del footprint.
-- name: ResourceNearby :one
SELECT EXISTS (
    SELECT 1
    FROM world.resource_deposits d
    JOIN world.products p ON p.id = d.product_id
    WHERE p.code = sqlc.arg(product_code)
      AND d.remaining_amount > 0
      AND ST_DWithin(
            d.location,
            ST_Centroid(ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(footprint_geojson)::text), 0)),
            sqlc.arg(max_distance_m)::float8)
)::boolean AS present;

-- NodeKindPresentInRegion comprueba la regla placement_rules.requires_node_kind:
-- existe un nodo del grafo logístico de ese kind en la región de la concesión.
-- name: NodeKindPresentInRegion :one
SELECT EXISTS (
    SELECT 1 FROM world.network_nodes
    WHERE region_id = sqlc.arg(region_id)
      AND kind = sqlc.arg(node_kind)::world.node_kind
)::boolean AS present;

-- ─── Construcción ─────────────────────────────────────────────────────────────

-- InsertBuilding crea el edificio under_construction (level 1, condition 100,
-- sin combustible ni receta). El coste ya se asentó al sink en la misma tx.
-- updated_at_sim es la marca de creación que el motor usa para completar la
-- construcción tras II_BUILD_SIM_SECONDS.
-- name: InsertBuilding :one
INSERT INTO world.buildings (
    id, owner_account_id, region_id, concession_id, building_type_id,
    footprint, level, status, condition_pct, fuel_stock, updated_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(region_id),
    sqlc.arg(concession_id), sqlc.arg(building_type_id),
    ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(footprint_geojson)::text), 0),
    1, 'under_construction', 100, 0, sqlc.arg(updated_at_sim))
RETURNING id, owner_account_id, region_id, concession_id, building_type_id,
          ST_AsGeoJSON(footprint)::text AS footprint,
          level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim;

-- InsertNetworkNode crea el nodo del grafo logístico ligado al edificio, en el
-- centroide del footprint. kind lo deriva la aplicación (mina→mine, resto→
-- factory).
-- name: InsertNetworkNode :one
INSERT INTO world.network_nodes (id, kind, region_id, building_id, location)
VALUES (
    sqlc.arg(id), sqlc.arg(kind)::world.node_kind, sqlc.arg(region_id), sqlc.arg(building_id),
    ST_Centroid(ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(footprint_geojson)::text), 0)))
RETURNING id;

-- ─── Configuración y mejora ───────────────────────────────────────────────────

-- NearestCityLevelInRegion devuelve el nivel de la ciudad más cercana al
-- edificio dentro de su región (cualificación laboral de la receta, GDD 5.7);
-- ErrNoRows si la región no tiene ciudades.
-- name: NearestCityLevelInRegion :one
SELECT c.level
FROM world.cities c
WHERE c.region_id = sqlc.arg(region_id)
ORDER BY c.location <-> (SELECT ST_Centroid(b.footprint) FROM world.buildings b WHERE b.id = sqlc.arg(building_id))
LIMIT 1;

-- SetBuildingRecipe fija (o detiene, con NULL) la receta activa del edificio.
-- name: SetBuildingRecipe :one
UPDATE world.buildings
   SET active_recipe_id = sqlc.narg(active_recipe_id), updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id)
RETURNING id, owner_account_id, region_id, concession_id, building_type_id,
          ST_AsGeoJSON(footprint)::text AS footprint,
          level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim;

-- SetBuildingStatus cambia el estado del edificio (p. ej. in_maintenance).
-- name: SetBuildingStatus :one
UPDATE world.buildings
   SET status = sqlc.arg(status)::world.building_status, updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id)
RETURNING id, owner_account_id, region_id, concession_id, building_type_id,
          ST_AsGeoJSON(footprint)::text AS footprint,
          level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim;

-- SetBuildingLevel sube el nivel del edificio (mejora). El coste ya se asentó al
-- sink en la misma tx.
-- name: SetBuildingLevel :one
UPDATE world.buildings
   SET level = sqlc.arg(level), updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id)
RETURNING id, owner_account_id, region_id, concession_id, building_type_id,
          ST_AsGeoJSON(footprint)::text AS footprint,
          level, status, active_recipe_id, condition_pct, fuel_stock, created_at, updated_at_sim;

-- ─── Inventario físico ────────────────────────────────────────────────────────

-- ListBuildingInventory devuelve el inventario FÍSICO del edificio por producto
-- (vista física; la partición libre/reservado vive en el ledger).
-- name: ListBuildingInventory :many
SELECT building_id, product_id, quantity, updated_at_sim
FROM world.building_inventories
WHERE building_id = sqlc.arg(building_id)
ORDER BY product_id;
