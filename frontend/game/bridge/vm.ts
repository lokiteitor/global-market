/**
 * game/bridge/vm — VIEW-MODELS de render (FAD §11.6) y mapeos presentacionales puros.
 *
 * Estructuras PLANAS y baratas: solo lo que el sprite necesita (posición en
 * METROS de mundo, tamaño, estado visual, flags), nunca la entidad de dominio
 * completa. El bridge las deriva de lo VISIBLE y el renderer las reconcilia
 * contra pools. La conversión a píxeles es asunto exclusivo del renderer vía
 * GridProjection (ADR-019): aquí solo metros.
 *
 * Los mapeos presentacionales (congestión → tier, culling de etiquetas por
 * zoom, tinte de vehículo, escala de ciudad) son funciones puras y testeadas.
 */

import { PX_PER_M } from '~shared/geometry/grid'
import type { BuildingStatus } from '~domain/buildings'
import type { VehicleStatus } from '~domain/fleet'
import type { WorldPathM } from '~domain/geo'

/** Rectángulo en metros de mundo (mismo shape que RectM de game/camera). */
export interface WorldRectM {
  readonly xM: number
  readonly yM: number
  readonly widthM: number
  readonly heightM: number
}

// ── View-models ──────────────────────────────────────────────────────────────

/** Edificio: bbox del footprint + estado visual + propiedad. */
export interface BuildingVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
  readonly wM: number
  readonly hM: number
  readonly status: BuildingStatus
  /** Código del BuildingType del catálogo (elige textura por tipo); `null` si no cargó. */
  readonly typeCode: string | null
  readonly own: boolean
}

/** Vehículo con su posición ANALÍTICA ya extrapolada al sim-now del frame. */
export interface VehicleVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
  /** Orientación del vector del tramo (radianes; 0 = +X, eje Y hacia abajo). */
  readonly angleRad: number
  readonly status: VehicleStatus
  readonly own: boolean
}

export interface CityVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
  readonly level: number
  readonly name: string
  readonly influenceRadiusM: number
}

export interface DepositVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
}

export interface NodeVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
}

/** Enlace logístico: trazado en metros + tier de congestión (peor segmento). */
export interface LinkVM {
  readonly id: string
  readonly points: WorldPathM
  readonly congestionTier: CongestionTier
}

/** Región para el overlay: bbox de sus bounds + nombre. */
export interface RegionVM {
  readonly id: string
  readonly name: string
  readonly xM: number
  readonly yM: number
  readonly wM: number
  readonly hM: number
}

// ── Mapeos presentacionales puros ────────────────────────────────────────────

/** Tier visual de congestión (mandato): verde < 1.2 ≤ ámbar < 2 ≤ rojo. */
export type CongestionTier = 'fluid' | 'busy' | 'jammed'

export function congestionTier(congestionEma: number): CongestionTier {
  if (Number.isNaN(congestionEma) || congestionEma < 1.2) {
    return 'fluid'
  }
  return congestionEma < 2 ? 'busy' : 'jammed'
}

/** Umbral de zoom por debajo del cual las etiquetas se ocultan (mandato). */
export const LABEL_MIN_ZOOM = 0.6

/** Culling de etiquetas por zoom: solo a zoom cercano son legibles y útiles. */
export function labelsVisibleAtZoom(zoom: number): boolean {
  return zoom >= LABEL_MIN_ZOOM
}

/**
 * Tinte del sprite de vehículo por estado (`null` = sin tinte, color base).
 * `broken` en rojo (mandato); `sealed` (handoff, no comandable) apagado en gris.
 */
export function vehicleTint(status: VehicleStatus): number | null {
  switch (status) {
    case 'broken':
      return 0xc4504a // $color-danger-500
    case 'sealed':
      return 0x8896ab // $color-gray-400 aprox (apagado, visible pero inerte)
    default:
      return null
  }
}

/** Escala del sprite de ciudad por nivel (nivel 1 → 0.8; crece 0.2 por nivel). */
export function cityScale(level: number): number {
  const clamped = Math.max(1, Math.min(10, Math.floor(level)))
  return 0.6 + 0.2 * clamped
}

/** Diámetro base de la textura de ciudad (px de render); espejo de textures.ts. */
export const CITY_TEXTURE_PX = 64

/** Radio VISUAL de la ciudad en metros de mundo (consistencia render ↔ picking). */
export function cityRadiusM(level: number): number {
  return ((CITY_TEXTURE_PX / 2) * cityScale(level)) / PX_PER_M
}
