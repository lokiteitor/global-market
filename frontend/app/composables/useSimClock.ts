/**
 * composables/useSimClock.ts — SimClock reactivo para la UI (ADR-FE-007/P5).
 *
 * Deriva el "ahora" de sim-time a partir de la última muestra autoritativa
 * (sim.store) con un tick de pared local. La aritmética sim↔wall vive SOLO en
 * lib/kernel/simtime.ts; aquí no hay conversión propia.
 */
import { computed, onScopeDispose, ref, type ComputedRef, type Ref } from 'vue'
import { formatSimTime } from '~/lib/kernel/simtime'
import { useSimStore } from '~/stores/sim.store'

/** Salud de la señal temporal para el indicador de conexión del HUD. */
export type SimHealth = 'live' | 'stale' | 'frozen'

/** Segundos de pared sin contacto a partir de los cuales la señal es 'stale'. */
const STALE_AFTER_SECONDS = 15

export interface SimClock {
  /** Wall-clock local (ms), actualizado cada tick. */
  nowWallMs: Ref<number>
  /** Sim-time "ahora" derivado (segundos desde el génesis). */
  nowSim: ComputedRef<number>
  /** Sim-time legible `AÑO-DDD-HH:MM`. */
  nowSimFormatted: ComputedRef<string>
  /** Wall-clock local legible (HH:MM:SS). */
  nowWallFormatted: ComputedRef<string>
  /** Segundos de pared desde el último contacto con el servidor (null = nunca). */
  stalenessSeconds: ComputedRef<number | null>
  health: ComputedRef<SimHealth>
}

export function useSimClock(intervalMs = 1000): SimClock {
  const sim = useSimStore()
  const nowWallMs = ref(Date.now())

  // Tick solo en cliente; se limpia con el scope del componente.
  if (typeof window !== 'undefined') {
    const timer = window.setInterval(() => {
      nowWallMs.value = Date.now()
    }, intervalMs)
    onScopeDispose(() => window.clearInterval(timer))
  }

  const nowSim = computed(() => sim.now(nowWallMs.value))
  const nowSimFormatted = computed(() => formatSimTime(nowSim.value))
  const nowWallFormatted = computed(() => {
    const d = new Date(nowWallMs.value)
    const pad = (n: number): string => String(n).padStart(2, '0')
    return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  })
  const stalenessSeconds = computed(() => sim.staleness(nowWallMs.value))

  const health = computed<SimHealth>(() => {
    if (sim.frozen) return 'frozen'
    const s = stalenessSeconds.value
    const connected = sim.connectionState === 'open'
    if (connected && s !== null && s <= STALE_AFTER_SECONDS) return 'live'
    // Sin WS abierto pero con contacto REST reciente también se considera vivo.
    if (s !== null && s <= STALE_AFTER_SECONDS) return 'live'
    return 'stale'
  })

  return { nowWallMs, nowSim, nowSimFormatted, nowWallFormatted, stalenessSeconds, health }
}
