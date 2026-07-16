import assert from "node:assert/strict";
import { test } from "node:test";
import { decideProducer } from "../src/archetypes/producer.js";
import type { ProducerObservation } from "../src/archetypes/producer.js";
import { squareAround } from "../src/actions.js";

function baseObs(overrides: Partial<ProducerObservation> = {}): ProducerObservation {
  return {
    deposit: { id: "dep-1", regionId: "region-norte", lon: 0.3, lat: 0.7 },
    concession: { id: "conc-1", parcel: squareAround(0.3, 0.7, 0.005) },
    building: { id: "bld-1", status: "operational" },
    myNodeId: "node-1",
    pendingBatches: 3,
    ironOreFreeQty: 0n,
    coalInventoryQty: 50n,
    openSellIronPubs: 0,
    openBuyCoalPubs: 0,
    ironOreBasePrice: 100n,
    coalBasePrice: 80n,
    ironOreProductId: "prod-iron",
    coalProductId: "prod-coal",
    ironMineTypeId: "btype-mine",
    mineIronRecipeId: "recipe-mine-iron",
    ...overrides,
  };
}

test("sin concesión → pide concesión con parcela ~0.01° alrededor del yacimiento", () => {
  const action = decideProducer(baseObs({ concession: null, building: null }));
  assert.equal(action.type, "create_concession");
  if (action.type !== "create_concession") return;
  assert.equal(action.regionId, "region-norte");
  const ring = action.parcel.coordinates[0]!;
  assert.equal(ring.length, 5);
  assert.deepEqual(ring[0], [0.3 - 0.005, 0.7 - 0.005]);
  assert.deepEqual(ring[2], [0.3 + 0.005, 0.7 + 0.005]);
});

test("sin concesión ni yacimiento → none", () => {
  const action = decideProducer(baseObs({ concession: null, deposit: null, building: null }));
  assert.equal(action.type, "none");
});

test("concesión sin mina → construye iron_mine con huella dentro de la parcela", () => {
  const action = decideProducer(baseObs({ building: null }));
  assert.equal(action.type, "create_building");
  if (action.type !== "create_building") return;
  assert.equal(action.buildingTypeId, "btype-mine");
  assert.equal(action.concessionId, "conc-1");
  // huella centrada en el centro de la parcela y más pequeña que ella
  const ring = action.footprint.coordinates[0]!;
  assert.ok(Math.abs(ring[0]![0]! - (0.3 - 0.002)) < 1e-9);
  assert.ok(Math.abs(ring[0]![1]! - (0.7 - 0.002)) < 1e-9);
});

test("mina en construcción → none (espera)", () => {
  const action = decideProducer(baseObs({ building: { id: "bld-1", status: "under_construction" } }));
  assert.equal(action.type, "none");
});

test("coal < 10 → publica buy de 20 coal a base×1.1 hacia su nodo", () => {
  const action = decideProducer(baseObs({ coalInventoryQty: 9n }));
  assert.equal(action.type, "publish_buy");
  if (action.type !== "publish_buy") return;
  assert.equal(action.productId, "prod-coal");
  assert.equal(action.quantity, 20n);
  assert.equal(action.unitPrice, 88n); // 80 × 1.1
  assert.equal(action.destinationNodeId, "node-1");
});

test("coal bajo pero ya hay buy propio publicado → no duplica; encola si la cola está viva → none", () => {
  const action = decideProducer(baseObs({ coalInventoryQty: 9n, openBuyCoalPubs: 1 }));
  assert.equal(action.type, "none");
});

test("cola muerta → encola mine_iron ×5", () => {
  const action = decideProducer(baseObs({ pendingBatches: 0 }));
  assert.equal(action.type, "queue_batches");
  if (action.type !== "queue_batches") return;
  assert.equal(action.buildingId, "bld-1");
  assert.equal(action.recipeId, "recipe-mine-iron");
  assert.equal(action.batches, 5);
});

test("iron_ore libre > 50 → publica sell de 50 a base×0.9 desde su nodo", () => {
  const action = decideProducer(baseObs({ ironOreFreeQty: 51n }));
  assert.equal(action.type, "publish_sell");
  if (action.type !== "publish_sell") return;
  assert.equal(action.productId, "prod-iron");
  assert.equal(action.quantity, 50n);
  assert.equal(action.unitPrice, 90n); // 100 × 0.9
  assert.equal(action.originNodeId, "node-1");
});

test("iron_ore libre exactamente 50 → no vende (umbral estricto)", () => {
  const action = decideProducer(baseObs({ ironOreFreeQty: 50n }));
  assert.equal(action.type, "none");
});

test("prioridad: comprar coal antes que encolar y que vender", () => {
  const action = decideProducer(
    baseObs({ coalInventoryQty: 0n, pendingBatches: 0, ironOreFreeQty: 500n }),
  );
  assert.equal(action.type, "publish_buy");
});

test("estado deseado alcanzado → none (máx. 1 escritura por tick, aquí ninguna)", () => {
  const action = decideProducer(baseObs());
  assert.equal(action.type, "none");
});
