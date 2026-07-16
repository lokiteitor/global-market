/**
 * game/kinematics.ts — interpolación pura de vehículos (P5, FAD §5.1).
 *
 * Funciones puras sin Phaser ni estado: la escena las llama cada frame con el
 * "ahora" del SimClock. El servidor solo emite hitos (vehicle.segment_entered,
 * vehicle.arrived…); la posición entre hitos es presentación derivada
 * analíticamente: progress = clamp(base + (simNow - entered)/duration, 0, 1).
 * El cliente NUNCA decide la llegada: solo pinta el avance (P1).
 */

export interface PathPoint {
  x: number
  y: number
}

export interface PathSample {
  x: number
  y: number
  /** Orientación del vector del tramo actual, en radianes (atan2 estándar). */
  angle: number
}

export interface SegmentKinematics {
  /** Sim-time de entrada al segmento (último hito autoritativo). */
  enteredSim: number
  /** Duración analítica del segmento en segundos de sim (> 0). */
  durationSim: number
  /** Progreso [0..1] ya recorrido en el momento del hito (segment_progress_pct/100). */
  baseProgress: number
}

/** Progreso [0..1] dentro del segmento para un instante de sim dado. */
export function progressAt(kin: SegmentKinematics, simNow: number): number {
  if (kin.durationSim <= 0) return 1
  const raw = kin.baseProgress + (simNow - kin.enteredSim) / kin.durationSim
  return Math.min(1, Math.max(0, raw))
}

/** Longitud euclídea total de una polilínea (en las unidades de sus puntos). */
export function pathLength(points: readonly PathPoint[]): number {
  let total = 0
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1] as PathPoint
    const b = points[i] as PathPoint
    total += Math.hypot(b.x - a.x, b.y - a.y)
  }
  return total
}

/**
 * Interpola una polilínea (LineString proyectado) en progress ∈ [0..1] por
 * longitud de arco. Devuelve posición + orientación del tramo (para orientar
 * el triángulo del vehículo al vector del segmento).
 */
export function interpolateOnPath(points: readonly PathPoint[], progress: number): PathSample {
  if (points.length === 0) return { x: 0, y: 0, angle: 0 }
  const first = points[0] as PathPoint
  if (points.length === 1) return { x: first.x, y: first.y, angle: 0 }

  const p = Math.min(1, Math.max(0, progress))
  const total = pathLength(points)

  if (total === 0) {
    const b = points[1] as PathPoint
    return { x: first.x, y: first.y, angle: Math.atan2(b.y - first.y, b.x - first.x) }
  }

  let remaining = p * total
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1] as PathPoint
    const b = points[i] as PathPoint
    const seg = Math.hypot(b.x - a.x, b.y - a.y)
    const angle = Math.atan2(b.y - a.y, b.x - a.x)
    if (remaining <= seg || i === points.length - 1) {
      const t = seg === 0 ? 0 : Math.min(1, remaining / seg)
      return { x: a.x + (b.x - a.x) * t, y: a.y + (b.y - a.y) * t, angle }
    }
    remaining -= seg
  }

  // Inalcanzable (el último tramo siempre retorna), pero TS quiere un cierre.
  const last = points[points.length - 1] as PathPoint
  return { x: last.x, y: last.y, angle: 0 }
}
