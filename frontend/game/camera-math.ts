/**
 * game/camera-math — matemática PURA de la cámara (FAD §17, ADR-019).
 *
 * Toda la aritmética de pan/zoom/clamping vive aquí, separada del wrapper
 * sobre la cámara de Phaser (camera.ts), para testearse sin GPU. El estado se
 * modela como `{ centerX, centerY, zoom }` en PÍXELES DE RENDER del mundo
 * (espacio de `shared/geometry`): zoom 1 ⇒ 1 px de mundo = 1 px de pantalla.
 */

import type { RectPx } from '~shared/geometry/grid'
import { WORLD_SIZE_PX } from '~shared/geometry/grid'

/** Límites de zoom del mandato: mundo entero legible ↔ detalle de edificio. */
export const ZOOM_MIN = 0.15
export const ZOOM_MAX = 3

/** Estado de cámara: centro en píxeles de render del mundo + zoom. */
export interface CameraState {
  readonly centerX: number
  readonly centerY: number
  readonly zoom: number
}

/** Tamaño del viewport en píxeles de PANTALLA. */
export interface ViewportSize {
  readonly width: number
  readonly height: number
}

export function clampZoom(zoom: number, min = ZOOM_MIN, max = ZOOM_MAX): number {
  return Math.min(max, Math.max(min, zoom))
}

/**
 * Rueda → factor multiplicativo de zoom (exponencial: pasos perceptualmente
 * uniformes). `deltaY` positivo (rueda hacia abajo) aleja; negativo acerca.
 */
export function wheelZoomFactor(deltaY: number, sensitivity = 0.0015): number {
  return Math.exp(-deltaY * sensitivity)
}

/**
 * Zoom HACIA EL CURSOR (FAD §17.2): el punto de mundo bajo el puntero
 * permanece fijo en pantalla tras cambiar el zoom (comportamiento estándar de
 * mapas). `cursor` en píxeles de pantalla relativos al viewport; el clamping a
 * bounds del mundo se aplica después con `clampCenter`.
 *
 * Derivación: worldUnderCursor = center + (cursor − viewport/2) / zoom; se
 * despeja el nuevo center para el nuevo zoom manteniendo worldUnderCursor.
 */
export function zoomAtCursor(
  state: CameraState,
  cursorX: number,
  cursorY: number,
  viewport: ViewportSize,
  nextZoomRaw: number,
): CameraState {
  const zoom = clampZoom(nextZoomRaw)
  const offsetX = cursorX - viewport.width / 2
  const offsetY = cursorY - viewport.height / 2
  const worldX = state.centerX + offsetX / state.zoom
  const worldY = state.centerY + offsetY / state.zoom
  return {
    centerX: worldX - offsetX / zoom,
    centerY: worldY - offsetY / zoom,
    zoom,
  }
}

/**
 * Pan por arrastre: el mundo sigue al puntero, luego el centro se desplaza en
 * sentido CONTRARIO al delta de pantalla, dividido por el zoom.
 */
export function panBy(state: CameraState, deltaScreenX: number, deltaScreenY: number): CameraState {
  return {
    centerX: state.centerX - deltaScreenX / state.zoom,
    centerY: state.centerY - deltaScreenY / state.zoom,
    zoom: state.zoom,
  }
}

/**
 * Clampea el centro a los bounds del mundo (FAD §17.6): el viewport no se sale
 * del rectángulo [0, worldPx]². Si a este zoom el viewport es más ancho/alto
 * que el mundo, se centra el mundo en ese eje (encuadre al alejar al máximo).
 */
export function clampCenter(
  state: CameraState,
  viewport: ViewportSize,
  worldPx = WORLD_SIZE_PX,
): CameraState {
  const halfW = viewport.width / (2 * state.zoom)
  const halfH = viewport.height / (2 * state.zoom)
  const clampAxis = (center: number, half: number): number => {
    if (half * 2 >= worldPx) {
      return worldPx / 2
    }
    return Math.min(worldPx - half, Math.max(half, center))
  }
  return {
    centerX: clampAxis(state.centerX, halfW),
    centerY: clampAxis(state.centerY, halfH),
    zoom: state.zoom,
  }
}

/**
 * Rectángulo de mundo visible (en píxeles de render) para el estado dado:
 * alimenta el chunking/culling (`visibleChunks`) y el bridge.
 */
export function viewRect(state: CameraState, viewport: ViewportSize): RectPx {
  const width = viewport.width / state.zoom
  const height = viewport.height / state.zoom
  return {
    x: state.centerX - width / 2,
    y: state.centerY - height / 2,
    width,
    height,
  }
}

/**
 * Decaimiento exponencial de la velocidad de inercia (px pantalla/ms).
 * `tauMs` = constante de tiempo; por debajo de `stopBelow` se satura a 0
 * (evita colas infinitas de recomputación de chunks).
 */
export function decayVelocity(velocity: number, deltaMs: number, tauMs = 200, stopBelow = 0.01): number {
  const next = velocity * Math.exp(-deltaMs / tauMs)
  return Math.abs(next) < stopBelow ? 0 : next
}
