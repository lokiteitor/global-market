/**
 * PRODUCER — Bot Minero Norte.
 *
 * Estado deseado: 1 concesión sobre un yacimiento de iron_ore, 1 iron_mine
 * en ella, cola de producción mine_iron viva, coal para el combustible de la
 * receta, y venta del excedente de iron_ore.
 *
 * `decideProducer` es una función PURA de estado observable → acción
 * (auditable y testeable con estados sintéticos). Prioridad, de mayor a menor:
 *   1. Sin concesión           → obtener concesión ~0.01° alrededor del yacimiento.
 *   2. Sin mina                → construir iron_mine dentro de la parcela.
 *   3. coal < COAL_MIN         → publicar buy de coal (20 uds a base×1.1) hacia su nodo.
 *   4. Cola muerta             → encolar mine_iron ×5.
 *   5. iron_ore libre > 50     → publicar sell (50 uds a base×0.9) desde su nodo.
 */

import { mulDiv, none, polygonCenter, squareAround, toBig } from "../actions.js";
import type { BotAction } from "../actions.js";
import { required } from "../catalog.js";
import type { Catalog } from "../catalog.js";
import type { ApiClient } from "../client.js";
import type {
  Building,
  BuildingStatus,
  Concession,
  GeoPolygon,
  InventoryItem,
  LedgerAccount,
  NetworkNode,
  ProductionBatch,
  Publication,
  ResourceDeposit,
} from "../types.js";

export const PRODUCER_PARAMS = {
  /** Mitad del lado de la parcela (~0.01° de lado). */
  PARCEL_HALF_DEG: 0.005,
  /** Mitad del lado de la huella del edificio (dentro de la parcela). */
  FOOTPRINT_HALF_DEG: 0.002,
  BATCHES_PER_ORDER: 5,
  SELL_TRIGGER_QTY: 50n,
  SELL_QTY: 50n,
  COAL_MIN_QTY: 10n,
  COAL_BUY_QTY: 20n,
} as const;

export interface ProducerObservation {
  /** Yacimiento de iron_ore elegido (null si no hay disponibles). */
  deposit: { id: string; regionId: string; lon: number; lat: number } | null;
  concession: { id: string; parcel: GeoPolygon } | null;
  building: { id: string; status: BuildingStatus } | null;
  /** Nodo logístico del edificio propio (null si aún no existe). */
  myNodeId: string | null;
  /** Lotes en estado queued o running. */
  pendingBatches: number;
  /** Saldo stock_free de iron_ore en el almacén propio. */
  ironOreFreeQty: bigint;
  /** Inventario físico de coal en el edificio. */
  coalInventoryQty: bigint;
  /** Publicaciones propias visibles: sell de iron_ore / buy de coal. */
  openSellIronPubs: number;
  openBuyCoalPubs: number;
  ironOreBasePrice: bigint;
  coalBasePrice: bigint;
  ironOreProductId: string;
  coalProductId: string;
  ironMineTypeId: string;
  mineIronRecipeId: string;
}

export function decideProducer(obs: ProducerObservation): BotAction {
  const P = PRODUCER_PARAMS;

  if (obs.concession === null) {
    if (obs.deposit === null) return none("sin yacimiento de iron_ore disponible");
    return {
      type: "create_concession",
      regionId: obs.deposit.regionId,
      parcel: squareAround(obs.deposit.lon, obs.deposit.lat, P.PARCEL_HALF_DEG),
    };
  }

  if (obs.building === null) {
    const [lon, lat] = polygonCenter(obs.concession.parcel);
    return {
      type: "create_building",
      buildingTypeId: obs.ironMineTypeId,
      concessionId: obs.concession.id,
      footprint: squareAround(lon, lat, P.FOOTPRINT_HALF_DEG),
    };
  }

  if (obs.building.status !== "operational") {
    return none(`mina aún no operativa (${obs.building.status})`);
  }

  if (
    obs.myNodeId !== null &&
    obs.coalInventoryQty < P.COAL_MIN_QTY &&
    obs.openBuyCoalPubs === 0
  ) {
    return {
      type: "publish_buy",
      productId: obs.coalProductId,
      quantity: P.COAL_BUY_QTY,
      unitPrice: mulDiv(obs.coalBasePrice, 11n, 10n), // base×1.1
      destinationNodeId: obs.myNodeId,
    };
  }

  if (obs.pendingBatches === 0) {
    return {
      type: "queue_batches",
      buildingId: obs.building.id,
      recipeId: obs.mineIronRecipeId,
      batches: P.BATCHES_PER_ORDER,
    };
  }

  if (
    obs.myNodeId !== null &&
    obs.ironOreFreeQty > P.SELL_TRIGGER_QTY &&
    obs.openSellIronPubs === 0
  ) {
    return {
      type: "publish_sell",
      productId: obs.ironOreProductId,
      quantity: P.SELL_QTY,
      unitPrice: mulDiv(obs.ironOreBasePrice, 9n, 10n), // base×0.9
      originNodeId: obs.myNodeId,
    };
  }

  return none("estado deseado alcanzado");
}

/** Refresca el estado observable vía API pública (solo lecturas). */
export async function observeProducer(
  client: ApiClient,
  catalog: Catalog,
): Promise<ProducerObservation> {
  const ironOre = required(catalog.products.get("iron_ore"), "producto iron_ore");
  const coal = required(catalog.products.get("coal"), "producto coal");
  const ironMine = required(catalog.buildingTypes.get("iron_mine"), "tipo iron_mine");
  const mineIron = required(catalog.recipes.get("mine_iron"), "receta mine_iron");

  const [depositsRes, concessionsRes, buildingsRes] = await Promise.all([
    client.get<ResourceDeposit[]>("/world/resource-deposits", {
      product_id: ironOre.id,
      only_available: true,
    }),
    client.get<Concession[]>("/world/concessions", { status: "active" }),
    client.get<Building[]>("/world/buildings", { building_type_id: ironMine.id }),
  ]);

  const depositDto = depositsRes.data[0];
  const deposit = depositDto
    ? {
        id: depositDto.id,
        regionId: depositDto.region_id,
        lon: depositDto.location.coordinates[0],
        lat: depositDto.location.coordinates[1],
      }
    : null;
  const concessionDto = concessionsRes.data[0];
  const concession = concessionDto
    ? { id: concessionDto.id, parcel: concessionDto.parcel }
    : null;
  const buildingDto = buildingsRes.data[0];
  const building = buildingDto
    ? { id: buildingDto.id, status: buildingDto.status }
    : null;

  let myNodeId: string | null = null;
  let pendingBatches = 0;
  let ironOreFreeQty = 0n;
  let coalInventoryQty = 0n;

  if (buildingDto) {
    const [nodesRes, batchesRes, inventoryRes, stockRes] = await Promise.all([
      // La API no filtra nodos por building: listamos los de la región del
      // edificio y usamos el que tenga su building_id.
      client.get<NetworkNode[]>("/logistics/network/nodes", {
        region_id: buildingDto.region_id,
        limit: 200,
      }),
      client.get<ProductionBatch[]>(
        `/world/buildings/${buildingDto.id}/production-batches`,
        { limit: 200 },
      ),
      client.get<InventoryItem[]>(`/world/buildings/${buildingDto.id}/inventory`),
      client.get<LedgerAccount[]>("/ledger/accounts", {
        kind: "stock_free",
        product_id: ironOre.id,
      }),
    ]);
    myNodeId =
      nodesRes.data.find((n) => n.building_id === buildingDto.id)?.id ?? null;
    pendingBatches = batchesRes.data.filter(
      (b) => b.status === "queued" || b.status === "running",
    ).length;
    coalInventoryQty = toBig(
      inventoryRes.data.find((i) => i.product_id === coal.id)?.quantity ?? "0",
    );
    ironOreFreeQty = stockRes.data
      .filter((a) => a.warehouse_building_id === buildingDto.id)
      .reduce((sum, a) => sum + toBig(a.balance), 0n);
  }

  const [sellBoard, buyBoard] = await Promise.all([
    client.get<Publication[]>("/contracts/board", {
      kind: "sell",
      product_id: ironOre.id,
      limit: 200,
    }),
    client.get<Publication[]>("/contracts/board", {
      kind: "buy",
      product_id: coal.id,
      limit: 200,
    }),
  ]);
  const openSellIronPubs = sellBoard.data.filter(
    (p) => p.publisher_account_id === client.accountId,
  ).length;
  const openBuyCoalPubs = buyBoard.data.filter(
    (p) => p.publisher_account_id === client.accountId,
  ).length;

  return {
    deposit,
    concession,
    building,
    myNodeId,
    pendingBatches,
    ironOreFreeQty,
    coalInventoryQty,
    openSellIronPubs,
    openBuyCoalPubs,
    ironOreBasePrice: toBig(ironOre.base_price),
    coalBasePrice: toBig(coal.base_price),
    ironOreProductId: ironOre.id,
    coalProductId: coal.id,
    ironMineTypeId: ironMine.id,
    mineIronRecipeId: mineIron.id,
  };
}
