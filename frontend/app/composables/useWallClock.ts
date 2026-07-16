/**
 * composables/useWallClock.ts — tick de reloj de pared para countdowns.
 *
 * Las ventanas de sorteo/micro-ventana y el cooldown anti-parpadeo son
 * mecánica en TIEMPO REAL (wall-clock del servidor), no sim-time: por eso este
 * ticker es independiente del SimClock. El vencimiento real lo decide el
 * servidor (P1); aquí solo se presenta la cuenta atrás.
 */
import { onScopeDispose, ref, type Ref } from 'vue'

export function useWallClock(intervalMs = 1000): Ref<number> {
  const nowMs = ref(Date.now())
  if (typeof window !== 'undefined') {
    const timer = window.setInterval(() => {
      nowMs.value = Date.now()
    }, intervalMs)
    onScopeDispose(() => window.clearInterval(timer))
  }
  return nowMs
}

/** Duración de pared legible: '1h 05m', '03:21', '0s'. */
export function formatWallDuration(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000))
  const hours = Math.floor(totalSeconds / 3600)
  const minutes = Math.floor((totalSeconds % 3600) / 60)
  const seconds = totalSeconds % 60
  const pad = (n: number): string => String(n).padStart(2, '0')
  if (hours > 0) return `${hours}h ${pad(minutes)}m`
  return `${pad(minutes)}:${pad(seconds)}`
}
