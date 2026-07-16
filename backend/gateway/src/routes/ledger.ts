// Ledger: solo lectura por API — el valor solo se mueve por operaciones de dominio.
import type { FastifyInstance } from 'fastify';
import { sendData } from '../lib/envelope.js';
import { ledgerAccountDto, ledgerEntryDto } from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import { forbidden, notFound } from '../lib/errors.js';
import {
  optionalEnumQuery,
  optionalSimTimeQuery,
  optionalUuidField,
  uuidParam,
} from '../lib/validate.js';

const ACCOUNT_KINDS = [
  'cash',
  'escrow',
  'guarantee',
  'stock_free',
  'stock_reserved',
  'custody',
  'sink',
  'emission',
] as const;

export function registerLedgerRoutes(app: FastifyInstance): void {
  app.get('/ledger/accounts', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const kind = optionalEnumQuery(q.kind, 'kind', ACCOUNT_KINDS);
    const productId = optionalUuidField(q.product_id, 'product_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);

    const params: unknown[] = [req.account!.id];
    let where = 'owner_account_id = $1';
    if (kind) {
      params.push(kind);
      where += ` AND kind = $${params.length}`;
    }
    if (productId) {
      params.push(productId);
      where += ` AND product_id = $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT * FROM ledger.accounts WHERE ${where}
        ORDER BY id LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    const hasMore = r.rows.length > limit;
    const rows = r.rows.slice(0, limit);
    await sendData(req, reply, rows.map(ledgerAccountDto), {
      nextCursor: hasMore ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/ledger/accounts/:ledgerAccountId/entries', async (req, reply) => {
    const accountId = uuidParam((req.params as Record<string, unknown>).ledgerAccountId, 'ledgerAccountId');
    const q = req.query as Record<string, unknown>;
    const fromSim = optionalSimTimeQuery(q.from_sim, 'from_sim');
    const toSim = optionalSimTimeQuery(q.to_sim, 'to_sim');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);

    const acc = await app.pool.query(
      'SELECT owner_account_id FROM ledger.accounts WHERE id = $1',
      [accountId],
    );
    if (!acc.rows[0]) throw notFound();
    if (acc.rows[0].owner_account_id !== req.account!.id) {
      throw forbidden('El extracto solo es accesible por el titular de la cuenta');
    }

    const params: unknown[] = [accountId];
    let where = 'e.account_id = $1';
    if (fromSim !== undefined) {
      params.push(fromSim);
      where += ` AND t.sim_time_at >= $${params.length}`;
    }
    if (toSim !== undefined) {
      params.push(toSim);
      where += ` AND t.sim_time_at <= $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT e.id, e.transaction_id, e.account_id, e.amount, e.created_at,
              t.kind AS transaction_kind, t.reference_id, t.description, t.sim_time_at
         FROM ledger.entries e
         JOIN ledger.transactions t ON t.id = e.transaction_id
        WHERE ${where}
        ORDER BY e.created_at DESC, e.id DESC
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    const hasMore = r.rows.length > limit;
    const rows = r.rows.slice(0, limit);
    await sendData(req, reply, rows.map(ledgerEntryDto), {
      nextCursor: hasMore ? encodeCursor(offset + limit) : undefined,
    });
  });
}
