-- =============================================================================
-- Imperio Industrial — queries sqlc del subpaquete world/production (ADR-020).
-- Cola de producción (GDD 6), motor event-driven de progreso ANALÍTICO (GDD
-- 1.1) y reconciliación física↔contable (ADR-004). Cierra el lazo
-- construir→producir→vender: cada lote completado mueve a la vez el plano
-- FÍSICO (world.building_inventories, world.resource_deposits, buildings.
-- fuel_stock) y el CONTABLE (asientos production_output/consumption/wage sobre
-- ledger.*), en UNA transacción SERIALIZABLE con outbox.Emit (GDD 15.3).
--
-- La frontera de módulo es de código Go, no de esquema: estas queries leen
-- world.* y asientan en ledger.* reutilizando el soporte de ledger COMPARTIDO
-- de land.sql (GetCashAccount, GetSinkAccount, InsertLedgerTransaction,
-- InsertLedgerEntry) y ListRecipeIngredients de catalog.sql — el paquete
-- sqlcgen del contexto es único. Aquí solo se AÑADEN las queries propias de la
-- producción, con nombres que no colisionan con los ya existentes.
--
-- Geometrías: SRID 0 planar (ADR-019). La extracción usa el nodo del grafo del
-- edificio (world.network_nodes, centroide del footprint) como origen físico y
-- ST_DWithin para localizar el yacimiento dentro del radio de influencia.
-- =============================================================================

-- ═════════════════════════════════════════════════════════════════════════════
-- (1) Barrido de CONSTRUCCIÓN: under_construction → operational tras un tiempo
--     fijo de construcción (II_BUILD_SIM_SECONDS) desde la marca updated_at_sim.
-- ═════════════════════════════════════════════════════════════════════════════

-- ListDueConstructionIDs lista los edificios en construcción cuyo tiempo fijo de
-- construcción ya venció (updated_at_sim + build_sim_seconds <= simNow).
-- name: ListDueConstructionIDs :many
SELECT id
FROM world.buildings
WHERE status = 'under_construction'
  AND updated_at_sim + sqlc.arg(build_sim_seconds)::bigint <= sqlc.arg(sim_now)::bigint
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockDueConstruction bloquea un edificio en construcción vencido (FOR UPDATE
-- SKIP LOCKED): pgx.ErrNoRows si ya lo completó o lo tomó otra instancia.
-- name: LockDueConstruction :one
SELECT id, owner_account_id, region_id, concession_id, building_type_id
FROM world.buildings
WHERE id = sqlc.arg(id)
  AND status = 'under_construction'
  AND updated_at_sim + sqlc.arg(build_sim_seconds)::bigint <= sqlc.arg(sim_now)::bigint
FOR UPDATE SKIP LOCKED;

-- CompleteConstruction pasa el edificio a operational con la marca de sim-time
-- de la finalización.
-- name: CompleteConstruction :one
UPDATE world.buildings
   SET status = 'operational', updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING id, owner_account_id, region_id, building_type_id;

-- ═════════════════════════════════════════════════════════════════════════════
-- (2) Cola de producción: handlers GET/POST/DELETE del contrato.
-- ═════════════════════════════════════════════════════════════════════════════

-- ListProductionBatches devuelve los lotes de un edificio con filtro opcional
-- por estado y paginación keyset por id. Incluye batch_sim_seconds de la receta,
-- el nivel del edificio y la level_curve del tipo para DERIVAR analíticamente
-- progress_pct/eta_sim del lote en curso en la capa de servicio (no persisten).
-- name: ListProductionBatches :many
SELECT b.id, b.building_id, b.recipe_id, b.batches_queued, b.batches_done,
       b.status, b.queue_position, b.started_at_sim,
       r.batch_sim_seconds, bld.level, bt.level_curve
FROM world.production_batches b
JOIN world.recipes r ON r.id = b.recipe_id
JOIN world.buildings bld ON bld.id = b.building_id
JOIN world.building_types bt ON bt.id = bld.building_type_id
WHERE b.building_id = sqlc.arg(building_id)
  AND (sqlc.narg(status)::world.batch_status IS NULL OR b.status = sqlc.narg(status)::world.batch_status)
  AND (sqlc.narg(after_id)::uuid IS NULL OR b.id > sqlc.narg(after_id)::uuid)
ORDER BY b.id
LIMIT sqlc.arg(page_limit);

-- GetProductionBatchWithOwner devuelve un lote con el dueño de su edificio (para
-- la autorización por propiedad de DELETE sin buildingId en la ruta) y los datos
-- para derivar progreso. pgx.ErrNoRows si no existe.
-- name: GetProductionBatchWithOwner :one
SELECT b.id, b.building_id, b.recipe_id, b.batches_queued, b.batches_done,
       b.status, b.queue_position, b.started_at_sim,
       r.batch_sim_seconds, bld.level, bt.level_curve, bld.owner_account_id
FROM world.production_batches b
JOIN world.recipes r ON r.id = b.recipe_id
JOIN world.buildings bld ON bld.id = b.building_id
JOIN world.building_types bt ON bt.id = bld.building_type_id
WHERE b.id = sqlc.arg(id);

-- LockBatchForCancel bloquea el lote y devuelve el dueño de su edificio para
-- cancelar bajo lock (FOR UPDATE del lote). pgx.ErrNoRows si no existe.
-- name: LockBatchForCancel :one
SELECT b.id, b.building_id, b.batches_queued, b.batches_done, b.status,
       b.queue_position, b.started_at_sim, bld.owner_account_id
FROM world.production_batches b
JOIN world.buildings bld ON bld.id = b.building_id
WHERE b.id = sqlc.arg(id)
FOR UPDATE OF b;

-- NextQueuePosition devuelve la siguiente posición libre de la cola de un
-- edificio (al final): MAX(queue_position)+1 sobre los lotes no terminales, o 0.
-- name: NextQueuePosition :one
SELECT COALESCE(MAX(queue_position) + 1, 0)::int AS position
FROM world.production_batches
WHERE building_id = sqlc.arg(building_id)
  AND status IN ('queued', 'running', 'paused_no_fuel', 'paused_no_workers');

-- CountActiveBatches cuenta los lotes en curso o pausados de un edificio (para
-- decidir si el nuevo lote se promueve a running al encolar).
-- name: CountActiveBatches :one
SELECT count(*)::bigint AS total
FROM world.production_batches
WHERE building_id = sqlc.arg(building_id)
  AND status IN ('running', 'paused_no_fuel', 'paused_no_workers');

-- InsertProductionBatch crea un lote encolado (queued).
-- name: InsertProductionBatch :one
INSERT INTO world.production_batches (
    id, building_id, recipe_id, batches_queued, batches_done, status,
    queue_position, updated_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(building_id), sqlc.arg(recipe_id),
    sqlc.arg(batches_queued), 0, 'queued', sqlc.arg(queue_position),
    sqlc.arg(updated_at_sim))
RETURNING id, building_id, recipe_id, batches_queued, batches_done, status,
          queue_position, started_at_sim;

-- LockNextQueuedHead bloquea el lote queued a la cabeza de la cola de un
-- edificio (menor queue_position) para promoverlo a running; FOR UPDATE SKIP
-- LOCKED. pgx.ErrNoRows si no hay ninguno.
-- name: LockNextQueuedHead :one
SELECT id, building_id, recipe_id, batches_queued, batches_done, status,
       queue_position, started_at_sim
FROM world.production_batches
WHERE building_id = sqlc.arg(building_id)
  AND status = 'queued'
ORDER BY queue_position, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- SetBatchRunning promueve/reanuda un lote a running arrancando el reloj del
-- lote en curso (started_at_sim = simNow).
-- name: SetBatchRunning :one
UPDATE world.production_batches
   SET status = 'running', started_at_sim = sqlc.arg(sim_now), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING id, building_id, recipe_id, batches_queued, batches_done, status,
          queue_position, started_at_sim;

-- SetBatchCancelled cancela lo NO producido: el lote pasa a cancelled y suelta
-- el reloj; lo ya producido (batches_done) queda asentado.
-- name: SetBatchCancelled :one
UPDATE world.production_batches
   SET status = 'cancelled', started_at_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING id, building_id, recipe_id, batches_queued, batches_done, status,
          queue_position, started_at_sim;

-- ═════════════════════════════════════════════════════════════════════════════
-- (3) Motor de producción: barrido analítico de lotes vencidos.
-- ═════════════════════════════════════════════════════════════════════════════

-- ListActiveBatchIDs lista los lotes running o pausados de edificios operativos
-- (candidatos del barrido). El due-ness (progreso analítico con la eficiencia
-- del nivel) se decide en Go tras bloquear cada lote.
-- name: ListActiveBatchIDs :many
SELECT b.id
FROM world.production_batches b
JOIN world.buildings bld ON bld.id = b.building_id
WHERE b.status IN ('running', 'paused_no_fuel', 'paused_no_workers')
  AND bld.status = 'operational'
ORDER BY b.id
LIMIT sqlc.arg(page_limit);

-- LockBatchForProcessing bloquea un lote (FOR UPDATE OF b SKIP LOCKED) y devuelve
-- TODO lo que el motor necesita para completarlo: estado del lote, edificio
-- (dueño, región, nivel, combustible, estado) y su tipo (almacén base,
-- level_curve, code, placement_rules) y la receta (duración, combustible,
-- trabajadores). pgx.ErrNoRows si otra instancia lo tomó o ya no aplica.
-- name: LockBatchForProcessing :one
SELECT b.id, b.building_id, b.recipe_id, b.batches_queued, b.batches_done,
       b.status, b.queue_position, b.started_at_sim,
       bld.owner_account_id, bld.region_id, bld.level, bld.fuel_stock,
       bld.status AS building_status,
       bt.base_storage, bt.level_curve, bt.code AS building_type_code, bt.placement_rules,
       r.batch_sim_seconds, r.fuel_product_id, r.fuel_per_batch, r.workers_required
FROM world.production_batches b
JOIN world.buildings bld ON bld.id = b.building_id
JOIN world.building_types bt ON bt.id = bld.building_type_id
JOIN world.recipes r ON r.id = b.recipe_id
WHERE b.id = sqlc.arg(id)
FOR UPDATE OF b SKIP LOCKED;

-- AdvanceBatch cierra un batch del lote: incrementa batches_done y fija el
-- estado y el reloj del siguiente (running con started_at_sim, o completed con
-- NULL cuando batches_done alcanza batches_queued).
-- name: AdvanceBatch :one
UPDATE world.production_batches
   SET batches_done = batches_done + 1,
       status = sqlc.arg(status)::world.batch_status,
       started_at_sim = sqlc.narg(started_at_sim),
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING id, building_id, recipe_id, batches_queued, batches_done, status,
          queue_position, started_at_sim;

-- PauseBatch pausa un lote por combustible o salarios (GDD 5.8/5.9): suelta el
-- reloj (started_at_sim NULL) y no produce ni cobra.
-- name: PauseBatch :one
UPDATE world.production_batches
   SET status = sqlc.arg(status)::world.batch_status,
       started_at_sim = NULL,
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING id, building_id, recipe_id, batches_queued, batches_done, status,
          queue_position, started_at_sim;

-- GetProductionRecipe devuelve la receta con TODOS los campos de producción
-- (combustible y trabajadores), para validar el encolado y procesar el lote.
-- pgx.ErrNoRows si no existe.
-- name: GetProductionRecipe :one
SELECT id, building_type_id, code, batch_sim_seconds,
       fuel_product_id, fuel_per_batch, workers_required, min_city_level
FROM world.recipes
WHERE id = sqlc.arg(id);

-- ─── Yacimientos (extracción) ─────────────────────────────────────────────────

-- LockNearestDeposit bloquea el yacimiento del producto más cercano al nodo del
-- edificio dentro del radio de influencia, con existencias (FOR UPDATE OF d SKIP
-- LOCKED). pgx.ErrNoRows si no hay ninguno alcanzable con remaining > 0.
-- name: LockNearestDeposit :one
SELECT d.id, d.remaining_amount
FROM world.resource_deposits d
JOIN world.network_nodes n ON n.building_id = sqlc.arg(building_id)
WHERE d.product_id = sqlc.arg(product_id)
  AND d.remaining_amount > 0
  AND ST_DWithin(d.location, n.location, sqlc.arg(radius_m)::float8)
ORDER BY d.location <-> n.location
LIMIT 1
FOR UPDATE OF d SKIP LOCKED;

-- DecrementDeposit descuenta lo extraído del yacimiento (recurso finito, GDD
-- 10); el CHECK remaining_amount >= 0 respalda la no-negatividad.
-- name: DecrementDeposit :one
UPDATE world.resource_deposits
   SET remaining_amount = remaining_amount - sqlc.arg(amount),
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id)
RETURNING remaining_amount;

-- ─── Inventario físico del edificio ───────────────────────────────────────────

-- GetBuildingInventoryQty devuelve la cantidad física de un producto en un
-- edificio (0 si no hay fila).
-- name: GetBuildingInventoryQty :one
SELECT COALESCE(
    (SELECT quantity FROM world.building_inventories
      WHERE building_id = sqlc.arg(building_id) AND product_id = sqlc.arg(product_id)),
    0)::bigint AS quantity;

-- SumBuildingInventory devuelve el total físico almacenado en un edificio (todas
-- las líneas de producto), base de la comprobación de capacidad de almacén.
-- name: SumBuildingInventory :one
SELECT COALESCE(SUM(quantity), 0)::bigint AS total
FROM world.building_inventories
WHERE building_id = sqlc.arg(building_id);

-- AddBuildingInventory suma una cantidad (>= 0) al inventario físico de un
-- producto (alta por producción/extracción), creando la fila si no existía.
-- name: AddBuildingInventory :exec
INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
VALUES (sqlc.arg(building_id), sqlc.arg(product_id), sqlc.arg(amount), sqlc.arg(sim_now))
ON CONFLICT (building_id, product_id)
DO UPDATE SET quantity = world.building_inventories.quantity + sqlc.arg(amount),
              updated_at_sim = sqlc.arg(sim_now);

-- ConsumeBuildingInventory descuenta una cantidad del inventario físico de un
-- producto (consumo de insumos/combustible). La fila debe existir y cubrir la
-- cantidad (comprobado antes); el CHECK quantity >= 0 respalda la invariante.
-- No usa UPSERT: la contrapartida especulativa del INSERT evaluaría el CHECK
-- sobre un valor negativo antes de resolver el conflicto.
-- name: ConsumeBuildingInventory :exec
UPDATE world.building_inventories
   SET quantity = quantity - sqlc.arg(amount), updated_at_sim = sqlc.arg(sim_now)
 WHERE building_id = sqlc.arg(building_id) AND product_id = sqlc.arg(product_id);

-- SetBuildingFuelStock actualiza la columna espejo fuel_stock del edificio (GDD
-- 5.8): refleja el inventario físico del producto combustible tras consumirlo.
-- name: SetBuildingFuelStock :exec
UPDATE world.buildings
   SET fuel_stock = sqlc.arg(fuel_stock), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Salario (fórmula laboral, GDD 5.7) ───────────────────────────────────────

-- NearestCityBaseSalary devuelve el salario base de la ciudad más cercana al
-- nodo del edificio en su región. pgx.ErrNoRows si la región no tiene ciudades.
-- name: NearestCityBaseSalary :one
SELECT c.base_salary
FROM world.cities c
WHERE c.region_id = sqlc.arg(region_id)
ORDER BY c.location <-> (SELECT n.location FROM world.network_nodes n WHERE n.building_id = sqlc.arg(building_id))
LIMIT 1;

-- RegionSaturation devuelve el factor de saturación industrial regional más
-- reciente (analytics.region_stats.industrial_occupation) como float8.
-- pgx.ErrNoRows si aún no hay estadística de la región (default 1.0 en Go).
-- name: RegionSaturation :one
SELECT industrial_occupation::float8 AS saturation
FROM analytics.region_stats
WHERE region_id = sqlc.arg(region_id)
ORDER BY bucket_start_sim DESC
LIMIT 1;

-- ─── Soporte de ledger para stock (ADR-022) ───────────────────────────────────

-- GetStockFreeAccount devuelve la cuenta stock_free de (dueño, producto,
-- almacén); pgx.ErrNoRows si no existe.
-- name: GetStockFreeAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'stock_free'
  AND owner_account_id = sqlc.arg(owner_account_id)
  AND product_id = sqlc.arg(product_id)
  AND warehouse_building_id = sqlc.arg(warehouse_building_id);

-- CreateStockFreeAccount crea la cuenta stock_free de (dueño, producto, almacén)
-- on-demand (la unicidad parcial ux_accounts_stock_free respalda la clave).
-- name: CreateStockFreeAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
VALUES (sqlc.arg(id), 'stock_free', sqlc.arg(owner_account_id), sqlc.arg(product_id), sqlc.arg(warehouse_building_id))
RETURNING id, balance;

-- GetWorldSourceAccount devuelve la cuenta world_source (contrapartida física
-- del banco central, ADR-022) de un producto; pgx.ErrNoRows si no existe.
-- name: GetWorldSourceAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'world_source' AND product_id = sqlc.arg(product_id)
LIMIT 1;

-- GetWorldSourceOwner devuelve el titular (banco central) de cualquier cuenta
-- world_source existente, para crear las de productos nuevos con el mismo
-- titular; pgx.ErrNoRows si aún no hay ninguna.
-- name: GetWorldSourceOwner :one
SELECT owner_account_id FROM ledger.accounts
WHERE kind = 'world_source' AND owner_account_id IS NOT NULL
LIMIT 1;

-- CreateWorldSourceAccount crea la cuenta world_source de un producto (titular:
-- banco central; NULL admitido como cuenta pura de sistema si aún no hay banco).
-- name: CreateWorldSourceAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id)
VALUES (sqlc.arg(id), 'world_source', sqlc.narg(owner_account_id), sqlc.arg(product_id))
RETURNING id, balance;

-- ═════════════════════════════════════════════════════════════════════════════
-- (4) Reconciliación física↔contable (ADR-004): el inventario físico de cada
--     (edificio, producto) debe igualar el stock COMPROMETIBLE contable de ese
--     almacén — la suma de saldos stock_free + stock_reserved (ambos siguen
--     físicamente en el almacén; el stock reservado por una publicación/contrato
--     no se ha movido, solo cambió de custodia contable). Se EXCLUYE custody:
--     esos bienes están en tránsito (flete), ya no en el almacén. La producción
--     mueve ambos planos juntos, así que el resultado esperado es CERO filas.
-- ═════════════════════════════════════════════════════════════════════════════

-- ListStockDiscrepancies lista las divergencias físico↔contable en ambos
-- sentidos (FULL OUTER JOIN): inventario físico sin respaldo contable y
-- viceversa. El agregado contable compara el físico contra el comprometible
-- (stock_free + stock_reserved) por (almacén, producto).
-- name: ListStockDiscrepancies :many
SELECT COALESCE(bi.building_id, sf.warehouse_building_id) AS building_id,
       COALESCE(bi.product_id, sf.product_id) AS product_id,
       COALESCE(bi.quantity, 0)::bigint AS physical,
       COALESCE(sf.total, 0)::bigint AS ledger
FROM world.building_inventories bi
FULL OUTER JOIN (
    SELECT warehouse_building_id, product_id, SUM(balance) AS total
    FROM ledger.accounts
    WHERE kind IN ('stock_free', 'stock_reserved') AND warehouse_building_id IS NOT NULL
    GROUP BY warehouse_building_id, product_id
) sf ON sf.warehouse_building_id = bi.building_id AND sf.product_id = bi.product_id
WHERE COALESCE(bi.quantity, 0) <> COALESCE(sf.total, 0)
LIMIT sqlc.arg(page_limit);
