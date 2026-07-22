/**
 * minimap-math — transformación PURA mundo ↔ canvas del minimapa (ADR-026).
 *
 * Encaja los bounds del mundo (`WorldBoundsM`, con mínimos posiblemente
 * negativos) en un canvas conservando la relación de aspecto (aspect-fit,
 * centrado). Sin DOM ni canvas: testeable en vitest.
 */

import type { WorldBoundsM } from '~shared/geometry/grid'
import type { WorldRectM } from '~~/game'

/** Transformación afín mundo → canvas (escala uniforme + offset). */
export interface MinimapTransform {
  readonly scale: number
  readonly offsetX: number
  readonly offsetY: number
  readonly bounds: WorldBoundsM
}

/** Rectángulo en píxeles de canvas. */
export interface CanvasRect {
  readonly x: number
  readonly y: number
  readonly w: number
  readonly h: number
}

/**
 * Aspect-fit de `bounds` en un canvas `canvasW × canvasH`, centrado en el eje
 * sobrante. `null` si el canvas o los bounds son degenerados.
 */
export function makeTransform(
  bounds: WorldBoundsM,
  canvasW: number,
  canvasH: number,
): MinimapTransform | null {
  const worldW = bounds.maxXM - bounds.minXM
  const worldH = bounds.maxYM - bounds.minYM
  if (worldW <= 0 || worldH <= 0 || canvasW <= 0 || canvasH <= 0) {
    return null
  }
  const scale = Math.min(canvasW / worldW, canvasH / worldH)
  return {
    scale,
    offsetX: (canvasW - worldW * scale) / 2 - bounds.minXM * scale,
    offsetY: (canvasH - worldH * scale) / 2 - bounds.minYM * scale,
    bounds,
  }
}

/** Punto del mundo (metros) → punto del canvas. */
export function worldToMini(
  t: MinimapTransform,
  xM: number,
  yM: number,
): { readonly x: number; readonly y: number } {
  return { x: xM * t.scale + t.offsetX, y: yM * t.scale + t.offsetY }
}

/** Punto del canvas → punto del mundo (metros). Inversa exacta de `worldToMini`. */
export function miniToWorld(
  t: MinimapTransform,
  x: number,
  y: number,
): { readonly xM: number; readonly yM: number } {
  return { xM: (x - t.offsetX) / t.scale, yM: (y - t.offsetY) / t.scale }
}

/** Rectángulo del mundo → rectángulo del canvas (sin clamping: el caller recorta con clip). */
export function rectToMini(t: MinimapTransform, rect: WorldRectM): CanvasRect {
  const origin = worldToMini(t, rect.xM, rect.yM)
  return { x: origin.x, y: origin.y, w: rect.widthM * t.scale, h: rect.heightM * t.scale }
}
