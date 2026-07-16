import assert from "node:assert/strict";
import { test } from "node:test";
import { decideTransformer } from "../src/archetypes/transformer.js";
import type { TransformerObservation } from "../src/archetypes/transformer.js";
import { squareAround } from "../src/actions.js";

function baseObs(overrides: Partial<TransformerObservation> = {}): TransformerObservation {
  return {
    cityLon: 0.5,
    cityLat: 0.5,
    cityRegionId: "region-norte",
    concession: { id: "conc-1", parcel: squareAround(0.53, 0.5, 0.005) },
    building: { id: "bld-1", status: "operational" },
    myNodeId: "node-1",
    pendingBatches: 2,
    ironOreInventoryQty: 100n,
    coalInventoryQty: 100n,
    steelFreeQty: 0n,
    openBuyIronPubs: 0,
    openBuyCoalPubs: 0,
    openSellSteelPubs: 0,
    ironOreBasePrice: 100n,
    coalBasePrice: 80n,
    steelBasePrice: 400n,
    ironOreProductId: "prod-iron",
    coalProductId: "prod-coal",
    steelProductId: "prod-steel",
    blastFurnaceTypeId: "btype-furnace",
    smeltSteelRecipeId: "recipe-smelt",
    ...overrides,
  };
}

test("sin concesión → concesión cerca de Ferrópolis (desplazada del centro)", () => {
  const action = decideTransformer(baseObs({ concession: null, building: null }));
  assert.equal(action.type, "create_concession");
  if (action.type !== "create_concession") return;
  assert.equal(action.regionId, "region-norte");
  const ring = action.parcel.coordinates[0]!;
  // centro de la parcela = ciudad + 0.03° de longitud
  assert.ok(Math.abs(ring[0]![0]! - (0.53 - 0.005)) < 1e-9);
  assert.ok(Math.abs(ring[0]![1]! - (0.5 - 0.005)) < 1e-9);
});

test("concesión sin alto horno → construye blast_furnace", () => {
  const action = decideTransformer(baseObs({ building: null }));
  assert.equal(action.type, "create_building");
  if (action.type !== "create_building") return;
  assert.equal(action.buildingTypeId, "btype-furnace");
  assert.equal(action.concessionId, "conc-1");
});

test("faltan insumos de iron_ore → buy 40 a base×1.1 hacia su nodo", () => {
  const action = decideTransformer(baseObs({ ironOreInventoryQty: 15n }));
  assert.equal(action.type, "publish_buy");
  if (action.type !== "publish_buy") return;
  assert.equal(action.productId, "prod-iron");
  assert.equal(action.quantity, 40n);
  assert.equal(action.unitPrice, 110n); // 100 × 1.1
  assert.equal(action.destinationNodeId, "node-1");
});

test("falta coal → buy 20 a base×1.1", () => {
  const action = decideTransformer(baseObs({ coalInventoryQty: 11n }));
  assert.equal(action.type, "publish_buy");
  if (action.type !== "publish_buy") return;
  assert.equal(action.productId, "prod-coal");
  assert.equal(action.quantity, 20n);
  assert.equal(action.unitPrice, 88n); // 80 × 1.1
});

test("iron_ore tiene prioridad sobre coal cuando faltan ambos", () => {
  const action = decideTransformer(
    baseObs({ ironOreInventoryQty: 0n, coalInventoryQty: 0n }),
  );
  assert.equal(action.type, "publish_buy");
  if (action.type !== "publish_buy") return;
  assert.equal(action.productId, "prod-iron");
});

test("buy ya publicado → no duplica publicaciones", () => {
  const action = decideTransformer(
    baseObs({ ironOreInventoryQty: 0n, openBuyIronPubs: 1, coalInventoryQty: 0n, openBuyCoalPubs: 1 }),
  );
  assert.equal(action.type, "none");
});

test("cola muerta con insumos para ≥1 lote → encola smelt_steel ×5", () => {
  const action = decideTransformer(baseObs({ pendingBatches: 0 }));
  assert.equal(action.type, "queue_batches");
  if (action.type !== "queue_batches") return;
  assert.equal(action.recipeId, "recipe-smelt");
  assert.equal(action.batches, 5);
});

test("cola muerta SIN insumos para un lote → no encola (esperan las compras)", () => {
  const action = decideTransformer(
    baseObs({
      pendingBatches: 0,
      ironOreInventoryQty: 7n, // < 8 por lote
      coalInventoryQty: 100n,
      openBuyIronPubs: 1, // compra ya en vuelo
    }),
  );
  assert.equal(action.type, "none");
});

test("steel_ingot libre > 20 → sell 20 a base×1.05", () => {
  const action = decideTransformer(baseObs({ steelFreeQty: 21n }));
  assert.equal(action.type, "publish_sell");
  if (action.type !== "publish_sell") return;
  assert.equal(action.productId, "prod-steel");
  assert.equal(action.quantity, 20n);
  assert.equal(action.unitPrice, 420n); // 400 × 1.05
  assert.equal(action.originNodeId, "node-1");
});

test("steel_ingot libre exactamente 20 → no vende", () => {
  const action = decideTransformer(baseObs({ steelFreeQty: 20n }));
  assert.equal(action.type, "none");
});

test("alto horno no operativo → none", () => {
  const action = decideTransformer(
    baseObs({ building: { id: "bld-1", status: "under_construction" }, steelFreeQty: 100n }),
  );
  assert.equal(action.type, "none");
});
