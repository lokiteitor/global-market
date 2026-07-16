/**
 * ARBITRAGEUR — Bot Arbitraje Sur.
 *
 * Observa el tablón global y la demanda de las ciudades:
 *
 *  - VENDE (acepta publicaciones `buy` ajenas) el stock que ya posee cuando
 *    el precio ofrecido > 1.2 × su precio medio de compra (base_price si no
 *    lo conoce). Al aceptar un `buy`, el vendedor debe entregar físicamente
 *    (auto-despacho del motor, ADR-IMPL-13).
 *  - COMPRA (acepta publicaciones `sell` ajenas) cuando unit_price <
 *    0.7 × current_price de la demanda de alguna ciudad para ese producto
 *    (retirada in situ: el stock queda suyo en ese almacén).
 *  - Exposición limitada: nunca más del 30% de su cash en una operación.
 *
 * `decideArbitrageur` y `updateAvgCost` son funciones PURAS (auditables).
 */

import { none } from "../actions.js";
import type { BotAction } from "../actions.js";
import { toBig } from "../actions.js";
import type { ApiClient } from "../client.js";
import type { Catalog } from "../catalog.js";
import type { CityDemand, LedgerAccount, Publication } from "../types.js";

export const ARBITRAGEUR_PARAMS = {
  /** Compra si unit_price × 10 < demanda × BUY_NUM (0.7×). */
  BUY_DISCOUNT_NUM: 7n,
  /** Vende si unit_price × 10 > coste × SELL_MARKUP_NUM (1.2×). */
  SELL_MARKUP_NUM: 12n,
  /** Exposición máxima por operación: cash × 3 / 10 (30%). */
  MAX_EXPOSURE_NUM: 3n,
  MAX_EXPOSURE_DEN: 10n,
} as const;

export interface BoardPublicationView {
  id: string;
  productId: string;
  publisherAccountId: string;
  unitPrice: bigint;
  quantityRemaining: bigint;
  minLot: bigint;
}

export interface ArbitrageurObservation {
  /** Cuenta propia (para descartar publicaciones propias). */
  myAccountId: string;
  /** Cash disponible. */
  cash: bigint;
  /** Publicaciones `sell` visibles del tablón. */
  sells: BoardPublicationView[];
  /** Publicaciones `buy` visibles del tablón (de ciudades u otros). */
  buys: BoardPublicationView[];
  /** Mejor current_price de demanda urbana por product_id. */
  bestDemandPrice: ReadonlyMap<string, bigint>;
  /** Stock libre propio por product_id (todas las ubicaciones). */
  stockFree: ReadonlyMap<string, bigint>;
  /** Precio medio de compra recordado por product_id (memoria del bot). */
  avgCost: ReadonlyMap<string, bigint>;
  /** base_price de catálogo por product_id (fallback de coste). */
  basePrice: ReadonlyMap<string, bigint>;
}

function minBig(a: bigint, b: bigint): bigint {
  return a < b ? a : b;
}

export function decideArbitrageur(obs: ArbitrageurObservation): BotAction {
  const P = ARBITRAGEUR_PARAMS;
  const maxSpend = (obs.cash * P.MAX_EXPOSURE_NUM) / P.MAX_EXPOSURE_DEN;

  // 1) Realizar beneficio: aceptar publicaciones `buy` ajenas con el stock propio.
  for (const pub of obs.buys) {
    if (pub.publisherAccountId === obs.myAccountId) continue;
    const held = obs.stockFree.get(pub.productId) ?? 0n;
    if (held <= 0n) continue;
    const cost = obs.avgCost.get(pub.productId) ?? obs.basePrice.get(pub.productId);
    if (cost === undefined) continue;
    // precio > 1.2 × coste  ⇔  precio×10 > coste×12 (aritmética entera)
    if (pub.unitPrice * 10n <= cost * P.SELL_MARKUP_NUM) continue;
    const qty = minBig(pub.quantityRemaining, held);
    if (qty < pub.minLot || qty <= 0n) continue;
    return {
      type: "accept_publication",
      publicationId: pub.id,
      quantity: qty,
      productId: pub.productId,
      unitPrice: pub.unitPrice,
      side: "selling",
    };
  }

  // 2) Comprar barato: aceptar publicaciones `sell` ajenas bajo el 70% de la
  //    mejor demanda urbana, sin superar el 30% del cash.
  for (const pub of obs.sells) {
    if (pub.publisherAccountId === obs.myAccountId) continue;
    const demand = obs.bestDemandPrice.get(pub.productId);
    if (demand === undefined) continue;
    // precio < 0.7 × demanda  ⇔  precio×10 < demanda×7
    if (pub.unitPrice * 10n >= demand * P.BUY_DISCOUNT_NUM) continue;
    if (pub.unitPrice <= 0n) continue;
    const affordable = maxSpend / pub.unitPrice;
    const qty = minBig(pub.quantityRemaining, affordable);
    if (qty < pub.minLot || qty <= 0n) continue;
    return {
      type: "accept_publication",
      publicationId: pub.id,
      quantity: qty,
      productId: pub.productId,
      unitPrice: pub.unitPrice,
      side: "buying",
    };
  }

  return none("sin oportunidades de arbitraje");
}

/**
 * Media ponderada del coste de compra (memoria por producto).
 * newAvg = (oldAvg×oldQty + price×qty) / (oldQty + qty), truncada.
 */
export function updateAvgCost(
  memory: Map<string, { avgCost: bigint; quantity: bigint }>,
  productId: string,
  unitPrice: bigint,
  quantity: bigint,
): void {
  const prev = memory.get(productId);
  if (prev === undefined || prev.quantity <= 0n) {
    memory.set(productId, { avgCost: unitPrice, quantity });
    return;
  }
  const totalQty = prev.quantity + quantity;
  const avgCost = (prev.avgCost * prev.quantity + unitPrice * quantity) / totalQty;
  memory.set(productId, { avgCost, quantity: totalQty });
}

function toView(p: Publication): BoardPublicationView | null {
  if (p.product_id === undefined) return null; // freight
  return {
    id: p.id,
    productId: p.product_id,
    publisherAccountId: p.publisher_account_id,
    unitPrice: toBig(p.unit_price),
    quantityRemaining: toBig(p.quantity_remaining),
    minLot: toBig(p.min_lot),
  };
}

/** Refresca el estado observable vía API pública (solo lecturas). */
export async function observeArbitrageur(
  client: ApiClient,
  catalog: Catalog,
  avgCostMemory: ReadonlyMap<string, { avgCost: bigint; quantity: bigint }>,
): Promise<ArbitrageurObservation> {
  const [cashRes, stockRes, sellsRes, buysRes] = await Promise.all([
    client.get<LedgerAccount[]>("/ledger/accounts", { kind: "cash" }),
    client.get<LedgerAccount[]>("/ledger/accounts", { kind: "stock_free", limit: 200 }),
    client.get<Publication[]>("/contracts/board", {
      kind: "sell",
      sort: "unit_price_asc",
      limit: 200,
    }),
    client.get<Publication[]>("/contracts/board", {
      kind: "buy",
      sort: "unit_price_desc",
      limit: 200,
    }),
  ]);

  const cash = cashRes.data.reduce((sum, a) => sum + toBig(a.balance), 0n);

  const stockFree = new Map<string, bigint>();
  for (const acc of stockRes.data) {
    if (acc.product_id === undefined) continue;
    stockFree.set(
      acc.product_id,
      (stockFree.get(acc.product_id) ?? 0n) + toBig(acc.balance),
    );
  }

  // Mejor precio de demanda urbana por producto (todas las ciudades del catálogo).
  const bestDemandPrice = new Map<string, bigint>();
  const demandLists = await Promise.all(
    [...catalog.cities.values()].map((city) =>
      client.get<CityDemand[]>(`/world/cities/${city.id}/demand`),
    ),
  );
  for (const list of demandLists) {
    for (const d of list.data) {
      const price = toBig(d.current_price);
      const prev = bestDemandPrice.get(d.product_id);
      if (prev === undefined || price > prev) bestDemandPrice.set(d.product_id, price);
    }
  }

  const basePrice = new Map<string, bigint>();
  for (const product of catalog.products.values()) {
    basePrice.set(product.id, toBig(product.base_price));
  }

  const avgCost = new Map<string, bigint>();
  for (const [productId, entry] of avgCostMemory) {
    avgCost.set(productId, entry.avgCost);
  }

  return {
    myAccountId: client.accountId,
    cash,
    sells: sellsRes.data.map(toView).filter((v): v is BoardPublicationView => v !== null),
    buys: buysRes.data.map(toView).filter((v): v is BoardPublicationView => v !== null),
    bestDemandPrice,
    stockFree,
    avgCost,
    basePrice,
  };
}
