/**
 * lib/net/simclock.ts — servicio SimClock (P5 / C8 / ADR-FE-007).
 *
 * ÚNICO punto de conversión sim↔wall en runtime. El estado (última muestra
 * autoritativa + ancla wall-clock + frozen) vive en sim.store; la aritmética
 * del ratio 24× vive en lib/kernel/simtime.ts. Este servicio añade lo que ni
 * la store ni el kernel deciden:
 *
 *   - sync(): registra muestras autoritativas (meta.sim_time_seconds de cada
 *     respuesta REST, hello_ok/pong del WS, snapshots, sim.frozen/resumed);
 *   - now(): sim-time derivado con deriva monotónica local (24× sobre el
 *     wall-clock transcurrido desde el último sync; congelado si frozen);
 *   - monotonicidad: el reloj visible NUNCA retrocede (FAD §12.7). Si una
 *     muestra del servidor llega "por detrás" de lo ya mostrado, now() se
 *     mantiene (clamp) hasta que la deriva del servidor lo alcance — la
 *     corrección es suave, sin saltos hacia atrás;
 *   - viewNowSeconds: ref reactiva para la UI, avanzada por el ticker del
 *     plugin 03.simclock.client.ts (la derivación pura no es reactiva al
 *     paso del tiempo).
 */
import { ref, type Ref } from 'vue'

/** Contrato mínimo con sim.store (inyectado; sin acoplar el servicio a Pinia). */
export interface SimClockStore {
  frozen: boolean
  now(wallMs?: number): number
  syncFromServer(simSeconds: number, frozen: boolean, wallMs?: number): void
  markContact(wallMs?: number): void
}

export interface SimClock {
  /**
   * Re-sincroniza con una muestra autoritativa del servidor. `frozen` omitido
   * conserva el estado frozen vigente (muestras que no lo informan, p. ej.
   * snapshots o meta REST).
   */
  sync(simSeconds: number, frozen?: boolean, wallMs?: number): void
  /** Sim-time derivado "ahora" (segundos, monotónico, nunca retrocede). */
  now(wallMs?: number): number
  /** Avanza la vista reactiva del reloj (lo llama el ticker); devuelve now(). */
  tick(wallMs?: number): number
  isFrozen(): boolean
  /** Vista reactiva de now() en segundos enteros para la UI. */
  viewNowSeconds: Ref<number>
}

export function createSimClock(store: SimClockStore): SimClock {
  /** Máximo sim-time ya observado/mostrado: la barrera de no-retroceso. */
  let highWaterMark = 0
  const viewNowSeconds = ref(0)

  function now(wallMs: number = Date.now()): number {
    const derived = store.now(wallMs)
    // Clamp monotónico: si el servidor corrigió hacia atrás, el reloj visible
    // se mantiene quieto hasta que la muestra + deriva lo alcance (P10:
    // nunca mentimos con un salto hacia atrás en countdowns o interpolación).
    if (derived > highWaterMark) highWaterMark = derived
    return highWaterMark
  }

  return {
    sync(simSeconds, frozen, wallMs = Date.now()) {
      store.syncFromServer(simSeconds, frozen ?? store.frozen, wallMs)
      store.markContact(wallMs)
    },

    now,

    tick(wallMs = Date.now()) {
      const current = now(wallMs)
      const floored = Math.floor(current)
      if (viewNowSeconds.value !== floored) viewNowSeconds.value = floored
      return current
    },

    isFrozen() {
      return store.frozen
    },

    viewNowSeconds
  }
}
