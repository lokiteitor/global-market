-- =============================================================================
-- Imperio Industrial — queries de rutas propietarias del Logistics Service.
--
-- Las rutas (world.routes + world.route_legs, GDD 7.2) son la definición de una
-- línea fija u orden bajo demanda como secuencia CONTIGUA de enlaces. Son el
-- ÚNICO estado que escribe el Logistics Service: PLANIFICACIÓN, no tránsito
-- (ADR-006). El motor de tránsito de internal/world consume estas rutas al
-- despachar vehículos; ambos contextos NO se importan (frontera de código Go).
--
-- La autorización es por propiedad (owner_account_id): un listado/detalle/patch/
-- delete sobre una ruta ajena es 403. El borrado de una ruta cae en cascada
-- sobre sus legs (ON DELETE CASCADE del esquema 0003).
-- =============================================================================

-- ─── Lectura ──────────────────────────────────────────────────────────────────

-- ListRoutes devuelve las rutas de un titular (SOLO propias) con filtros
-- opcionales por tipo y estado activo, y paginación keyset por id. Los legs se
-- resuelven aparte (ListRouteLegsByRoutes) por los ids de la página.
-- name: ListRoutes :many
SELECT id, owner_account_id, name, kind, active, created_at, updated_at
FROM world.routes
WHERE owner_account_id = sqlc.arg(owner_account_id)
  AND (sqlc.narg(kind)::world.route_kind IS NULL OR kind = sqlc.narg(kind)::world.route_kind)
  AND (sqlc.narg(active)::boolean IS NULL OR active = sqlc.narg(active)::boolean)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetRoute devuelve una ruta por id (la autorización por titular la aplica la
-- capa de servicio: 403 si es ajena).
-- name: GetRoute :one
SELECT id, owner_account_id, name, kind, active, created_at, updated_at
FROM world.routes
WHERE id = sqlc.arg(id);

-- GetRouteForUpdate bloquea la fila (SELECT FOR UPDATE) para patch/delete: la
-- validación de titular y el reemplazo de legs se deciden bajo lock.
-- name: GetRouteForUpdate :one
SELECT id, owner_account_id, name, kind, active, created_at, updated_at
FROM world.routes
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- ListRouteLegsByRoutes devuelve los legs (enlaces ordenados) de un conjunto de
-- rutas, en orden estable por ruta y posición.
-- name: ListRouteLegsByRoutes :many
SELECT route_id, leg_index, link_id
FROM world.route_legs
WHERE route_id = ANY(sqlc.arg(route_ids)::uuid[])
ORDER BY route_id, leg_index;

-- ─── Escritura ────────────────────────────────────────────────────────────────

-- InsertRoute crea la definición de ruta (activa por defecto). Los legs se
-- insertan aparte, en la MISMA transacción, tras validar su contigüidad.
-- name: InsertRoute :one
INSERT INTO world.routes (id, owner_account_id, name, kind)
VALUES (sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(name), sqlc.arg(kind)::world.route_kind)
RETURNING id, owner_account_id, name, kind, active, created_at, updated_at;

-- InsertRouteLeg añade un tramo (enlace) a una ruta en su posición ordenada.
-- name: InsertRouteLeg :exec
INSERT INTO world.route_legs (route_id, leg_index, link_id)
VALUES (sqlc.arg(route_id), sqlc.arg(leg_index), sqlc.arg(link_id));

-- UpdateRoute aplica un patch parcial de name/active (COALESCE conserva lo no
-- enviado) y toca updated_at. El reemplazo de legs, si lo hay, va aparte en la
-- misma transacción (DeleteRouteLegs + InsertRouteLeg).
-- name: UpdateRoute :one
UPDATE world.routes
   SET name   = COALESCE(sqlc.narg(name), name),
       active = COALESCE(sqlc.narg(active), active),
       updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING id, owner_account_id, name, kind, active, created_at, updated_at;

-- DeleteRouteLegs borra todos los legs de una ruta (paso previo al reemplazo de
-- la secuencia en un patch de legs).
-- name: DeleteRouteLegs :exec
DELETE FROM world.route_legs WHERE route_id = sqlc.arg(route_id);

-- DeleteRoute elimina la ruta; sus legs caen en cascada (ON DELETE CASCADE). Los
-- vehículos asignados los deja sin ruta el motor de tránsito de world al no
-- encontrarla — aquí solo se borra la definición.
-- name: DeleteRoute :exec
DELETE FROM world.routes WHERE id = sqlc.arg(id);
