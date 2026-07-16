// Idempotencia de comandos (ADR-IMPL-09): en POST con Idempotency-Key, si la
// clave ya existe para la misma cuenta se reproduce la respuesta almacenada;
// si no, se ejecuta y se guarda el resultado cuando status < 500.
import type { FastifyInstance } from 'fastify';
import { isUuid } from '../lib/validate.js';

export function registerIdempotency(app: FastifyInstance): void {
  app.addHook('preHandler', async (req, reply) => {
    if (req.method !== 'POST' || !req.url.startsWith('/api/')) return;
    if (!req.account) return;
    const key = req.headers['idempotency-key'];
    if (typeof key !== 'string' || !isUuid(key)) return;
    const r = await app.pool.query(
      `SELECT account_id, response_status, response_body FROM auth.idempotency_keys WHERE key = $1`,
      [key],
    );
    const row = r.rows[0];
    if (row && row.account_id === req.account.id) {
      req.idemReplayed = true;
      await reply.status(Number(row.response_status)).send(row.response_body);
      return;
    }
    // Clave nueva (o de otra cuenta: se ejecuta y el ON CONFLICT protege la PK).
    if (!row) req.idemKey = key;
  });

  app.addHook('onSend', async (req, reply, payload) => {
    if (!req.idemKey || req.idemReplayed || !req.account) return payload;
    if (reply.statusCode >= 500) return payload;
    let bodyJson = 'null';
    if (typeof payload === 'string' && payload.length > 0) {
      try {
        JSON.parse(payload);
        bodyJson = payload;
      } catch {
        bodyJson = JSON.stringify(String(payload));
      }
    }
    try {
      await app.pool.query(
        `INSERT INTO auth.idempotency_keys (key, account_id, endpoint, response_status, response_body)
         VALUES ($1, $2, $3, $4, $5::jsonb) ON CONFLICT (key) DO NOTHING`,
        [req.idemKey, req.account.id, req.routeOptions?.url ?? req.url, reply.statusCode, bodyJson],
      );
    } catch (err) {
      req.log.error({ err }, 'idempotency: fallo al persistir la clave');
    }
    return payload;
  });
}
