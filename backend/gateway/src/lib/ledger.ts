// Helpers contables del camino de comando (ADR-IMPL-03). Los triggers de la
// base (saldo por cuenta, no-negatividad, doble entrada diferida) son la
// garantía final: aquí solo se orquesta.
import type { DbClient } from '../db.js';

async function newUuid(client: DbClient): Promise<string> {
  const r = await client.query('SELECT uuidv7() AS id');
  return r.rows[0]!.id as string;
}

export { newUuid };

/** Caja única por corporación (índice único parcial ux_accounts_cash). */
export async function ensureCashAccount(client: DbClient, ownerId: string): Promise<string> {
  const found = await client.query(
    `SELECT id FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1`,
    [ownerId],
  );
  if (found.rows[0]) return found.rows[0].id as string;
  const ins = await client.query(
    `INSERT INTO ledger.accounts (kind, owner_account_id) VALUES ('cash', $1) RETURNING id`,
    [ownerId],
  );
  return ins.rows[0]!.id as string;
}

/** Cuenta de stock libre por (dueño, producto, almacén). */
export async function ensureStockFreeAccount(
  client: DbClient,
  ownerId: string,
  productId: string,
  warehouseBuildingId: string,
): Promise<string> {
  const found = await client.query(
    `SELECT id FROM ledger.accounts
      WHERE kind = 'stock_free' AND owner_account_id = $1
        AND product_id = $2 AND warehouse_building_id = $3`,
    [ownerId, productId, warehouseBuildingId],
  );
  if (found.rows[0]) return found.rows[0].id as string;
  const ins = await client.query(
    `INSERT INTO ledger.accounts (kind, owner_account_id, product_id, warehouse_building_id)
     VALUES ('stock_free', $1, $2, $3) RETURNING id`,
    [ownerId, productId, warehouseBuildingId],
  );
  return ins.rows[0]!.id as string;
}

/** Cuenta espejo de una publicación/aceptación/contrato (reference_id = entidad). */
export async function createMirrorAccount(
  client: DbClient,
  kind: 'stock_reserved' | 'guarantee' | 'escrow' | 'custody',
  ownerId: string,
  referenceId: string,
  productId?: string | null,
  warehouseBuildingId?: string | null,
): Promise<string> {
  const ins = await client.query(
    `INSERT INTO ledger.accounts (kind, owner_account_id, product_id, warehouse_building_id, reference_id)
     VALUES ($1, $2, $3, $4, $5) RETURNING id`,
    [kind, ownerId, productId ?? null, warehouseBuildingId ?? null, referenceId],
  );
  return ins.rows[0]!.id as string;
}

/** Cuenta sink del banco central (única cuenta sink sembrada). */
export async function sinkAccount(client: DbClient): Promise<string> {
  const r = await client.query(`SELECT id FROM ledger.accounts WHERE kind = 'sink' ORDER BY created_at LIMIT 1`);
  if (!r.rows[0]) throw new Error('ledger: no existe cuenta sink del banco central');
  return r.rows[0].id as string;
}

export interface Entry {
  accountId: string;
  amount: bigint;
}

/**
 * Asiento de doble entrada: cabecera + partidas. La suma por activo debe ser
 * cero al COMMIT (constraint trigger diferido de la base).
 */
export async function postTransaction(
  client: DbClient,
  kind: string,
  simTimeAt: number,
  referenceId: string | null,
  description: string,
  entries: Entry[],
): Promise<string> {
  const txId = await newUuid(client);
  await client.query(
    `INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
     VALUES ($1, $2, $3, $4, $5)`,
    [txId, kind, simTimeAt, referenceId, description],
  );
  for (const e of entries) {
    if (e.amount === 0n) continue; // las partidas nunca son cero
    await client.query(
      `INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES ($1, $2, $3)`,
      [txId, e.accountId, e.amount.toString()],
    );
  }
  return txId;
}

/**
 * Evento outbox emitido EN LA MISMA transacción que el cambio de estado
 * (patrón transactional outbox). payload.entity = DTO REST de la entidad.
 */
export async function emitOutbox(
  client: DbClient,
  aggregateType: string,
  aggregateId: string,
  eventType: string,
  payload: Record<string, unknown>,
  simTimeAt: number,
): Promise<void> {
  await client.query(
    `INSERT INTO outbox.events (aggregate_type, aggregate_id, event_type, payload, sim_time_at)
     VALUES ($1, $2, $3, $4, $5)`,
    [aggregateType, aggregateId, eventType, JSON.stringify(payload), simTimeAt],
  );
}
