// Acceso a datos: pg (node-postgres) con SQL explícito (ADR-IMPL-04).
// Los comandos corren en transacciones SERIALIZABLE con reintento ante
// SQLSTATE 40001 (hasta 3 intentos) — convención transversal del proyecto.
import pg from 'pg';

const { Pool } = pg;

export type DbClient = pg.PoolClient;
export type DbPool = pg.Pool;

export function createPool(databaseUrl: string): DbPool {
  return new Pool({
    connectionString: databaseUrl,
    max: 10,
    idleTimeoutMillis: 30_000,
  });
}

const MAX_ATTEMPTS = 3;

function isSerializationFailure(err: unknown): boolean {
  return (
    typeof err === 'object' &&
    err !== null &&
    (err as { code?: string }).code === '40001'
  );
}

/**
 * Ejecuta `fn` dentro de una transacción SERIALIZABLE. Ante un fallo de
 * serialización (40001) reintenta hasta 3 veces; cualquier otro error
 * hace ROLLBACK y se propaga — la base garantiza que no queda estado parcial.
 */
export async function withSerializable<T>(
  pool: DbPool,
  fn: (client: DbClient) => Promise<T>,
): Promise<T> {
  let lastError: unknown;
  for (let attempt = 1; attempt <= MAX_ATTEMPTS; attempt++) {
    const client = await pool.connect();
    try {
      await client.query('BEGIN ISOLATION LEVEL SERIALIZABLE');
      const result = await fn(client);
      await client.query('COMMIT');
      return result;
    } catch (err) {
      try {
        await client.query('ROLLBACK');
      } catch {
        /* la conexión puede estar rota; release(err) la descarta */
      }
      lastError = err;
      if (!isSerializationFailure(err) || attempt === MAX_ATTEMPTS) {
        throw err;
      }
    } finally {
      client.release();
    }
  }
  throw lastError;
}
