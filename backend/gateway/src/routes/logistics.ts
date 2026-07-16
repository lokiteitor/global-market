// Logistics Service: topología del grafo, pathfinding informativo (Dijkstra
// v1 sobre congestión EMA) y rutas definidas por el jugador.
import type { FastifyInstance } from 'fastify';
import type { DbClient, DbPool } from '../db.js';
import { withSerializable } from '../db.js';
import { sendData } from '../lib/envelope.js';
import { networkLinkDto, networkNodeDto, routeDto } from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import { AppError, badRequest, forbidden, notFound, validation } from '../lib/errors.js';
import {
  body,
  enumField,
  optionalEnumQuery,
  optionalUuidField,
  stringField,
  uuidField,
  uuidParam,
} from '../lib/validate.js';

const MODES = ['road', 'rail', 'sea'] as const;

const LINK_SELECT = `
  SELECT l.*, ST_AsGeoJSON(l.path) AS path_geojson,
         COALESCE(json_agg(json_build_object(
             'id', s.id, 'region_id', s.region_id, 'seq', s.seq,
             'length_m', s.length_m, 'congestion_ema', s.congestion_ema,
             'updated_at_sim', s.updated_at_sim) ORDER BY s.seq)
           FILTER (WHERE s.id IS NOT NULL), '[]'::json) AS segments
    FROM world.network_links l
    LEFT JOIN world.link_segments s ON s.link_id = l.id`;

const ROUTE_SELECT = `
  SELECT r.*, COALESCE(json_agg(json_build_object('leg_index', rl.leg_index, 'link_id', rl.link_id)
           ORDER BY rl.leg_index) FILTER (WHERE rl.route_id IS NOT NULL), '[]'::json) AS legs
    FROM world.routes r
    LEFT JOIN world.route_legs rl ON rl.route_id = r.id`;

interface GraphLink {
  id: string;
  from: string;
  to: string;
  mode: string;
  lengthM: number;
  baseSpeed: number;
  congestion: number;
}

/** Los enlaces se recorren como grafo NO dirigido (convención del seed). */
async function loadGraph(pool: DbPool, modes: readonly string[]): Promise<GraphLink[]> {
  const r = await pool.query(
    `SELECT l.id, l.from_node_id, l.to_node_id, l.mode, l.length_m, l.base_speed_kmh,
            COALESCE(avg(s.congestion_ema), 1) AS congestion
       FROM world.network_links l
       LEFT JOIN world.link_segments s ON s.link_id = l.id
      WHERE l.mode = ANY($1)
      GROUP BY l.id`,
    [modes],
  );
  return r.rows.map((row) => ({
    id: row.id,
    from: row.from_node_id,
    to: row.to_node_id,
    mode: row.mode,
    lengthM: Number(row.length_m),
    baseSpeed: Number(row.base_speed_kmh),
    congestion: Number(row.congestion),
  }));
}

/** Dijkstra con peso = length_m × congestion_ema. Devuelve enlaces en orden. */
function dijkstra(links: GraphLink[], origin: string, destination: string): GraphLink[] | null {
  const adj = new Map<string, { link: GraphLink; next: string }[]>();
  const push = (from: string, next: string, link: GraphLink): void => {
    const list = adj.get(from) ?? [];
    list.push({ link, next });
    adj.set(from, list);
  };
  for (const l of links) {
    push(l.from, l.to, l);
    push(l.to, l.from, l);
  }
  const dist = new Map<string, number>([[origin, 0]]);
  const prev = new Map<string, { node: string; link: GraphLink }>();
  const visited = new Set<string>();
  // Cola simple (grafo pequeño en Fases 0-1); sustituible por un heap.
  const frontier: { node: string; d: number }[] = [{ node: origin, d: 0 }];
  while (frontier.length > 0) {
    frontier.sort((a, b) => a.d - b.d);
    const cur = frontier.shift();
    if (!cur || visited.has(cur.node)) continue;
    visited.add(cur.node);
    if (cur.node === destination) break;
    for (const edge of adj.get(cur.node) ?? []) {
      const w = edge.link.lengthM * edge.link.congestion;
      const nd = cur.d + w;
      const old = dist.get(edge.next);
      if (old === undefined || nd < old) {
        dist.set(edge.next, nd);
        prev.set(edge.next, { node: cur.node, link: edge.link });
        frontier.push({ node: edge.next, d: nd });
      }
    }
  }
  if (!visited.has(destination) && !dist.has(destination)) return null;
  if (origin === destination) return [];
  const path: GraphLink[] = [];
  let node = destination;
  while (node !== origin) {
    const p = prev.get(node);
    if (!p) return null;
    path.unshift(p.link);
    node = p.node;
  }
  return path;
}

function legEta(link: GraphLink): number {
  // velocidad efectiva = min(60, base_speed) / congestión; ETA en segundos sim.
  const speedEff = Math.min(60, link.baseSpeed) / link.congestion;
  return Math.round((link.lengthM * 3.6) / speedEff);
}

async function assertContiguousLegs(client: DbClient, legIds: string[]): Promise<void> {
  const r = await client.query(
    'SELECT id, from_node_id, to_node_id, mode FROM world.network_links WHERE id = ANY($1)',
    [legIds],
  );
  const byId = new Map<string, { from: string; to: string; mode: string }>();
  for (const row of r.rows) {
    byId.set(row.id, { from: row.from_node_id, to: row.to_node_id, mode: row.mode });
  }
  for (const id of legIds) {
    if (!byId.has(id)) throw validation(`El enlace ${id} no existe`);
  }
  // v1: solo se valida el encadenado (enlaces consecutivos comparten nodo);
  // la terminal intermodal en cambios de modo queda para Fase 2.
  for (let i = 0; i + 1 < legIds.length; i++) {
    const a = byId.get(legIds[i]!)!;
    const b = byId.get(legIds[i + 1]!)!;
    const shared =
      a.from === b.from || a.from === b.to || a.to === b.from || a.to === b.to;
    if (!shared) {
      throw validation('La secuencia de enlaces no es contigua');
    }
  }
}

export function registerLogisticsRoutes(app: FastifyInstance): void {
  // ── Grafo ───────────────────────────────────────────────────────────────
  app.get('/logistics/network/nodes', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const kind = optionalEnumQuery(q.kind, 'kind', [
      'mine', 'factory', 'warehouse', 'port', 'station',
      'distribution_center', 'junction', 'city_gate',
    ] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (regionId) {
      params.push(regionId);
      wheres.push(`region_id = $${params.length}`);
    }
    if (kind) {
      params.push(kind);
      wheres.push(`kind = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT *, ST_AsGeoJSON(location) AS location_geojson FROM world.network_nodes
        WHERE ${wheres.join(' AND ')} ORDER BY id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(networkNodeDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/logistics/network/links', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const mode = optionalEnumQuery(q.mode, 'mode', MODES);
    const fromNodeId = optionalUuidField(q.from_node_id, 'from_node_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (regionId) {
      params.push(regionId);
      wheres.push(
        `l.id IN (SELECT link_id FROM world.link_segments WHERE region_id = $${params.length})`,
      );
    }
    if (mode) {
      params.push(mode);
      wheres.push(`l.mode = $${params.length}`);
    }
    if (fromNodeId) {
      params.push(fromNodeId);
      wheres.push(`(l.from_node_id = $${params.length} OR l.to_node_id = $${params.length})`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `${LINK_SELECT} WHERE ${wheres.join(' AND ')} GROUP BY l.id ORDER BY l.id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(networkLinkDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  // ── Asistente de ruta (solo cálculo; no persiste nada) ──────────────────
  app.post('/logistics/route-plans', async (req, reply) => {
    const b = body(req.body);
    const origin = uuidField(b.origin_node_id, 'origin_node_id');
    const destination = uuidField(b.destination_node_id, 'destination_node_id');
    let modes: readonly string[] = MODES;
    if (b.modes !== undefined) {
      if (!Array.isArray(b.modes) || b.modes.length === 0) {
        throw badRequest('modes debe ser un array no vacío de modos');
      }
      modes = b.modes.map((m) => enumField(m, 'modes', MODES));
    }
    const nodes = await app.pool.query(
      'SELECT id FROM world.network_nodes WHERE id = ANY($1)',
      [[origin, destination]],
    );
    const found = new Set(nodes.rows.map((r) => r.id as string));
    if (!found.has(origin) || !found.has(destination)) throw notFound('Nodo inexistente');

    const links = await loadGraph(app.pool, modes);
    const path = dijkstra(links, origin, destination);
    if (path === null) {
      throw new AppError(422, 'NO_ROUTE_FOUND',
        'No existe ruta ejecutable entre los nodos con los modos indicados');
    }
    const legs = path.map((link, i) => ({
      seq: i,
      link_id: link.id,
      mode: link.mode,
      eta_sim_seconds: legEta(link),
    }));
    const totalEta = legs.reduce((acc, l) => acc + l.eta_sim_seconds, 0);
    const totalLen = path.reduce((acc, l) => acc + l.lengthM, 0);
    // v1 heurístico: coste estimado = 2 unidades por km recorrido.
    const estimatedCost = String(Math.round((totalLen / 1000) * 2));
    await sendData(req, reply, {
      origin_node_id: origin,
      destination_node_id: destination,
      legs,
      total_eta_sim_seconds: totalEta,
      estimated_cost: estimatedCost,
    });
  });

  // ── Rutas del jugador ───────────────────────────────────────────────────
  app.get('/logistics/routes', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const kind = optionalEnumQuery(q.kind, 'kind', ['fixed_line', 'on_demand'] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [req.account!.id];
    const wheres: string[] = ['r.owner_account_id = $1'];
    if (kind) {
      params.push(kind);
      wheres.push(`r.kind = $${params.length}`);
    }
    if (q.active !== undefined && q.active !== '') {
      params.push(q.active === 'true' || q.active === true);
      wheres.push(`r.active = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `${ROUTE_SELECT} WHERE ${wheres.join(' AND ')} GROUP BY r.id ORDER BY r.id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(routeDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.post('/logistics/routes', async (req, reply) => {
    const b = body(req.body);
    const name = stringField(b.name, 'name');
    const kind = enumField(b.kind, 'kind', ['fixed_line', 'on_demand'] as const);
    if (!Array.isArray(b.legs) || b.legs.length === 0) {
      throw badRequest('legs debe ser un array no vacío de enlaces');
    }
    const legIds = b.legs.map((l, i) => uuidField(l, `legs[${i}]`));
    const me = req.account!.id;
    const row = await withSerializable(app.pool, async (client) => {
      await assertContiguousLegs(client, legIds);
      const ins = await client.query(
        `INSERT INTO world.routes (owner_account_id, name, kind) VALUES ($1, $2, $3) RETURNING id`,
        [me, name, kind],
      );
      const routeId = ins.rows[0]!.id as string;
      for (let i = 0; i < legIds.length; i++) {
        await client.query(
          'INSERT INTO world.route_legs (route_id, leg_index, link_id) VALUES ($1, $2, $3)',
          [routeId, i, legIds[i]],
        );
      }
      const r = await client.query(`${ROUTE_SELECT} WHERE r.id = $1 GROUP BY r.id`, [routeId]);
      return r.rows[0]!;
    });
    await sendData(req, reply, routeDto(row), { status: 201 });
  });

  app.get('/logistics/routes/:routeId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).routeId, 'routeId');
    const r = await app.pool.query(`${ROUTE_SELECT} WHERE r.id = $1 GROUP BY r.id`, [id]);
    const route = r.rows[0];
    if (!route) throw notFound();
    if (route.owner_account_id !== req.account!.id) {
      throw forbidden('La ruta pertenece a otra corporación');
    }
    await sendData(req, reply, routeDto(route));
  });

  app.patch('/logistics/routes/:routeId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).routeId, 'routeId');
    const b = body(req.body);
    if (b.name === undefined && b.active === undefined && b.legs === undefined) {
      throw validation('El comando debe incluir name, active o legs', undefined, 400);
    }
    const me = req.account!.id;
    const row = await withSerializable(app.pool, async (client) => {
      const r = await client.query('SELECT * FROM world.routes WHERE id = $1 FOR UPDATE', [id]);
      const route = r.rows[0];
      if (!route) throw notFound();
      if (route.owner_account_id !== me) throw forbidden('La ruta pertenece a otra corporación');
      if (b.name !== undefined) {
        await client.query('UPDATE world.routes SET name = $2, updated_at = now() WHERE id = $1',
          [id, stringField(b.name, 'name')]);
      }
      if (b.active !== undefined) {
        if (typeof b.active !== 'boolean') throw badRequest('active debe ser booleano');
        await client.query('UPDATE world.routes SET active = $2, updated_at = now() WHERE id = $1',
          [id, b.active]);
      }
      if (b.legs !== undefined) {
        if (!Array.isArray(b.legs) || b.legs.length === 0) {
          throw badRequest('legs debe ser un array no vacío de enlaces');
        }
        const legIds = b.legs.map((l, i) => uuidField(l, `legs[${i}]`));
        await assertContiguousLegs(client, legIds);
        await client.query('DELETE FROM world.route_legs WHERE route_id = $1', [id]);
        for (let i = 0; i < legIds.length; i++) {
          await client.query(
            'INSERT INTO world.route_legs (route_id, leg_index, link_id) VALUES ($1, $2, $3)',
            [id, i, legIds[i]],
          );
        }
      }
      const out = await client.query(`${ROUTE_SELECT} WHERE r.id = $1 GROUP BY r.id`, [id]);
      return out.rows[0]!;
    });
    await sendData(req, reply, routeDto(row));
  });

  app.delete('/logistics/routes/:routeId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).routeId, 'routeId');
    const me = req.account!.id;
    await withSerializable(app.pool, async (client) => {
      const r = await client.query('SELECT owner_account_id FROM world.routes WHERE id = $1 FOR UPDATE', [id]);
      const route = r.rows[0];
      if (!route) throw notFound();
      if (route.owner_account_id !== me) throw forbidden('La ruta pertenece a otra corporación');
      // Los vehículos asignados quedan sin ruta.
      await client.query(
        'UPDATE world.vehicles SET route_id = NULL, route_leg_index = NULL WHERE route_id = $1',
        [id],
      );
      await client.query('DELETE FROM world.route_legs WHERE route_id = $1', [id]);
      await client.query('DELETE FROM world.routes WHERE id = $1', [id]);
    });
    await reply.status(204).send();
  });
}
