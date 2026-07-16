// World (parte 1): mundo estático, catálogos, ciudades, suelo (concesiones y
// traspasos). Edificios/producción y flota viven en world_buildings.ts y
// world_fleet.ts; se registran juntos como módulo world.
import type { FastifyInstance } from 'fastify';
import { withSerializable } from '../db.js';
import { sendData } from '../lib/envelope.js';
import {
  buildingTypeDto,
  cityDemandDto,
  cityDto,
  concessionDto,
  concessionTransferDto,
  depositDto,
  productDto,
  recipeDto,
  regionDto,
} from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import {
  AppError,
  badRequest,
  conflict,
  forbidden,
  mapPgError,
  notFound,
} from '../lib/errors.js';
import { parsePositiveAmount } from '../lib/money.js';
import {
  body,
  geoPolygonField,
  optionalEnumQuery,
  optionalUuidField,
  uuidField,
  uuidParam,
} from '../lib/validate.js';
import { ensureCashAccount, postTransaction, sinkAccount, newUuid } from '../lib/ledger.js';
import { registerBuildingRoutes } from './world_buildings.js';
import { registerFleetRoutes } from './world_fleet.js';

const SIM_DAY = 86_400;

function paged(offset: number, limit: number, rowsLen: number): string | undefined {
  return rowsLen > limit ? encodeCursor(offset + limit) : undefined;
}

export function registerWorldRoutes(app: FastifyInstance): void {
  // ── Regiones ────────────────────────────────────────────────────────────
  app.get('/world/regions', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const biome = optionalEnumQuery(q.biome, 'biome', [
      'plains', 'forest', 'desert', 'mountain', 'ocean', 'coast',
    ] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    let where = 'TRUE';
    if (biome) {
      params.push(biome);
      where = `biome = $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT *, ST_AsGeoJSON(bounds) AS bounds_geojson FROM world.regions
        WHERE ${where} ORDER BY grid_y, grid_x
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(regionDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  app.get('/world/regions/:regionId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).regionId, 'regionId');
    const r = await app.pool.query(
      'SELECT *, ST_AsGeoJSON(bounds) AS bounds_geojson FROM world.regions WHERE id = $1',
      [id],
    );
    if (!r.rows[0]) throw notFound();
    await sendData(req, reply, regionDto(r.rows[0]));
  });

  // ── Productos ───────────────────────────────────────────────────────────
  app.get('/world/products', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const cls = optionalEnumQuery(q.class, 'class', ['basic', 'luxury'] as const);
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (cls) {
      params.push(cls);
      wheres.push(`class = $${params.length}`);
    }
    if (q.is_fuel !== undefined && q.is_fuel !== '') {
      params.push(q.is_fuel === 'true' || q.is_fuel === true);
      wheres.push(`is_fuel = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT * FROM world.products WHERE ${wheres.join(' AND ')}
        ORDER BY code LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(productDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  // ── Tipos de edificio ───────────────────────────────────────────────────
  app.get('/world/building-types', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const r = await app.pool.query(
      'SELECT * FROM world.building_types ORDER BY code LIMIT $1 OFFSET $2',
      [limit + 1, offset],
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(buildingTypeDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  // ── Recetas (CON ingredients embebidos) ─────────────────────────────────
  app.get('/world/recipes', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const buildingTypeId = optionalUuidField(q.building_type_id, 'building_type_id');
    const productId = optionalUuidField(q.product_id, 'product_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (buildingTypeId) {
      params.push(buildingTypeId);
      wheres.push(`r.building_type_id = $${params.length}`);
    }
    if (productId) {
      params.push(productId);
      wheres.push(
        `r.id IN (SELECT recipe_id FROM world.recipe_ingredients WHERE product_id = $${params.length})`,
      );
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT r.*, COALESCE(
              json_agg(json_build_object('product_id', ri.product_id, 'role', ri.role,
                                         'quantity', ri.quantity::text)
                       ORDER BY ri.role, ri.product_id)
              FILTER (WHERE ri.recipe_id IS NOT NULL), '[]'::json) AS ingredients
         FROM world.recipes r
         LEFT JOIN world.recipe_ingredients ri ON ri.recipe_id = r.id
        WHERE ${wheres.join(' AND ')}
        GROUP BY r.id
        ORDER BY r.code LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(recipeDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  // ── Yacimientos ─────────────────────────────────────────────────────────
  app.get('/world/resource-deposits', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const productId = optionalUuidField(q.product_id, 'product_id');
    const onlyAvailable = q.only_available === undefined ? true : q.only_available !== 'false';
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (regionId) {
      params.push(regionId);
      wheres.push(`region_id = $${params.length}`);
    }
    if (productId) {
      params.push(productId);
      wheres.push(`product_id = $${params.length}`);
    }
    if (onlyAvailable) wheres.push('remaining_amount > 0');
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT *, ST_AsGeoJSON(location) AS location_geojson FROM world.resource_deposits
        WHERE ${wheres.join(' AND ')} ORDER BY id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(depositDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  // ── Ciudades y demanda ──────────────────────────────────────────────────
  app.get('/world/cities', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [];
    const wheres: string[] = ['TRUE'];
    if (regionId) {
      params.push(regionId);
      wheres.push(`region_id = $${params.length}`);
    }
    if (q.min_level !== undefined && q.min_level !== '') {
      const n = Number(q.min_level);
      if (!Number.isInteger(n) || n < 1) throw badRequest('min_level debe ser un entero >= 1');
      params.push(n);
      wheres.push(`level >= $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT *, ST_AsGeoJSON(location) AS location_geojson FROM world.cities
        WHERE ${wheres.join(' AND ')} ORDER BY name
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(cityDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  app.get('/world/cities/:cityId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).cityId, 'cityId');
    const r = await app.pool.query(
      'SELECT *, ST_AsGeoJSON(location) AS location_geojson FROM world.cities WHERE id = $1',
      [id],
    );
    if (!r.rows[0]) throw notFound();
    await sendData(req, reply, cityDto(r.rows[0]));
  });

  app.get('/world/cities/:cityId/demand', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).cityId, 'cityId');
    const q = req.query as Record<string, unknown>;
    const productId = optionalUuidField(q.product_id, 'product_id');
    const city = await app.pool.query('SELECT id, level FROM world.cities WHERE id = $1', [id]);
    if (!city.rows[0]) throw notFound();
    const params: unknown[] = [id];
    let where = 'city_id = $1';
    if (productId) {
      params.push(productId);
      where += ` AND product_id = $${params.length}`;
    }
    const r = await app.pool.query(
      `SELECT * FROM world.city_demand WHERE ${where} ORDER BY product_id`,
      params,
    );
    await sendData(req, reply, r.rows.map(cityDemandDto));
  });

  // ── Concesiones de suelo ────────────────────────────────────────────────
  const CONCESSION_SELECT =
    'SELECT *, ST_AsGeoJSON(parcel) AS parcel_geojson FROM world.land_concessions';

  app.get('/world/concessions', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const status = optionalEnumQuery(q.status, 'status', [
      'active', 'delinquent', 'grace', 'reverted',
    ] as const);
    const regionId = optionalUuidField(q.region_id, 'region_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const params: unknown[] = [req.account!.id];
    const wheres: string[] = ['holder_account_id = $1'];
    if (status) {
      params.push(status);
      wheres.push(`status = $${params.length}`);
    }
    if (regionId) {
      params.push(regionId);
      wheres.push(`region_id = $${params.length}`);
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `${CONCESSION_SELECT} WHERE ${wheres.join(' AND ')} ORDER BY id
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    await sendData(req, reply, r.rows.slice(0, limit).map(concessionDto), {
      nextCursor: paged(offset, limit, r.rows.length),
    });
  });

  app.get('/world/concessions/:concessionId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).concessionId, 'concessionId');
    const r = await app.pool.query(`${CONCESSION_SELECT} WHERE id = $1`, [id]);
    const c = r.rows[0];
    if (!c) throw notFound();
    if (c.holder_account_id !== req.account!.id) {
      throw forbidden('La concesión pertenece a otra corporación');
    }
    await sendData(req, reply, concessionDto(c));
  });

  app.post('/world/concessions', async (req, reply) => {
    const b = body(req.body);
    const regionId = uuidField(b.region_id, 'region_id');
    const parcelJson = geoPolygonField(b.parcel, 'parcel');
    const me = req.account!.id;
    const sim = await app.getSim();

    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const region = await client.query(
          `SELECT id, canon_base,
                  ST_Covers(bounds, ST_SetSRID(ST_GeomFromGeoJSON($2), 4326)) AS covered
             FROM world.regions WHERE id = $1`,
          [regionId, parcelJson],
        );
        if (!region.rows[0]) throw notFound('La región no existe');
        if (!region.rows[0].covered) {
          throw new AppError(422, 'PLACEMENT_INVALID', 'La parcela debe estar dentro de la región');
        }
        const overlap = await client.query(
          `SELECT 1 FROM world.land_concessions
            WHERE status <> 'reverted'
              AND ST_Intersects(parcel, ST_SetSRID(ST_GeomFromGeoJSON($1), 4326))
            LIMIT 1`,
          [parcelJson],
        );
        if (overlap.rows[0]) {
          throw conflict('VALIDATION_ERROR', 'La parcela solicitada se solapa con una concesión vigente');
        }
        const canon = BigInt(region.rows[0].canon_base);
        const concessionId = await newUuid(client);
        const cashId = await ensureCashAccount(client, me);
        const sinkId = await sinkAccount(client);
        await postTransaction(client, 'canon', sim.simSeconds, concessionId,
          'Primer canon de concesión de suelo (sink estructural)', [
            { accountId: cashId, amount: -canon },
            { accountId: sinkId, amount: canon },
          ]);
        const ins = await client.query(
          `INSERT INTO world.land_concessions
             (id, region_id, holder_account_id, parcel, canon_amount, period_sim_days,
              expires_at_sim, status, granted_at_sim)
           VALUES ($1, $2, $3, ST_SetSRID(ST_GeomFromGeoJSON($4), 4326), $5, 90, $6, 'active', $7)
           RETURNING *, ST_AsGeoJSON(parcel) AS parcel_geojson`,
          [concessionId, regionId, me, parcelJson, canon.toString(),
            sim.simSeconds + 90 * SIM_DAY, sim.simSeconds],
        );
        return ins.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, concessionDto(row), { status: 201 });
  });

  app.post('/world/concessions/:concessionId/renew', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).concessionId, 'concessionId');
    const me = req.account!.id;
    const sim = await app.getSim();
    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const r = await client.query(
          'SELECT * FROM world.land_concessions WHERE id = $1 FOR UPDATE', [id]);
        const c = r.rows[0];
        if (!c) throw notFound();
        if (c.holder_account_id !== me) throw forbidden('Solo el titular puede renovar la concesión');
        if (c.status === 'reverted') {
          throw conflict('VALIDATION_ERROR', 'La concesión ya está revertida al sistema');
        }
        const cashId = await ensureCashAccount(client, me);
        const sinkId = await sinkAccount(client);
        await postTransaction(client, 'canon', sim.simSeconds, id,
          'Renovación de concesión: canon del periodo', [
            { accountId: cashId, amount: -BigInt(c.canon_amount) },
            { accountId: sinkId, amount: BigInt(c.canon_amount) },
          ]);
        // Extiende desde el vencimiento vigente (o desde ahora si ya venció).
        const base = Math.max(Number(c.expires_at_sim), sim.simSeconds);
        const upd = await client.query(
          `UPDATE world.land_concessions
              SET expires_at_sim = $2, status = 'active', updated_at = now()
            WHERE id = $1 RETURNING *, ST_AsGeoJSON(parcel) AS parcel_geojson`,
          [id, base + Number(c.period_sim_days) * SIM_DAY],
        );
        return upd.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, concessionDto(row));
  });

  app.post('/world/concession-transfers', async (req, reply) => {
    const b = body(req.body);
    const concessionId = uuidField(b.concession_id, 'concession_id');
    const toAccountId = uuidField(b.to_account_id, 'to_account_id');
    const price = parsePositiveAmount(b.price, 'price');
    const me = req.account!.id;
    const sim = await app.getSim();

    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const r = await client.query(
          'SELECT * FROM world.land_concessions WHERE id = $1 FOR UPDATE', [concessionId]);
        const c = r.rows[0];
        if (!c) throw notFound('La concesión no existe');
        if (c.holder_account_id !== me) throw forbidden('Solo el titular puede traspasar la concesión');
        if (c.status === 'reverted') {
          throw conflict('VALIDATION_ERROR', 'La concesión ya está revertida al sistema');
        }
        const to = await client.query(
          `SELECT id FROM auth.accounts WHERE id = $1 AND status = 'active'`, [toAccountId]);
        if (!to.rows[0]) throw notFound('La cuenta destinataria no existe');
        if (toAccountId === me) throw badRequest('No puedes traspasarte la concesión a ti mismo');

        // v1: el titular ejecuta el traspaso — el precio lo paga la cuenta
        // destinataria al titular; la tasa del sistema (5% del precio) la paga
        // el titular al sink. Mercado secundario mínimo viable (GDD 11.1).
        const fee = (price * 5n) / 100n;
        const transferId = await newUuid(client);
        const fromCash = await ensureCashAccount(client, me);
        const toCash = await ensureCashAccount(client, toAccountId);
        const sinkId = await sinkAccount(client);
        await postTransaction(client, 'transfer', sim.simSeconds, transferId,
          'Traspaso de concesión: precio + tasa del sistema', [
            { accountId: toCash, amount: -price },
            { accountId: fromCash, amount: price },
            { accountId: fromCash, amount: -fee },
            { accountId: sinkId, amount: fee },
          ]);
        const ins = await client.query(
          `INSERT INTO world.concession_transfers
             (id, concession_id, from_account_id, to_account_id, price, system_fee, occurred_at_sim)
           VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *`,
          [transferId, concessionId, me, toAccountId, price.toString(), fee.toString(), sim.simSeconds],
        );
        await client.query(
          `UPDATE world.land_concessions SET holder_account_id = $2, updated_at = now() WHERE id = $1`,
          [concessionId, toAccountId],
        );
        return ins.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_FUNDS');
    }
    await sendData(req, reply, concessionTransferDto(row), { status: 201 });
  });

  registerBuildingRoutes(app);
  registerFleetRoutes(app);
}
