import assert from "node:assert/strict";
import { test } from "node:test";
import { decideArbitrageur, updateAvgCost } from "../src/archetypes/arbitrageur.js";
import type {
  ArbitrageurObservation,
  BoardPublicationView,
} from "../src/archetypes/arbitrageur.js";

const ME = "acc-arb";

function pub(overrides: Partial<BoardPublicationView> = {}): BoardPublicationView {
  return {
    id: "pub-1",
    productId: "prod-coal",
    publisherAccountId: "acc-otro",
    unitPrice: 100n,
    quantityRemaining: 100n,
    minLot: 1n,
    ...overrides,
  };
}

function baseObs(overrides: Partial<ArbitrageurObservation> = {}): ArbitrageurObservation {
  return {
    myAccountId: ME,
    cash: 1_000_000n,
    sells: [],
    buys: [],
    bestDemandPrice: new Map(),
    stockFree: new Map(),
    avgCost: new Map(),
    basePrice: new Map([["prod-coal", 80n]]),
    ...overrides,
  };
}

// ── Compra: aceptar publicaciones sell baratas frente a la demanda urbana ──

test("acepta sell con unit_price < 0.7×demanda urbana", () => {
  const action = decideArbitrageur(
    baseObs({
      sells: [pub({ unitPrice: 69n })],
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "accept_publication");
  if (action.type !== "accept_publication") return;
  assert.equal(action.publicationId, "pub-1");
  assert.equal(action.side, "buying");
  assert.equal(action.quantity, 100n); // remaining cabe dentro del 30% del cash
});

test("NO acepta sell con unit_price = 0.7×demanda (umbral estricto)", () => {
  const action = decideArbitrageur(
    baseObs({
      sells: [pub({ unitPrice: 70n })],
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "none");
});

test("NO compra productos sin demanda urbana conocida", () => {
  const action = decideArbitrageur(baseObs({ sells: [pub({ unitPrice: 1n })] }));
  assert.equal(action.type, "none");
});

test("exposición: nunca más del 30% del cash en una operación (recorta cantidad)", () => {
  const action = decideArbitrageur(
    baseObs({
      cash: 1000n, // 30% = 300
      sells: [pub({ unitPrice: 69n, quantityRemaining: 100n })],
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "accept_publication");
  if (action.type !== "accept_publication") return;
  assert.equal(action.quantity, 4n); // floor(300 / 69)
});

test("exposición: si lo asumible queda bajo el min_lot → no opera", () => {
  const action = decideArbitrageur(
    baseObs({
      cash: 1000n,
      sells: [pub({ unitPrice: 69n, quantityRemaining: 100n, minLot: 10n })],
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "none");
});

test("ignora sus propias publicaciones sell", () => {
  const action = decideArbitrageur(
    baseObs({
      sells: [pub({ unitPrice: 1n, publisherAccountId: ME })],
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "none");
});

// ── Venta: aceptar publicaciones buy con margen > 1.2× sobre el coste ──

test("acepta buy cuyo precio > 1.2×coste medio conocido, limitado a su stock", () => {
  const action = decideArbitrageur(
    baseObs({
      buys: [pub({ id: "pub-buy", unitPrice: 121n, quantityRemaining: 100n })],
      stockFree: new Map([["prod-coal", 30n]]),
      avgCost: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "accept_publication");
  if (action.type !== "accept_publication") return;
  assert.equal(action.publicationId, "pub-buy");
  assert.equal(action.side, "selling");
  assert.equal(action.quantity, 30n); // min(remaining, stock propio)
});

test("NO acepta buy con precio = 1.2×coste (umbral estricto)", () => {
  const action = decideArbitrageur(
    baseObs({
      buys: [pub({ unitPrice: 120n })],
      stockFree: new Map([["prod-coal", 30n]]),
      avgCost: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "none");
});

test("sin coste medio conocido usa el base_price como referencia", () => {
  // base_price coal = 80 → vende si precio > 96
  const yes = decideArbitrageur(
    baseObs({
      buys: [pub({ unitPrice: 97n })],
      stockFree: new Map([["prod-coal", 10n]]),
    }),
  );
  assert.equal(yes.type, "accept_publication");
  const no = decideArbitrageur(
    baseObs({
      buys: [pub({ unitPrice: 96n })],
      stockFree: new Map([["prod-coal", 10n]]),
    }),
  );
  assert.equal(no.type, "none");
});

test("NO vende lo que no posee (stock_free = 0)", () => {
  const action = decideArbitrageur(
    baseObs({ buys: [pub({ unitPrice: 1000n })], avgCost: new Map([["prod-coal", 1n]]) }),
  );
  assert.equal(action.type, "none");
});

test("respeta min_lot al vender", () => {
  const action = decideArbitrageur(
    baseObs({
      buys: [pub({ unitPrice: 200n, minLot: 50n })],
      stockFree: new Map([["prod-coal", 30n]]),
      avgCost: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "none");
});

test("prioriza realizar beneficio (aceptar buy) sobre comprar más", () => {
  const action = decideArbitrageur(
    baseObs({
      buys: [pub({ id: "pub-buy", unitPrice: 200n })],
      sells: [pub({ id: "pub-sell", unitPrice: 10n })],
      stockFree: new Map([["prod-coal", 5n]]),
      avgCost: new Map([["prod-coal", 100n]]),
      bestDemandPrice: new Map([["prod-coal", 100n]]),
    }),
  );
  assert.equal(action.type, "accept_publication");
  if (action.type !== "accept_publication") return;
  assert.equal(action.publicationId, "pub-buy");
});

test("sin oportunidades → none", () => {
  assert.equal(decideArbitrageur(baseObs()).type, "none");
});

// ── Memoria de coste medio ──

test("updateAvgCost: media ponderada por cantidades", () => {
  const memory = new Map<string, { avgCost: bigint; quantity: bigint }>();
  updateAvgCost(memory, "prod-coal", 100n, 10n);
  assert.deepEqual(memory.get("prod-coal"), { avgCost: 100n, quantity: 10n });
  updateAvgCost(memory, "prod-coal", 200n, 30n);
  // (100×10 + 200×30) / 40 = 175
  assert.deepEqual(memory.get("prod-coal"), { avgCost: 175n, quantity: 40n });
});
