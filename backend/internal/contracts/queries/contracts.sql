-- =============================================================================
-- Imperio Industrial — queries sqlc del módulo contracts (ADR-020).
-- Ciclo de publicación y aceptación del CCRI (GDD 5.3/5.3.1). Las invariantes
-- de dinero/stock las garantizan los triggers de 0004_ledger (saldo por
-- cuenta, doble entrada diferida, no-negatividad, inmutabilidad): aquí nunca
-- se recalculan saldos. Las ventanas wall-clock (sorteo, micro-ventana,
-- cooldown) se calculan SIEMPRE con now() de la BD, nunca con el reloj de la
-- aplicación.
--
-- La frontera de módulo es de código Go, no de esquema: estas queries asientan
-- en ledger.* (publicaciones, aceptaciones, cuentas espejo y partidas de los
-- bloqueos) y leen world.* (validación de productos y nodos).
-- =============================================================================

-- ─── Publicaciones ───────────────────────────────────────────────────────────

-- InsertPublication crea la publicación con su ventana de sorteo y su cooldown
-- anti-parpadeo calculados con now() de la BD. Las cuentas espejo de la
-- garantía propia (creadas antes, en la misma transacción) llegan por id; los
-- CHECK de 0004 exigen las que corresponden al kind.
-- name: InsertPublication :one
INSERT INTO ledger.publications (
    id, kind, publisher_account_id, channel, counterparty_account_id,
    product_id, quantity_total, quantity_remaining, unit_price, min_lot,
    origin_node_id, destination_node_id, delivery_sim_seconds, status,
    window_closes_at, cancel_cooldown_until,
    stock_reserve_account_id, guarantee_account_id, escrow_account_id,
    published_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(kind), sqlc.arg(publisher_account_id),
    sqlc.arg(channel), sqlc.narg(counterparty_account_id),
    sqlc.narg(product_id), sqlc.arg(quantity_total), sqlc.arg(quantity_total),
    sqlc.arg(unit_price), sqlc.arg(min_lot),
    sqlc.narg(origin_node_id), sqlc.narg(destination_node_id),
    sqlc.arg(delivery_sim_seconds), 'draw_window',
    now() + sqlc.arg(draw_window_seconds)::bigint * interval '1 second',
    now() + sqlc.arg(cancel_cooldown_seconds)::bigint * interval '1 second',
    sqlc.narg(stock_reserve_account_id), sqlc.narg(guarantee_account_id),
    sqlc.narg(escrow_account_id), sqlc.arg(published_at_sim))
RETURNING *;

-- GetPublication devuelve una publicación por id (la autorización de canal
-- privado la aplica la capa de servicio).
-- name: GetPublication :one
SELECT * FROM ledger.publications WHERE id = sqlc.arg(id);

-- GetPublicationForUpdate bloquea la fila de la publicación para las
-- operaciones que la mutan (cancelación, aceptación): las validaciones de
-- estado/cantidad y la transición de ventana se deciden bajo el lock.
-- name: GetPublicationForUpdate :one
SELECT * FROM ledger.publications WHERE id = sqlc.arg(id) FOR UPDATE;

-- SetPublicationCancelled marca la publicación como cancelada (la liberación
-- contable de garantías se asienta aparte, en la misma transacción).
-- name: SetPublicationCancelled :one
UPDATE ledger.publications
   SET status = 'cancelled', updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- SetPublicationMicroWindow abre la micro-ventana sobre una publicación
-- madura (open): primera aceptación posterior a su ventana inicial (ADR-011).
-- name: SetPublicationMicroWindow :one
UPDATE ledger.publications
   SET status = 'micro_window',
       window_closes_at = now() + sqlc.arg(micro_window_seconds)::bigint * interval '1 second',
       updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- ListBoardPublications consulta el tablón global (solo canal board y estados
-- visibles) con los filtros del contrato, orden seleccionable y paginación
-- keyset por (clave de orden, id). El discriminador sort activa una única
-- rama de ORDER BY/predicado keyset por consulta; las ramas inactivas evalúan
-- NULL de forma uniforme y no alteran el orden. after_key/after_id es la
-- última fila de la página anterior (NULL en la primera).
-- name: ListBoardPublications :many
SELECT p.*
FROM ledger.publications p
WHERE p.channel = 'board'
  AND p.status IN ('draw_window', 'open', 'micro_window')
  AND (sqlc.narg(kind)::ledger.publication_kind IS NULL OR p.kind = sqlc.narg(kind)::ledger.publication_kind)
  AND (sqlc.narg(product_id)::uuid IS NULL OR p.product_id = sqlc.narg(product_id)::uuid)
  AND (sqlc.narg(origin_region_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM world.network_nodes n
        WHERE n.id = p.origin_node_id AND n.region_id = sqlc.narg(origin_region_id)::uuid))
  AND (sqlc.narg(destination_region_id)::uuid IS NULL OR EXISTS (
        SELECT 1 FROM world.network_nodes n
        WHERE n.id = p.destination_node_id AND n.region_id = sqlc.narg(destination_region_id)::uuid))
  AND (sqlc.narg(min_unit_price)::bigint IS NULL OR p.unit_price >= sqlc.narg(min_unit_price)::bigint)
  AND (sqlc.narg(max_unit_price)::bigint IS NULL OR p.unit_price <= sqlc.narg(max_unit_price)::bigint)
  AND (sqlc.narg(min_quantity_remaining)::bigint IS NULL OR p.quantity_remaining >= sqlc.narg(min_quantity_remaining)::bigint)
  AND (sqlc.narg(max_delivery_sim_seconds)::bigint IS NULL OR p.delivery_sim_seconds <= sqlc.narg(max_delivery_sim_seconds)::bigint)
  AND (sqlc.narg(after_id)::uuid IS NULL OR CASE sqlc.arg(sort)::text
        WHEN 'unit_price_desc'   THEN (p.unit_price, p.id)           < (sqlc.narg(after_key)::bigint, sqlc.narg(after_id)::uuid)
        WHEN 'published_at_desc' THEN (p.published_at_sim, p.id)     < (sqlc.narg(after_key)::bigint, sqlc.narg(after_id)::uuid)
        WHEN 'deadline_asc'      THEN (p.delivery_sim_seconds, p.id) > (sqlc.narg(after_key)::bigint, sqlc.narg(after_id)::uuid)
        ELSE                          (p.unit_price, p.id)           > (sqlc.narg(after_key)::bigint, sqlc.narg(after_id)::uuid)
      END)
ORDER BY
  CASE WHEN sqlc.arg(sort)::text = 'unit_price_desc'   THEN p.unit_price END DESC,
  CASE WHEN sqlc.arg(sort)::text = 'published_at_desc' THEN p.published_at_sim END DESC,
  CASE WHEN sqlc.arg(sort)::text = 'deadline_asc'      THEN p.delivery_sim_seconds END,
  CASE WHEN sqlc.arg(sort)::text NOT IN ('unit_price_desc', 'published_at_desc', 'deadline_asc') THEN p.unit_price END,
  CASE WHEN sqlc.arg(sort)::text IN ('unit_price_desc', 'published_at_desc') THEN p.id END DESC,
  p.id
LIMIT sqlc.arg(page_limit);

-- CountBoardPublications cuenta las publicaciones visibles del tablón
-- (gauge ii_board_open_publications).
-- name: CountBoardPublications :one
SELECT count(*) FROM ledger.publications
WHERE channel = 'board' AND status IN ('draw_window', 'open', 'micro_window');

-- ─── Aceptaciones ────────────────────────────────────────────────────────────

-- InsertAcceptance registra una aceptación pending_draw con sus cuentas
-- espejo (reference_id = id de la aceptación), ya fondeadas en la misma
-- transacción por el asiento acceptance_lock.
-- name: InsertAcceptance :one
INSERT INTO ledger.publication_acceptances (
    id, publication_id, acceptor_account_id, quantity,
    stock_reserve_account_id, guarantee_account_id, escrow_account_id)
VALUES (
    sqlc.arg(id), sqlc.arg(publication_id), sqlc.arg(acceptor_account_id),
    sqlc.arg(quantity), sqlc.narg(stock_reserve_account_id),
    sqlc.narg(guarantee_account_id), sqlc.narg(escrow_account_id))
RETURNING *;

-- GetAcceptance devuelve una aceptación por id (autorización por propiedad en
-- la capa de servicio: solo el aceptante).
-- name: GetAcceptance :one
SELECT * FROM ledger.publication_acceptances WHERE id = sqlc.arg(id);

-- ListPendingAcceptancesForUpdate bloquea y devuelve las aceptaciones
-- pendientes de sorteo de una publicación, en orden estable de llegada (la
-- cancelación libera sus garantías y les asigna draw_order por ese orden).
-- name: ListPendingAcceptancesForUpdate :many
SELECT * FROM ledger.publication_acceptances
WHERE publication_id = sqlc.arg(publication_id) AND status = 'pending_draw'
ORDER BY accepted_at, id
FOR UPDATE;

-- ReleaseAcceptance resuelve una aceptación como no servida. draw_order es
-- obligatorio al salir de pending_draw (CHECK de 0004); en una liberación por
-- cancelación se asigna por orden de llegada.
-- name: ReleaseAcceptance :one
UPDATE ledger.publication_acceptances
   SET status = 'released', resolved_at = now(), draw_order = sqlc.arg(draw_order)
 WHERE id = sqlc.arg(id)
RETURNING *;

-- ─── Soporte de ledger (cuentas espejo y asientos de bloqueo/liberación) ─────

-- CreateLedgerAccount crea una cuenta del ledger (espejo de publicación o
-- aceptación vía reference_id). El id (UUIDv7) lo genera la aplicación
-- (ADR-018); la forma la validan los constraints de 0004/0008.
-- name: CreateLedgerAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id, reference_id)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.narg(owner_account_id),
        sqlc.narg(product_id), sqlc.narg(warehouse_building_id), sqlc.narg(reference_id))
RETURNING *;

-- GetLedgerAccount lee una cuenta del ledger (saldos de cuentas espejo dentro
-- de la transacción que los libera).
-- name: GetLedgerAccount :one
SELECT * FROM ledger.accounts WHERE id = sqlc.arg(id);

-- GetCashAccount devuelve la única caja de una corporación (unicidad parcial
-- ux_accounts_cash).
-- name: GetCashAccount :one
SELECT * FROM ledger.accounts
WHERE kind = 'cash' AND owner_account_id = sqlc.arg(owner_account_id);

-- GetStockFreeAccount devuelve la cuenta de stock libre de (dueño, producto,
-- almacén) — unicidad parcial ux_accounts_stock_free.
-- name: GetStockFreeAccount :one
SELECT * FROM ledger.accounts
WHERE kind = 'stock_free'
  AND owner_account_id = sqlc.arg(owner_account_id)
  AND product_id = sqlc.arg(product_id)
  AND warehouse_building_id = sqlc.arg(warehouse_building_id);

-- InsertLedgerTransaction inserta la cabecera de un asiento (inmutable).
-- name: InsertLedgerTransaction :exec
INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(sim_time_at),
        sqlc.narg(reference_id), sqlc.narg(description));

-- InsertLedgerEntry inserta una partida de doble entrada; los triggers aplican
-- saldo, no-negatividad y (diferido) el balance por activo del asiento.
-- name: InsertLedgerEntry :exec
INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
VALUES (sqlc.arg(id), sqlc.arg(transaction_id), sqlc.arg(account_id), sqlc.arg(amount));

-- ─── Validaciones de mundo ───────────────────────────────────────────────────

-- ProductExists comprueba la existencia de un producto del catálogo.
-- name: ProductExists :one
SELECT EXISTS (SELECT 1 FROM world.products WHERE id = sqlc.arg(id))::boolean AS present;

-- GetNodeBuilding devuelve un nodo del grafo logístico con el edificio que lo
-- respalda (si lo hay) y su titular: la regla del CCRI exige que el origen de
-- una venta y el destino de una compra sean nodos con almacén.
-- name: GetNodeBuilding :one
SELECT n.id, n.region_id, n.building_id, b.owner_account_id AS building_owner_account_id
FROM world.network_nodes n
LEFT JOIN world.buildings b ON b.id = n.building_id
WHERE n.id = sqlc.arg(id);

-- DBNow devuelve el now() de la BD: las comparaciones de ventanas wall-clock
-- (cooldown anti-parpadeo) usan siempre el reloj de la base, nunca el de la
-- aplicación.
-- name: DBNow :one
SELECT now()::timestamptz AS db_now;

-- ═════════════════════════════════════════════════════════════════════════════
-- Parte B: barridos periódicos (resolución de sorteo, expiración de TTL,
-- liquidación por vencimiento) y lecturas de contratos/entregas. Cada barrido
-- localiza sus candidatos con una query ligera (sin lock) y procesa cada
-- publicación/contrato en SU PROPIA transacción SERIALIZABLE bloqueándolo con
-- FOR UPDATE SKIP LOCKED (la fila re-verifica su condición bajo el lock: otra
-- instancia del worker pudo resolverla ya).
-- ═════════════════════════════════════════════════════════════════════════════

-- ─── (a) Resolución de sorteo (ADR-011) ──────────────────────────────────────

-- ListDueDrawPublicationIDs lista las publicaciones cuya ventana de sorteo o
-- micro-ventana ya venció (window_closes_at <= now() de la BD), pendientes de
-- resolver. Solo IDs: el worker bloquea cada una en su propia transacción.
-- name: ListDueDrawPublicationIDs :many
SELECT id FROM ledger.publications
WHERE status IN ('draw_window', 'micro_window') AND window_closes_at <= now()
ORDER BY window_closes_at
LIMIT sqlc.arg(page_limit);

-- LockDueDrawPublication bloquea una publicación con la ventana vencida
-- (re-verifica estado y vencimiento bajo el lock; FOR UPDATE SKIP LOCKED salta
-- las ya tomadas por otra instancia). Sin fila ⇒ ya resuelta o en curso.
-- name: LockDueDrawPublication :one
SELECT * FROM ledger.publications
WHERE id = sqlc.arg(id)
  AND status IN ('draw_window', 'micro_window')
  AND window_closes_at <= now()
FOR UPDATE SKIP LOCKED;

-- ServeAcceptance resuelve una aceptación como servida (total o parcialmente)
-- en el orden sorteado, con la cantidad servida y su draw_order.
-- name: ServeAcceptance :one
UPDATE ledger.publication_acceptances
   SET status = 'served', quantity_served = sqlc.arg(quantity_served),
       draw_order = sqlc.arg(draw_order), resolved_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- SetPublicationDrawResult fija la cantidad restante y el estado tras el
-- sorteo: 'exhausted' si se agotó, 'open' si resta cantidad (madura, sin
-- ventana). window_closes_at se limpia en ambos casos.
-- name: SetPublicationDrawResult :one
UPDATE ledger.publications
   SET quantity_remaining = sqlc.arg(quantity_remaining),
       status = sqlc.arg(status),
       window_closes_at = NULL,
       updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- ─── (b) Expiración de publicaciones abiertas por TTL ─────────────────────────

-- ListExpiredPublicationIDs lista las publicaciones abiertas (open) cuyo TTL de
-- sim-time venció (published_at_sim + ttl <= sim-time actual).
-- name: ListExpiredPublicationIDs :many
SELECT id FROM ledger.publications
WHERE status = 'open'
  AND published_at_sim + sqlc.arg(ttl_sim_seconds)::bigint <= sqlc.arg(sim_now)::bigint
ORDER BY published_at_sim
LIMIT sqlc.arg(page_limit);

-- LockExpiredPublication bloquea una publicación abierta vencida por TTL
-- (re-verifica bajo el lock; SKIP LOCKED salta las tomadas por otra instancia).
-- name: LockExpiredPublication :one
SELECT * FROM ledger.publications
WHERE id = sqlc.arg(id)
  AND status = 'open'
  AND published_at_sim + sqlc.arg(ttl_sim_seconds)::bigint <= sqlc.arg(sim_now)::bigint
FOR UPDATE SKIP LOCKED;

-- SetPublicationExpired marca la publicación como expirada (la liberación de la
-- garantía restante se asienta aparte, en la misma transacción).
-- name: SetPublicationExpired :one
UPDATE ledger.publications
   SET status = 'expired', updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- ─── (c) Liquidación de contratos por vencimiento de plazo ────────────────────

-- ListDueContractIDs lista los contratos activos cuyo plazo de entrega venció
-- (deadline_sim <= sim-time actual).
-- name: ListDueContractIDs :many
SELECT id FROM ledger.contracts
WHERE status = 'active' AND deadline_sim <= sqlc.arg(sim_now)::bigint
ORDER BY deadline_sim
LIMIT sqlc.arg(page_limit);

-- LockDueContract bloquea un contrato activo vencido (re-verifica bajo el lock;
-- SKIP LOCKED salta los tomados por otra instancia). settle_contract_prorata
-- vuelve a bloquear la fila (FOR UPDATE) y re-verifica que sigue activo.
-- name: LockDueContract :one
SELECT * FROM ledger.contracts
WHERE id = sqlc.arg(id) AND status = 'active' AND deadline_sim <= sqlc.arg(sim_now)::bigint
FOR UPDATE SKIP LOCKED;

-- ─── Contratos: creación, entrega y consulta ─────────────────────────────────

-- InsertContract crea el contrato en estado active con las cuentas espejo de su
-- bloqueo triple (creadas antes, en la misma transacción). El bloqueo triple lo
-- asienta a continuación ledger.confirm_contract.
-- name: InsertContract :one
INSERT INTO ledger.contracts (
    id, publication_id, channel, buyer_account_id, seller_account_id,
    product_id, quantity_agreed, unit_price, origin_node_id, destination_node_id,
    deadline_sim, stock_reserve_account_id, seller_guarantee_account_id,
    escrow_account_id, confirmed_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(publication_id), sqlc.arg(channel),
    sqlc.arg(buyer_account_id), sqlc.arg(seller_account_id),
    sqlc.arg(product_id), sqlc.arg(quantity_agreed), sqlc.arg(unit_price),
    sqlc.arg(origin_node_id), sqlc.arg(destination_node_id),
    sqlc.arg(deadline_sim), sqlc.arg(stock_reserve_account_id),
    sqlc.arg(seller_guarantee_account_id), sqlc.arg(escrow_account_id),
    sqlc.arg(confirmed_at_sim))
RETURNING *;

-- SetContractQuantityDelivered acumula la cantidad entregada a tiempo de un
-- contrato (la lee settle_contract_prorata para el reparto pro-rata).
-- name: SetContractQuantityDelivered :one
UPDATE ledger.contracts
   SET quantity_delivered = sqlc.arg(quantity_delivered), updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING *;

-- GetContract devuelve un contrato por id (la autorización por partes la aplica
-- la capa de servicio).
-- name: GetContract :one
SELECT * FROM ledger.contracts WHERE id = sqlc.arg(id);

-- GetContractForUpdate bloquea el contrato (SELECT FOR UPDATE) para el
-- consumidor de entregas: fija el estado (active/settled/failed) y el
-- quantity_delivered bajo el lock antes de acumular la entrega y liquidar,
-- serializándose con el barrido de vencimiento (que lo bloquea con SKIP LOCKED).
-- name: GetContractForUpdate :one
SELECT * FROM ledger.contracts WHERE id = sqlc.arg(id) FOR UPDATE;

-- GetContractForAcceptance resuelve el contrato resultante de una aceptación
-- servida. El esquema NO liga la aceptación al contrato con una FK: el vínculo
-- es publication_id + el aceptante como comprador (venta) o vendedor (compra).
-- name: GetContractForAcceptance :one
SELECT * FROM ledger.contracts
WHERE publication_id = sqlc.arg(publication_id)
  AND (buyer_account_id = sqlc.arg(acceptor_account_id)
       OR seller_account_id = sqlc.arg(acceptor_account_id))
ORDER BY confirmed_at_sim, id
LIMIT 1;

-- ListContracts lista los contratos en los que account_id es comprador o
-- vendedor, con filtros de rol/estado/producto y paginación keyset (id DESC:
-- los UUIDv7 preservan el orden de creación, primero los más recientes).
-- name: ListContracts :many
SELECT * FROM ledger.contracts
WHERE (buyer_account_id = sqlc.arg(account_id) OR seller_account_id = sqlc.arg(account_id))
  AND (sqlc.narg(role)::text IS NULL
       OR (sqlc.narg(role)::text = 'buyer' AND buyer_account_id = sqlc.arg(account_id))
       OR (sqlc.narg(role)::text = 'seller' AND seller_account_id = sqlc.arg(account_id)))
  AND (sqlc.narg(status)::text IS NULL OR status::text = sqlc.narg(status)::text)
  AND (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id < sqlc.narg(after_id)::uuid)
ORDER BY id DESC
LIMIT sqlc.arg(page_limit);

-- InsertContractDelivery registra una entrega parcial confirmada de un
-- contrato (verificación de entrega acumulativa, GDD 5.3 paso 5).
-- name: InsertContractDelivery :one
INSERT INTO ledger.contract_deliveries (
    id, contract_id, shipment_id, quantity, delivered_at_sim, on_time)
VALUES (
    sqlc.arg(id), sqlc.arg(contract_id), sqlc.arg(shipment_id),
    sqlc.arg(quantity), sqlc.arg(delivered_at_sim), sqlc.arg(on_time))
RETURNING *;

-- InsertContractDeliveryIfNew registra la entrega de un cargamento de forma
-- IDEMPOTENTE: un cargamento (world.shipments) llega físicamente a su destino
-- una sola vez, luego su entrega se cuenta una sola vez. Si ya existe una
-- entrega para ese shipment_id (índice único ux_contract_deliveries_shipment,
-- 0010), no inserta nada y no devuelve fila (pgx.ErrNoRows): reprocesar el mismo
-- shipment.arrived no duplica la partida ni la cantidad entregada.
-- name: InsertContractDeliveryIfNew :one
INSERT INTO ledger.contract_deliveries (
    id, contract_id, shipment_id, quantity, delivered_at_sim, on_time)
VALUES (
    sqlc.arg(id), sqlc.arg(contract_id), sqlc.arg(shipment_id),
    sqlc.arg(quantity), sqlc.arg(delivered_at_sim), sqlc.arg(on_time))
ON CONFLICT (shipment_id) DO NOTHING
RETURNING *;

-- ListContractDeliveries devuelve las entregas confirmadas de un contrato, de
-- la más antigua a la más reciente.
-- name: ListContractDeliveries :many
SELECT * FROM ledger.contract_deliveries
WHERE contract_id = sqlc.arg(contract_id)
ORDER BY delivered_at_sim, id;

-- InsertShipment crea el cargamento físico de una entrega. En la retirada in
-- situ (origen = destino) el cargamento nace ya 'released_in_situ' en el nodo
-- de destino: la mercancía nunca se movió, cambia de dueño en el sitio.
-- name: InsertShipment :one
INSERT INTO world.shipments (
    id, owner_account_id, product_id, quantity, contract_id, at_node_id,
    status, updated_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(owner_account_id), sqlc.arg(product_id),
    sqlc.arg(quantity), sqlc.arg(contract_id), sqlc.arg(at_node_id),
    sqlc.arg(status), sqlc.arg(updated_at_sim))
RETURNING id;

-- ─── Cuentas de liquidación (sink del banco central, stock libre on-demand) ──

-- GetSinkAccount devuelve la cuenta sink del banco central (destino de la parte
-- sancionadora de la garantía en un fallo de entrega).
-- name: GetSinkAccount :one
SELECT * FROM ledger.accounts WHERE kind = 'sink' ORDER BY id LIMIT 1;

-- GetNodeByBuilding devuelve el nodo del grafo logístico respaldado por un
-- edificio: reconstruye el nodo de origen de un contrato de compra a partir del
-- almacén que el aceptante-vendedor aportó (la aceptación solo persiste el
-- edificio, vía la cuenta espejo de stock, no el nodo).
-- name: GetNodeByBuilding :one
SELECT id, region_id FROM world.network_nodes
WHERE building_id = sqlc.arg(building_id)
ORDER BY id
LIMIT 1;
