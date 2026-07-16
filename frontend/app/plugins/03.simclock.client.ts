/**
 * plugins/03.simclock.client.ts — ticker del reloj de simulación (client-only).
 *
 * Avanza la vista reactiva del SimClock cada 250 ms (suficiente para relojes
 * y countdowns de UI con resolución de segundo; el render de mundo muestrea
 * simClock.now() en su propio rAF — P5: mismo servicio, distintos muestreos).
 * La derivación es pura (muestra + deriva 24×): el ticker solo refresca la
 * ref reactiva, no "posee" el tiempo.
 */
import type { SimClock } from '~/lib/net/simclock'

const TICK_MS = 250

export default defineNuxtPlugin((nuxtApp) => {
  const simClock = nuxtApp.$simClock as SimClock | undefined
  if (simClock === undefined) return

  const timer = setInterval(() => {
    simClock.tick()
  }, TICK_MS)

  // Teardown ordenado al cerrar la pestaña (el intervalo muere con la página).
  window.addEventListener('beforeunload', () => clearInterval(timer), { once: true })
})
