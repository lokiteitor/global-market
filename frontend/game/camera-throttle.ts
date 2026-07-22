/**
 * game/camera-throttle — puerta PURA del evento `camera` del mundo vivo.
 *
 * El minimapa (FAD §15.11) necesita la vista de cámara a baja frecuencia, no
 * por frame: esta puerta emite solo si la vista CAMBIÓ y pasó el intervalo
 * mínimo desde la última emisión (~5 Hz). El reloj entra por parámetro
 * (wall-clock del llamante): testeable sin timers.
 */

import type { WorldRectM } from './bridge/vm'

/** Intervalo mínimo entre emisiones del evento camera (≈5 Hz). */
export const CAMERA_EVENT_MIN_INTERVAL_MS = 200

export type CameraEventGate = (nowMs: number, viewM: WorldRectM, zoom: number) => boolean

/** Crea una puerta con estado propio (última vista emitida + instante). */
export function createCameraEventGate(
  minIntervalMs: number = CAMERA_EVENT_MIN_INTERVAL_MS,
): CameraEventGate {
  let lastEmitMs = Number.NEGATIVE_INFINITY
  let last: { readonly view: WorldRectM; readonly zoom: number } | null = null

  return (nowMs, viewM, zoom) => {
    const changed =
      last === null ||
      last.zoom !== zoom ||
      last.view.xM !== viewM.xM ||
      last.view.yM !== viewM.yM ||
      last.view.widthM !== viewM.widthM ||
      last.view.heightM !== viewM.heightM
    if (!changed || nowMs - lastEmitMs < minIntervalMs) {
      return false
    }
    lastEmitMs = nowMs
    last = { view: viewM, zoom }
    return true
  }
}
