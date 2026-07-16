// Historial de mercado: velas OHLC agregadas AL VUELO desde contratos
// liquidados (ledger.contracts settled) — nunca de órdenes vivas (GDD 5.2).
// La región de la vela es la región del nodo de destino del contrato.
import type { FastifyInstance } from 'fastify';
import { sendData } from '../lib/envelope.js';
import { ohlcDto } from '../lib/dto.js';
import { parseLimit } from '../lib/cursor.js';
import { badRequest } from '../lib/errors.js';
import { optionalSimTimeQuery, optionalUuidField, uuidField } from '../lib/validate.js';

export function registerMarketRoutes(app: FastifyInstance): void {
  app.get('/market/ohlc', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const productId = uuidField(q.product_id, 'product_id');
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const bucket = q.bucket_sim_secs === undefined ? 3600 : Number(q.bucket_sim_secs);
    if (!Number.isSafeInteger(bucket) || bucket < 1) {
      throw badRequest('bucket_sim_secs debe ser un entero >= 1');
    }
    const fromSim = optionalSimTimeQuery(q.from_sim, 'from_sim');
    const toSim = optionalSimTimeQuery(q.to_sim, 'to_sim');
    const limit = parseLimit(q.limit);

    const params: unknown[] = [productId, bucket];
    let where = `c.status = 'settled' AND c.product_id = $1 AND c.settled_at_sim IS NOT NULL`;
    if (regionId) {
      params.push(regionId);
      where += ` AND n.region_id = $${params.length}`;
    }
    if (fromSim !== undefined) {
      params.push(fromSim);
      where += ` AND c.settled_at_sim >= $${params.length}`;
    }
    if (toSim !== undefined) {
      params.push(toSim);
      where += ` AND c.settled_at_sim <= $${params.length}`;
    }
    params.push(limit);
    // bucket = floor(settled_at_sim / bucket) * bucket (división entera BIGINT).
    const r = await app.pool.query(
      `SELECT (c.settled_at_sim / $2::bigint) * $2::bigint AS bucket_start_sim,
              n.region_id,
              (array_agg(c.unit_price ORDER BY c.settled_at_sim, c.id))[1]           AS open_price,
              (array_agg(c.unit_price ORDER BY c.settled_at_sim DESC, c.id DESC))[1] AS close_price,
              max(c.unit_price) AS high_price,
              min(c.unit_price) AS low_price,
              sum(c.quantity_delivered) AS volume,
              count(*) AS contract_count
         FROM ledger.contracts c
         JOIN world.network_nodes n ON n.id = c.destination_node_id
        WHERE ${where}
        GROUP BY 1, 2
        ORDER BY 1 DESC
        LIMIT $${params.length}`,
      params,
    );
    // Se devuelve en orden cronológico ascendente.
    const rows = r.rows.reverse();
    await sendData(req, reply, rows.map((row) => ohlcDto(row, productId, bucket)));
  });
}
