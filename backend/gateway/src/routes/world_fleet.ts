// World (parte 3): flota, cargamentos y terminales.
import type { FastifyInstance } from 'fastify';
import { withSerializable } from '../db.js';
import { sendData } from '../lib/envelope.js';
import {
  shipmentDto,
  terminalDto,
  terminalSlotDto,
  vehicleDto,
  vehicleSelectSql,
  vehicleTypeDto,
} from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import {
  conflict,
  forbidden,
  mapPgError,
  notFound,
  validation,
} from '../lib/errors.js';
import {
  body,
  isUuid,
  optionalEnumQuery,
  optionalUuidField,
  uuidField,
  uuidParam,
} from '../lib/validate.js';
import { ensureCashAccount, postTransaction, sinkAccount } from '../lib/ledger.js';

export function registerFleetRoutes(app: FastifyInstance): void {
  // ── Catálogo de vehículos ───────────────────────────────────────────────
  app.get('/world/vehicle-types', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const mode = optionalEnumQuery(q.mode, 'mode', ['road', 'rail', 'sea'] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    let where = 'TRUE';
    if (mode) {
      params.push(mode);
      where = `mode = $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT * FROM world.vehicle_types WHERE ${where} ORDER BY code
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(vehicleTypeDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  // ── Flota propia ────────────────────────────────────────────────────────
  app.get('/world/vehicles', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const status = optionalEnumQuery(q.status, 'status', [
      'idle', 'loading', 'in_transit', 'unloading', 'broken', 'in_maintenance', 'sealed',
    ] as const);
    const routeId = optionalUuidField(q.route_id, 'route_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const sim = await app.getSim();
    const params: unknown[] = [sim.simSeconds, req.account!.id];
    const wheres: string[] = ['v.owner_account_id = $2'];
    if (status) {
      params.push(status);
      wheres.push(`v.status = $${params.length}`);
    }
    if (routeId) {
      params.push(routeId);
      wheres.push(`v.route_id = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `${vehicleSelectSql('$1')} WHERE ${wheres.join(' AND ')} ORDER BY v.id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map((row) => vehicleDto(row, sim.simSeconds)), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/world/vehicles/:vehicleId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).vehicleId, 'vehicleId');
    const sim = await app.getSim();
    const r = await app.pool.query(`${vehicleSelectSql('$1')} WHERE v.id = $2`, [sim.simSeconds, id]);
    const v = r.rows[0];
    if (!v) throw notFound();
    if (v.owner_account_id !== req.account!.id) {
      throw forbidden('El vehículo pertenece a otra corporación');
    }
    await sendData(req, reply, vehicleDto(v, sim.simSeconds));
  });

  // ── Comprar vehículo a catálogo ─────────────────────────────────────────
  app.post('/world/vehicles', async (req, reply) => {
    const b = body(req.body);
    const vehicleTypeId = uuidField(b.vehicle_type_id, 'vehicle_type_id');
    const deliveryNodeId = uuidField(b.delivery_node_id, 'delivery_node_id');
    const me = req.account!.id;
    const sim = await app.getSim();
    let vehicleId: string;
    try {
      vehicleId = await withSerializable(app.pool, async (client) => {
        const vt = await client.query('SELECT * FROM world.vehicle_types WHERE id = $1', [vehicleTypeId]);
        if (!vt.rows[0]) throw notFound('El tipo de vehículo no existe');
        const node = await client.query('SELECT id, kind FROM world.network_nodes WHERE id = $1', [deliveryNodeId]);
        if (!node.rows[0]) throw notFound('El nodo de entrega no existe');
        // v1: para modo 'road' cualquier nodo es entrega válida; rail/sea
        // exigirían estación/puerto (Fase 2).
        if (vt.rows[0].mode !== 'road' && !['station', 'port'].includes(node.rows[0].kind as string)) {
          throw validation('Nodo de entrega incompatible con el modo del vehículo');
        }
        const price = BigInt(vt.rows[0].purchase_price);
        const cashId = await ensureCashAccount(client, me);
        const sinkId = await sinkAccount(client);
        const ins = await client.query(
          `INSERT INTO world.vehicles
             (vehicle_type_id, owner_account_id, status, at_node_id, updated_at_sim)
           VALUES ($1, $2, 'idle', $3, $4) RETURNING id`,
          [vehicleTypeId, me, deliveryNodeId, sim.simSeconds],
        );
        const vid = ins.rows[0]!.id as string;
        await postTransaction(client, 'transfer', sim.simSeconds, vid,
          `Compra de vehículo ${vt.rows[0].code} a catálogo`, [
            { accountId: cashId, amount: -price },
            { accountId: sinkId, amount: price },
          ]);
        return vid;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    const sim2 = await app.getSim();
    const r = await app.pool.query(`${vehicleSelectSql('$1')} WHERE v.id = $2`, [sim2.simSeconds, vehicleId]);
    await sendData(req, reply, vehicleDto(r.rows[0]!, sim2.simSeconds), { status: 201 });
  });

  // ── Comandar vehículo ───────────────────────────────────────────────────
  app.patch('/world/vehicles/:vehicleId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).vehicleId, 'vehicleId');
    const b = body(req.body);
    if (b.route_id === undefined && b.schedule_maintenance === undefined) {
      throw validation('El comando debe incluir route_id o schedule_maintenance', undefined, 400);
    }
    const me = req.account!.id;
    const sim = await app.getSim();
    await withSerializable(app.pool, async (client) => {
      const r = await client.query('SELECT * FROM world.vehicles WHERE id = $1 FOR UPDATE', [id]);
      const v = r.rows[0];
      if (!v) throw notFound();
      if (v.owner_account_id !== me) throw forbidden('El vehículo pertenece a otra corporación');
      if (v.status === 'sealed') {
        // SELLADO durante handoff entre shards: visible pero no comandable.
        throw forbidden('El vehículo está SELLADO durante un handoff', 'VEHICLE_SEALED');
      }
      if (b.route_id !== undefined) {
        if (b.route_id === null) {
          await client.query(
            'UPDATE world.vehicles SET route_id = NULL, route_leg_index = NULL, updated_at_sim = $2 WHERE id = $1',
            [id, sim.simSeconds],
          );
        } else {
          if (!isUuid(b.route_id)) throw validation('route_id debe ser un UUID o null');
          const route = await client.query('SELECT owner_account_id FROM world.routes WHERE id = $1', [b.route_id]);
          if (!route.rows[0]) throw notFound('La ruta no existe');
          if (route.rows[0].owner_account_id !== me) {
            throw forbidden('La ruta pertenece a otra corporación');
          }
          await client.query(
            'UPDATE world.vehicles SET route_id = $2, route_leg_index = 0, updated_at_sim = $3 WHERE id = $1',
            [id, b.route_id, sim.simSeconds],
          );
        }
      }
      if (b.schedule_maintenance === true) {
        // v1: mantenimiento instantáneo — wear_pct a 0 y el vehículo sigue/queda
        // 'idle' (el paso por 'in_maintenance' de un día sim lo modelará el motor).
        await client.query(
          `UPDATE world.vehicles SET wear_pct = 0,
                  status = CASE WHEN status = 'in_maintenance' THEN 'idle'::world.vehicle_status ELSE status END,
                  updated_at_sim = $2
            WHERE id = $1`,
          [id, sim.simSeconds],
        );
      }
    });
    const r = await app.pool.query(`${vehicleSelectSql('$1')} WHERE v.id = $2`, [sim.simSeconds, id]);
    await sendData(req, reply, vehicleDto(r.rows[0]!, sim.simSeconds));
  });

  // ── Cargamentos ─────────────────────────────────────────────────────────
  app.get('/world/shipments', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const status = optionalEnumQuery(q.status, 'status', [
      'in_warehouse', 'in_transit', 'at_terminal', 'delivered', 'released_in_situ',
    ] as const);
    const contractId = optionalUuidField(q.contract_id, 'contract_id');
    const vehicleId = optionalUuidField(q.vehicle_id, 'vehicle_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [req.account!.id];
    const wheres: string[] = ['owner_account_id = $1'];
    if (status) {
      params.push(status);
      wheres.push(`status = $${params.length}`);
    }
    if (contractId) {
      params.push(contractId);
      wheres.push(`contract_id = $${params.length}`);
    }
    if (vehicleId) {
      params.push(vehicleId);
      wheres.push(`vehicle_id = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT * FROM world.shipments WHERE ${wheres.join(' AND ')} ORDER BY id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(shipmentDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/world/shipments/:shipmentId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).shipmentId, 'shipmentId');
    const r = await app.pool.query('SELECT * FROM world.shipments WHERE id = $1', [id]);
    const s = r.rows[0];
    if (!s) throw notFound();
    if (s.owner_account_id !== req.account!.id) {
      throw forbidden('El cargamento pertenece a otra corporación');
    }
    await sendData(req, reply, shipmentDto(s));
  });

  // ── Terminales (Fase 2: las tablas existen aunque vacías) ───────────────
  app.get('/world/terminals/:terminalId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).terminalId, 'terminalId');
    const r = await app.pool.query('SELECT * FROM world.terminals WHERE id = $1', [id]);
    if (!r.rows[0]) throw notFound();
    await sendData(req, reply, terminalDto(r.rows[0]));
  });

  app.get('/world/terminals/:terminalId/slots', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).terminalId, 'terminalId');
    const t = await app.pool.query('SELECT id FROM world.terminals WHERE id = $1', [id]);
    if (!t.rows[0]) throw notFound();
    const q = req.query as Record<string, unknown>;
    const onlyAvailable = q.only_available === 'true' || q.only_available === true;
    const r = await app.pool.query(
      `SELECT * FROM world.terminal_slots WHERE terminal_id = $1
        ${onlyAvailable ? 'AND holder_account_id IS NULL' : ''}
        ORDER BY priority_tier, id`,
      [id],
    );
    await sendData(req, reply, r.rows.map(terminalSlotDto));
  });

  app.post('/world/terminal-slots/:slotId/purchase', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).slotId, 'slotId');
    const me = req.account!.id;
    const sim = await app.getSim();
    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const r = await client.query(
          `SELECT ts.*, t.owner_account_id AS terminal_owner
             FROM world.terminal_slots ts JOIN world.terminals t ON t.id = ts.terminal_id
            WHERE ts.id = $1 FOR UPDATE OF ts`,
          [id],
        );
        const slot = r.rows[0];
        if (!slot) throw notFound();
        if (slot.holder_account_id) {
          throw conflict('VALIDATION_ERROR', 'El slot ya tiene titular vigente');
        }
        const price = BigInt(slot.price);
        if (price > 0n) {
          const buyerCash = await ensureCashAccount(client, me);
          const ownerCash = await ensureCashAccount(client, slot.terminal_owner);
          await postTransaction(client, 'transfer', sim.simSeconds, id,
            'Compra de slot de prioridad de terminal', [
              { accountId: buyerCash, amount: -price },
              { accountId: ownerCash, amount: price },
            ]);
        }
        const upd = await client.query(
          'UPDATE world.terminal_slots SET holder_account_id = $2 WHERE id = $1 RETURNING *',
          [id, me],
        );
        return upd.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, terminalSlotDto(row));
  });
}
