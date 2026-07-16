// Contract Service (CCRI): tablón, publicaciones, aceptaciones y lecturas de
// contratos. El gateway ejecuta el camino de comando en SERIALIZABLE
// (ADR-IMPL-03); los sorteos y liquidaciones los hace el motor Go.
import type { FastifyInstance } from 'fastify';
import { withSerializable } from '../db.js';
import { sendData } from '../lib/envelope.js';
import {
  acceptanceDto,
  contractDeliveryDto,
  contractDto,
  publicationDto,
} from '../lib/dto.js';
import { decodeCursor, encodeCursor, parseLimit } from '../lib/cursor.js';
import {
  AppError,
  conflict,
  forbidden,
  insufficient,
  mapPgError,
  notFound,
  validation,
} from '../lib/errors.js';
import { guaranteeTenPct, parseAmount, parsePositiveAmount } from '../lib/money.js';
import {
  body,
  enumField,
  optionalEnumQuery,
  optionalSimTimeQuery,
  optionalUuidField,
  simTimeField,
  uuidField,
  uuidParam,
} from '../lib/validate.js';
import {
  createMirrorAccount,
  emitOutbox,
  ensureCashAccount,
  ensureStockFreeAccount,
  newUuid,
  postTransaction,
} from '../lib/ledger.js';

const VISIBLE_STATUSES = ['draw_window', 'open', 'micro_window'];

export function registerContractRoutes(app: FastifyInstance): void {
  // ── Tablón global (pull con filtros) ────────────────────────────────────
  app.get('/contracts/board', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const kind = optionalEnumQuery(q.kind, 'kind', ['sell', 'buy', 'freight'] as const);
    const productId = optionalUuidField(q.product_id, 'product_id');
    const originRegionId = optionalUuidField(q.origin_region_id, 'origin_region_id');
    const destRegionId = optionalUuidField(q.destination_region_id, 'destination_region_id');
    const maxDelivery = optionalSimTimeQuery(q.max_delivery_sim_seconds, 'max_delivery_sim_seconds');
    const sort =
      optionalEnumQuery(q.sort, 'sort', [
        'unit_price_asc',
        'unit_price_desc',
        'published_at_desc',
        'deadline_asc',
      ] as const) ?? 'unit_price_asc';
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);

    const params: unknown[] = [];
    const wheres: string[] = [
      `p.channel = 'board'`,
      `p.status IN ('draw_window','open','micro_window')`,
    ];
    const add = (clause: string, value: unknown): void => {
      params.push(value);
      wheres.push(clause.replace('?', `$${params.length}`));
    };
    if (kind) add('p.kind = ?', kind);
    if (productId) add('p.product_id = ?', productId);
    if (originRegionId) add('onode.region_id = ?', originRegionId);
    if (destRegionId) add('dnode.region_id = ?', destRegionId);
    // Los precios se comparan numéricamente (unit_price es BIGINT en la base).
    if (q.max_unit_price !== undefined && q.max_unit_price !== '') {
      add('p.unit_price <= ?', parseAmount(q.max_unit_price, 'max_unit_price').toString());
    }
    if (q.min_unit_price !== undefined && q.min_unit_price !== '') {
      add('p.unit_price >= ?', parseAmount(q.min_unit_price, 'min_unit_price').toString());
    }
    if (q.min_quantity_remaining !== undefined && q.min_quantity_remaining !== '') {
      add(
        'p.quantity_remaining >= ?',
        parseAmount(q.min_quantity_remaining, 'min_quantity_remaining').toString(),
      );
    }
    if (maxDelivery !== undefined) add('p.delivery_sim_seconds <= ?', maxDelivery);

    const orderBy = {
      unit_price_asc: 'p.unit_price ASC, p.id ASC',
      unit_price_desc: 'p.unit_price DESC, p.id ASC',
      published_at_desc: 'p.published_at_sim DESC, p.id DESC',
      deadline_asc: 'p.delivery_sim_seconds ASC, p.id ASC',
    }[sort];

    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT p.* FROM ledger.publications p
         LEFT JOIN world.network_nodes onode ON onode.id = p.origin_node_id
         LEFT JOIN world.network_nodes dnode ON dnode.id = p.destination_node_id
        WHERE ${wheres.join(' AND ')}
        ORDER BY ${orderBy}
        LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    const hasMore = r.rows.length > limit;
    await sendData(req, reply, r.rows.slice(0, limit).map(publicationDto), {
      nextCursor: hasMore ? encodeCursor(offset + limit) : undefined,
    });
  });

  // ── Publicar (garantía propia bloqueada íntegramente en el mismo acto) ──
  app.post('/contracts/publications', async (req, reply) => {
    const b = body(req.body);
    const kind = enumField(b.kind, 'kind', ['sell', 'buy', 'freight'] as const);
    if (kind === 'freight') {
      throw validation('CCRI-Flete se activa en Fase 2');
    }
    const channel = b.channel === undefined ? 'board' : enumField(b.channel, 'channel', ['board', 'private'] as const);
    const counterparty =
      channel === 'private'
        ? uuidField(b.counterparty_account_id, 'counterparty_account_id')
        : optionalUuidField(b.counterparty_account_id, 'counterparty_account_id') ?? null;
    const productId = uuidField(b.product_id, 'product_id');
    const quantity = parsePositiveAmount(b.quantity_total, 'quantity_total');
    const unitPrice = parsePositiveAmount(b.unit_price, 'unit_price');
    const minLot = b.min_lot === undefined ? 1n : parsePositiveAmount(b.min_lot, 'min_lot');
    const deliverySimSeconds = simTimeField(b.delivery_sim_seconds, 'delivery_sim_seconds');
    const me = req.account!.id;
    const sim = await app.getSim();
    const insufficientCode = kind === 'sell' ? 'INSUFFICIENT_COLLATERAL' : 'INSUFFICIENT_FUNDS';

    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const pubId = await newUuid(client);
        const cashId = await ensureCashAccount(client, me);
        let stockReserveId: string | null = null;
        let guaranteeId: string | null = null;
        let escrowId: string | null = null;
        let originNodeId: string | null = null;
        let destNodeId: string | null = null;

        if (kind === 'sell') {
          originNodeId = uuidField(b.origin_node_id, 'origin_node_id');
          const node = await client.query(
            'SELECT id, building_id FROM world.network_nodes WHERE id = $1',
            [originNodeId],
          );
          if (!node.rows[0]) throw notFound('El nodo de origen no existe');
          const warehouseId = node.rows[0].building_id as string | null;
          if (!warehouseId) {
            throw validation('El nodo de origen no tiene almacén asociado (el stock debe existir físicamente)');
          }
          // El stock libre del publicador en ese almacén debe cubrir la publicación.
          const free = await client.query(
            `SELECT id, balance FROM ledger.accounts
              WHERE kind = 'stock_free' AND owner_account_id = $1
                AND product_id = $2 AND warehouse_building_id = $3`,
            [me, productId, warehouseId],
          );
          const available = free.rows[0] ? BigInt(free.rows[0].balance) : 0n;
          if (available < quantity) {
            throw insufficient('INSUFFICIENT_COLLATERAL', 'La garantía disponible no cubre la publicación solicitada', {
              required: quantity.toString(),
              available: available.toString(),
            });
          }
          const stockFreeId = free.rows[0]!.id as string;
          stockReserveId = await createMirrorAccount(client, 'stock_reserved', me, pubId, productId, warehouseId);
          guaranteeId = await createMirrorAccount(client, 'guarantee', me, pubId);
          const guarantee = guaranteeTenPct(quantity * unitPrice);
          await postTransaction(client, 'publication_lock', sim.simSeconds, pubId,
            'Bloqueo de garantía propia al publicar (venta)', [
              { accountId: stockFreeId, amount: -quantity },
              { accountId: stockReserveId, amount: quantity },
              { accountId: cashId, amount: -guarantee },
              { accountId: guaranteeId, amount: guarantee },
            ]);
        } else {
          destNodeId = uuidField(b.destination_node_id, 'destination_node_id');
          const node = await client.query('SELECT id FROM world.network_nodes WHERE id = $1', [destNodeId]);
          if (!node.rows[0]) throw notFound('El nodo de destino no existe');
          escrowId = await createMirrorAccount(client, 'escrow', me, pubId);
          const escrow = quantity * unitPrice;
          await postTransaction(client, 'publication_lock', sim.simSeconds, pubId,
            'Bloqueo del pago en escrow al publicar (compra)', [
              { accountId: cashId, amount: -escrow },
              { accountId: escrowId, amount: escrow },
            ]);
        }

        const ins = await client.query(
          `INSERT INTO ledger.publications
             (id, kind, publisher_account_id, channel, counterparty_account_id, product_id,
              quantity_total, quantity_remaining, unit_price, min_lot,
              origin_node_id, destination_node_id, delivery_sim_seconds, status,
              window_closes_at, cancel_cooldown_until,
              stock_reserve_account_id, guarantee_account_id, escrow_account_id, published_at_sim)
           VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $9, $10, $11, $12, 'draw_window',
                   now() + make_interval(secs => $13), now() + make_interval(secs => $14),
                   $15, $16, $17, $18)
           RETURNING *`,
          [
            pubId, kind, me, channel, counterparty, productId,
            quantity.toString(), unitPrice.toString(), minLot.toString(),
            originNodeId, destNodeId, deliverySimSeconds,
            app.cfg.drawWindowSeconds, app.cfg.cancelCooldownSeconds,
            stockReserveId, guaranteeId, escrowId, sim.simSeconds,
          ],
        );
        const pub = ins.rows[0]!;
        await emitOutbox(client, 'publication', pubId, 'publication.created',
          { entity: publicationDto(pub) }, sim.simSeconds);
        return pub;
      });
    } catch (err) {
      throw mapPgError(err, insufficientCode);
    }
    await sendData(req, reply, publicationDto(row), { status: 201 });
  });

  // ── Detalle de publicación ──────────────────────────────────────────────
  app.get('/contracts/publications/:publicationId', async (req, reply) => {
    const pubId = uuidParam((req.params as Record<string, unknown>).publicationId, 'publicationId');
    const r = await app.pool.query('SELECT * FROM ledger.publications WHERE id = $1', [pubId]);
    const pub = r.rows[0];
    if (!pub) throw notFound();
    const me = req.account!.id;
    const isParty = pub.publisher_account_id === me || pub.counterparty_account_id === me;
    if (pub.channel !== 'board' && !isParty) {
      throw forbidden('La publicación es privada');
    }
    await sendData(req, reply, publicationDto(pub));
  });

  // ── Cancelar publicación (cooldown anti-parpadeo) ───────────────────────
  app.delete('/contracts/publications/:publicationId', async (req, reply) => {
    const pubId = uuidParam((req.params as Record<string, unknown>).publicationId, 'publicationId');
    const me = req.account!.id;
    const sim = await app.getSim();
    const row = await withSerializable(app.pool, async (client) => {
      const r = await client.query('SELECT * FROM ledger.publications WHERE id = $1 FOR UPDATE', [pubId]);
      const pub = r.rows[0];
      if (!pub) throw notFound();
      if (pub.publisher_account_id !== me) {
        throw forbidden('Solo el publicador puede cancelar la publicación');
      }
      if (pub.cancel_cooldown_until && new Date(pub.cancel_cooldown_until).getTime() > Date.now()) {
        throw conflict('CANCEL_COOLDOWN_ACTIVE',
          'La publicación no puede cancelarse durante el cooldown anti-parpadeo',
          { cancel_cooldown_until: new Date(pub.cancel_cooldown_until).toISOString() });
      }
      if (!VISIBLE_STATUSES.includes(pub.status as string)) {
        throw conflict('PUBLICATION_EXHAUSTED', 'La publicación ya no es cancelable', {
          status: pub.status,
        });
      }

      // Libera los SALDOS ACTUALES de las cuentas espejo (la parte ya
      // convertida en contrato salió de ellas con contract_confirmation).
      const entries: { accountId: string; amount: bigint }[] = [];
      const release = async (mirrorId: string | null, target: () => Promise<string>): Promise<void> => {
        if (!mirrorId) return;
        const bal = await client.query('SELECT balance FROM ledger.accounts WHERE id = $1', [mirrorId]);
        const amount = bal.rows[0] ? BigInt(bal.rows[0].balance) : 0n;
        if (amount > 0n) {
          const targetId = await target();
          entries.push({ accountId: mirrorId, amount: -amount });
          entries.push({ accountId: targetId, amount });
        }
      };
      await release(pub.stock_reserve_account_id, async () => {
        const mirror = await client.query(
          'SELECT product_id, warehouse_building_id FROM ledger.accounts WHERE id = $1',
          [pub.stock_reserve_account_id],
        );
        return ensureStockFreeAccount(client, me,
          mirror.rows[0]!.product_id, mirror.rows[0]!.warehouse_building_id);
      });
      const cash = (): Promise<string> => ensureCashAccount(client, me);
      await release(pub.guarantee_account_id, cash);
      await release(pub.escrow_account_id, cash);
      if (entries.length > 0) {
        await postTransaction(client, 'publication_release', sim.simSeconds, pubId,
          'Cancelación de publicación: garantía restante liberada', entries);
      }

      // Las aceptaciones aún pendientes de sorteo pierden su ventana: se
      // libera su colateral (escrow / stock+garantía) y quedan 'released'.
      // Sin esto, el colateral del aceptante quedaría congelado para siempre:
      // el motor solo resuelve sorteos de publicaciones en draw/micro_window.
      const pend = await client.query(
        `SELECT * FROM ledger.publication_acceptances
          WHERE publication_id = $1 AND status = 'pending_draw' FOR UPDATE`,
        [pubId],
      );
      for (const acc of pend.rows) {
        const accEntries: { accountId: string; amount: bigint }[] = [];
        const releaseAcc = async (mirrorId: string | null): Promise<void> => {
          if (!mirrorId) return;
          const m = await client.query(
            `SELECT kind, product_id, warehouse_building_id, balance
               FROM ledger.accounts WHERE id = $1`,
            [mirrorId],
          );
          const mirror = m.rows[0];
          const amount = mirror ? BigInt(mirror.balance) : 0n;
          if (amount <= 0n) return;
          const targetId = mirror.kind === 'stock_reserved' && mirror.product_id
            ? await ensureStockFreeAccount(client, acc.acceptor_account_id,
                mirror.product_id, mirror.warehouse_building_id)
            : await ensureCashAccount(client, acc.acceptor_account_id);
          accEntries.push({ accountId: mirrorId, amount: -amount });
          accEntries.push({ accountId: targetId, amount });
        };
        await releaseAcc(acc.stock_reserve_account_id);
        await releaseAcc(acc.guarantee_account_id);
        await releaseAcc(acc.escrow_account_id);
        if (accEntries.length > 0) {
          await postTransaction(client, 'publication_release', sim.simSeconds, acc.id,
            'Cancelación de publicación: colateral de aceptación liberado', accEntries);
        }
        const updAcc = await client.query(
          `UPDATE ledger.publication_acceptances
              SET status = 'released', draw_order = 0, resolved_at = now()
            WHERE id = $1 RETURNING *`,
          [acc.id],
        );
        await emitOutbox(client, 'acceptance', acc.id, 'acceptance.resolved',
          { entity: acceptanceDto(updAcc.rows[0]!), acceptance_id: acc.id, status: 'released' },
          sim.simSeconds);
      }

      const upd = await client.query(
        `UPDATE ledger.publications SET status = 'cancelled', updated_at = now() WHERE id = $1 RETURNING *`,
        [pubId],
      );
      const updated = upd.rows[0]!;
      await emitOutbox(client, 'publication', pubId, 'publication.cancelled',
        { entity: publicationDto(updated) }, sim.simSeconds);
      return updated;
    });
    await sendData(req, reply, publicationDto(row));
  });

  // ── Aceptar (ventana de sorteo, ADR-011) ────────────────────────────────
  app.post('/contracts/publications/:publicationId/acceptances', async (req, reply) => {
    const pubId = uuidParam((req.params as Record<string, unknown>).publicationId, 'publicationId');
    const b = body(req.body);
    const quantity = parsePositiveAmount(b.quantity, 'quantity');
    const me = req.account!.id;
    const sim = await app.getSim();

    let row: Record<string, unknown>;
    try {
      row = await withSerializable(app.pool, async (client) => {
        const r = await client.query('SELECT * FROM ledger.publications WHERE id = $1 FOR UPDATE', [pubId]);
        const pub = r.rows[0];
        if (!pub) throw notFound();
        if (!VISIBLE_STATUSES.includes(pub.status as string)) {
          throw conflict('PUBLICATION_EXHAUSTED', 'La publicación está agotada, cancelada o expirada', {
            status: pub.status,
          });
        }
        if (pub.channel === 'private' && pub.counterparty_account_id !== me) {
          throw forbidden('La publicación privada solo admite a su contraparte', 'FORBIDDEN');
        }
        if (pub.publisher_account_id === me) {
          throw conflict('VALIDATION_ERROR', 'No puedes aceptar tu propia publicación');
        }
        if (quantity < BigInt(pub.min_lot)) {
          throw new AppError(422, 'BELOW_MIN_LOT', 'Lote menor al mínimo de aceptación', {
            min_lot: String(pub.min_lot),
            quantity: quantity.toString(),
          });
        }
        if (quantity > BigInt(pub.quantity_remaining)) {
          throw conflict('PUBLICATION_EXHAUSTED', 'La cantidad solicitada supera la restante', {
            quantity_remaining: String(pub.quantity_remaining),
          });
        }

        const accId = await newUuid(client);
        let stockReserveId: string | null = null;
        let guaranteeId: string | null = null;
        let escrowId: string | null = null;

        if (pub.kind === 'sell') {
          // El aceptante es comprador: bloquea el 100% del pago en escrow.
          escrowId = await createMirrorAccount(client, 'escrow', me, accId);
          const cashId = await ensureCashAccount(client, me);
          const escrow = quantity * BigInt(pub.unit_price);
          await postTransaction(client, 'acceptance_lock', sim.simSeconds, accId,
            'Bloqueo de escrow del aceptante (compra sobre venta publicada)', [
              { accountId: cashId, amount: -escrow },
              { accountId: escrowId, amount: escrow },
            ]);
        } else {
          // El aceptante es vendedor: elige su almacén con más stock_free
          // suficiente del producto y bloquea stock + garantía del 10%.
          const wh = await client.query(
            `SELECT id, warehouse_building_id, balance FROM ledger.accounts
              WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2
                AND warehouse_building_id IS NOT NULL AND balance >= $3
              ORDER BY balance DESC LIMIT 1`,
            [me, pub.product_id, quantity.toString()],
          );
          if (!wh.rows[0]) {
            throw insufficient('INSUFFICIENT_COLLATERAL',
              'Ningún almacén propio tiene stock libre suficiente del producto', {
                required: quantity.toString(),
              });
          }
          const stockFreeId = wh.rows[0].id as string;
          const warehouseId = wh.rows[0].warehouse_building_id as string;
          stockReserveId = await createMirrorAccount(client, 'stock_reserved', me, accId, pub.product_id, warehouseId);
          guaranteeId = await createMirrorAccount(client, 'guarantee', me, accId);
          const cashId = await ensureCashAccount(client, me);
          const guarantee = guaranteeTenPct(quantity * BigInt(pub.unit_price));
          await postTransaction(client, 'acceptance_lock', sim.simSeconds, accId,
            'Bloqueo de stock y garantía del aceptante (venta sobre compra publicada)', [
              { accountId: stockFreeId, amount: -quantity },
              { accountId: stockReserveId, amount: quantity },
              { accountId: cashId, amount: -guarantee },
              { accountId: guaranteeId, amount: guarantee },
            ]);
        }

        // Aceptar una publicación madura abre su micro-ventana (15–30 s).
        if (pub.status === 'open') {
          await client.query(
            `UPDATE ledger.publications
                SET status = 'micro_window',
                    window_closes_at = now() + make_interval(secs => $2),
                    updated_at = now()
              WHERE id = $1`,
            [pubId, app.cfg.microWindowSeconds],
          );
        }

        const ins = await client.query(
          `INSERT INTO ledger.publication_acceptances
             (id, publication_id, acceptor_account_id, quantity, status,
              stock_reserve_account_id, guarantee_account_id, escrow_account_id)
           VALUES ($1, $2, $3, $4, 'pending_draw', $5, $6, $7)
           RETURNING *`,
          [accId, pubId, me, quantity.toString(), stockReserveId, guaranteeId, escrowId],
        );
        return ins.rows[0]!;
      });
    } catch (err) {
      throw mapPgError(err, 'INSUFFICIENT_COLLATERAL');
    }
    await sendData(req, reply, acceptanceDto(row), { status: 201 });
  });

  // ── Resultado de una aceptación (solo el aceptante) ─────────────────────
  app.get('/contracts/acceptances/:acceptanceId', async (req, reply) => {
    const accId = uuidParam((req.params as Record<string, unknown>).acceptanceId, 'acceptanceId');
    const r = await app.pool.query('SELECT * FROM ledger.publication_acceptances WHERE id = $1', [accId]);
    const acc = r.rows[0];
    if (!acc) throw notFound();
    if (acc.acceptor_account_id !== req.account!.id) {
      throw forbidden('La aceptación solo es visible para el aceptante');
    }
    let contractId: string | undefined;
    if (acc.status === 'served') {
      // v1: el contrato resultante se localiza por (publicación, aceptante) —
      // no hay FK acceptance→contract en el esquema.
      const c = await app.pool.query(
        `SELECT id FROM ledger.contracts
          WHERE publication_id = $1 AND (buyer_account_id = $2 OR seller_account_id = $2)
          ORDER BY created_at DESC LIMIT 1`,
        [acc.publication_id, acc.acceptor_account_id],
      );
      contractId = c.rows[0]?.id;
    }
    await sendData(req, reply, acceptanceDto({ ...acc, contract_id: contractId ?? null }));
  });

  // ── Contratos propios ───────────────────────────────────────────────────
  app.get('/contracts/contracts', async (req, reply) => {
    const q = req.query as Record<string, unknown>;
    const role = optionalEnumQuery(q.role, 'role', ['buyer', 'seller'] as const);
    const status = optionalEnumQuery(q.status, 'status', ['active', 'settled', 'failed'] as const);
    const productId = optionalUuidField(q.product_id, 'product_id');
    const offset = decodeCursor(q.cursor);
    const limit = parseLimit(q.limit);
    const me = req.account!.id;

    const params: unknown[] = [me];
    let where =
      role === 'buyer'
        ? 'c.buyer_account_id = $1'
        : role === 'seller'
          ? 'c.seller_account_id = $1'
          : '(c.buyer_account_id = $1 OR c.seller_account_id = $1)';
    if (status) {
      params.push(status);
      where += ` AND c.status = $${params.length}`;
    }
    if (productId) {
      params.push(productId);
      where += ` AND c.product_id = $${params.length}`;
    }
    params.push(limit + 1, offset);
    const r = await app.pool.query(
      `SELECT c.* FROM ledger.contracts c WHERE ${where}
        ORDER BY c.id DESC LIMIT $${params.length - 1} OFFSET $${params.length}`,
      params,
    );
    const hasMore = r.rows.length > limit;
    await sendData(req, reply, r.rows.slice(0, limit).map(contractDto), {
      nextCursor: hasMore ? encodeCursor(offset + limit) : undefined,
    });
  });

  app.get('/contracts/contracts/:contractId', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).contractId, 'contractId');
    const r = await app.pool.query('SELECT * FROM ledger.contracts WHERE id = $1', [id]);
    const c = r.rows[0];
    if (!c) throw notFound();
    const me = req.account!.id;
    if (c.buyer_account_id !== me && c.seller_account_id !== me) {
      throw forbidden('El contrato solo es visible para sus partes');
    }
    await sendData(req, reply, contractDto(c));
  });

  app.get('/contracts/contracts/:contractId/deliveries', async (req, reply) => {
    const id = uuidParam((req.params as Record<string, unknown>).contractId, 'contractId');
    const r = await app.pool.query(
      'SELECT buyer_account_id, seller_account_id FROM ledger.contracts WHERE id = $1',
      [id],
    );
    const c = r.rows[0];
    if (!c) throw notFound();
    const me = req.account!.id;
    if (c.buyer_account_id !== me && c.seller_account_id !== me) {
      throw forbidden('Las entregas solo son visibles para las partes del contrato');
    }
    const d = await app.pool.query(
      'SELECT * FROM ledger.contract_deliveries WHERE contract_id = $1 ORDER BY delivered_at_sim, id',
      [id],
    );
    await sendData(req, reply, d.rows.map(contractDeliveryDto));
  });

  // ── CCRI-Flete (Fase 2): lista vacía + detalle 404 ──────────────────────
  app.get('/contracts/freight-contracts', async (req, reply) => {
    await sendData(req, reply, []);
  });

  app.get('/contracts/freight-contracts/:freightContractId', async (req) => {
    uuidParam((req.params as Record<string, unknown>).freightContractId, 'freightContractId');
    throw notFound('CCRI-Flete se activa en Fase 2');
  });
}
