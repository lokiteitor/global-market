// Ventana de mantenimiento (ADR-003): si world.sim_clock.frozen, TODA la API
// responde 503 MAINTENANCE_WINDOW con Retry-After. La lectura del reloj se
// cachea ~1 s (misma caché que alimenta meta.sim_time).
import type { FastifyInstance } from 'fastify';
import type { SimState } from '../types.js';

export function registerSimClock(app: FastifyInstance): void {
  let cache: SimState = { simSeconds: 0, frozen: false };
  let fetchedAt = 0;
  let inflight: Promise<SimState> | null = null;

  app.decorate('getSim', async (): Promise<SimState> => {
    const now = Date.now();
    if (now - fetchedAt < app.cfg.simCacheMs) return cache;
    if (!inflight) {
      inflight = (async () => {
        try {
          const r = await app.pool.query(
            'SELECT sim_seconds, frozen FROM world.sim_clock WHERE id = 1',
          );
          const row = r.rows[0];
          if (row) {
            cache = { simSeconds: Number(row.sim_seconds), frozen: Boolean(row.frozen) };
          }
          fetchedAt = Date.now();
          return cache;
        } finally {
          inflight = null;
        }
      })();
    }
    return inflight;
  });
}

export function registerMaintenanceGate(app: FastifyInstance): void {
  app.addHook('onRequest', async (req, reply) => {
    // El upgrade WS no es parte de la superficie REST; el pong informa frozen.
    if (!req.url.startsWith('/api/')) return;
    const sim = await app.getSim();
    if (sim.frozen) {
      await reply
        .status(503)
        .header('Retry-After', String(app.cfg.maintenanceRetryAfterSeconds))
        .send({
          error: {
            code: 'MAINTENANCE_WINDOW',
            message:
              'El mundo está en su ventana de mantenimiento diaria; el sim-time está congelado',
            details: { retry_after_seconds: app.cfg.maintenanceRetryAfterSeconds },
          },
        });
    }
  });
}
