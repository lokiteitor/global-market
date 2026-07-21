-- =============================================================================
-- Queries sqlc — tick del mercado spot eléctrico (GDD 5.8/18.1, ADR-025).
-- El tick es del Economy Balancer (PowerWorker, proceso engine): cada región
-- con líneas operativas casa oferta (centrales con power_offers) y demanda
-- (lotes activos de recetas eléctricas conectadas) por orden de mérito, con
-- precio de cierre uniforme, recorte rotatorio por prioridad inversa de puja
-- y pagos consumidores→generadores en UN asiento power_spot por tick.
--
-- Frontera de servicio de CÓDIGO Go, no de esquema (SAD §7): estas queries
-- leen/escriben world.* y ledger.* sin importar los paquetes world/contracts.
-- El soporte de ledger (GetCashAccount, GetStockFreeAccount, world_source,
-- InsertLedgerTransaction/Entry) vive en balancer.sql y se reutiliza.
-- =============================================================================

-- ─── Regiones con red ─────────────────────────────────────────────────────────

-- ListPowerRegions lista las regiones con al menos una línea operativa (las
-- únicas con pool eléctrico) y el sello del último tick liquidado (-1 si
-- ninguno) para decidir el bucket vencido en Go. La subconsulta escalar de
-- MAX(tick_sim) resuelve con un escaneo hacia atrás del PK (region_id,
-- tick_sim): coste O(1) por región aunque el histórico de ticks crezca.
-- name: ListPowerRegions :many
SELECT r.id AS region_id, r.name,
       COALESCE((SELECT MAX(t.tick_sim) FROM world.power_spot_ticks t
                 WHERE t.region_id = r.id), -1)::bigint AS last_tick_sim
FROM world.regions r
WHERE EXISTS (SELECT 1 FROM world.power_lines pl
              WHERE pl.region_id = r.id AND pl.status = 'operational')
ORDER BY r.id;

-- ExistsPowerSpotTick es la guarda de idempotencia del tick (PK region+bucket).
-- name: ExistsPowerSpotTick :one
SELECT EXISTS(
    SELECT 1 FROM world.power_spot_ticks
    WHERE region_id = sqlc.arg(region_id) AND tick_sim = sqlc.arg(tick_sim)
)::bool AS present;

-- ─── Participantes del tick ───────────────────────────────────────────────────

-- ListPowerConsumers lista la demanda candidata de una región: lotes activos
-- (running o paused_no_power) de recetas eléctricas en edificios operativos
-- CONECTADOS (a <= radio de una línea operativa de su región, medido desde el
-- centroide del footprint — el mismo ancla que su network_node). Trae la puja
-- explícita (0 = sin fila: rige el default) y la caja del dueño para la
-- exclusión por insolvencia (GDD 5.9: sin compra, sin deuda).
-- name: ListPowerConsumers :many
SELECT pb.id AS batch_id, pb.status AS batch_status,
       b.id AS building_id, b.owner_account_id, b.last_curtailed_at_sim,
       r.power_per_hour,
       COALESCE(bid.unit_price, 0) AS bid_price,
       COALESCE(cash.balance, 0) AS owner_cash
FROM world.production_batches pb
JOIN world.buildings b ON b.id = pb.building_id
JOIN world.recipes r ON r.id = pb.recipe_id
LEFT JOIN world.power_bids bid ON bid.building_id = b.id
LEFT JOIN ledger.accounts cash
       ON cash.kind = 'cash' AND cash.owner_account_id = b.owner_account_id
WHERE b.region_id = sqlc.arg(region_id)
  AND b.status = 'operational'
  AND pb.status IN ('running', 'paused_no_power')
  AND r.power_per_hour > 0
  AND EXISTS (SELECT 1 FROM world.power_lines pl
              WHERE pl.region_id = b.region_id AND pl.status = 'operational'
                AND ST_DWithin(pl.path, ST_Centroid(b.footprint), sqlc.arg(connect_radius_m)::float8))
ORDER BY b.id, pb.id;

-- ListPowerGenerators lista la oferta candidata: centrales operativas
-- conectadas CON oferta publicada. Para las térmicas trae el combustible en
-- los dos planos (físico e inventario contable stock_free): la capacidad
-- ofertable se limita en Go por min(físico, contable)/fuel_per_unit — sin
-- combustible no despachan (GDD 5.8).
-- name: ListPowerGenerators :many
SELECT b.id AS building_id, b.owner_account_id, b.level,
       bt.level_curve,
       ppt.capacity, ppt.fuel_product_id, ppt.fuel_per_unit,
       fp.code AS fuel_code,
       po.unit_price AS offer_price,
       COALESCE(bi.quantity, 0) AS fuel_physical,
       COALESCE(sf.balance, 0) AS fuel_ledger
FROM world.buildings b
JOIN world.power_plant_types ppt ON ppt.building_type_id = b.building_type_id
JOIN world.building_types bt ON bt.id = b.building_type_id
JOIN world.power_offers po ON po.building_id = b.id
LEFT JOIN world.products fp ON fp.id = ppt.fuel_product_id
LEFT JOIN world.building_inventories bi
       ON bi.building_id = b.id AND bi.product_id = ppt.fuel_product_id
LEFT JOIN ledger.accounts sf
       ON sf.kind = 'stock_free' AND sf.owner_account_id = b.owner_account_id
      AND sf.product_id = ppt.fuel_product_id AND sf.warehouse_building_id = b.id
WHERE b.region_id = sqlc.arg(region_id)
  AND b.status = 'operational'
  AND EXISTS (SELECT 1 FROM world.power_lines pl
              WHERE pl.region_id = b.region_id AND pl.status = 'operational'
                AND ST_DWithin(pl.path, ST_Centroid(b.footprint), sqlc.arg(connect_radius_m)::float8))
ORDER BY po.unit_price, b.id;

-- ─── Resultado del tick (plano físico) ────────────────────────────────────────

-- InsertPowerSpotTick registra el resultado agregado del tick. INSERT plano:
-- bajo SERIALIZABLE, dos instancias sobre el mismo bucket abortan una a la
-- otra (la guarda ExistsPowerSpotTick evita el caso común).
-- name: InsertPowerSpotTick :exec
INSERT INTO world.power_spot_ticks
       (region_id, tick_sim, interval_sim, closing_price,
        demand_units, supplied_units, curtailed_units, curtailed_buildings)
VALUES (sqlc.arg(region_id), sqlc.arg(tick_sim), sqlc.arg(interval_sim), sqlc.arg(closing_price),
        sqlc.arg(demand_units), sqlc.arg(supplied_units), sqlc.arg(curtailed_units), sqlc.arg(curtailed_buildings));

-- InsertPowerDispatch registra el despacho (generator) o consumo (consumer)
-- de un edificio en el tick, al precio de cierre.
-- name: InsertPowerDispatch :exec
INSERT INTO world.power_dispatches
       (region_id, tick_sim, building_id, owner_account_id, role, units, unit_price, amount)
VALUES (sqlc.arg(region_id), sqlc.arg(tick_sim), sqlc.arg(building_id), sqlc.arg(owner_account_id),
        sqlc.arg(role), sqlc.arg(units), sqlc.arg(unit_price), sqlc.arg(amount));

-- ─── Efectos sobre el mundo ───────────────────────────────────────────────────

-- SetBuildingPowered fija la cobertura de suministro de un edificio: al servir,
-- until = tick + intervalo × 1.5 (la gracia absorbe el desfase wall-clock) y
-- rate = power_per_hour FACTURADO; al NO servir (recorte/insolvencia), el tick
-- la CIERRA (until = tick, rate = 0) — sin ese cierre, la gracia residual del
-- tick anterior dejaría producir con energía no comprada.
-- name: SetBuildingPowered :exec
UPDATE world.buildings
   SET powered_until_sim = sqlc.arg(powered_until_sim),
       powered_rate = sqlc.arg(powered_rate),
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- MarkBuildingCurtailed sella la rotación del recorte (GDD 5.8): el recortado
-- más reciente será el último candidato del próximo recorte entre pujas
-- iguales.
-- name: MarkBuildingCurtailed :exec
UPDATE world.buildings
   SET last_curtailed_at_sim = sqlc.arg(sim_now), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- PauseRunningBatchesNoPower pausa los lotes en marcha de un edificio sin
-- suministro (recorte/insolvencia/desconexión): suelta el reloj como toda
-- pausa (el progreso del lote se pierde, GDD 5.9 — pausa, nunca deuda).
-- name: PauseRunningBatchesNoPower :many
UPDATE world.production_batches
   SET status = 'paused_no_power', started_at_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE building_id = sqlc.arg(building_id) AND status = 'running'
RETURNING id;

-- ResumeNoPowerBatches reanuda los lotes pausados por suministro de un
-- edificio servido, rearrancando el reloj del lote.
-- name: ResumeNoPowerBatches :many
UPDATE world.production_batches
   SET status = 'running', started_at_sim = sqlc.arg(sim_now), updated_at_sim = sqlc.arg(sim_now)
 WHERE building_id = sqlc.arg(building_id) AND status = 'paused_no_power'
RETURNING id;

-- RefreshPlantFuelMirror refresca la columna espejo fuel_stock de una térmica
-- tras quemar combustible (v1.3: espejo del inventario físico, visibilidad).
-- name: RefreshPlantFuelMirror :exec
UPDATE world.buildings
   SET fuel_stock = COALESCE((SELECT quantity FROM world.building_inventories
                              WHERE building_id = sqlc.arg(building_id)
                                AND product_id = sqlc.arg(product_id)), 0)
 WHERE id = sqlc.arg(building_id);
