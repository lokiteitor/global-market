/**
 * game/input/types — tipos del input espacial (FAD §11.10, salida de intents §11.1).
 *
 * La selección y los INTENTS son la única salida del render hacia la app
 * (eventos tipados por el emitter del mundo vivo). El cliente presenta, nunca
 * decide: un intent de build/parcel es una PROPUESTA — la validación real es
 * del servidor (aquí solo forma: dentro del mundo).
 */

import type { WorldRectM } from '../bridge/vm'

/**
 * Modo de interacción del puntero:
 * - `select`: clic = picking (prioridad vehículo > edificio > ciudad >
 *   yacimiento > nodo); doble clic en vehículo = seguir con la cámara.
 * - `pan`: botón izquierdo arrastra la cámara (el central panea SIEMPRE).
 * - `build`: ghost de emplazamiento siguiendo el cursor; clic emite intent.
 * - `parcel`: arrastre de rectángulo (concesiones); al soltar emite intent.
 */
export type InputMode = 'select' | 'pan' | 'build' | 'parcel'

export type SelectableType = 'vehicle' | 'building' | 'city' | 'deposit' | 'node'

/** Selección espacial actual (`null` = nada seleccionado). */
export interface SelectionRef {
  readonly type: SelectableType
  readonly id: string
}

/** Propuesta de emplazamiento de edificio (esquina del tile ancla, en metros). */
export interface BuildIntent {
  readonly type: 'build'
  readonly xM: number
  readonly yM: number
}

/** Propuesta de parcela para concesión (rectángulo normalizado, en metros). */
export interface ParcelIntent {
  readonly type: 'parcel'
  readonly rectM: WorldRectM
}

export type WorldIntent = BuildIntent | ParcelIntent

/** Vista de cámara para la app (minimapa): viewport en metros + zoom. */
export interface CameraView {
  readonly viewM: WorldRectM
  readonly zoom: number
}

/** Eventos del mundo vivo hacia la app (consumidos vía `WorldLive.on`). */
export type WorldLiveEvents = {
  /** Cambio de selección (clic con/sin hit, o `select()` programático). */
  selection: SelectionRef | null
  /** Intent espacial (build/parcel) para que la UI lo convierta en comando REST. */
  intent: WorldIntent
  /** Vehículo seguido por la cámara (`null` = seguimiento cancelado). */
  follow: string | null
  /** Cambio de modo de interacción. */
  mode: InputMode
  /** Vista de cámara, throttled (~5 Hz) y solo si cambió (minimapa, FAD §15.11). */
  camera: CameraView
}
