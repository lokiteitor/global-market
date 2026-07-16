// Notification/Event Gateway — WebSocket /ws (specs/ws-protocol.md, 1:1).
// Rooms corp:/viewport:/alerts:, snapshot inicial por SQL + patches derivados
// del polling de outbox.events (cursor 'notification_gateway'). Los payloads
// del outbox traen la entidad ya serializada como DTO REST (clave `entity`),
// así que los patch se construyen SIN releer la base.
import type { FastifyInstance } from 'fastify';
import type { WebSocket } from 'ws';
import { lookupSession } from '../plugins/auth.js';
import type { AuthAccount } from '../types.js';
import {
  buildingDto,
  cityDto,
  contractDto,
  ledgerAccountDto,
  publicationDto,
  shipmentDto,
  vehicleDto,
  vehicleSelectSql,
} from '../lib/dto.js';

type Json = Record<string, unknown>;

interface Conn {
  socket: WebSocket;
  account: AuthAccount | null;
  /** seq monotónico por (conexión, room); el snapshot fija la base 0. */
  rooms: Map<string, number>;
  viewportRoom: string | null;
  bbox: [number, number, number, number] | null;
}

const VIEWPORT_RE = /^viewport:(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)$/;
const UUID_RE = /^[0-9a-fA-F-]{36}$/;

export function registerWsGateway(app: FastifyInstance): void {
  const conns = new Set<Conn>();

  const send = (conn: Conn, frame: Json): void => {
    if (conn.socket.readyState === conn.socket.OPEN) {
      conn.socket.send(JSON.stringify(frame));
    }
  };

  const sendError = (conn: Conn, code: string, message: string): void => {
    send(conn, { type: 'error', code, message });
  };

  const nextSeq = (conn: Conn, room: string): number => {
    const seq = (conn.rooms.get(room) ?? -1) + 1;
    conn.rooms.set(room, seq);
    return seq;
  };

  // ── Snapshots por SQL ─────────────────────────────────────────────────────
  async function corpSnapshot(accountId: string, simSeconds: number): Promise<Json> {
    const pool = app.pool;
    const [buildings, vehicles, shipments, publications, contracts, accounts] = await Promise.all([
      pool.query(
        `SELECT *, ST_AsGeoJSON(footprint) AS footprint_geojson FROM world.buildings
          WHERE owner_account_id = $1 ORDER BY id`,
        [accountId],
      ),
      pool.query(`${vehicleSelectSql('$1')} WHERE v.owner_account_id = $2 ORDER BY v.id`, [
        simSeconds,
        accountId,
      ]),
      pool.query('SELECT * FROM world.shipments WHERE owner_account_id = $1 ORDER BY id', [accountId]),
      pool.query(
        `SELECT * FROM ledger.publications
          WHERE publisher_account_id = $1 AND status IN ('draw_window','open','micro_window')
          ORDER BY id`,
        [accountId],
      ),
      pool.query(
        `SELECT * FROM ledger.contracts
          WHERE buyer_account_id = $1 OR seller_account_id = $1 ORDER BY id`,
        [accountId],
      ),
      pool.query('SELECT * FROM ledger.accounts WHERE owner_account_id = $1 ORDER BY id', [accountId]),
    ]);
    return {
      buildings: buildings.rows.map(buildingDto),
      vehicles: vehicles.rows.map((r) => vehicleDto(r, simSeconds)),
      shipments: shipments.rows.map(shipmentDto),
      publications: publications.rows.map(publicationDto),
      contracts: contracts.rows.map(contractDto),
      ledger_accounts: accounts.rows.map(ledgerAccountDto),
    };
  }

  async function viewportSnapshot(
    bbox: [number, number, number, number],
    simSeconds: number,
  ): Promise<Json> {
    const pool = app.pool;
    const env = [bbox[0], bbox[1], bbox[2], bbox[3]];
    const [cities, buildings, vehicles] = await Promise.all([
      pool.query(
        `SELECT *, ST_AsGeoJSON(location) AS location_geojson FROM world.cities
          WHERE ST_Intersects(location, ST_MakeEnvelope($1, $2, $3, $4, 4326))`,
        env,
      ),
      pool.query(
        `SELECT *, ST_AsGeoJSON(footprint) AS footprint_geojson FROM world.buildings
          WHERE ST_Intersects(footprint, ST_MakeEnvelope($1, $2, $3, $4, 4326))`,
        env,
      ),
      // Vehículos: detenidos en un nodo del bbox, o cuyo segmento interseca el bbox.
      pool.query(
        `${vehicleSelectSql('$5')}
          WHERE (v.at_node_id IS NOT NULL AND ST_Intersects(n.location, ST_MakeEnvelope($1, $2, $3, $4, 4326)))
             OR (v.on_segment_id IS NOT NULL AND ST_Intersects(s.portion, ST_MakeEnvelope($1, $2, $3, $4, 4326)))`,
        [...env, simSeconds],
      ),
    ]);
    return {
      cities: cities.rows.map(cityDto),
      buildings: buildings.rows.map(buildingDto),
      vehicles: vehicles.rows.map((r) => vehicleDto(r, simSeconds)),
    };
  }

  async function handleJoin(conn: Conn, room: unknown): Promise<void> {
    if (typeof room !== 'string') {
      sendError(conn, 'VALIDATION_ERROR', 'room inválida');
      return;
    }
    const me = conn.account!;
    const sim = await app.getSim();
    if (room.startsWith('corp:')) {
      const target = room.slice(5);
      if (!UUID_RE.test(target) || target !== me.id) {
        sendError(conn, 'FORBIDDEN', 'Solo puedes unirte a tu propia room corp:');
        return;
      }
      const data = await corpSnapshot(me.id, sim.simSeconds);
      conn.rooms.set(room, 0);
      send(conn, { type: 'snapshot', room, seq: 0, sim_seconds: sim.simSeconds, data });
      return;
    }
    if (room.startsWith('alerts:')) {
      const target = room.slice(7);
      if (!UUID_RE.test(target) || target !== me.id) {
        sendError(conn, 'FORBIDDEN', 'Solo puedes unirte a tus propias alertas');
        return;
      }
      // La room de alertas no tiene snapshot de estado (solo `message`).
      conn.rooms.set(room, 0);
      return;
    }
    const vm = VIEWPORT_RE.exec(room);
    if (vm) {
      const bbox: [number, number, number, number] = [
        Number(vm[1]), Number(vm[2]), Number(vm[3]), Number(vm[4]),
      ];
      // Un join a viewport: REEMPLAZA cualquier viewport anterior de la conexión.
      if (conn.viewportRoom && conn.viewportRoom !== room) {
        conn.rooms.delete(conn.viewportRoom);
      }
      conn.viewportRoom = room;
      conn.bbox = bbox;
      const data = await viewportSnapshot(bbox, sim.simSeconds);
      conn.rooms.set(room, 0);
      send(conn, { type: 'snapshot', room, seq: 0, sim_seconds: sim.simSeconds, data });
      return;
    }
    sendError(conn, 'VALIDATION_ERROR', `room desconocida: ${room}`);
  }

  // ── Ruta WS ───────────────────────────────────────────────────────────────
  app.get('/ws', { websocket: true, config: { public: true } }, (socket, _req) => {
    const conn: Conn = { socket, account: null, rooms: new Map(), viewportRoom: null, bbox: null };
    conns.add(conn);
    socket.on('close', () => conns.delete(conn));
    socket.on('error', () => conns.delete(conn));

    socket.on('message', (raw: Buffer | string) => {
      void (async () => {
        let frame: Json;
        try {
          frame = JSON.parse(String(raw)) as Json;
        } catch {
          sendError(conn, 'VALIDATION_ERROR', 'Frame JSON inválido');
          return;
        }
        const type = frame.type;
        if (!conn.account) {
          // Cualquier frame previo a hello distinto de hello → error y cierre.
          if (type !== 'hello') {
            sendError(conn, 'NOT_AUTHENTICATED', 'El primer frame debe ser hello');
            socket.close(4401, 'NOT_AUTHENTICATED');
            return;
          }
          const token = typeof frame.token === 'string' ? frame.token : '';
          const session = token ? await lookupSession(app.pool, token) : null;
          if (!session) {
            sendError(conn, 'UNAUTHORIZED', 'Token de sesión inválido');
            socket.close(4401, 'UNAUTHORIZED');
            return;
          }
          conn.account = session.account;
          const sim = await app.getSim();
          send(conn, {
            type: 'hello_ok',
            account: { id: conn.account.id, name: conn.account.name, kind: conn.account.kind },
            sim: { sim_seconds: sim.simSeconds, frozen: sim.frozen },
            server_time: new Date().toISOString(),
          });
          return;
        }
        switch (type) {
          case 'hello':
            sendError(conn, 'VALIDATION_ERROR', 'La conexión ya está autenticada');
            return;
          case 'join':
            await handleJoin(conn, frame.room);
            return;
          case 'leave': {
            if (typeof frame.room === 'string') {
              conn.rooms.delete(frame.room);
              if (conn.viewportRoom === frame.room) {
                conn.viewportRoom = null;
                conn.bbox = null;
              }
            }
            return;
          }
          case 'ping': {
            const sim = await app.getSim();
            send(conn, { type: 'pong', t: frame.t, sim_seconds: sim.simSeconds, frozen: sim.frozen });
            return;
          }
          default:
            sendError(conn, 'VALIDATION_ERROR', `Frame desconocido: ${String(type)}`);
        }
      })().catch((err) => {
        app.log.error({ err }, 'ws: error procesando frame');
        sendError(conn, 'INTERNAL', 'Error interno');
      });
    });
  });

  // ── Difusión de eventos ──────────────────────────────────────────────────
  const patchToRoom = (room: string, entityName: string, entity: Json, simSeconds: number): void => {
    for (const conn of conns) {
      if (!conn.rooms.has(room)) continue;
      const seq = nextSeq(conn, room);
      send(conn, {
        type: 'patch',
        room,
        seq,
        sim_seconds: simSeconds,
        ops: [{ op: 'upsert', entity: entityName, id: entity.id, data: entity }],
      });
    }
  };

  const patchToViewports = (
    entityName: string,
    entity: Json,
    location: { lon: number; lat: number },
    simSeconds: number,
  ): void => {
    for (const conn of conns) {
      if (!conn.viewportRoom || !conn.bbox || !conn.rooms.has(conn.viewportRoom)) continue;
      const [minLon, minLat, maxLon, maxLat] = conn.bbox;
      if (location.lon < minLon || location.lon > maxLon || location.lat < minLat || location.lat > maxLat) {
        continue;
      }
      const room = conn.viewportRoom;
      const seq = nextSeq(conn, room);
      send(conn, {
        type: 'patch',
        room,
        seq,
        sim_seconds: simSeconds,
        ops: [{ op: 'upsert', entity: entityName, id: entity.id, data: entity }],
      });
    }
  };

  const messageToAlerts = (accountId: string, event: string, data: Json, simSeconds: number): void => {
    const room = `alerts:${accountId}`;
    for (const conn of conns) {
      if (!conn.rooms.has(room)) continue;
      send(conn, { type: 'message', room, event, sim_seconds: simSeconds, data });
    }
  };

  const broadcastMessage = (event: string, data: Json, simSeconds: number): void => {
    for (const conn of conns) {
      if (!conn.account) continue;
      send(conn, { type: 'message', room: 'world', event, sim_seconds: simSeconds, data });
    }
  };

  /** Campos propios del evento (payload sin entity/location) para `message`. */
  const messageData = (payload: Json, aggregateId: string): Json => {
    const { entity: _e, location: _l, ...rest } = payload;
    return Object.keys(rest).length > 0 ? (rest as Json) : { id: aggregateId };
  };

  function dispatchEvent(evt: {
    event_type: string;
    aggregate_id: string;
    payload: Json;
    sim_time_at: unknown;
  }): void {
    const payload = evt.payload ?? {};
    const entity = (payload.entity ?? {}) as Json;
    const location = payload.location as { lon: number; lat: number } | undefined;
    const sim = Number(evt.sim_time_at);
    const type = evt.event_type;

    switch (type) {
      case 'publication.created':
      case 'publication.cancelled':
      case 'publication.window_closed':
      case 'publication.expired': {
        const publisher = entity.publisher_account_id as string | undefined;
        if (publisher) patchToRoom(`corp:${publisher}`, 'publication', entity, sim);
        return;
      }
      case 'acceptance.resolved': {
        const acceptor = (entity.acceptor_account_id ?? payload.acceptor_account_id) as string | undefined;
        if (acceptor) messageToAlerts(acceptor, type, messageData(payload, evt.aggregate_id), sim);
        return;
      }
      case 'contract.confirmed':
      case 'contract.settled': {
        const buyer = entity.buyer_account_id as string | undefined;
        const seller = entity.seller_account_id as string | undefined;
        for (const party of new Set([buyer, seller])) {
          if (!party) continue;
          patchToRoom(`corp:${party}`, 'contract', entity, sim);
          messageToAlerts(party, type, messageData(payload, evt.aggregate_id), sim);
        }
        return;
      }
      case 'delivery.confirmed': {
        const buyer = entity.buyer_account_id as string | undefined;
        const seller = entity.seller_account_id as string | undefined;
        for (const party of new Set([buyer, seller])) {
          if (party) patchToRoom(`corp:${party}`, 'contract', entity, sim);
        }
        return;
      }
      case 'batch.completed':
      case 'batch.paused': {
        const owner = (payload.owner_account_id ?? entity.owner_account_id) as string | undefined;
        if (owner) patchToRoom(`corp:${owner}`, 'production_batch', entity, sim);
        return;
      }
      case 'vehicle.departed':
      case 'vehicle.segment_entered':
      case 'vehicle.arrived':
      case 'vehicle.broken':
      case 'vehicle.repaired': {
        const owner = entity.owner_account_id as string | undefined;
        if (owner) patchToRoom(`corp:${owner}`, 'vehicle', entity, sim);
        if (location) patchToViewports('vehicle', entity, location, sim);
        return;
      }
      case 'building.status_changed': {
        const owner = entity.owner_account_id as string | undefined;
        if (owner) patchToRoom(`corp:${owner}`, 'building', entity, sim);
        if (location) patchToViewports('building', entity, location, sim);
        return;
      }
      case 'city.level_changed':
      case 'city.demand_updated': {
        if (location) patchToViewports('city', entity, location, sim);
        broadcastMessage(type, messageData(payload, evt.aggregate_id), sim);
        return;
      }
      case 'sim.frozen':
      case 'sim.resumed': {
        broadcastMessage(type, messageData(payload, evt.aggregate_id), sim);
        return;
      }
      default:
        return; // evento fuera del catálogo v1: se ignora (el cursor avanza)
    }
  }

  // ── Poller del outbox (cursor 'notification_gateway', cada 1 s) ──────────
  let polling = false;
  const poll = async (): Promise<void> => {
    if (polling) return;
    polling = true;
    try {
      await app.pool.query(
        `INSERT INTO outbox.consumer_cursors (consumer_name) VALUES ('notification_gateway')
         ON CONFLICT (consumer_name) DO NOTHING`,
      );
      const cur = await app.pool.query(
        `SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = 'notification_gateway'`,
      );
      const lastSeq = Number(cur.rows[0]?.last_seq ?? 0);
      const events = await app.pool.query(
        `SELECT seq, event_type, aggregate_type, aggregate_id, payload, sim_time_at
           FROM outbox.events WHERE seq > $1 ORDER BY seq LIMIT 500`,
        [lastSeq],
      );
      if (events.rows.length === 0) return;
      for (const evt of events.rows) {
        try {
          dispatchEvent(evt);
        } catch (err) {
          app.log.error({ err, seq: evt.seq }, 'ws: error difundiendo evento outbox');
        }
      }
      const newSeq = Number(events.rows[events.rows.length - 1]!.seq);
      await app.pool.query(
        `UPDATE outbox.consumer_cursors SET last_seq = $1, updated_at = now()
          WHERE consumer_name = 'notification_gateway' AND last_seq < $1`,
        [newSeq],
      );
    } catch (err) {
      app.log.error({ err }, 'ws: fallo en el polling del outbox');
    } finally {
      polling = false;
    }
  };
  const timer = setInterval(() => void poll(), app.cfg.outboxPollMs);

  app.addHook('onClose', async () => {
    clearInterval(timer);
    for (const conn of conns) {
      try {
        conn.socket.close(1001, 'server shutdown');
      } catch {
        /* ignorado */
      }
    }
    conns.clear();
  });
}
