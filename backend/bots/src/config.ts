/**
 * Configuración del Bot Orchestration Service.
 *
 * Los bots consumen EXCLUSIVAMENTE la API pública REST del gateway
 * (specs/openapi.yaml v1.1.0) — igualdad de API literal, ADR-IMPL-11.
 */

/**
 * URL base de la API pública (gateway Fastify). Acepta la URL con o sin el
 * prefijo `/api/v1` (el Makefile pasa `http://localhost:8080`) y lo añade
 * si falta.
 */
function normalizeBaseUrl(url: string): string {
  const trimmed = url.replace(/\/+$/, "");
  return /\/api\/v[0-9]+$/.test(trimmed) ? trimmed : `${trimmed}/api/v1`;
}

export const GATEWAY_URL: string = normalizeBaseUrl(
  process.env.GATEWAY_URL ?? "http://localhost:8080",
);

/** Periodo del bucle del orquestador (ms reales). */
export const TICK_MS: number = Number(process.env.TICK_MS ?? 5000);

/** Jitter máximo por bot dentro de cada tick (ms reales). */
export const TICK_JITTER_MS = 1500;

/**
 * Plazo de entrega que los bots pactan en sus publicaciones (sim-time).
 * 172800 s sim = 2 días de juego (ratio 24×: 2 h reales).
 */
export const DELIVERY_SIM_SECONDS = 172_800;

export type ArchetypeName = "producer" | "transformer" | "arbitrageur";

export interface BotCredentials {
  readonly accountName: string;
  readonly secret: string;
  readonly archetype: ArchetypeName;
}

/** Los 3 bots del seed (backend/seeds/seed_world.sql §1-2). */
export const BOTS: readonly BotCredentials[] = [
  { accountName: "Bot Minero Norte", secret: "botmineronorte", archetype: "producer" },
  { accountName: "Bot Fundición Este", secret: "botfundicioneste", archetype: "transformer" },
  { accountName: "Bot Arbitraje Sur", secret: "botarbitrajesur", archetype: "arbitrageur" },
];
