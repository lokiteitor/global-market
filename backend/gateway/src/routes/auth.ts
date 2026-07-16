// Rutas de autenticación: POST /auth/sessions, DELETE /auth/sessions/current,
// GET /auth/me. El token se devuelve una única vez; se almacena su sha256.
import { randomBytes } from 'node:crypto';
import type { FastifyInstance } from 'fastify';
import { sha256hex } from '../plugins/auth.js';
import { sendData } from '../lib/envelope.js';
import { accountDto } from '../lib/dto.js';
import { unauthorized } from '../lib/errors.js';
import { body, stringField } from '../lib/validate.js';

export function registerAuthRoutes(app: FastifyInstance): void {
  app.post('/auth/sessions', { config: { public: true } }, async (req, reply) => {
    const b = body(req.body);
    const accountName = stringField(b.account_name, 'account_name');
    const secret = stringField(b.secret, 'secret');
    const clientInfo =
      b.client_info && typeof b.client_info === 'object' ? b.client_info : {};

    const r = await app.pool.query(
      `SELECT a.id, a.kind, a.name, a.status, a.created_at, c.secret_hash,
              bp.archetype AS bot_archetype
         FROM auth.accounts a
         JOIN auth.account_credentials c ON c.account_id = a.id
         LEFT JOIN auth.bot_profiles bp ON bp.account_id = a.id
        WHERE lower(a.name) = lower($1) AND a.status = 'active'`,
      [accountName],
    );
    const row = r.rows[0];
    if (!row || row.secret_hash !== sha256hex(secret)) {
      throw unauthorized('Credenciales inválidas');
    }

    const token = randomBytes(32).toString('hex');
    const expires = new Date(Date.now() + app.cfg.sessionTtlDays * 24 * 3600 * 1000);
    const ins = await app.pool.query(
      `INSERT INTO auth.sessions (account_id, token_hash, client_info, expires_at)
       VALUES ($1, $2, $3, $4) RETURNING id, expires_at`,
      [row.id, sha256hex(token), JSON.stringify(clientInfo), expires.toISOString()],
    );
    const session = ins.rows[0]!;
    await sendData(req, reply, {
      session_id: session.id,
      token,
      expires_at: new Date(session.expires_at).toISOString(),
      account: accountDto(row),
    }, { status: 201 });
  });

  app.delete('/auth/sessions/current', async (req, reply) => {
    if (req.sessionId) {
      await app.pool.query('DELETE FROM auth.sessions WHERE id = $1', [req.sessionId]);
    }
    await reply.status(204).send();
  });

  app.get('/auth/me', async (req, reply) => {
    await sendData(req, reply, accountDto(req.account as unknown as Record<string, unknown>));
  });
}
