import 'fastify';
import type { DbPool } from './db.js';
import type { Config } from './config.js';

export interface AuthAccount {
  id: string;
  kind: 'human' | 'bot' | 'city' | 'system';
  name: string;
  status: 'active' | 'suspended' | 'retired';
  created_at: Date;
  bot_archetype: string | null;
}

export interface SimState {
  simSeconds: number;
  frozen: boolean;
}

declare module 'fastify' {
  interface FastifyInstance {
    pool: DbPool;
    cfg: Config;
    /** Lectura de world.sim_clock con caché de ~1 s (meta + mantenimiento). */
    getSim(): Promise<SimState>;
  }
  interface FastifyRequest {
    account?: AuthAccount;
    sessionId?: string;
    idemKey?: string;
    idemReplayed?: boolean;
  }
  interface FastifyContextConfig {
    public?: boolean;
  }
}
