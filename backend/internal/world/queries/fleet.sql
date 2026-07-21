-- =============================================================================
-- Imperio Industrial — queries sqlc de FLOTA y CARGAMENTOS (Incremento 3, Fase 1
-- terrestre). Superficie de los handlers world/* del contrato v1.3.0
-- (internal/world/fleet): catálogo de vehículos, flota propia con posición
-- ANALÍTICA derivada bajo demanda (GDD 1.1/7.3), compra, comando y despacho de
-- cargamentos. Las queries del MOTOR de tránsito (barridos, congestión, entrega)
-- viven en transit.sql; las reutilizadas (ledger/inventario) en land.sql y
-- production.sql (paquete sqlc compartido del contexto world).
--
-- Geometrías (location/portion): SRID 0 planar, metros de mundo (ADR-019). Se
-- proyectan con ST_AsGeoJSON(...)::text. congestion_ema es NUMERIC → float8.
-- Dinero/stock (money_amount/stock_qty) son enteros de 64 bits (nunca float).
-- =============================================================================

-- ─── Catálogo de tipos de vehículo ────────────────────────────────────────────

-- ListVehicleTypes devuelve el catálogo con filtro opcional por modo y keyset
-- por id (GET /world/vehicle-types). Lectura pública del mundo (sin propiedad).
-- name: ListVehicleTypes :many
SELECT id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id,
       fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day
FROM world.vehicle_types
WHERE (sqlc.narg(mode)::world.link_mode IS NULL OR mode = sqlc.narg(mode)::world.link_mode)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetVehicleType devuelve un tipo por id (compra: precio, autonomía, capacidad,
-- combustible y modo). pgx.ErrNoRows si no existe.
-- name: GetVehicleType :one
SELECT id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id,
       fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day
FROM world.vehicle_types
WHERE id = sqlc.arg(id);

-- ─── Flota propia con posición ANALÍTICA ──────────────────────────────────────

-- ListVehicles devuelve los vehículos de un titular (SOLO propios) con filtros
-- por estado/ruta, keyset por id, y la POSICIÓN derivada al observarla (GDD 1.1):
-- si on_segment, segment_progress_pct = avance en [0,100] y location interpolada
-- a lo largo de la porción del segmento (ST_LineInterpolatePoint); si at_node, la
-- location del nodo. El tiempo de viaje sale de la función canónica
-- world.segment_travel_seconds (misma fórmula que el motor).
-- name: ListVehicles :many
SELECT
    v.id, v.vehicle_type_id, v.owner_account_id, v.status, v.wear_pct, v.fuel,
    v.route_id, v.route_leg_index, v.at_node_id, v.on_segment_id,
    v.repair_until_sim, v.updated_at_sim,
    CASE WHEN v.on_segment_id IS NOT NULL THEN
        LEAST(100.0, GREATEST(0.0,
            (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
            / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8 * 100.0))
    END AS segment_progress_pct,
    CASE
        WHEN v.at_node_id IS NOT NULL THEN ST_AsGeoJSON(n.location)::text
        WHEN v.on_segment_id IS NOT NULL THEN ST_AsGeoJSON(ST_LineInterpolatePoint(
            s.portion,
            CASE WHEN COALESCE((v.advance_fn->>'dir')::int, 1) = -1
                 THEN 1.0 - LEAST(1.0, GREATEST(0.0,
                        (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
                        / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8))
                 ELSE        LEAST(1.0, GREATEST(0.0,
                        (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
                        / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8))
            END))::text
    END AS location
FROM world.vehicles v
LEFT JOIN world.network_nodes n ON n.id = v.at_node_id
LEFT JOIN world.link_segments s ON s.id = v.on_segment_id
WHERE v.owner_account_id = sqlc.arg(owner_account_id)
  AND (sqlc.narg(status)::world.vehicle_status IS NULL OR v.status = sqlc.narg(status)::world.vehicle_status)
  AND (sqlc.narg(route_id)::uuid IS NULL OR v.route_id = sqlc.narg(route_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR v.id > sqlc.narg(after_id)::uuid)
ORDER BY v.id
LIMIT sqlc.arg(page_limit);

-- GetVehicle devuelve un vehículo por id con su posición derivada (la propiedad
-- la aplica el servicio: 403 si es ajeno). pgx.ErrNoRows si no existe.
-- name: GetVehicle :one
SELECT
    v.id, v.vehicle_type_id, v.owner_account_id, v.status, v.wear_pct, v.fuel,
    v.route_id, v.route_leg_index, v.at_node_id, v.on_segment_id,
    v.repair_until_sim, v.updated_at_sim,
    CASE WHEN v.on_segment_id IS NOT NULL THEN
        LEAST(100.0, GREATEST(0.0,
            (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
            / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8 * 100.0))
    END AS segment_progress_pct,
    CASE
        WHEN v.at_node_id IS NOT NULL THEN ST_AsGeoJSON(n.location)::text
        WHEN v.on_segment_id IS NOT NULL THEN ST_AsGeoJSON(ST_LineInterpolatePoint(
            s.portion,
            CASE WHEN COALESCE((v.advance_fn->>'dir')::int, 1) = -1
                 THEN 1.0 - LEAST(1.0, GREATEST(0.0,
                        (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
                        / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8))
                 ELSE        LEAST(1.0, GREATEST(0.0,
                        (sqlc.arg(sim_now)::bigint - v.segment_entered_sim)::float8
                        / NULLIF(world.segment_travel_seconds(v.advance_fn), 0)::float8))
            END))::text
    END AS location
FROM world.vehicles v
LEFT JOIN world.network_nodes n ON n.id = v.at_node_id
LEFT JOIN world.link_segments s ON s.id = v.on_segment_id
WHERE v.id = sqlc.arg(id);

-- LockVehicle bloquea un vehículo (FOR UPDATE) para PATCH o despacho: decide bajo
-- lock la propiedad, el estado (idle/sealed) y la ubicación. pgx.ErrNoRows si no
-- existe.
-- name: LockVehicle :one
SELECT id, vehicle_type_id, owner_account_id, status, wear_pct, fuel,
       route_id, route_leg_index, at_node_id, on_segment_id, repair_until_sim, updated_at_sim
FROM world.vehicles
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- ─── Compra y comando ─────────────────────────────────────────────────────────

-- InsertVehicle da de alta un vehículo idle en el nodo de entrega con el tanque
-- lleno (equivalente a autonomy_km) y sin desgaste. La posición la deriva la
-- lectura posterior (GetVehicle).
-- name: InsertVehicle :one
INSERT INTO world.vehicles (id, vehicle_type_id, owner_account_id, status, wear_pct, fuel, at_node_id, updated_at_sim)
VALUES (sqlc.arg(id), sqlc.arg(vehicle_type_id), sqlc.arg(owner_account_id), 'idle', 0, sqlc.arg(fuel), sqlc.arg(at_node_id), sqlc.arg(sim_now))
RETURNING id;

-- SetVehicleRoute asigna (o retira, con NULL) la ruta de un vehículo.
-- name: SetVehicleRoute :exec
UPDATE world.vehicles
   SET route_id = sqlc.narg(route_id), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- SetVehicleMaintenance programa mantenimiento: reduce el desgaste a 0 y deja el
-- vehículo in_maintenance hasta repair_until_sim (el barrido lo devuelve a idle).
-- name: SetVehicleMaintenance :exec
UPDATE world.vehicles
   SET status = 'in_maintenance', wear_pct = 0,
       repair_until_sim = sqlc.arg(repair_until_sim), updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Rutas (validación del despacho; lectura, no escritura: eso es logistics) ──

-- GetRouteOwnerActive devuelve el titular y si está activa una ruta (validación
-- de propiedad del despacho). pgx.ErrNoRows si no existe.
-- name: GetRouteOwnerActive :one
SELECT owner_account_id, active FROM world.routes WHERE id = sqlc.arg(id);

-- GetRouteEndpoints devuelve el nodo de origen del primer leg, el nodo destino
-- del último leg, la distancia total (SUM de longitudes de enlace) y el número de
-- legs de una ruta. Sirve para validar que la ruta empieza en el nodo del
-- cargamento y termina en el destino del contrato, y para la comprobación de
-- combustible.
-- name: GetRouteEndpoints :one
SELECT
  (SELECT l.from_node_id FROM world.route_legs rl JOIN world.network_links l ON l.id = rl.link_id
     WHERE rl.route_id = sqlc.arg(route_id) ORDER BY rl.leg_index ASC LIMIT 1) AS first_from_node,
  (SELECT l.to_node_id FROM world.route_legs rl JOIN world.network_links l ON l.id = rl.link_id
     WHERE rl.route_id = sqlc.arg(route_id) ORDER BY rl.leg_index DESC LIMIT 1) AS last_to_node,
  (SELECT COALESCE(SUM(l.length_m), 0)::bigint FROM world.route_legs rl JOIN world.network_links l ON l.id = rl.link_id
     WHERE rl.route_id = sqlc.arg(route_id)) AS total_length_m,
  (SELECT COUNT(*)::bigint FROM world.route_legs rl WHERE rl.route_id = sqlc.arg(route_id)) AS leg_count;

-- CountRouteLegsWrongMode cuenta los tramos de una ruta cuyo enlace NO es del modo
-- dado. El despacho de la Fase 2 es POR TRAMO DE UN SOLO MODO (transbordo explícito
-- en terminal, GDD 7.3): un vehículo solo puede recorrer enlaces de SU modo, así
-- que una ruta con algún tramo de otro modo (0 = todos coinciden) no es despachable
-- por ese vehículo. Un tren no circula por road, ni un camión por rail/sea.
-- name: CountRouteLegsWrongMode :one
SELECT COUNT(*)::bigint AS wrong
FROM world.route_legs rl
JOIN world.network_links l ON l.id = rl.link_id
WHERE rl.route_id = sqlc.arg(route_id)
  AND l.mode <> sqlc.arg(mode)::world.link_mode;

-- GetTerminalByNode devuelve la terminal intermodal de un nodo (si existe): su id,
-- dueño y capacidad de transbordo por hora. La usan el despacho (puerta de tiempo
-- de transbordo de un cargamento at_terminal) y el motor de tránsito (decidir si
-- una llegada intermedia es un transbordo). pgx.ErrNoRows si el nodo no la tiene.
-- name: GetTerminalByNode :one
SELECT id, node_id, owner_account_id, transshipment_per_hour, queue_length
FROM world.terminals
WHERE node_id = sqlc.arg(node_id);

-- GetRouteFirstSegment devuelve el PRIMER segmento del PRIMER leg de una ruta
-- (menor leg_index, menor seq) con los parámetros para poblar advance_fn al
-- despachar (longitud, congestión EMA snapshot, velocidad base del enlace).
-- name: GetRouteFirstSegment :one
SELECT s.id AS segment_id, s.length_m,
       s.congestion_ema::float8 AS congestion_ema,
       l.base_speed_kmh, l.from_node_id, l.to_node_id
FROM world.route_legs rl
JOIN world.network_links l ON l.id = rl.link_id
JOIN world.link_segments s ON s.link_id = l.id
WHERE rl.route_id = sqlc.arg(route_id)
ORDER BY rl.leg_index ASC, s.seq ASC
LIMIT 1;

-- ─── Nodos y productos (validación de compra y despacho) ──────────────────────

-- GetNode devuelve id, región y edificio de un nodo (existencia + almacén del
-- destino/origen). building_id NULL si el nodo no es una instalación.
-- name: GetNode :one
SELECT id, region_id, building_id FROM world.network_nodes WHERE id = sqlc.arg(id);

-- NodeHasModeLink indica si un nodo está conectado a ALGÚN enlace de un modo
-- (accesibilidad del nodo de entrega para el modo del vehículo).
-- name: NodeHasModeLink :one
SELECT EXISTS (
    SELECT 1 FROM world.network_links
    WHERE mode = sqlc.arg(mode)::world.link_mode
      AND (from_node_id = sqlc.arg(node_id) OR to_node_id = sqlc.arg(node_id))
)::boolean AS accessible;

-- GetProductUnitVolume devuelve el volumen logístico por unidad de un producto
-- (comprobación de capacidad de carga del vehículo).
-- name: GetProductUnitVolume :one
SELECT unit_volume FROM world.products WHERE id = sqlc.arg(id);

-- ─── Cargamentos visibles ─────────────────────────────────────────────────────

-- ListShipments devuelve los cargamentos VISIBLES para una corporación: los
-- propios y —en un CCRI-Flete— los que le corresponden como TRANSPORTISTA (el
-- dueño es el CARGADOR, pero quien tiene que despacharlos y llevarlos es el
-- transportista, GDD 5.3.2; misma autorización que DispatchShipment). Lee
-- ledger.freight_contracts cross-schema (sin importar internal/contracts, SAD 7),
-- como GetFreightCarrier. Filtros por estado/contrato/flete/vehículo y keyset por
-- id.
-- name: ListShipments :many
SELECT sh.id, sh.owner_account_id, sh.product_id, sh.quantity, sh.contract_id,
       sh.freight_contract_id, sh.vehicle_id, sh.at_node_id, sh.status, sh.updated_at_sim
FROM world.shipments sh
LEFT JOIN ledger.freight_contracts fc ON fc.id = sh.freight_contract_id
WHERE (sh.owner_account_id = sqlc.arg(account_id) OR fc.carrier_account_id = sqlc.arg(account_id))
  AND (sqlc.narg(status)::world.shipment_status IS NULL OR sh.status = sqlc.narg(status)::world.shipment_status)
  AND (sqlc.narg(contract_id)::uuid IS NULL OR sh.contract_id = sqlc.narg(contract_id)::uuid)
  AND (sqlc.narg(freight_contract_id)::uuid IS NULL OR sh.freight_contract_id = sqlc.narg(freight_contract_id)::uuid)
  AND (sqlc.narg(vehicle_id)::uuid IS NULL OR sh.vehicle_id = sqlc.narg(vehicle_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR sh.id > sqlc.narg(after_id)::uuid)
ORDER BY sh.id
LIMIT sqlc.arg(page_limit);

-- GetShipment devuelve un cargamento por id (la propiedad la aplica el servicio).
-- name: GetShipment :one
SELECT id, owner_account_id, product_id, quantity, contract_id, freight_contract_id,
       vehicle_id, at_node_id, status, updated_at_sim
FROM world.shipments
WHERE id = sqlc.arg(id);

-- LockShipmentForDispatch bloquea un cargamento (FOR UPDATE) para despacharlo:
-- añade el destino del contrato y el plazo (columnas de 0009). Sirve tanto al
-- despacho del primer tramo (in_warehouse) como al de un tramo posterior de una
-- ruta multimodal (at_terminal tras un transbordo). pgx.ErrNoRows si no existe.
-- name: LockShipmentForDispatch :one
SELECT id, owner_account_id, product_id, quantity, contract_id, freight_contract_id,
       vehicle_id, at_node_id, destination_node_id, deadline_sim, status, updated_at_sim,
       transship_ready_at_sim
FROM world.shipments
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- DispatchShipment pone un cargamento a bordo del vehículo y en tránsito (deja el
-- almacén: at_node_id = NULL). Limpia transship_ready_at_sim: si el cargamento
-- venía at_terminal (tramo posterior de una ruta multimodal), abandona la cola de
-- esa terminal; un transbordo futuro se sirve de cero.
-- name: DispatchShipment :exec
UPDATE world.shipments
   SET vehicle_id = sqlc.arg(vehicle_id), at_node_id = NULL,
       status = 'in_transit', transship_ready_at_sim = NULL, updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- DispatchVehicle arranca un vehículo idle: lo pone in_transit sobre el primer
-- segmento del primer leg de la ruta, con advance_fn poblado y el reloj del
-- segmento arrancado.
-- name: DispatchVehicle :exec
UPDATE world.vehicles
   SET status = 'in_transit', route_id = sqlc.arg(route_id), route_leg_index = 0,
       on_segment_id = sqlc.arg(on_segment_id), at_node_id = NULL,
       segment_entered_sim = sqlc.arg(sim_now), advance_fn = sqlc.arg(advance_fn),
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ─── Consumidor shipment_creator (contract.confirmed → cargamento) ────────────

-- InsertShipmentInWarehouse crea el cargamento in_warehouse en el nodo de origen
-- del contrato, etiquetado por contract_id y con el destino/plazo del contrato
-- persistidos localmente (0009) para el despacho y la confirmación de entrega.
-- name: InsertShipmentInWarehouse :one
INSERT INTO world.shipments
    (id, owner_account_id, product_id, quantity, contract_id, at_node_id, destination_node_id, deadline_sim, status, updated_at_sim)
VALUES
    (sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(product_id), sqlc.arg(quantity),
     sqlc.arg(contract_id), sqlc.arg(at_node_id), sqlc.arg(destination_node_id), sqlc.arg(deadline_sim),
     'in_warehouse', sqlc.arg(sim_now))
RETURNING id;

-- ShipmentExistsForContract indica si ya hay un cargamento para un contrato
-- (guarda de idempotencia del consumidor ante reprocesos, defensiva).
-- name: ShipmentExistsForContract :one
SELECT EXISTS (SELECT 1 FROM world.shipments WHERE contract_id = sqlc.arg(contract_id))::boolean AS present;

-- ─── Consumidor freight_shipment_creator (freight.confirmed → cargamento) ─────

-- InsertFreightShipmentInWarehouse crea el cargamento del CARGADOR (owner=shipper)
-- in_warehouse en el nodo de origen del flete, etiquetado por freight_contract_id
-- (contract_id NULL: flete puro). El transportista lo despacha en su vehículo; la
-- mercancía ya está en custodia contable (la asentó el Contract Service al confirmar).
-- name: InsertFreightShipmentInWarehouse :one
INSERT INTO world.shipments
    (id, owner_account_id, product_id, quantity, freight_contract_id, at_node_id, destination_node_id, deadline_sim, status, updated_at_sim)
VALUES
    (sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(product_id), sqlc.arg(quantity),
     sqlc.arg(freight_contract_id), sqlc.arg(at_node_id), sqlc.arg(destination_node_id), sqlc.arg(deadline_sim),
     'in_warehouse', sqlc.arg(sim_now))
RETURNING id;

-- ShipmentExistsForFreightContract indica si ya hay un cargamento para un flete
-- (idempotencia del freight_shipment_creator ante reprocesos).
-- name: ShipmentExistsForFreightContract :one
SELECT EXISTS (SELECT 1 FROM world.shipments WHERE freight_contract_id = sqlc.arg(freight_contract_id))::boolean AS present;

-- GetFreightCarrier devuelve el transportista de un flete: el despacho de un
-- cargamento de flete lo autoriza el TRANSPORTISTA (no el dueño=cargador). world
-- lee ledger.freight_contracts (cross-schema) sin importar internal/contracts.
-- name: GetFreightCarrier :one
SELECT carrier_account_id FROM ledger.freight_contracts WHERE id = sqlc.arg(id);

-- ═════════════════════════════════════════════════════════════════════════════
-- Terminales y slots de prioridad (GDD 7.3, Incremento 8). Las terminales tienen
-- dueño y venden slots de prioridad de atraque/transbordo (priority_tier menor =
-- más prioritario). El transbordo del motor de tránsito sirve ANTES a los
-- cargamentos de un dueño/transportista con slot vigente.
-- ═════════════════════════════════════════════════════════════════════════════

-- GetTerminal devuelve una terminal por id; pgx.ErrNoRows si no existe.
-- name: GetTerminal :one
SELECT id, node_id, owner_account_id, transshipment_per_hour, queue_length, updated_at_sim
FROM world.terminals WHERE id = sqlc.arg(id);

-- ListTerminalSlots lista los slots de una terminal; only_available filtra los
-- que están en venta (sin titular vigente en sim_now).
-- name: ListTerminalSlots :many
SELECT id, terminal_id, priority_tier, price, holder_account_id, valid_until_sim
FROM world.terminal_slots
WHERE terminal_id = sqlc.arg(terminal_id)
  AND (NOT sqlc.arg(only_available)::boolean
       OR holder_account_id IS NULL
       OR (valid_until_sim IS NOT NULL AND valid_until_sim < sqlc.arg(sim_now)::bigint))
ORDER BY priority_tier, id;

-- GetSlotForPurchase bloquea un slot (FOR UPDATE) con el dueño de su terminal:
-- la compra valida vigencia y asienta el pago bajo el lock. pgx.ErrNoRows si no existe.
-- name: GetSlotForPurchase :one
SELECT s.id, s.terminal_id, s.priority_tier, s.price, s.holder_account_id, s.valid_until_sim,
       t.owner_account_id AS terminal_owner_account_id
FROM world.terminal_slots s
JOIN world.terminals t ON t.id = s.terminal_id
WHERE s.id = sqlc.arg(id)
FOR UPDATE OF s;

-- SetSlotHolder asigna el titular y la vigencia de un slot (compra).
-- name: SetSlotHolder :one
UPDATE world.terminal_slots
   SET holder_account_id = sqlc.arg(holder_account_id), valid_until_sim = sqlc.arg(valid_until_sim)
 WHERE id = sqlc.arg(id)
RETURNING id, terminal_id, priority_tier, price, holder_account_id, valid_until_sim;

-- La prioridad de un dueño en una terminal (mejor priority_tier vigente) la resuelve
-- ListTerminalTransshipQueue (transit.sql) por subconsulta, al servir la cola.

-- CreateCashAccount crea la caja de una corporación (on-demand): el dueño de una
-- terminal —p. ej. el banco central— puede no tener caja aún al cobrar un slot.
-- name: CreateCashAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id)
VALUES (sqlc.arg(id), 'cash', sqlc.arg(owner_account_id))
RETURNING id, balance;
