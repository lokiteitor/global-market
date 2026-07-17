/**
 * app/plugins/sim-clock.client — instancia ÚNICA del SimClock (ADR-FE-007, P5).
 *
 * Client-only: el sim-time visible es presentación en vivo; nunca se hidrata
 * desde SSR (FAD §6.2). Provee:
 *
 * - `$simClock`: el servicio de dominio (domain/simclock). El plugin de red
 *   lo re-ancla con la meta de cada respuesta y lo congela ante 503
 *   MAINTENANCE_WINDOW — este plugin no importa red ni stores.
 * - `$simNow`: ref reactivo con el sim-time actual, actualizado ~1 s por un
 *   ticker visible-aware (se detiene con la pestaña oculta y se reanuda, con
 *   tick inmediato, al volver a ser visible). Lo consume `useSimNow()`.
 * - `$simFrozen`: ref reactivo con el estado `frozen` del reloj (mundo en
 *   ventana de mantenimiento, FAD §12.9). Lo consume `useSimFrozen()`.
 */

import { readonly, ref } from 'vue'
import { createSimClock } from '~domain/simclock'
import type { SimTime } from '~shared/simtime'

const TICK_INTERVAL_MS = 1_000

export default defineNuxtPlugin(() => {
  const simClock = createSimClock()
  const simNowInternal = ref<SimTime | null>(null)
  const simFrozenInternal = ref(false)

  let timer: ReturnType<typeof setInterval> | null = null

  function tick(): void {
    simNowInternal.value = simClock.now()
    simFrozenInternal.value = simClock.isFrozen()
  }

  function start(): void {
    if (timer === null) {
      tick()
      timer = setInterval(tick, TICK_INTERVAL_MS)
    }
  }

  function stop(): void {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  }

  document.addEventListener('visibilitychange', () => {
    if (document.hidden) {
      stop()
    } else {
      start()
    }
  })

  start()

  return {
    provide: {
      simClock,
      simNow: readonly(simNowInternal),
      simFrozen: readonly(simFrozenInternal),
    },
  }
})
