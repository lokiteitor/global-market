// World (parte 2): edificios, inventario y producción.
import type { FastifyInstance } from 'fastify';
import type { DbClient } from '../db.js';
import { withSerializable } from '../db.js';
import { sendData } from '../lib/envelope.js';
import { buildingDto, inventoryItemDto, productionBatchDto } from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import {
  AppError,
  conflict,
  forbidden,
  mapPgError,
  notFound,
  validation,
} from '../lib/errors.js';
import {
  body,
  geoPolygonField,
  intField,
  isUuid,
  optionalEnumQuery,
  optionalUuidField,
  uuidField,
  uuidParam,
} from '../lib/validate.js';
import { ensureCashAccount, newUuid, postTransaction, sinkAccount } from '../lib/ledger.js';

const BUILDING_SELECT =
  'SELECT b.*, ST_AsGeoJSON(b.footprint) AS footprint_geojson FROM world.buildings b';

/**
 * ADR-IMPL-15 (documentado aquí como decisión de implementación del gateway):
 * al construir un edificio se crea automáticamente su conexión logística —
 * un nodo del grafo en el centroide de la huella (kind según el tipo:
 * 'mine' si el code contiene "mine", 'warehouse' si contiene "warehouse",
 * 'factory' en el resto) y un enlace 'road' al nodo YA EXISTENTE más cercano
 * (length_m por geografía, capacidad 60/h, velocidad base 60 km/h) con un
 * único link_segment (seq 0, congestion_ema 1). Sin esto, ningún edificio
 * nuevo sería alcanzable por la logística y el ciclo CCRI no cerraría.
 */
async function createLogisticsConnection(
  client: DbClient,
  buildingId: string,
  regionId: string,
  buildingTypeCode: string,
): Promise<void> {
  const kind = buildingTypeCode.includes('mine')
    ? 'mine'
    : buildingTypeCode.includes('warehouse')
      ? 'warehouse'
      : 'factory';

  // Nodo más cercano ANTES de insertar el nuevo (así no se encuentra a sí mismo).
  const nearest = await client.query(
    `SELECT id, location FROM world.network_nodes
      ORDER BY location <-> (SELECT ST_Centroid(footprint) FROM world.buildings WHERE id = $1)
      LIMIT 1`,
    [buildingId],
  );

  const node = await client.query(
    `INSERT INTO world.network_nodes (kind, region_id, building_id, location)
     SELECT $1::world.node_kind, $2, $3, ST_Centroid(footprint)
       FROM world.buildings WHERE id = $3
     RETURNING id`,
    [kind, regionId, buildingId],
  );
  const nodeId = node.rows[0]!.id as string;
  if (!nearest.rows[0]) return; // mundo sin red previa: nodo aislado

  const link = await client.query(
    `INSERT INTO world.network_links
       (mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
     SELECT 'road', $1, $2,
            ST_MakeLine(a.location, b.location),
            GREATEST(1, round(ST_Distance(a.location::geography, b.location::geography)))::int,
            60, 60
       FROM world.network_nodes a, world.network_nodes b
      WHERE a.id = $1 AND b.id = $2
     RETURNING id, length_m, path`,
    [nodeId, nearest.rows[0].id],
  );
  await client.query(
    `INSERT INTO world.link_segments (link_id, region_id, seq, portion, length_m, congestion_ema)
     VALUES ($1, $2, 0, $3, $4, 1)`,
    [link.rows[0]!.id, regionId, link.rows[0]!.path, link.rows[0]!.length_m],
  );
}

async function loadOwnedBuilding(
  app: FastifyInstance,
  client: DbClient | null,
  buildingId: string,
  accountId: string,
): Promise<Record<string, any>> {
  const runner = client ?? app.pool;
  const r = await runner.query(
    `${BUILDING_SELECT} WHERE b.id = $1${client ? ' FOR UPDATE OF b' : ''}`,
    [buildingId],
  );
  const b = r.rows[0];
  if (!b) throw notFound();
  if (b.owner_account_id !== accountId) {
    throw forbidden('El edificio pertenece a otra corporación');
  }
  return b;
}

export function registerBuildingRoutes(app: FastifyInstance): void {
  // ── Lista y detalle ─────────────────────────────────────────────────────
  app.get('/world/buildings', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const status = optionalEnumQuery(q.status, 'status', [
      'under_construction', 'operational', 'damaged', 'in_maintenance', 'abandoned', 'seized',
    ] as const);
    const buildingTypeId = optionalUuidField(q.building_type_id, 'building_type_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [req.account!.id];
    const wheres: string[] = ['b.owner_account_id = $1'];
    if (regionId) {
      params.push(regionId);
      wheres.push(`b.region_id = $${params.length}`);
    }
    if (status) {
      params.push(status);
      wheres.push(`b.status = $${params.length}`);
    }
    if (buildingTypeId) {
      params.push(buildingTypeId);
      wheres.push(`b.building_type_id = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `${BUILDING_SELECT} WHERE ${wheres.join(' AND ')} ORDER BY b.id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(buildingDto), {
      nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/world/buildings/:buildingId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    const b = await loadOwnedBuilding(app, null, id, req.account!.id);
    await sendData(req, reply, buildingDto(b));
  });

  // ── Construir ───────────────────────────────────────────────────────────
  app.post('/world/buildings', async (req, reply) => {
    const b = body(req.body);
    const buildingTypeId = uuidField(b.building_type_id, 'building_type_id');
    const concessionId = uuidField(b.concession_id, 'concession_id');
    const footprintJson = geoPolygonField(b.footprint, 'footprint');
    const me = req.account!.id;
    const sim = await app.getSim();

    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const conc = await client.query(
          `SELECT *, ST_Covers(parcel, ST_SetSRID(ST_GeomFromGeoJSON($2), 4326)) AS covered
             FROM world.land_concessions WHERE id = $1 FOR UPDATE`,
          [concessionId, footprintJson],
        );
        const c = conc.rows[0];
        if (!c) throw notFound('La concesión no existe');
        if (c.holder_account_id !== me) throw forbidden('La concesión pertenece a otra corporación');
        if (c.status === 'reverted' || Number(c.expires_at_sim) <= sim.simSeconds) {
          throw validation('La concesión no está vigente');
        }
        if (!c.covered) {
          throw new AppError(422, 'PLACEMENT_INVALID', 'La huella debe estar dentro de la parcela de la concesión');
        }
        const overlap = await client.query(
          `SELECT 1 FROM world.buildings
            WHERE ST_Intersects(footprint, ST_SetSRID(ST_GeomFromGeoJSON($1), 4326)) LIMIT 1`,
          [footprintJson],
        );
        if (overlap.rows[0]) {
          throw conflict('VALIDATION_ERROR', 'La huella se solapa con otro edificio');
        }
        const bt = await client.query('SELECT * FROM world.building_types WHERE id = $1', [buildingTypeId]);
        if (!bt.rows[0]) throw notFound('El tipo de edificio no existe');

        const buildingId = await newUuid(client);
        const cashId = await ensureCashAccount(client, me);
        const sinkId = await sinkAccount(client);
        const cost = BigInt(bt.rows[0].build_cost);
        await postTransaction(client, 'transfer', sim.simSeconds, buildingId,
          `Coste de construcción de ${bt.rows[0].code}`, [
            { accountId: cashId, amount: -cost },
            { accountId: sinkId, amount: cost },
          ]);
        const ins = await client.query(
          `INSERT INTO world.buildings
             (id, owner_account_id, region_id, concession_id, building_type_id, footprint, status, updated_at_sim)
           VALUES ($1, $2, $3, $4, $5, ST_SetSRID(ST_GeomFromGeoJSON($6), 4326), 'under_construction', $7)
           RETURNING *, ST_AsGeoJSON(footprint) AS footprint_geojson`,
          [buildingId, me, c.region_id, concessionId, buildingTypeId, footprintJson, sim.simSeconds],
        );
        await createLogisticsConnection(client, buildingId, c.region_id, bt.rows[0].code as string);
        return ins.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, buildingDto(row), { status: 201 });
  });

  // ── Configurar ──────────────────────────────────────────────────────────
  app.patch('/world/buildings/:buildingId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    const b = body(req.body);
    if (b.active_recipe_id === undefined && b.start_maintenance === undefined) {
      throw validation('El comando debe incluir active_recipe_id o start_maintenance', undefined, 400);
    }
    const me = req.account!.id;
    const sim = await app.getSim();
    const row = await withSerializable(app.pool, async (client) => {
      const building = await loadOwnedBuilding(app, client, id, me);
      if (b.active_recipe_id !== undefined) {
        if (b.active_recipe_id === null) {
          await client.query('UPDATE world.buildings SET active_recipe_id = NULL, updated_at_sim = $2 WHERE id = $1',
            [id, sim.simSeconds]);
        } else {
          if (!isUuid(b.active_recipe_id)) throw validation('active_recipe_id debe ser un UUID o null');
          const recipe = await client.query('SELECT building_type_id FROM world.recipes WHERE id = $1',
            [b.active_recipe_id]);
          if (!recipe.rows[0] || recipe.rows[0].building_type_id !== building.building_type_id) {
            throw validation('La receta no está soportada por el tipo de edificio');
          }
          // v1: el changeover_seconds no se modela — la receta aplica de inmediato.
          await client.query('UPDATE world.buildings SET active_recipe_id = $2, updated_at_sim = $3 WHERE id = $1',
            [id, b.active_recipe_id, sim.simSeconds]);
        }
      }
      if (b.start_maintenance === true) {
        // v1: el mantenimiento es instantáneo — el edificio queda 'operational'
        // con condition_pct = 100 (el ciclo de un día sim lo modelará el motor).
        await client.query(
          `UPDATE world.buildings SET status = 'operational', condition_pct = 100, updated_at_sim = $2 WHERE id = $1`,
          [id, sim.simSeconds],
        );
      }
      const r = await client.query(`${BUILDING_SELECT} WHERE b.id = $1`, [id]);
      return r.rows[0]!;
    });
    await sendData(req, reply, buildingDto(row));
  });

  // ── Mejorar nivel ───────────────────────────────────────────────────────
  app.post('/world/buildings/:buildingId/upgrade', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    const me = req.account!.id;
    const sim = await app.getSim();
    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const building = await loadOwnedBuilding(app, client, id, me);
        const bt = await client.query('SELECT max_level, build_cost FROM world.building_types WHERE id = $1',
          [building.building_type_id]);
        const maxLevel = Number(bt.rows[0]!.max_level);
        if (Number(building.level) >= maxLevel) {
          throw conflict('VALIDATION_ERROR', 'Nivel máximo ya alcanzado', { max_level: maxLevel });
        }
        // v1: coste no lineal = build_cost * 2^nivel_actual (la level_curve del
        // tipo aún no define costes explícitos por nivel).
        const cost = BigInt(bt.rows[0]!.build_cost) * (2n ** BigInt(building.level));
        const cashId = await ensureCashAccount(client, me);
        const sinkId = await sinkAccount(client);
        await postTransaction(client, 'transfer', sim.simSeconds, id,
          `Mejora de edificio a nivel ${Number(building.level) + 1}`, [
            { accountId: cashId, amount: -cost },
            { accountId: sinkId, amount: cost },
          ]);
        const upd = await client.query(
          `UPDATE world.buildings SET level = level + 1, updated_at_sim = $2 WHERE id = $1
           RETURNING *, ST_AsGeoJSON(footprint) AS footprint_geojson`,
          [id, sim.simSeconds],
        );
        return upd.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, buildingDto(row));
  });

  // ── Inventario físico (solo el dueño) ───────────────────────────────────
  app.get('/world/buildings/:buildingId/inventory', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    await loadOwnedBuilding(app, null, id, req.account!.id);
    const r = await app.pool.query(
      'SELECT * FROM world.building_inventories WHERE building_id = $1 ORDER BY product_id',
      [id],
    );
    await sendData(req, reply, r.rows.map(inventoryItemDto));
  });

  // ── Cola de producción ──────────────────────────────────────────────────
  app.get('/world/buildings/:buildingId/production-batches', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    await loadOwnedBuilding(app, null, id, req.account!.id);
    const q = req.query as Record<string, unknown>;
    const status = optionalEnumQuery(q.status, 'status', [
      'queued', 'running', 'paused_no_fuel', 'paused_no_workers', 'completed', 'cancelled',
    ] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const sim = await app.getSim();
    const params: unknown[] = [id];
    let where = 'pb.building_id = $1';
    if (status) {
      params.push(status);
      where += ` AND pb.status = $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT pb.*, r.batch_sim_seconds
         FROM world.production_batches pb JOIN world.recipes r ON r.id = pb.recipe_id
        WHERE ${where} ORDER BY pb.queue_position, pb.id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(
      req,
      reply,
      r.rows.slice(0, limit).map((row) =>
        productionBatchDto(row, sim.simSeconds, Number(row.batch_sim_seconds))),
      { nextCursor: r.rows.length > limit ? encodeCursor(offset + limit) : undefined },
    );
  });

  app.post('/world/buildings/:buildingId/production-batches', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).buildingId, 'buildingId');
    const b = body(req.body);
    const recipeId = uuidField(b.recipe_id, 'recipe_id');
    const batchesQueued = intField(b.batches_queued, 'batches_queued', 1);
    const me = req.account!.id;
    const sim = await app.getSim();
    const row = await withSerializable(app.pool, async (client) => {
      const building = await loadOwnedBuilding(app, client, id, me);
      if (building.status !== 'operational') {
        throw validation('El edificio no está operativo');
      }
      const recipe = await client.query('SELECT building_type_id FROM world.recipes WHERE id = $1', [recipeId]);
      if (!recipe.rows[0] || recipe.rows[0].building_type_id !== building.building_type_id) {
        throw validation('La receta no está soportada por el tipo de edificio');
      }
      // La orden entra al final de la cola viva del edificio.
      const pos = await client.query(
        `SELECT COALESCE(MAX(queue_position) + 1, 0) AS next FROM world.production_batches
          WHERE building_id = $1 AND status IN ('queued','running','paused_no_fuel','paused_no_workers')`,
        [id],
      );
      const ins = await client.query(
        `INSERT INTO world.production_batches
           (building_id, recipe_id, batches_queued, status, queue_position, updated_at_sim)
         VALUES ($1, $2, $3, 'queued', $4, $5) RETURNING *`,
        [id, recipeId, batchesQueued, pos.rows[0]!.next, sim.simSeconds],
      );
      return ins.rows[0]!;
    });
    await sendData(req, reply, productionBatchDto(row, sim.simSeconds), { status: 201 });
  });

  app.delete('/world/production-batches/:batchId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).batchId, 'batchId');
    const me = req.account!.id;
    const sim = await app.getSim();
    const row = await withSerializable(app.pool, async (client) => {
      const r = await client.query(
        `SELECT pb.*, b.owner_account_id FROM world.production_batches pb
           JOIN world.buildings b ON b.id = pb.building_id
          WHERE pb.id = $1 FOR UPDATE OF pb`,
        [id],
      );
      const batch = r.rows[0];
      if (!batch) throw notFound();
      if (batch.owner_account_id !== me) throw forbidden('La orden pertenece a otra corporación');
      if (batch.status === 'completed' || batch.status === 'cancelled') {
        throw conflict('VALIDATION_ERROR', 'La orden ya está completada o cancelada', {
          status: batch.status,
        });
      }
      // Lo ya producido queda asentado; solo se cancela lo pendiente.
      const upd = await client.query(
        `UPDATE world.production_batches SET status = 'cancelled', updated_at_sim = $2
          WHERE id = $1 RETURNING *`,
        [id, sim.simSeconds],
      );
      return upd.rows[0]!;
    });
    await sendData(req, reply, productionBatchDto(row, sim.simSeconds));
  });
}
