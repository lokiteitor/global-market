-- =============================================================================
-- Imperio Industrial — queries sqlc del subpaquete world/catalog (ADR-020).
-- Lectura del mundo estático y de catálogos (contrato world/*): regiones,
-- productos, tipos de edificio, recetas, yacimientos, ciudades y demanda. Es
-- lectura pura y observable por cualquier corporación (sin filtro de propiedad).
--
-- Geometrías (bounds/location/parcel): SRID 0 planar, metros de mundo
-- (ADR-019). Se proyectan con ST_AsGeoJSON(...)::text — un objeto GeoJSON con
-- coordenadas planas [x_m, y_m] que los handlers embeben tal cual en el schema
-- GeoPolygon/GeoPoint del contrato (jamás lon/lat). Los NUMERIC del dominio de
-- oferta (supply_index, saturation_factor) se proyectan a float8: son
-- `type: number` del contrato, no dinero (dinero/stock siguen siendo int64).
--
-- Paginación keyset por id (UUIDv7 ≈ orden de creación) con page_limit; la
-- capa de servicio pide page_limit+1 para detectar la página siguiente.
-- =============================================================================

-- ─── Regiones ────────────────────────────────────────────────────────────────

-- ListRegions devuelve las macro-regiones con filtro opcional por bioma y
-- paginación keyset por id. bounds sale como GeoJSON plano (SRID 0).
-- name: ListRegions :many
SELECT id, name, grid_x, grid_y,
       ST_AsGeoJSON(bounds)::text AS bounds,
       biome, tax_rate_bp, customs_rate_bp, canon_base, opened_at_sim
FROM world.regions
WHERE (sqlc.narg(biome)::world.biome IS NULL OR biome = sqlc.narg(biome)::world.biome)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetRegion devuelve una región por id.
-- name: GetRegion :one
SELECT id, name, grid_x, grid_y,
       ST_AsGeoJSON(bounds)::text AS bounds,
       biome, tax_rate_bp, customs_rate_bp, canon_base, opened_at_sim
FROM world.regions
WHERE id = sqlc.arg(id);

-- ─── Productos ───────────────────────────────────────────────────────────────

-- ListProducts devuelve el catálogo de productos con filtros opcionales por
-- clase y por si es combustible, y paginación keyset por id.
-- name: ListProducts :many
SELECT id, code, name, class, unit_volume, base_price, price_floor, price_ceiling, is_fuel
FROM world.products
WHERE (sqlc.narg(class)::world.product_class IS NULL OR class = sqlc.narg(class)::world.product_class)
  AND (sqlc.narg(is_fuel)::boolean IS NULL OR is_fuel = sqlc.narg(is_fuel)::boolean)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- ─── Tipos de edificio ───────────────────────────────────────────────────────

-- ListBuildingTypes devuelve el catálogo de tipos de edificio con sus reglas de
-- emplazamiento y curva de niveles (JSONB → bytes crudos que el handler embebe).
-- name: ListBuildingTypes :many
SELECT id, code, name, footprint_cells, max_level, base_storage,
       placement_rules, level_curve, build_cost, maintenance_cost
FROM world.building_types
WHERE (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- ─── Recetas ─────────────────────────────────────────────────────────────────

-- ListRecipes devuelve las recetas con filtros opcionales por tipo de edificio
-- y por producto que aparezca en sus ingredientes (cualquier rol). Los
-- ingredientes se resuelven aparte (ListRecipeIngredients) por los ids de la
-- página, para no duplicar filas de receta en el JOIN.
-- name: ListRecipes :many
SELECT r.id, r.building_type_id, r.code, r.name, r.batch_sim_seconds,
       r.fuel_product_id, r.fuel_per_batch, r.workers_required,
       r.min_city_level, r.changeover_seconds
FROM world.recipes r
WHERE (sqlc.narg(building_type_id)::uuid IS NULL OR r.building_type_id = sqlc.narg(building_type_id)::uuid)
  AND (sqlc.narg(product_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM world.recipe_ingredients ri
        WHERE ri.recipe_id = r.id AND ri.product_id = sqlc.narg(product_id)::uuid))
  AND (sqlc.narg(after_id)::uuid IS NULL OR r.id > sqlc.narg(after_id)::uuid)
ORDER BY r.id
LIMIT sqlc.arg(page_limit);

-- ListRecipeIngredients devuelve los ingredientes (insumos y productos) de un
-- conjunto de recetas, en orden estable por receta, rol y producto.
-- name: ListRecipeIngredients :many
SELECT recipe_id, product_id, role, quantity
FROM world.recipe_ingredients
WHERE recipe_id = ANY(sqlc.arg(recipe_ids)::uuid[])
ORDER BY recipe_id, role, product_id;

-- ─── Yacimientos ─────────────────────────────────────────────────────────────

-- ListResourceDeposits devuelve los yacimientos con filtros opcionales por
-- región y producto; only_available (default true en el contrato) recorta a los
-- que aún tienen cantidad restante. location sale como GeoJSON plano.
-- name: ListResourceDeposits :many
SELECT id, region_id, product_id,
       ST_AsGeoJSON(location)::text AS location,
       initial_amount, remaining_amount, renewable, regen_per_sim_day
FROM world.resource_deposits
WHERE (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id)::uuid)
  AND (NOT sqlc.arg(only_available)::boolean OR remaining_amount > 0)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- ─── Ciudades ────────────────────────────────────────────────────────────────

-- ListCities devuelve las ciudades con filtros opcionales por región y nivel
-- mínimo, y paginación keyset por id. supply_index es NUMERIC → float8 (número
-- del contrato, no dinero). location sale como GeoJSON plano.
-- name: ListCities :many
SELECT id, region_id, account_id, name,
       ST_AsGeoJSON(location)::text AS location,
       level, population, supply_index::float8 AS supply_index,
       influence_radius_m, base_salary
FROM world.cities
WHERE (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(min_level)::int IS NULL OR level >= sqlc.narg(min_level)::int)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetCity devuelve una ciudad por id.
-- name: GetCity :one
SELECT id, region_id, account_id, name,
       ST_AsGeoJSON(location)::text AS location,
       level, population, supply_index::float8 AS supply_index,
       influence_radius_m, base_salary
FROM world.cities
WHERE id = sqlc.arg(id);

-- CityExists indica si una ciudad existe (para distinguir 404 de "sin demanda"
-- en el endpoint de curva de demanda).
-- name: CityExists :one
SELECT EXISTS (SELECT 1 FROM world.cities WHERE id = sqlc.arg(id));

-- ListCityDemand devuelve la curva de demanda vigente de una ciudad, con filtro
-- opcional por producto, ordenada por producto. saturation_factor es NUMERIC →
-- float8 (número del contrato). Sin paginación: el contrato no la define aquí.
-- name: ListCityDemand :many
SELECT city_id, product_id, d0_per_sim_day,
       saturation_factor::float8 AS saturation_factor,
       current_price, unlocked_at_level, updated_at_sim
FROM world.city_demand
WHERE city_id = sqlc.arg(city_id)
  AND (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id)::uuid)
ORDER BY product_id;
