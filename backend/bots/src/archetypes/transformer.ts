/**
 * TRANSFORMER — Bot Fundición Este.
 *
 * Estado deseado: concesión + blast_furnace cerca de Ferrópolis; compra de
 * insumos (iron_ore y coal) hacia su nodo cuando falten; cola smelt_steel
 * viva; venta de steel_ingot a base×1.05 cuando acumule > 20.
 *
 * `decideTransformer` es una función PURA de estado observable → acción.
 * Prioridad, de mayor a menor:
 *   1. Sin concesión              → concesión ~0.01° desplazada del centro de Ferrópolis.
 *   2. Sin alto horno             → construir blast_furnace en la parcela.
 *   3. iron_ore < IRON_MIN        → buy iron_ore (40 uds a base×1.1) hacia su nodo.
 *   4. coal < COAL_MIN            → buy coal (20 uds a base×1.1) hacia su nodo.
 *   5. Cola muerta + insumos ≥ 1 lote → encolar smelt_steel ×5.
 *   6. steel_ingot libre > 20     → sell (20 uds a base×1.05) desde su nodo.
 *
 * La receta smelt_steel consume por lote: 8 iron_ore + 4 coal (insumos)
 * + 2 coal (combustible).
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
} from "../types.js";

export const TRANSFORMER_PARAMS = {
  /** Desplazamiento al este del centro de la ciudad para no pisar el casco. */
  CITY_OFFSET_DEG: 0.03,
  PARCEL_HALF_DEG: 0.005,
  FOOTPRINT_HALF_DEG: 0.002,
  BATCHES_PER_ORDER: 5,
  IRON_MIN_QTY: 16n, // insumos para 2 lotes (8/lote)
  IRON_BUY_QTY: 40n,
  COAL_MIN_QTY: 12n, // 2 lotes × (4 insumo + 2 combustible)
  COAL_BUY_QTY: 20n,
  /** Insumos mínimos para encolar (1 lote). */
  IRON_PER_BATCH: 8n,
  COAL_PER_BATCH: 6n, // 4 insumo + 2 combustible
  SELL_TRIGGER_QTY: 20n,
  SELL_QTY: 20n,
} as const;

export interface TransformerObservation {
  /** Centro de Ferrópolis (lon, lat). */
  cityLon: number;
  cityLat: number;
  cityRegionId: string;
  concession: { id: string; parcel: GeoPolygon } | null;
  building: { id: string; status: BuildingStatus } | null;
  myNodeId: string | null;
  pendingBatches: number;
  ironOreInventoryQty: bigint;
  coalInventoryQty: bigint;
  /** Saldo stock_free de steel_ingot en el almacén propio. */
  steelFreeQty: bigint;
  openBuyIronPubs: number;
  openBuyCoalPubs: number;
  openSellSteelPubs: number;
  ironOreBasePrice: bigint;
  coalBasePrice: bigint;
  steelBasePrice: bigint;
  ironOreProductId: string;
  coalProductId: string;
  steelProductId: string;
  blastFurnaceTypeId: string;
  smeltSteelRecipeId: string;
}

export function decideTransformer(obs: TransformerObservation): BotAction {
  const P = TRANSFORMER_PARAMS;

  if (obs.concession === null) {
    return {
      type: "create_concession",
      regionId: obs.cityRegionId,
      parcel: squareAround(
        obs.cityLon + P.CITY_OFFSET_DEG,
        obs.cityLat,
        P.PARCEL_HALF_DEG,
      ),
    };
  }

  if (obs.building === null) {
    const [lon, lat] = polygonCenter(obs.concession.parcel);
    return {
      type: "create_building",
      buildingTypeId: obs.blastFurnaceTypeId,
      concessionId: obs.concession.id,
      footprint: squareAround(lon, lat, P.FOOTPRINT_HALF_DEG),
    };
  }

  if (obs.building.status !== "operational") {
    return none(`alto horno aún no operativo (${obs.building.status})`);
  }

  if (obs.myNodeId !== null && obs.ironOreInventoryQty < P.IRON_MIN_QTY && obs.openBuyIronPubs === 0) {
    return {
      type: "publish_buy",
      productId: obs.ironOreProductId,
      quantity: P.IRON_BUY_QTY,
      unitPrice: mulDiv(obs.ironOreBasePrice, 11n, 10n), // base×1.1
      destinationNodeId: obs.myNodeId,
    };
  }

  if (obs.myNodeId !== null && obs.coalInventoryQty < P.COAL_MIN_QTY && obs.openBuyCoalPubs === 0) {
    return {
      type: "publish_buy",
      productId: obs.coalProductId,
      quantity: P.COAL_BUY_QTY,
      unitPrice: mulDiv(obs.coalBasePrice, 11n, 10n), // base×1.1
      destinationNodeId: obs.myNodeId,
    };
  }

  if (
    obs.pendingBatches === 0 &&
    obs.ironOreInventoryQty >= P.IRON_PER_BATCH &&
    obs.coalInventoryQty >= P.COAL_PER_BATCH
  ) {
    return {
      type: "queue_batches",
      buildingId: obs.building.id,
      recipeId: obs.smeltSteelRecipeId,
      batches: P.BATCHES_PER_ORDER,
    };
  }

  if (obs.myNodeId !== null && obs.steelFreeQty > P.SELL_TRIGGER_QTY && obs.openSellSteelPubs === 0) {
    return {
      type: "publish_sell",
      productId: obs.steelProductId,
      quantity: P.SELL_QTY,
      unitPrice: mulDiv(obs.steelBasePrice, 21n, 20n), // base×1.05
      originNodeId: obs.myNodeId,
    };
  }

  return none("estado deseado alcanzado");
}

/** Refresca el estado observable vía API pública (solo lecturas). */
export async function observeTransformer(
  client: ApiClient,
  catalog: Catalog,
): Promise<TransformerObservation> {
  const ironOre = required(catalog.products.get("iron_ore"), "producto iron_ore");
  const coal = required(catalog.products.get("coal"), "producto coal");
  const steel = required(catalog.products.get("steel_ingot"), "producto steel_ingot");
  const blastFurnace = required(
    catalog.buildingTypes.get("blast_furnace"),
    "tipo blast_furnace",
  );
  const smeltSteel = required(catalog.recipes.get("smelt_steel"), "receta smelt_steel");
  const city = required(catalog.cities.get("Ferrópolis"), "ciudad Ferrópolis");

  const [concessionsRes, buildingsRes] = await Promise.all([
    client.get<Concession[]>("/world/concessions", { status: "active" }),
    client.get<Building[]>("/world/buildings", { building_type_id: blastFurnace.id }),
  ]);
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
  let ironOreInventoryQty = 0n;
  let coalInventoryQty = 0n;
  let steelFreeQty = 0n;

  if (buildingDto) {
    const [nodesRes, batchesRes, inventoryRes, stockRes] = await Promise.all([
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
        product_id: steel.id,
      }),
    ]);
    myNodeId =
      nodesRes.data.find((n) => n.building_id === buildingDto.id)?.id ?? null;
    pendingBatches = batchesRes.data.filter(
      (b) => b.status === "queued" || b.status === "running",
    ).length;
    ironOreInventoryQty = toBig(
      inventoryRes.data.find((i) => i.product_id === ironOre.id)?.quantity ?? "0",
    );
    coalInventoryQty = toBig(
      inventoryRes.data.find((i) => i.product_id === coal.id)?.quantity ?? "0",
    );
    steelFreeQty = stockRes.data
      .filter((a) => a.warehouse_building_id === buildingDto.id)
      .reduce((sum, a) => sum + toBig(a.balance), 0n);
  }

  const [buyIronBoard, buyCoalBoard, sellSteelBoard] = await Promise.all([
    client.get<Publication[]>("/contracts/board", {
      kind: "buy",
      product_id: ironOre.id,
      limit: 200,
    }),
    client.get<Publication[]>("/contracts/board", {
      kind: "buy",
      product_id: coal.id,
      limit: 200,
    }),
    client.get<Publication[]>("/contracts/board", {
      kind: "sell",
      product_id: steel.id,
      limit: 200,
    }),
  ]);
  const mine = (p: Publication): boolean => p.publisher_account_id === client.accountId;

  return {
    cityLon: city.location.coordinates[0],
    cityLat: city.location.coordinates[1],
    cityRegionId: city.region_id,
    concession,
    building,
    myNodeId,
    pendingBatches,
    ironOreInventoryQty,
    coalInventoryQty,
    steelFreeQty,
    openBuyIronPubs: buyIronBoard.data.filter(mine).length,
    openBuyCoalPubs: buyCoalBoard.data.filter(mine).length,
    openSellSteelPubs: sellSteelBoard.data.filter(mine).length,
    ironOreBasePrice: toBig(ironOre.base_price),
    coalBasePrice: toBig(coal.base_price),
    steelBasePrice: toBig(steel.base_price),
    ironOreProductId: ironOre.id,
    coalProductId: coal.id,
    steelProductId: steel.id,
    blastFurnaceTypeId: blastFurnace.id,
    smeltSteelRecipeId: smeltSteel.id,
  };
}
