/**
 * domain/simclock — servicio horario único del cliente (FAD §12.7, ADR-FE-007, P5).
 *
 * ÚNICO punto de conversión sim-time ↔ wall-clock de la aplicación. Nadie más
 * llama a `Date.now()` para lógica de dominio ni aplica el ratio 24× a mano.
 *
 * Modelo: el reloj se re-ancla con cada `meta` autoritativa del servidor
 * (`{anchorSimSeconds, anchorWallMs}`) y deriva el "ahora" localmente con el
 * kernel `shared/simtime`. El valor visible es MONÓTONO: si un re-anclaje
 * llega "por detrás" de lo ya mostrado (jitter de red, respuesta vieja), el
 * reloj visible no retrocede — se mantiene y deja que la derivación real lo
 * alcance (corrección suave). Los saltos hacia delante sí se aplican de
 * inmediato (el servidor manda).
 *
 * Estado `frozen` (FAD §12.9, C9): durante la ventana de mantenimiento el
 * sim-time mundial está congelado; `freeze()` detiene el avance visible en el
 * valor actual. Cualquier `update()` posterior (toda respuesta con envelope
 * `{data, meta}` implica mundo vivo: en mantenimiento la API solo responde
 * 503) reanuda el reloj — el descongelado es autocurativo.
 *
 * Framework-agnostic: sin Vue/Nuxt/Pinia. La cara reactiva (`useSimNow`) vive
 * en `app/` sobre este servicio.
 */

import type { SimTime } from '~shared/simtime'
import { deriveNow, simToWallMs as simToWallMsFromAnchor } from '~shared/simtime'

/** Ancla autoritativa: qué sim-time regía y en qué instante local se supo. */
export interface SimClockUpdate {
  /** `meta.sim_time_seconds` de una respuesta del servidor. */
  readonly simTimeSeconds: SimTime
  /** Wall-clock local (ms) de recepción; por defecto, "ahora". */
  readonly receivedAtWallMs?: number
}

/** Foto del estado interno, para diagnóstico y tests (FAD §21.10). */
export interface SimClockSnapshot {
  readonly anchorSimSeconds: SimTime | null
  readonly anchorWallMs: number | null
  readonly frozen: boolean
}

export interface SimClock {
  /** Re-ancla con la meta de una respuesta y reanuda el reloj si estaba frozen. */
  update(update: SimClockUpdate): void
  /** Congela el sim-time visible (ventana de mantenimiento, FAD §12.9). */
  freeze(): void
  /** ¿Está el mundo en pausa de mantenimiento (según este cliente)? */
  isFrozen(): boolean
  /**
   * Sim-time actual derivado del ancla (monótono, nunca retrocede).
   * `null` mientras no haya llegado ninguna meta del servidor.
   */
  now(): SimTime | null
  /**
   * Traduce un instante sim-time (deadline, countdown) a wall-clock local (ms).
   * `null` si el reloj no está anclado o está frozen (durante mantenimiento
   * ningún plazo avanza; un countdown congelado no tiene instante wall-clock).
   */
  simToWallMs(target: SimTime): number | null
  snapshot(): SimClockSnapshot
}

export interface SimClockOptions {
  /** Fuente de wall-clock inyectable (tests deterministas); default `Date.now`. */
  readonly wallNow?: () => number
}

export function createSimClock(options: SimClockOptions = {}): SimClock {
  const wallNow = options.wallNow ?? (() => Date.now())

  let anchorSimSeconds: SimTime | null = null
  let anchorWallMs = 0
  let frozen = false
  /** Último valor devuelto por `now()`: cota inferior de monotonía. */
  let lastVisible: SimTime | null = null

  function derive(): SimTime | null {
    if (anchorSimSeconds === null) {
      return null
    }
    return deriveNow(anchorSimSeconds, anchorWallMs, wallNow(), frozen)
  }

  function now(): SimTime | null {
    const derived = derive()
    if (derived === null) {
      return null
    }
    // Corrección monótona suave: nunca por debajo de lo ya mostrado; si el
    // ancla nueva viene "por detrás", el valor visible espera a ser alcanzado.
    const visible = lastVisible !== null && lastVisible > derived ? lastVisible : derived
    lastVisible = visible
    return visible
  }

  return {
    update({ simTimeSeconds, receivedAtWallMs }) {
      anchorSimSeconds = simTimeSeconds
      anchorWallMs = receivedAtWallMs ?? wallNow()
      // Una meta fresca solo puede venir de un mundo vivo (en mantenimiento
      // toda la API responde 503 sin envelope): reanuda si estaba frozen.
      frozen = false
    },

    freeze() {
      if (anchorSimSeconds !== null) {
        // Congela EXACTAMENTE el valor visible actual (respeta la monotonía).
        const current = now()
        if (current !== null) {
          anchorSimSeconds = current
          anchorWallMs = wallNow()
        }
      }
      frozen = true
    },

    isFrozen() {
      return frozen
    },

    now,

    simToWallMs(target) {
      if (anchorSimSeconds === null || frozen) {
        return null
      }
      return simToWallMsFromAnchor(target, anchorSimSeconds, anchorWallMs)
    },

    snapshot() {
      return {
        anchorSimSeconds,
        anchorWallMs: anchorSimSeconds === null ? null : anchorWallMs,
        frozen,
      }
    },
  }
}
