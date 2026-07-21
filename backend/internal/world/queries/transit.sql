-- =============================================================================
-- Imperio Industrial — queries sqlc del MOTOR DE TRÁNSITO (Incremento 3, Fase 1
-- terrestre). El shard espacial (internal/world/fleet.TransitWorker) simula el
-- movimiento físico: barrido de segmentos vencidos, combustible/desgaste, avería
-- probabilística, avance segmento/leg, llegada y entrega, y el job de congestión
-- (GDD 1.1/7.3). Event-driven: sólo los hitos escriben (coste proporcional a
-- eventos, no a entidades). Cada vehículo se procesa en SU transacción
-- SERIALIZABLE, bloqueado con FOR UPDATE SKIP LOCKED (varias instancias en
-- paralelo sin pisarse). El tiempo de viaje sale de world.segment_travel_seconds.
-- =============================================================================

-- ─── Barrido de segmentos vencidos ────────────────────────────────────────────

-- ListDueTransitVehicleIDs lista los vehículos in_transit cuyo segmento venció
-- (segment_entered_sim + tiempo_de_viaje <= simNow), en orden de entrada.
-- name: ListDueTransitVehicleIDs :many
SELECT id FROM world.vehicles
WHERE status = 'in_transit' AND on_segment_id IS NOT NULL AND advance_fn IS NOT NULL
  AND segment_entered_sim + world.segment_travel_seconds(advance_fn) <= sqlc.arg(sim_now)
ORDER BY segment_entered_sim
LIMIT sqlc.arg(page_limit);

-- LockTransitVehicle bloquea un vehículo in_transit (FOR UPDATE SKIP LOCKED) con
-- el segmento, su enlace y el tipo de vehículo: todo lo necesario para consumir
-- combustible, avanzar o llegar. pgx.ErrNoRows si otra instancia lo tomó.
-- name: LockTransitVehicle :one
SELECT v.id, v.vehicle_type_id, v.owner_account_id, v.status, v.wear_pct, v.fuel,
       v.route_id, v.route_leg_index, v.on_segment_id, v.segment_entered_sim, v.advance_fn,
       s.link_id, s.seq AS segment_seq, s.length_m AS segment_length_m,
       l.from_node_id, l.to_node_id, l.base_speed_kmh,
       vt.fuel_per_100km, vt.speed_kmh
FROM world.vehicles v
JOIN world.link_segments s ON s.id = v.on_segment_id
JOIN world.network_links l ON l.id = s.link_id
JOIN world.vehicle_types vt ON vt.id = v.vehicle_type_id
WHERE v.id = sqlc.arg(id) AND v.status = 'in_transit'
FOR UPDATE OF v SKIP LOCKED;

-- GetNextSegmentInLink devuelve el siguiente segmento del MISMO enlace (seq
-- mayor), para avanzar dentro de un leg multi-segmento. pgx.ErrNoRows si el
-- segmento actual era el último del enlace (en Fase 1, 1 segmento por enlace).
-- name: GetNextSegmentInLink :one
SELECT s.id AS segment_id, s.length_m, s.congestion_ema::float8 AS congestion_ema, l.base_speed_kmh
FROM world.link_segments s
JOIN world.network_links l ON l.id = s.link_id
WHERE s.link_id = sqlc.arg(link_id) AND s.seq > sqlc.arg(after_seq)
ORDER BY s.seq ASC
LIMIT 1;

-- GetNextLegFirstSegment devuelve el PRIMER segmento del leg en next_leg_index de
-- una ruta (avance al siguiente enlace), con su nodo destino. pgx.ErrNoRows si no
-- hay siguiente leg (el vehículo llegó al destino final).
-- name: GetNextLegFirstSegment :one
SELECT rl.leg_index, s.id AS segment_id, s.length_m,
       s.congestion_ema::float8 AS congestion_ema, l.base_speed_kmh, l.to_node_id
FROM world.route_legs rl
JOIN world.network_links l ON l.id = rl.link_id
JOIN world.link_segments s ON s.link_id = l.id
WHERE rl.route_id = sqlc.arg(route_id) AND rl.leg_index = sqlc.arg(next_leg_index)
ORDER BY s.seq ASC
LIMIT 1;

-- AdvanceVehicleToSegment mueve el vehículo al siguiente segmento (mismo leg o
-- siguiente), reiniciando el reloj del segmento y guardando combustible/desgaste
-- ya aplicados. Permanece in_transit.
-- name: AdvanceVehicleToSegment :exec
UPDATE world.vehicles
   SET on_segment_id = sqlc.arg(on_segment_id), route_leg_index = sqlc.arg(route_leg_index),
       segment_entered_sim = sqlc.arg(sim_now), advance_fn = sqlc.arg(advance_fn),
       fuel = sqlc.arg(fuel), wear_pct = sqlc.arg(wear_pct),
       status = 'in_transit', at_node_id = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ArriveVehicle asienta la llegada al nodo destino final: idle en el nodo, sin
-- segmento ni advance_fn, con el combustible/desgaste finales.
-- name: ArriveVehicle :exec
UPDATE world.vehicles
   SET status = 'idle', at_node_id = sqlc.arg(at_node_id), on_segment_id = NULL,
       segment_entered_sim = NULL, advance_fn = NULL, route_leg_index = NULL,
       fuel = sqlc.arg(fuel), wear_pct = sqlc.arg(wear_pct), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- BreakVehicle asienta una avería: broken sobre el MISMO segmento (la carga
-- espera a bordo, GDD 7.3), con repair_until_sim y el combustible/desgaste ya
-- aplicados. El barrido lo reanuda al vencer la reparación.
-- name: BreakVehicle :exec
UPDATE world.vehicles
   SET status = 'broken', repair_until_sim = sqlc.arg(repair_until_sim),
       fuel = sqlc.arg(fuel), wear_pct = sqlc.arg(wear_pct), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- StrandVehicle detiene el vehículo por falta de combustible en el nodo previo
-- (idle, sin segmento). Caso defensivo: el combustible se valida para toda la
-- ruta al despachar (GDD 7.3/CONTEXT). El combustible no cambia (era insuficiente).
-- name: StrandVehicle :exec
UPDATE world.vehicles
   SET status = 'idle', at_node_id = sqlc.arg(at_node_id), on_segment_id = NULL,
       segment_entered_sim = NULL, advance_fn = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Reanudación de averías y mantenimiento ───────────────────────────────────

-- ListDueRecoveryVehicleIDs lista los vehículos broken/in_maintenance cuyo tiempo
-- de reparación/servicio venció (repair_until_sim <= simNow).
-- name: ListDueRecoveryVehicleIDs :many
SELECT id, status FROM world.vehicles
WHERE status IN ('broken', 'in_maintenance')
  AND repair_until_sim IS NOT NULL AND repair_until_sim <= sqlc.arg(sim_now)
ORDER BY repair_until_sim
LIMIT sqlc.arg(page_limit);

-- LockRecoveryVehicle bloquea un vehículo broken/in_maintenance (FOR UPDATE SKIP
-- LOCKED) para reanudarlo. pgx.ErrNoRows si otra instancia lo tomó.
-- name: LockRecoveryVehicle :one
SELECT id, status, repair_until_sim FROM world.vehicles
WHERE id = sqlc.arg(id) AND status IN ('broken', 'in_maintenance')
FOR UPDATE SKIP LOCKED;

-- ResumeBrokenVehicle reanuda una avería: vuelve a in_transit y RE-ENTRA al mismo
-- segmento con el reloj arrancado en simNow (advance_fn/on_segment intactos).
-- name: ResumeBrokenVehicle :exec
UPDATE world.vehicles
   SET status = 'in_transit', segment_entered_sim = sqlc.arg(sim_now),
       repair_until_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- FinishMaintenanceVehicle devuelve un vehículo in_maintenance a idle tras el
-- servicio.
-- name: FinishMaintenanceVehicle :exec
UPDATE world.vehicles
   SET status = 'idle', repair_until_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Entrega al llegar al nodo destino ────────────────────────────────────────

-- ListVehicleShipmentsForNode devuelve los cargamentos a bordo de un vehículo con
-- destino ESE nodo (los demás siguen a bordo). FOR UPDATE dentro de la tx del
-- vehículo.
-- name: ListVehicleShipmentsForNode :many
SELECT id, owner_account_id, product_id, quantity, contract_id, freight_contract_id, destination_node_id, deadline_sim
FROM world.shipments
WHERE vehicle_id = sqlc.arg(vehicle_id) AND status = 'in_transit'
  AND destination_node_id = sqlc.arg(node_id)
FOR UPDATE;

-- DeliverShipment marca un cargamento entregado en el nodo destino: delivered,
-- reposa en el nodo, ya no viaja a bordo.
-- name: DeliverShipment :exec
UPDATE world.shipments
   SET status = 'delivered', at_node_id = sqlc.arg(at_node_id), vehicle_id = NULL,
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Transbordo en terminal intermodal (ruta multimodal por tramos) ───────────

-- ListVehicleShipmentsToTransship devuelve los cargamentos a bordo de un vehículo
-- cuyo destino NO es el nodo de llegada: candidatos a TRANSBORDO cuando el vehículo
-- termina su tramo en una terminal intermodal (el cargamento cambia de modo, GDD
-- 7.3). Complementa a ListVehicleShipmentsForNode (que entrega los de destino ese
-- nodo). FOR UPDATE dentro de la tx del vehículo.
-- name: ListVehicleShipmentsToTransship :many
SELECT id, owner_account_id, product_id, quantity, contract_id, freight_contract_id, destination_node_id, deadline_sim
FROM world.shipments
WHERE vehicle_id = sqlc.arg(vehicle_id) AND status = 'in_transit'
  AND (destination_node_id IS NULL OR destination_node_id <> sqlc.arg(node_id))
FOR UPDATE;

-- TransshipShipment ENCOLA un cargamento EN LA TERMINAL (at_terminal) a la espera de
-- ser servido por la cola de transbordo: reposa en el nodo de la terminal, ya no
-- viaja a bordo. updated_at_sim marca el momento de llegada a la terminal (clave
-- FIFO de la cola). transship_ready_at_sim se pone a NULL: el cargamento entra en la
-- cola SIN servir; el barrido de transbordo (sweepTransship) le asigna el instante
-- de fin de servicio según su prioridad (slot) y su posición, y el siguiente
-- despacho usa ese valor como puerta de tiempo.
-- name: TransshipShipment :exec
UPDATE world.shipments
   SET status = 'at_terminal', at_node_id = sqlc.arg(at_node_id), vehicle_id = NULL,
       transship_ready_at_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Cola de transbordo con prioridad de slots (GDD 7.3) ──────────────────────

-- ListTerminalsWithPendingTransship lista las terminales con cargamentos ENCOLADOS
-- (at_terminal, aún sin servir) esperando transbordo: candidatas del barrido de
-- servicio de cola (sweepTransship). Acotado por page_limit.
-- name: ListTerminalsWithPendingTransship :many
SELECT DISTINCT t.id, t.node_id, t.owner_account_id, t.transshipment_per_hour
FROM world.terminals t
JOIN world.shipments s ON s.at_node_id = t.node_id
WHERE s.status = 'at_terminal' AND s.transship_ready_at_sim IS NULL
LIMIT sqlc.arg(page_limit);

-- LockTerminalForServe bloquea una terminal (FOR UPDATE) para serializar el
-- servicio de su cola entre instancias del motor. pgx.ErrNoRows si no existe.
-- name: LockTerminalForServe :one
SELECT id, node_id, owner_account_id, transshipment_per_hour, queue_length
FROM world.terminals
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- TerminalServerBusyUntil devuelve hasta cuándo está OCUPADO el servidor de la
-- terminal: el mayor transship_ready_at_sim de los cargamentos ya servidos que aún
-- no han partido y cuyo servicio NO ha terminado en sim_now (ready > sim_now). 0 si
-- el servidor está libre. Base del encolado secuencial entre barridos.
-- name: TerminalServerBusyUntil :one
SELECT COALESCE(MAX(transship_ready_at_sim), 0)::bigint AS busy_until
FROM world.shipments
WHERE at_node_id = sqlc.arg(node_id) AND status = 'at_terminal'
  AND transship_ready_at_sim IS NOT NULL
  AND transship_ready_at_sim > sqlc.arg(sim_now)::bigint;

-- ListTerminalTransshipQueue lista la COLA de una terminal: cargamentos at_terminal
-- aún sin servir en su nodo, con el volumen del cargamento (quantity·unit_volume) y
-- el MEJOR (menor) priority_tier VIGENTE del dueño en esa terminal (centinela alto
-- si no posee slot). Ordena por prioridad (slot ascendente; sin slot al final) y,
-- dentro de cada clase, FIFO por llegada (updated_at_sim, id): el orden de servicio.
-- name: ListTerminalTransshipQueue :many
SELECT s.id, s.owner_account_id, s.quantity, s.updated_at_sim AS arrival_sim,
       (s.quantity * p.unit_volume)::bigint AS volume,
       COALESCE((
           SELECT MIN(ts.priority_tier) FROM world.terminal_slots ts
           WHERE ts.terminal_id = sqlc.arg(terminal_id)
             AND ts.holder_account_id = s.owner_account_id
             AND (ts.valid_until_sim IS NULL OR ts.valid_until_sim >= sqlc.arg(sim_now)::bigint)
       ), 2147483647)::int AS slot_tier
FROM world.shipments s
JOIN world.products p ON p.id = s.product_id
WHERE s.at_node_id = sqlc.arg(node_id) AND s.status = 'at_terminal'
  AND s.transship_ready_at_sim IS NULL
ORDER BY slot_tier ASC, s.updated_at_sim ASC, s.id ASC;

-- SetShipmentTransshipReady marca un cargamento como SERVIDO por la cola: fija el
-- instante de fin de transbordo (listo para el siguiente tramo). No toca
-- updated_at_sim (conserva la llegada).
-- name: SetShipmentTransshipReady :exec
UPDATE world.shipments
   SET transship_ready_at_sim = sqlc.arg(ready_at_sim)
 WHERE id = sqlc.arg(id);

-- RecountTerminalQueue recalcula la longitud de cola de una terminal: cargamentos
-- at_terminal en su nodo aún sin servir (encolados) o con servicio en curso
-- (transship_ready_at_sim > sim_now). Publica también updated_at_sim.
-- name: RecountTerminalQueue :exec
UPDATE world.terminals t
   SET queue_length = (
           SELECT count(*) FROM world.shipments s
           WHERE s.at_node_id = t.node_id AND s.status = 'at_terminal'
             AND (s.transship_ready_at_sim IS NULL
                  OR s.transship_ready_at_sim > sqlc.arg(sim_now)::bigint)
       ),
       updated_at_sim = sqlc.arg(sim_now)
 WHERE t.id = sqlc.arg(id);

-- ─── Congestión (job periódico) y métricas ────────────────────────────────────

-- RecomputeSegmentCongestion recalcula la EMA de TODOS los segmentos en una sola
-- sentencia: carga_normalizada = clamp(vehiculos_in_transit / capacity_ref, 1, 3)
-- y congestion_ema = 0.3*carga_normalizada + 0.7*congestion_ema (suelo 1.0). Con
-- 0 vehículos la EMA decae hacia el suelo fluido 1.0. Devuelve las filas para el
-- gauge ii_segment_congestion.
-- name: RecomputeSegmentCongestion :many
UPDATE world.link_segments s
   SET congestion_ema = GREATEST(1.0,
           0.3 * LEAST(3.0, GREATEST(1.0, cnt.n::float8 / NULLIF(sqlc.arg(capacity_ref)::float8, 0)))
           + 0.7 * s.congestion_ema),
       updated_at_sim = sqlc.arg(sim_now)
  FROM (
      SELECT ls.id,
             (SELECT count(*) FROM world.vehicles v
               WHERE v.on_segment_id = ls.id AND v.status = 'in_transit') AS n
      FROM world.link_segments ls
  ) cnt
 WHERE cnt.id = s.id
RETURNING s.id, s.congestion_ema::float8 AS congestion_ema;

-- CountInTransitVehicles cuenta los vehículos en tránsito (gauge
-- ii_vehicles_in_transit).
-- name: CountInTransitVehicles :one
SELECT count(*)::bigint FROM world.vehicles WHERE status = 'in_transit';
