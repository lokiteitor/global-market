-- =============================================================================
-- Imperio Industrial — queries sqlc del subpaquete world/enforcement (ADR-020).
-- Incremento 6a: CASCADA DE INSOLVENCIA (GDD 5.9, 11.2). Motor de mantenimiento,
-- canon, degradación y embargo. Es el lado de CONSECUENCIA FÍSICA del contexto
-- world: cobra mantenimiento/canon (cash → sink, "cobra SOLO lo disponible",
-- jamás deja la caja negativa), degrada y abandona edificios, y embarga (congela
-- el edificio + revierte el suelo) publicando su stock por evento.
--
-- La frontera de módulo es de código Go, no de esquema: estas queries leen y
-- escriben world.* (buildings, vehicles, land_concessions, production_batches,
-- network_nodes) y asientan en ledger.* como SINK (transacción maintenance/canon),
-- exactamente como land/production. Los helpers de ledger (GetCashAccount,
-- GetSinkAccount, InsertLedgerTransaction, InsertLedgerEntry) ya viven en
-- land.sql/production.sql: se REUTILIZAN desde el paquete sqlcgen compartido.
--
-- Cada barrido bloquea su entidad con FOR UPDATE SKIP LOCKED y decide en Go
-- (varias instancias del motor pueden correr en paralelo sin pisarse).
-- =============================================================================

-- ═════════════════════════════════════════════════════════════════════════════
-- (1) Mantenimiento de edificios (GDD 11.2: degradación por impago)
-- ═════════════════════════════════════════════════════════════════════════════

-- ListBuildingsDueMaintenance lista los edificios operativos/dañados con al menos
-- un día-sim de mantenimiento vencido (paid_before = simNow - SimDay). El importe
-- exacto y la degradación se deciden en Go tras bloquear cada edificio.
-- name: ListBuildingsDueMaintenance :many
SELECT id
FROM world.buildings
WHERE status IN ('operational', 'damaged')
  AND maintenance_paid_until_sim <= sqlc.arg(paid_before)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockBuildingForMaintenance bloquea un edificio vencido (FOR UPDATE OF b SKIP
-- LOCKED) con el coste de mantenimiento de su tipo. pgx.ErrNoRows si otra
-- instancia lo tomó o ya no aplica.
-- name: LockBuildingForMaintenance :one
SELECT b.id, b.owner_account_id, b.region_id, b.concession_id, b.status,
       b.condition_pct, b.maintenance_paid_until_sim, bt.maintenance_cost
FROM world.buildings b
JOIN world.building_types bt ON bt.id = b.building_type_id
WHERE b.id = sqlc.arg(id)
  AND b.status IN ('operational', 'damaged')
  AND b.maintenance_paid_until_sim <= sqlc.arg(paid_before)
FOR UPDATE OF b SKIP LOCKED;

-- UpdateBuildingMaintenance asienta el resultado del barrido: avanza el marcador
-- de liquidación, fija la condición (recuperación o degradación) y el estado
-- (operational/damaged/abandoned).
-- name: UpdateBuildingMaintenance :exec
UPDATE world.buildings
   SET maintenance_paid_until_sim = sqlc.arg(maintenance_paid_until_sim),
       condition_pct = sqlc.arg(condition_pct),
       status = sqlc.arg(status)::world.building_status,
       updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id);

-- ═════════════════════════════════════════════════════════════════════════════
-- (1b) Mantenimiento de flota (opex; sin condición: solo drena caja)
-- ═════════════════════════════════════════════════════════════════════════════

-- ListVehiclesDueMaintenance lista los vehículos con al menos un día-sim de opex
-- vencido (todos los estados: el opex se cobra se mueva o no el vehículo).
-- name: ListVehiclesDueMaintenance :many
SELECT id
FROM world.vehicles
WHERE maintenance_paid_until_sim <= sqlc.arg(paid_before)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockVehicleForMaintenance bloquea un vehículo vencido con su opex por día.
-- name: LockVehicleForMaintenance :one
SELECT v.id, v.owner_account_id, v.maintenance_paid_until_sim, vt.operating_cost_per_day
FROM world.vehicles v
JOIN world.vehicle_types vt ON vt.id = v.vehicle_type_id
WHERE v.id = sqlc.arg(id)
  AND v.maintenance_paid_until_sim <= sqlc.arg(paid_before)
FOR UPDATE OF v SKIP LOCKED;

-- SetVehicleMaintenancePaid avanza el marcador de liquidación del opex del
-- vehículo (los días que no pudo pagar se condonan: sin deuda, GDD 5.9).
-- name: SetVehicleMaintenancePaid :exec
UPDATE world.vehicles
   SET maintenance_paid_until_sim = sqlc.arg(maintenance_paid_until_sim),
       updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id);

-- ═════════════════════════════════════════════════════════════════════════════
-- (2) Canon de concesión (GDD 11.1/11.2: active → delinquent → grace → reverted)
-- ═════════════════════════════════════════════════════════════════════════════

-- ListConcessionsDueCanon lista las concesiones activas con el periodo vencido
-- (candidatas a renovación/impago). Reutiliza ix_concessions_expiry.
-- name: ListConcessionsDueCanon :many
SELECT id
FROM world.land_concessions
WHERE status = 'active' AND expires_at_sim < sqlc.arg(sim_now)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockConcessionForCanon bloquea una concesión activa vencida con lo necesario
-- para cobrar el canon y extender el periodo.
-- name: LockConcessionForCanon :one
SELECT id, region_id, holder_account_id, canon_amount, period_sim_days,
       expires_at_sim, status, grace_until_sim
FROM world.land_concessions
WHERE id = sqlc.arg(id) AND status = 'active' AND expires_at_sim < sqlc.arg(sim_now)
FOR UPDATE SKIP LOCKED;

-- ExtendConcession renueva: extiende el vencimiento un periodo, deja la concesión
-- 'active' y limpia el marcador de gracia (canon vigente ya cobrado al sink).
-- name: ExtendConcession :exec
UPDATE world.land_concessions
   SET expires_at_sim = expires_at_sim + sqlc.arg(extend_sim_seconds)::bigint,
       status = 'active',
       grace_until_sim = NULL,
       updated_at = now()
 WHERE id = sqlc.arg(id);

-- MarkConcessionDelinquent marca la concesión morosa (canon impagado) y fija el
-- vencimiento del periodo de gracia si no lo tenía ya (COALESCE preserva la
-- fecha de gracia original en reintentos).
-- name: MarkConcessionDelinquent :exec
UPDATE world.land_concessions
   SET status = 'delinquent',
       grace_until_sim = COALESCE(grace_until_sim, sqlc.arg(grace_until_sim)),
       updated_at = now()
 WHERE id = sqlc.arg(id);

-- ListDelinquentDueGrace lista las concesiones morosas cuyo periodo de gracia ya
-- venció (delinquent → grace).
-- name: ListDelinquentDueGrace :many
SELECT id
FROM world.land_concessions
WHERE status = 'delinquent'
  AND grace_until_sim IS NOT NULL
  AND grace_until_sim < sqlc.arg(sim_now)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- LockConcessionForGrace bloquea una concesión morosa con la gracia vencida.
-- name: LockConcessionForGrace :one
SELECT id
FROM world.land_concessions
WHERE id = sqlc.arg(id) AND status = 'delinquent'
  AND grace_until_sim IS NOT NULL
  AND grace_until_sim < sqlc.arg(sim_now)
FOR UPDATE SKIP LOCKED;

-- MarkConcessionGrace marca la concesión para embargo (grace); el barrido de
-- embargo la procesa a reverted.
-- name: MarkConcessionGrace :exec
UPDATE world.land_concessions
   SET status = 'grace', updated_at = now()
 WHERE id = sqlc.arg(id);

-- ═════════════════════════════════════════════════════════════════════════════
-- (3) Embargo / reclamo (GDD 11.2: congela el edificio + revierte el suelo)
-- ═════════════════════════════════════════════════════════════════════════════

-- ListConcessionsInGrace lista las concesiones marcadas para embargo (rama canon).
-- name: ListConcessionsInGrace :many
SELECT id
FROM world.land_concessions
WHERE status = 'grace'
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- ListConcessionsToEmbargoByAbandon lista las concesiones (aún no revertidas) con
-- algún edificio ABANDONADO cuyo periodo de gracia ya venció (rama mantenimiento;
-- grace_before = simNow - II_SEIZE_GRACE_SIM_SECONDS). El marcador
-- maintenance_paid_until_sim de un edificio abandonado es el instante del abandono.
-- name: ListConcessionsToEmbargoByAbandon :many
SELECT DISTINCT b.concession_id
FROM world.buildings b
JOIN world.land_concessions c ON c.id = b.concession_id
WHERE b.status = 'abandoned'
  AND b.maintenance_paid_until_sim <= sqlc.arg(grace_before)
  AND c.status <> 'reverted'
ORDER BY b.concession_id
LIMIT sqlc.arg(page_limit);

-- LockConcessionForEmbargo bloquea la concesión a embargar (idempotente: filtra
-- las ya revertidas). pgx.ErrNoRows si ya está reverted o la tomó otra instancia.
-- name: LockConcessionForEmbargo :one
SELECT id, region_id, holder_account_id, status
FROM world.land_concessions
WHERE id = sqlc.arg(id) AND status <> 'reverted'
FOR UPDATE SKIP LOCKED;

-- ListBuildingsOnConcessionForSeize bloquea (FOR UPDATE) los edificios aún no
-- embargados sobre una concesión (los que el embargo debe congelar in situ).
-- name: ListBuildingsOnConcessionForSeize :many
SELECT id, owner_account_id, region_id, status
FROM world.buildings
WHERE concession_id = sqlc.arg(concession_id) AND status <> 'seized'
ORDER BY id
FOR UPDATE;

-- GetBuildingNodeID devuelve el nodo logístico del edificio (origin_node_id de la
-- retirada in situ). pgx.ErrNoRows si el edificio no tiene nodo.
-- name: GetBuildingNodeID :one
SELECT id
FROM world.network_nodes
WHERE building_id = sqlc.arg(building_id)
ORDER BY id
LIMIT 1;

-- ListBuildingStockFree lista el stock LIBRE del edificio en el momento del
-- embargo (cuentas stock_free del ledger por almacén). Es el stock que la
-- liquidación del sistema publicará por CCRI (el embargo NO lo mueve aquí).
-- name: ListBuildingStockFree :many
SELECT product_id, warehouse_building_id, balance
FROM ledger.accounts
WHERE kind = 'stock_free'
  AND warehouse_building_id = sqlc.arg(warehouse_building_id)
  AND balance > 0
ORDER BY product_id;

-- SeizeBuilding congela el edificio (seized: incomandable, no produce). El
-- registro permanece; su reclamo físico completo es refinamiento Fase 2.
-- name: SeizeBuilding :exec
UPDATE world.buildings
   SET status = 'seized', updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id);

-- RevertConcession revierte el suelo al sistema (la parcela queda libre: el POST
-- concessions solo valida solape contra concesiones activas, así que otro jugador
-- puede volver a pedirla).
-- name: RevertConcession :exec
UPDATE world.land_concessions
   SET status = 'reverted', updated_at = now()
 WHERE id = sqlc.arg(id);

-- PauseRunningBatchesForBuilding para la producción de un edificio embargado/
-- abandonado: los lotes en curso pasan a paused_no_workers (reutiliza el estado
-- del enum: sin dueño operativo no hay trabajadores). Suelta el reloj del lote.
-- name: PauseRunningBatchesForBuilding :exec
UPDATE world.production_batches
   SET status = 'paused_no_workers',
       started_at_sim = NULL,
       updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE building_id = sqlc.arg(building_id) AND status = 'running';
