/**
 * game/entities/link-geometry — geometría PURA de patrones de trazo (FAD §16.7).
 *
 * Phaser Graphics no tiene trazo discontinuo nativo: los patrones por modo de
 * enlace (sea = dashes, rail = travesaños) se calculan aquí como segmentos
 * rectos en PÍXELES de render, recorriendo la polilínea por longitud acumulada
 * (misma técnica que `poseAlongPath` de domain/kinematics). Puro y testeado
 * sin GPU.
 *
 * Los patrones se definen en px de render (escalan con el zoom): a zoom lejano
 * se compactan visualmente en una línea — aceptado; la alternativa (patrón en
 * metros) los haría invisibles de lejos.
 */

/** Punto en píxeles de render `[xPx, yPx]`. */
export type PointPx2 = readonly [number, number]

/** Segmento recto en píxeles: [x1, y1, x2, y2]. */
export type SegmentPx = readonly [number, number, number, number]

interface Leg {
  readonly x1: number
  readonly y1: number
  readonly dx: number
  readonly dy: number
  readonly length: number
}

function legsOf(points: readonly PointPx2[]): Leg[] {
  const legs: Leg[] = []
  for (let i = 1; i < points.length; i += 1) {
    const a = points[i - 1]
    const b = points[i]
    if (!a || !b) {
      continue
    }
    const dx = b[0] - a[0]
    const dy = b[1] - a[1]
    const length = Math.hypot(dx, dy)
    if (length > 0) {
      legs.push({ x1: a[0], y1: a[1], dx: dx / length, dy: dy / length, length })
    }
  }
  return legs
}

/**
 * Trazo discontinuo: segmentos de `dashPx` separados por `gapPx` a lo largo de
 * la polilínea (el patrón continúa a través de los vértices). Un tramo final
 * más corto que `dashPx` se emite recortado. `gapPx <= 0` degenera en la
 * polilínea original por tramos.
 */
export function dashSegmentsPx(
  points: readonly PointPx2[],
  dashPx: number,
  gapPx: number,
): SegmentPx[] {
  if (dashPx <= 0) {
    return []
  }
  const legs = legsOf(points)
  const out: SegmentPx[] = []
  const period = dashPx + Math.max(gapPx, 0)
  // Posición del cursor DENTRO del período del patrón (0 = empieza un dash).
  let phase = 0
  for (const leg of legs) {
    let traveled = 0
    while (traveled < leg.length) {
      const inDash = phase < dashPx
      const spanLeft = inDash ? dashPx - phase : period - phase
      const step = Math.min(spanLeft, leg.length - traveled)
      if (inDash) {
        const x1 = leg.x1 + leg.dx * traveled
        const y1 = leg.y1 + leg.dy * traveled
        out.push([x1, y1, x1 + leg.dx * step, y1 + leg.dy * step])
      }
      traveled += step
      phase = (phase + step) % period
    }
  }
  return mergeCollinearRuns(out)
}

/**
 * Travesaños perpendiculares (patrón de vía férrea): un segmento de
 * `2 × halfLenPx` centrado en la polilínea cada `spacingPx`, perpendicular a
 * la dirección local del tramo. El primero cae a media separación del origen.
 */
export function crossTicksPx(
  points: readonly PointPx2[],
  spacingPx: number,
  halfLenPx: number,
): SegmentPx[] {
  if (spacingPx <= 0 || halfLenPx <= 0) {
    return []
  }
  const legs = legsOf(points)
  const out: SegmentPx[] = []
  let untilNext = spacingPx / 2
  for (const leg of legs) {
    let traveled = 0
    while (traveled + untilNext <= leg.length) {
      traveled += untilNext
      untilNext = spacingPx
      const cx = leg.x1 + leg.dx * traveled
      const cy = leg.y1 + leg.dy * traveled
      // Perpendicular al tramo: (-dy, dx).
      out.push([
        cx - -leg.dy * halfLenPx,
        cy - leg.dx * halfLenPx,
        cx + -leg.dy * halfLenPx,
        cy + leg.dx * halfLenPx,
      ])
    }
    untilNext -= leg.length - traveled
  }
  return out
}

/**
 * Une dashes consecutivos y colineales generados por el cruce de vértices (el
 * patrón continúa a través del vértice: dos mitades del mismo dash). Mantiene
 * el conteo de dashes estable para los tests.
 */
function mergeCollinearRuns(segments: readonly SegmentPx[]): SegmentPx[] {
  const out: SegmentPx[] = []
  for (const seg of segments) {
    const prev = out[out.length - 1]
    if (prev && prev[2] === seg[0] && prev[3] === seg[1] && collinear(prev, seg)) {
      out[out.length - 1] = [prev[0], prev[1], seg[2], seg[3]]
    } else {
      out.push(seg)
    }
  }
  return out
}

function collinear(a: SegmentPx, b: SegmentPx): boolean {
  const ax = a[2] - a[0]
  const ay = a[3] - a[1]
  const bx = b[2] - b[0]
  const by = b[3] - b[1]
  return Math.abs(ax * by - ay * bx) < 1e-9
}
