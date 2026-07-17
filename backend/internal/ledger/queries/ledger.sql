-- =============================================================================
-- Imperio Industrial — queries sqlc del módulo ledger (ADR-020).
-- Solo lecturas del núcleo contable y primitivas de asiento; las invariantes
-- (balance por cuenta, doble entrada, no-negatividad, inmutabilidad) las
-- garantizan los triggers de 0004_ledger — aquí nunca se recalculan saldos.
-- =============================================================================

-- ListAccountsByOwner lista las cuentas del ledger de una corporación con
-- filtros opcionales por tipo y producto. Paginación keyset ascendente por id
-- (UUIDv7 ≈ orden temporal de creación): after_id es el último id de la
-- página anterior; NULL en la primera página.
-- name: ListAccountsByOwner :many
SELECT id, kind, owner_account_id, product_id, warehouse_building_id,
       reference_id, balance, created_at, updated_at
FROM ledger.accounts
WHERE owner_account_id = sqlc.arg(owner_account_id)
  AND (sqlc.narg(kind)::ledger.account_kind IS NULL OR kind = sqlc.narg(kind)::ledger.account_kind)
  AND (sqlc.narg(product_id)::uuid IS NULL OR product_id = sqlc.narg(product_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetAccount devuelve una cuenta del ledger por id (autorización por
-- propiedad en la capa de servicio).
-- name: GetAccount :one
SELECT id, kind, owner_account_id, product_id, warehouse_building_id,
       reference_id, balance, created_at, updated_at
FROM ledger.accounts
WHERE id = sqlc.arg(id);

-- ListEntriesByAccount devuelve el extracto de una cuenta, de más reciente a
-- más antigua, con la cabecera del asiento (kind/reference/description/
-- sim_time_at, schema LedgerEntry del contrato). Rango opcional de sim-time
-- y paginación keyset por (created_at, id) DESC: before_* es la última
-- partida de la página anterior; NULL en la primera página.
-- name: ListEntriesByAccount :many
SELECT e.id, e.transaction_id, e.account_id, e.amount, e.created_at,
       t.kind AS transaction_kind, t.reference_id, t.description, t.sim_time_at
FROM ledger.entries e
JOIN ledger.transactions t ON t.id = e.transaction_id
WHERE e.account_id = sqlc.arg(account_id)
  AND (sqlc.narg(from_sim)::bigint IS NULL OR t.sim_time_at >= sqlc.narg(from_sim)::bigint)
  AND (sqlc.narg(to_sim)::bigint IS NULL OR t.sim_time_at <= sqlc.narg(to_sim)::bigint)
  AND (sqlc.narg(before_created_at)::timestamptz IS NULL
       OR (e.created_at, e.id) < (sqlc.narg(before_created_at)::timestamptz, sqlc.narg(before_id)::uuid))
ORDER BY e.created_at DESC, e.id DESC
LIMIT sqlc.arg(page_limit);

-- CreateLedgerAccount crea una cuenta del ledger. El id (UUIDv7) lo genera la
-- aplicación (ADR-018); los constraints ck_accounts_asset y las unicidades
-- parciales (ux_accounts_cash, ux_accounts_stock_free) validan la forma.
-- name: CreateLedgerAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id, reference_id)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.narg(owner_account_id),
        sqlc.narg(product_id), sqlc.narg(warehouse_building_id), sqlc.narg(reference_id))
RETURNING id, kind, owner_account_id, product_id, warehouse_building_id,
          reference_id, balance, created_at, updated_at;

-- GetCashAccountByOwner devuelve la única cuenta de caja de una corporación
-- (unicidad parcial ux_accounts_cash).
-- name: GetCashAccountByOwner :one
SELECT id, kind, owner_account_id, product_id, warehouse_building_id,
       reference_id, balance, created_at, updated_at
FROM ledger.accounts
WHERE kind = 'cash' AND owner_account_id = sqlc.arg(owner_account_id);

-- InsertTransaction inserta la cabecera de un asiento. Inmutable una vez
-- asentada (trigger trg_transactions_immutable).
-- name: InsertTransaction :exec
INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(sim_time_at),
        sqlc.narg(reference_id), sqlc.narg(description));

-- InsertEntry inserta una partida de doble entrada. Los triggers aplican el
-- saldo, la no-negatividad y (diferido, en el COMMIT) el balance del asiento.
-- name: InsertEntry :exec
INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
VALUES (sqlc.arg(id), sqlc.arg(transaction_id), sqlc.arg(account_id), sqlc.arg(amount));
