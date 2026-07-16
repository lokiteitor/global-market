/**
 * Bucle del Bot Orchestration Service.
 *
 * Cada TICK_MS ejecuta UN paso de cada bot activo con jitter aleatorio:
 * observar (solo lecturas) → decidir (función pura) → ejecutar (como máximo
 * 1 comando de escritura por tick). Cada bot tolera sus propios errores
 * (log y continúa) y nunca reintenta en tromba (el backoff vive en client.ts).
 */

import { BOTS, DELIVERY_SIM_SECONDS, TICK_JITTER_MS, TICK_MS } from "./config.js";
import type { BotCredentials } from "./config.js";
import { ApiClient } from "./client.js";
import { loadCatalog } from "./catalog.js";
import type { Catalog } from "./catalog.js";
import type { BotAction } from "./actions.js";
import { decideProducer, observeProducer } from "./archetypes/producer.js";
import { decideTransformer, observeTransformer } from "./archetypes/transformer.js";
import {
  decideArbitrageur,
  observeArbitrageur,
  updateAvgCost,
} from "./archetypes/arbitrageur.js";

interface BotRuntime {
  cfg: BotCredentials;
  client: ApiClient;
  catalog: Catalog | null;
  busy: boolean;
  /** Memoria del arbitrajista: precio medio de compra por product_id. */
  avgCostMemory: Map<string, { avgCost: bigint; quantity: bigint }>;
}

function log(bot: string, message: string): void {
  console.log(`${new Date().toISOString()} [${bot}] ${message}`);
}

function describe(action: BotAction): string {
  switch (action.type) {
    case "none":
      return `sin acción — ${action.reason}`;
    case "create_concession":
      return `POST /world/concessions (región ${action.regionId})`;
    case "create_building":
      return `POST /world/buildings (tipo ${action.buildingTypeId} en concesión ${action.concessionId})`;
    case "queue_batches":
      return `POST production-batches ×${action.batches} (receta ${action.recipeId}, edificio ${action.buildingId})`;
    case "publish_sell":
      return `publicar SELL ${action.quantity} de ${action.productId} @ ${action.unitPrice} desde ${action.originNodeId}`;
    case "publish_buy":
      return `publicar BUY ${action.quantity} de ${action.productId} @ ${action.unitPrice} hacia ${action.destinationNodeId}`;
    case "accept_publication":
      return `aceptar publicación ${action.publicationId} ×${action.quantity} (${action.side}, ${action.productId} @ ${action.unitPrice})`;
  }
}

/** Ejecuta la (única) acción de escritura del tick contra la API pública. */
async function execute(bot: BotRuntime, action: BotAction): Promise<void> {
  const client = bot.client;
  switch (action.type) {
    case "none":
      return;
    case "create_concession":
      await client.post("/world/concessions", {
        region_id: action.regionId,
        parcel: action.parcel,
      });
      return;
    case "create_building":
      await client.post("/world/buildings", {
        building_type_id: action.buildingTypeId,
        concession_id: action.concessionId,
        footprint: action.footprint,
      });
      return;
    case "queue_batches":
      await client.post(`/world/buildings/${action.buildingId}/production-batches`, {
        recipe_id: action.recipeId,
        batches_queued: action.batches,
      });
      return;
    case "publish_sell":
      await client.post("/contracts/publications", {
        kind: "sell",
        product_id: action.productId,
        quantity_total: action.quantity.toString(),
        unit_price: action.unitPrice.toString(),
        min_lot: "1",
        origin_node_id: action.originNodeId,
        delivery_sim_seconds: DELIVERY_SIM_SECONDS,
      });
      return;
    case "publish_buy":
      await client.post("/contracts/publications", {
        kind: "buy",
        product_id: action.productId,
        quantity_total: action.quantity.toString(),
        unit_price: action.unitPrice.toString(),
        min_lot: "1",
        destination_node_id: action.destinationNodeId,
        delivery_sim_seconds: DELIVERY_SIM_SECONDS,
      });
      return;
    case "accept_publication":
      await client.post(`/contracts/publications/${action.publicationId}/acceptances`, {
        quantity: action.quantity.toString(),
      });
      if (action.side === "buying") {
        // Memoria del arbitrajista: coste medio optimista (si el sorteo no le
        // sirve, el próximo refresh de stock lo corrige de facto).
        updateAvgCost(bot.avgCostMemory, action.productId, action.unitPrice, action.quantity);
      }
      return;
  }
}

async function decideOneStep(bot: BotRuntime, catalog: Catalog): Promise<BotAction> {
  switch (bot.cfg.archetype) {
    case "producer":
      return decideProducer(await observeProducer(bot.client, catalog));
    case "transformer":
      return decideTransformer(await observeTransformer(bot.client, catalog));
    case "arbitrageur":
      return decideArbitrageur(
        await observeArbitrageur(bot.client, catalog, bot.avgCostMemory),
      );
  }
}

async function tickBot(bot: BotRuntime): Promise<void> {
  if (bot.busy) {
    log(bot.cfg.accountName, "tick omitido: el paso anterior sigue en curso");
    return;
  }
  bot.busy = true;
  try {
    if (bot.catalog === null) {
      await bot.client.login();
      bot.catalog = await loadCatalog(bot.client);
      log(bot.cfg.accountName, `sesión iniciada (cuenta ${bot.client.accountId}); catálogo cargado`);
    }
    const action = await decideOneStep(bot, bot.catalog);
    log(bot.cfg.accountName, `decisión: ${describe(action)}`);
    if (action.type !== "none") {
      await execute(bot, action);
      log(bot.cfg.accountName, "acción ejecutada con éxito");
    }
  } catch (err) {
    // Tolerancia por acción: log y continuar en el próximo tick.
    const message = err instanceof Error ? err.message : String(err);
    log(bot.cfg.accountName, `ERROR: ${message}`);
  } finally {
    bot.busy = false;
  }
}

function main(): void {
  const bots: BotRuntime[] = BOTS.map((cfg) => ({
    cfg,
    client: new ApiClient({ accountName: cfg.accountName, secret: cfg.secret }),
    catalog: null,
    busy: false,
    avgCostMemory: new Map(),
  }));

  console.log(
    `${new Date().toISOString()} [orchestrator] ${bots.length} bots, tick=${TICK_MS}ms, jitter≤${TICK_JITTER_MS}ms`,
  );

  const timer = setInterval(() => {
    for (const bot of bots) {
      const jitter = Math.floor(Math.random() * TICK_JITTER_MS);
      setTimeout(() => void tickBot(bot), jitter);
    }
  }, TICK_MS);

  const shutdown = (signal: string): void => {
    console.log(`${new Date().toISOString()} [orchestrator] ${signal} recibido, parando`);
    clearInterval(timer);
    process.exit(0);
  };
  process.on("SIGINT", () => shutdown("SIGINT"));
  process.on("SIGTERM", () => shutdown("SIGTERM"));
}

main();
