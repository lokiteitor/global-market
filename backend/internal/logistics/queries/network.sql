-- =============================================================================
-- Imperio Industrial — queries sqlc del bounded context logistics (ADR-020).
-- Lectura del grafo logístico (contrato logistics/network/*): nodos, enlaces de
-- uso común y sus segmentos con la congestión suavizada (EMA) que publican los
-- shards. Es lectura pública observable por cualquier corporación (sin filtro
-- de propiedad): el grafo es información del mundo, no de un titular.
--
-- Geometrías (location/path): SRID 0 planar, metros de mundo (ADR-019). Se
-- proyectan con ST_AsGeoJSON(...)::text — objetos GeoJSON con coordenadas planas
-- [x_m, y_m] que los handlers embeben tal cual en GeoPoint/GeoLineString (jamás
-- lon/lat). congestion_ema es NUMERIC → float8 (número del contrato, no dinero).
--
-- Paginación keyset por id (UUIDv7 ≈ orden de creación) con page_limit; la capa
-- de servicio pide page_limit+1 para detectar la página siguiente.
--
-- La frontera de bounded context es de código Go, no de esquema: estas queries
-- leen world.* (el paquete Go internal/logistics NUNCA importa internal/world).
-- =============================================================================

-- ─── Nodos del grafo ──────────────────────────────────────────────────────────

-- ListNetworkNodes devuelve los nodos con filtros opcionales por región y clase,
-- y paginación keyset por id. location sale como GeoJSON plano (SRID 0).
-- terminal_id (v1.7.0) identifica la terminal intermodal del nodo, si la tiene:
-- es la única vía del contrato para descubrir terminales desde el grafo.
-- name: ListNetworkNodes :many
SELECT n.id, n.kind, n.region_id, n.building_id, n.city_id,
       ST_AsGeoJSON(n.location)::text AS location,
       t.id AS terminal_id
FROM world.network_nodes n
LEFT JOIN world.terminals t ON t.node_id = n.id
WHERE (sqlc.narg(region_id)::uuid IS NULL OR n.region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(kind)::world.node_kind IS NULL OR n.kind = sqlc.narg(kind)::world.node_kind)
  AND (sqlc.narg(after_id)::uuid IS NULL OR n.id > sqlc.narg(after_id)::uuid)
ORDER BY n.id
LIMIT sqlc.arg(page_limit);

-- NetworkNodeExists comprueba la existencia de un nodo del grafo (validación de
-- origen/destino del route-plan y de los legs de una ruta).
-- name: NetworkNodeExists :one
SELECT EXISTS (SELECT 1 FROM world.network_nodes WHERE id = sqlc.arg(id));

-- ─── Enlaces del grafo ────────────────────────────────────────────────────────

-- ListNetworkLinks devuelve los enlaces de uso común con filtros opcionales por
-- región (de ALGUNO de sus segmentos), modo y nodo de origen, y paginación
-- keyset por id. path sale como GeoJSON plano; los segmentos se resuelven aparte
-- (ListLinkSegmentsByLinks) por los ids de la página, para no duplicar filas de
-- enlace en el JOIN.
-- name: ListNetworkLinks :many
SELECT l.id, l.mode, l.from_node_id, l.to_node_id,
       ST_AsGeoJSON(l.path)::text AS path,
       l.length_m, l.capacity_per_hour, l.base_speed_kmh
FROM world.network_links l
WHERE (sqlc.narg(region_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM world.link_segments s
        WHERE s.link_id = l.id AND s.region_id = sqlc.narg(region_id)::uuid))
  AND (sqlc.narg(mode)::world.link_mode IS NULL OR l.mode = sqlc.narg(mode)::world.link_mode)
  AND (sqlc.narg(from_node_id)::uuid IS NULL OR l.from_node_id = sqlc.narg(from_node_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR l.id > sqlc.narg(after_id)::uuid)
ORDER BY l.id
LIMIT sqlc.arg(page_limit);

-- ListLinkSegmentsByLinks devuelve los segmentos (con su congestión EMA) de un
-- conjunto de enlaces, en orden estable por enlace y secuencia. congestion_ema
-- es NUMERIC → float8 (número del contrato, no dinero).
-- name: ListLinkSegmentsByLinks :many
SELECT id, link_id, region_id, seq, length_m,
       congestion_ema::float8 AS congestion_ema, updated_at_sim
FROM world.link_segments
WHERE link_id = ANY(sqlc.arg(link_ids)::uuid[])
ORDER BY link_id, seq;
