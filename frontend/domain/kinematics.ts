/**
 * domain/kinematics — extrapolación cinemática PURA de vehículos (FAD §5.4).
 *
 * El servidor solo escribe HITOS; entre hitos, la posición es analítica y el
 * CLIENTE la deriva (interpolación honesta, nunca predicción de decisiones).
 * Este módulo es matemática pura: sin Phaser, sin Vue, sin stores.
 *
 * Fórmula del mandato (contrato/ADR-019):
 *   travel_time_sim_s = (length_m / 1000) / (base_speed_kmh / congestion_ema) * 3600
 *   progress(t)       = progress_0 + (simNow - t_observed) / travel_time * 100
 * acotado a [0, 100] — el vehículo jamás "se pasa" del final del segmento:
 * el hito de llegada lo emite el servidor.
 *
 * Las coordenadas del mundo van en METROS (SRID 0); la conversión a píxeles
 * es asunto exclusivo de GridProjection en la capa de render.
 */

import type { SimTime } from '~shared/simtime'
import type { WorldPathM, WorldPointM } from './geo'

/** Observación cinemática de un vehículo sobre un segmento (última autoritativa). */
export interface SegmentTraversalObservation {
  /** Progreso observado dentro del segmento, 0–100 (se acota si llega fuera de rango). */
  readonly progressPct0: number
  /** Sim-time de la observación (Vehicle.observedAtSim). */
  readonly simTimeObserved: SimTime
  /** Longitud del segmento en metros (LinkSegment.lengthM). */
  readonly lengthM: number
  /** Velocidad base del enlace en km/h (NetworkLink.baseSpeedKmh). */
  readonly baseSpeedKmh: number
  /** Congestión EMA del segmento (1 = fluido; mayor = más lento). */
  readonly congestionEma: number
}

/**
 * Tiempo total de recorrido del segmento en SEGUNDOS DE SIM-TIME con la
 * congestión vigente. Devuelve `Infinity` si el vehículo no puede avanzar
 * (velocidad o congestión no positivas / no finitas) y `0` para un segmento
 * de longitud no positiva.
 */
export function segmentTravelSimSeconds(
  lengthM: number,
  baseSpeedKmh: number,
  congestionEma: number,
): number {
  if (!Number.isFinite(lengthM) || lengthM <= 0) {
    return 0
  }
  if (
    !Number.isFinite(baseSpeedKmh) ||
    baseSpeedKmh <= 0 ||
    !Number.isFinite(congestionEma) ||
    congestionEma <= 0
  ) {
    return Number.POSITIVE_INFINITY
  }
  const effectiveSpeedKmh = baseSpeedKmh / congestionEma
  return (lengthM / 1_000 / effectiveSpeedKmh) * 3_600
}

/**
 * Progreso actual (%) del vehículo dentro del segmento en `simNow`, acotado a
 * [0, 100]. Reglas de borde:
 *
 * - `simNow` anterior a la observación (relojes/llegada desordenada): el
 *   progreso NO retrocede — se usa transcurso 0.
 * - Segmento de longitud 0: ya está recorrido → 100.
 * - Velocidad/congestión inválidas (travel time infinito): el vehículo no
 *   avanza → progreso observado, acotado.
 * - `progressPct0` fuera de [0, 100] se acota antes de extrapolar.
 */
export function extrapolateProgressPct(
  observation: SegmentTraversalObservation,
  simNow: SimTime,
): number {
  const start = clampPct(observation.progressPct0)
  const travelSimSeconds = segmentTravelSimSeconds(
    observation.lengthM,
    observation.baseSpeedKmh,
    observation.congestionEma,
  )
  if (travelSimSeconds === 0) {
    return 100
  }
  if (!Number.isFinite(travelSimSeconds)) {
    return start
  }
  const elapsedSimSeconds = Math.max(0, simNow - observation.simTimeObserved)
  return clampPct(start + (elapsedSimSeconds / travelSimSeconds) * 100)
}

/**
 * Punto del mundo (metros) a la fracción `fraction` ∈ [0, 1] de una polilínea,
 * por interpolación lineal sobre la LONGITUD ACUMULADA (no por índice de
 * vértice: los tramos pueden medir distinto).
 *
 * - `fraction` se acota a [0, 1]; no finita → `RangeError` (bug del llamador).
 * - Camino vacío → `RangeError`; de un solo punto → ese punto.
 * - Camino degenerado (longitud total 0) → el primer punto.
 */
export function pointAlongPath(path: WorldPathM, fraction: number): WorldPointM {
  if (path.length === 0) {
    throw new RangeError('pointAlongPath: el camino no tiene vértices')
  }
  if (!Number.isFinite(fraction)) {
    throw new RangeError(`pointAlongPath: fracción no finita (${String(fraction)})`)
  }
  const first = path[0] as WorldPointM
  if (path.length === 1) {
    return [first[0], first[1]]
  }

  const clamped = Math.min(1, Math.max(0, fraction))

  // Longitudes por tramo y total.
  const segmentLengths: number[] = []
  let totalLength = 0
  for (let i = 0; i < path.length - 1; i++) {
    const a = path[i] as WorldPointM
    const b = path[i + 1] as WorldPointM
    const length = Math.hypot(b[0] - a[0], b[1] - a[1])
    segmentLengths.push(length)
    totalLength += length
  }
  if (totalLength === 0) {
    return [first[0], first[1]]
  }

  const target = clamped * totalLength
  let travelled = 0
  for (let i = 0; i < segmentLengths.length; i++) {
    const length = segmentLengths[i] as number
    if (travelled + length >= target && length > 0) {
      const a = path[i] as WorldPointM
      const b = path[i + 1] as WorldPointM
      const t = (target - travelled) / length
      return [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t]
    }
    travelled += length
  }

  // fraction = 1 (o acumulación con error flotante): el último vértice.
  const last = path[path.length - 1] as WorldPointM
  return [last[0], last[1]]
}

/** Fracción [0, 1] equivalente a un progreso porcentual [0, 100]. */
export function progressPctToFraction(progressPct: number): number {
  return clampPct(progressPct) / 100
}

function clampPct(value: number): number {
  if (Number.isNaN(value)) {
    return 0
  }
  return Math.min(100, Math.max(0, value))
}
