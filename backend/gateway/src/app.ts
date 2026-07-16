// buildApp(): instancia Fastify completa (REST /api/v1 + WS /ws) para el
// servidor y para los tests. El orden de hooks importa: mantenimiento →
// auth → rate limit → idempotencia.
import Fastify, { type FastifyInstance } from 'fastify';
import websocket from '@fastify/websocket';
import { loadConfig, type Config } from './config.js';
import { createPool } from './db.js';
import { AppError } from './lib/errors.js';
import { registerSimClock, registerMaintenanceGate } from './plugins/maintenance.js';
import { registerAuth } from './plugins/auth.js';
import { registerRateLimit } from './plugins/ratelimit.js';
import { registerIdempotency } from './plugins/idempotency.js';
import { registerAuthRoutes } from './routes/auth.js';
import { registerLedgerRoutes } from './routes/ledger.js';
import { registerContractRoutes } from './routes/contracts.js';
import { registerMarketRoutes } from './routes/market.js';
import { registerWorldRoutes } from './routes/world.js';
import { registerLogisticsRoutes } from './routes/logistics.js';
import { registerWsGateway } from './ws/gateway.js';
import './types.js';

export async function buildApp(overrides: Partial<Config> = {}): Promise<FastifyInstance> {
  const cfg: Config = { ...loadConfig(), ...overrides };
  const app = Fastify({
    logger: {
      level: process.env.LOG_LEVEL ?? 'info',
    },
  });

  app.decorate('cfg', cfg);
  app.decorate('pool', createPool(cfg.databaseUrl));
  registerSimClock(app);

  await app.register(websocket);

  // Hooks transversales (el orden de registro fija el orden de ejecución).
  registerMaintenanceGate(app);
  registerAuth(app);
  registerRateLimit(app);
  registerIdempotency(app);

  // Handler global de errores: envoltura { error: { code, message, details } }
  // exacta del spec; nunca filtra stacktraces.
  app.setErrorHandler(async (err, req, reply) => {
    if (err instanceof AppError) {
      const payload: Record<string, unknown> = { code: err.code, message: err.message };
      if (err.details) payload.details = err.details;
      await reply.status(err.status).send({ error: payload });
      return;
    }
    const fastifyErr = err as { statusCode?: number };
    if (fastifyErr.statusCode === 400 || fastifyErr.statusCode === 415) {
      await reply.status(400).send({
        error: { code: 'VALIDATION_ERROR', message: 'Comando malformado o unidades inválidas' },
      });
      return;
    }
    req.log.error({ err }, 'error interno no controlado');
    await reply.status(500).send({ error: { code: 'INTERNAL', message: 'Error interno' } });
  });

  app.setNotFoundHandler(async (_req, reply) => {
    await reply.status(404).send({ error: { code: 'NOT_FOUND', message: 'Recurso inexistente' } });
  });

  // Superficie REST bajo /api/v1 (specs/openapi.yaml v1.1.0).
  await app.register(
    async (api) => {
      registerAuthRoutes(api);
      registerLedgerRoutes(api);
      registerContractRoutes(api);
      registerMarketRoutes(api);
      registerWorldRoutes(api);
      registerLogisticsRoutes(api);
    },
    { prefix: '/api/v1' },
  );

  // WebSocket /ws (specs/ws-protocol.md).
  registerWsGateway(app);

  app.addHook('onClose', async () => {
    await app.pool.end();
  });

  return app;
}
