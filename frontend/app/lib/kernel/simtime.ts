/**
 * kernel/simtime.ts — sim-time como tipo y aritmética (C8 / P5 / ADR-FE-007).
 *
 * Todo plazo de dominio llega en segundos de sim-time desde el génesis
 * (ratio 24×: 1 s de pared = 24 s de sim; ADR-IMPL-06). Este módulo es el
 * ÚNICO punto del cliente que conoce la aritmética sim↔wall; el servicio
 * SimClock (estado en sim.store) deriva el "ahora" con estos helpers y es el
 * único punto de conversión en runtime.
 *
 * Formato legible: `AÑO-DDD-HH:MM` con año = floor(días_totales/360) + 1 y
 * DDD = día del año 001..360 (docs/desarrollo.md, ADR-IMPL-06).
 */

declare const simTimeBrand: unique symbol

/** Segundos de sim-time desde el génesis del mundo (entero >= 0). */
export type SimTime = number & { readonly [simTimeBrand]: 'SimTime' }

/** Ratio de compresión temporal: 1 segundo de pared = 24 segundos de sim. */
export const SIM_RATIO = 24

export const SIM_SECONDS_PER_DAY = 86_400
export const SIM_DAYS_PER_YEAR = 360

/** Constructor validado del tipo branded. */
export function simTime(seconds: number): SimTime {
  if (!Number.isInteger(seconds) || seconds < 0) {
    throw new RangeError(`SimTime inválido: ${seconds} (se esperaba entero >= 0)`)
  }
  return seconds as SimTime
}

function pad(n: number, width: number): string {
  return String(n).padStart(width, '0')
}

/** `formatSimTime(0) === '1-001-00:00'` — génesis del mundo. */
export function formatSimTime(simSeconds: SimTime | number): string {
  const s = Math.max(0, Math.floor(simSeconds))
  const totalDays = Math.floor(s / SIM_SECONDS_PER_DAY)
  const year = Math.floor(totalDays / SIM_DAYS_PER_YEAR) + 1
  const dayOfYear = (totalDays % SIM_DAYS_PER_YEAR) + 1
  const secondsOfDay = s % SIM_SECONDS_PER_DAY
  const hours = Math.floor(secondsOfDay / 3600)
  const minutes = Math.floor((secondsOfDay % 3600) / 60)
  return `${year}-${pad(dayOfYear, 3)}-${pad(hours, 2)}:${pad(minutes, 2)}`
}

/** Duración de pared (ms) que corresponde a una duración de sim (segundos). */
export function simToWallMs(simSeconds: number): number {
  return (simSeconds / SIM_RATIO) * 1000
}

/** Duración de sim (segundos) que corresponde a una duración de pared (ms). */
export function wallMsToSim(wallMs: number): number {
  return (wallMs / 1000) * SIM_RATIO
}

/**
 * Plazo restante en segundos de sim (>= 0). El cliente solo lo PRESENTA
 * (countdowns); el vencimiento real lo decide el servidor (P1).
 */
export function plazoRestante(deadlineSim: SimTime | number, nowSim: SimTime | number): SimTime {
  return Math.max(0, Math.floor(deadlineSim) - Math.floor(nowSim)) as SimTime
}

/** Duración de sim legible y compacta para countdowns: '2d 04:30' / '00:05:59'. */
export function formatSimDuration(simSeconds: number): string {
  const s = Math.max(0, Math.floor(simSeconds))
  const days = Math.floor(s / SIM_SECONDS_PER_DAY)
  const hours = Math.floor((s % SIM_SECONDS_PER_DAY) / 3600)
  const minutes = Math.floor((s % 3600) / 60)
  const seconds = s % 60
  if (days > 0) return `${days}d ${pad(hours, 2)}:${pad(minutes, 2)}`
  return `${pad(hours, 2)}:${pad(minutes, 2)}:${pad(seconds, 2)}`
}
