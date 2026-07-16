// Autenticación bearer: sha256(token) contra auth.sessions vigente.
// El servidor nunca almacena el token en claro (ADR-IMPL-05).
import { createHash } from 'node:crypto';
import type { FastifyInstance } from 'fastify';
import type { DbPool } from '../db.js';
import type { AuthAccount } from '../types.js';

export function sha256hex(input: string): string {
  return createHash('sha256').update(input, 'utf8').digest('hex');
}

export interface SessionInfo {
  sessionId: string;
  account: AuthAccount;
}

export async function lookupSession(pool: DbPool, token: string): Promise<SessionInfo | null> {
  const hash = sha256hex(token);
  const r = await pool.query(
    `SELECT s.id AS session_id, a.id, a.kind, a.name, a.status, a.created_at,
            bp.archetype AS bot_archetype
       FROM auth.sessions s
       JOIN auth.accounts a ON a.id = s.account_id
       LEFT JOIN auth.bot_profiles bp ON bp.account_id = a.id
      WHERE s.token_hash = $1 AND s.expires_at > now() AND a.status = 'active'`,
    [hash],
  );
  const row = r.rows[0];
  if (!row) return null;
  return {
    sessionId: row.session_id as string,
    account: {
      id: row.id,
      kind: row.kind,
      name: row.name,
      status: row.status,
      created_at: row.created_at,
      bot_archetype: row.bot_archetype ?? null,
    },
  };
}

export function registerAuth(app: FastifyInstance): void {
  app.addHook('onRequest', async (req, reply) => {
    if (!req.url.startsWith('/api/')) return;
    if (req.routeOptions?.config?.public) return;
    const header = req.headers.authorization;
    const token =
      typeof header === 'string' && header.startsWith('Bearer ') ? header.slice(7).trim() : null;
    if (!token) {
      await reply
        .status(401)
        .send({ error: { code: 'UNAUTHORIZED', message: 'Sesión ausente o expirada' } });
      return;
    }
    const session = await lookupSession(app.pool, token);
    if (!session) {
      await reply
        .status(401)
        .send({ error: { code: 'UNAUTHORIZED', message: 'Sesión ausente o expirada' } });
      return;
    }
    req.account = session.account;
    req.sessionId = session.sessionId;
  });
}
