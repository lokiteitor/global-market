/**
 * app/composables/useWallClock — hora local (wall-clock) para la UI.
 *
 * El wall-clock es legítimo SOLO como dato de sesión/UI (FAD C8): aquí no hay
 * conversión sim↔wall alguna (eso es exclusivo del SimClock, P5/ADR-FE-007);
 * es simplemente "qué hora es para el jugador", mostrada junto al sim-time.
 *
 * `null` en SSR y hasta el primer tick post-montaje (evita hydration mismatch).
 */

import type { Ref } from 'vue'
import { onMounted, onUnmounted, readonly, ref } from 'vue'

const TICK_INTERVAL_MS = 1_000

export function useWallClock(): Readonly<Ref<string | null>> {
  const value = ref<string | null>(null)

  if (import.meta.server) {
    return readonly(value)
  }

  const formatter = new Intl.DateTimeFormat('es-ES', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })

  let timer: ReturnType<typeof setInterval> | null = null

  function tick(): void {
    value.value = formatter.format(new Date())
  }

  onMounted(() => {
    tick()
    timer = setInterval(tick, TICK_INTERVAL_MS)
  })

  onUnmounted(() => {
    if (timer !== null) {
      clearInterval(timer)
      timer = null
    }
  })

  return readonly(value)
}
