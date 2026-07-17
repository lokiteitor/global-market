/**
 * shared/simtime — tipo SimTime y aritmética sim-time ↔ wall-clock (kernel, FAD C8/§12.7).
 *
 * El sim-time es el reloj de dominio: segundos enteros desde el génesis del
 * mundo, con ratio 24× (1 h real = 1 día de juego). El contrato lo transporta
 * como entero (schema SimTime) y `meta.sim_time` lo presenta legible con el
 * patrón `^[0-9]{1,4}-[0-9]{3}-[0-9]{2}:[0-9]{2}$` (AÑO-DÍA-HH:MM).
 *
 * Este módulo contiene SOLO la matemática pura; el servicio `SimClock`
 * (único punto de conversión de la app, P5/ADR-FE-007) se construye sobre él
 * en un incremento posterior. Nadie más convierte tiempo.
 */

/** Segundos de sim-time desde el génesis (branded number, entero ≥ 0). */
export type SimTime = number & { readonly __brand: 'SimTime' }

/** Ratio sim-time/wall-clock: 1 segundo real = 24 segundos de juego. */
export const RATIO = 24

/** Segundos de sim-time por minuto/hora/día/año de juego (año de 360 días). */
export const SIM_SECONDS_PER_MINUTE = 60
export const SIM_SECONDS_PER_HOUR = 3_600
export const SIM_SECONDS_PER_DAY = 86_400
export const DAYS_PER_YEAR = 360
export const SIM_SECONDS_PER_YEAR = DAYS_PER_YEAR * SIM_SECONDS_PER_DAY // 31_104_000

/**
 * Construye un SimTime validado: entero ≥ 0 (forma del contrato, int64).
 * Lanza `RangeError` ante valores no representables.
 */
export function simTime(seconds: number): SimTime {
  if (!Number.isSafeInteger(seconds) || seconds < 0) {
    throw new RangeError(
      `simTime: valor inválido (${String(seconds)}); se requiere entero seguro >= 0`,
    )
  }
  return seconds as SimTime
}

/**
 * Formatea al formato legible del contrato "AAA-DDD-HH:MM":
 * año 1-based de 360 días (mínimo 3 dígitos), día del año 1-based (3 dígitos),
 * hora y minuto de juego (2 dígitos). Ej.: génesis → "001-001-00:00".
 */
export function formatSimTime(t: SimTime): string {
  const year = Math.floor(t / SIM_SECONDS_PER_YEAR) + 1
  const secondsIntoYear = t % SIM_SECONDS_PER_YEAR
  const day = Math.floor(secondsIntoYear / SIM_SECONDS_PER_DAY) + 1
  const secondsIntoDay = secondsIntoYear % SIM_SECONDS_PER_DAY
  const hours = Math.floor(secondsIntoDay / SIM_SECONDS_PER_HOUR)
  const minutes = Math.floor((secondsIntoDay % SIM_SECONDS_PER_HOUR) / SIM_SECONDS_PER_MINUTE)

  const yyy = String(year).padStart(3, '0')
  const ddd = String(day).padStart(3, '0')
  const hh = String(hours).padStart(2, '0')
  const mm = String(minutes).padStart(2, '0')
  return `${yyy}-${ddd}-${hh}:${mm}`
}

/**
 * Deriva el sim-time "ahora" a partir de un ancla autoritativa del servidor
 * (`meta.sim_time_seconds` + instante wall-clock local en que se recibió).
 *
 * - `frozen` (ventana de mantenimiento, C9): el sim-time no avanza.
 * - Ratio 24× sobre el tiempo real transcurrido, truncado a segundos enteros.
 * - El transcurso negativo (reloj local retrocedió) se satura a 0: el sim-time
 *   derivado nunca retrocede respecto al ancla (FAD §12.7).
 */
export function deriveNow(
  anchorSimSeconds: SimTime,
  anchorWallMs: number,
  nowWallMs: number,
  frozen: boolean,
): SimTime {
  if (frozen) {
    return anchorSimSeconds
  }
  const elapsedWallMs = Math.max(0, nowWallMs - anchorWallMs)
  const elapsedSimSeconds = Math.floor((elapsedWallMs * RATIO) / 1_000)
  return simTime(anchorSimSeconds + elapsedSimSeconds)
}

/**
 * Traduce un instante de sim-time (p. ej. un deadline de contrato) al
 * wall-clock local, usando la misma ancla que `deriveNow`. Puede devolver
 * milisegundos fraccionarios (1 segundo sim = 1000/24 ms reales).
 */
export function simToWallMs(
  target: SimTime,
  anchorSimSeconds: SimTime,
  anchorWallMs: number,
): number {
  return anchorWallMs + ((target - anchorSimSeconds) * 1_000) / RATIO
}

/** Duración sim-time (segundos) → duración real (ms). */
export function simDurationToWallMs(simSeconds: number): number {
  return (simSeconds * 1_000) / RATIO
}

/** Duración real (ms) → duración sim-time (segundos, truncada a entero). */
export function wallDurationToSimSeconds(wallMs: number): number {
  return Math.floor((wallMs * RATIO) / 1_000)
}
