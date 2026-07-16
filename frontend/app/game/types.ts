/**
 * game/types.ts — contratos del mundo Phaser (FAD §11).
 *
 * Este módulo define el puerto `WorldRenderer` (lo que el bridge necesita del
 * renderer, no lo que Phaser ofrece) y los view-models PLANOS que cruzan esa
 * frontera. No importa Phaser ni Vue: es seguro de evaluar en cualquier
 * entorno (tests node, SSR nunca lo carga porque game/ solo se importa
 * dinámicamente client-side).
 */
import type { AppEvents, EventBus } from '~/lib/kernel/event-bus'
import type { WorldProjection } from '~/lib/kernel/projection'
import type { PathPoint } from './kinematics'

// ─── Viewport en coordenadas de MUNDO (lon/lat, SRID 4326) ──────────────────

export interface WorldBbox {
  minLon: number
  minLat: number
  maxLon: number
  maxLat: number
}

// ─── View-models (coordenadas en px de pantalla-mundo, ya proyectadas) ──────

export type EntityKind = 'region' | 'city' | 'deposit' | 'node' | 'link' | 'building' | 'vehicle'

export interface RegionVM {
  id: string
  x: number
  y: number
  width: number
  height: number
  /** Tinte por bioma (config de paleta, nunca importado de Sass). */
  fillColor: number
  strokeColor: number
  name: string
}

export interface CityVM {
  id: string
  x: number
  y: number
  /** Radio en px derivado del nivel (presentación, no dominio). */
  radius: number
  color: number
  /** Etiqueta "Nombre · Nv N". */
  label: string
}

export interface DepositVM {
  id: string
  x: number
  y: number
  size: number
  color: number
}

export interface NodeVM {
  id: string
  x: number
  y: number
  size: number
  color: number
}

export interface LinkVM {
  id: string
  /** Polilínea proyectada a px. */
  points: PathPoint[]
  width: number
  color: number
}

export interface BuildingVM {
  id: string
  x: number
  y: number
  size: number
  /** Color por status (operational/under_construction/damaged/seized/…). */
  color: number
  /** Borde destacado si es de la corporación propia (C13: observable ≠ comandable). */
  owned: boolean
}

/**
 * Cinemática del vehículo (P5): el bridge deriva los datos analíticos
 * (tramo + t_entrada + función de avance) y la ESCENA interpola cada frame
 * con simNow() del SimClock. El bridge no interpola: solo replica.
 */
export type VehicleMotion =
  | { kind: 'fixed'; x: number; y: number }
  | {
      kind: 'path'
      /** LineString del segmento actual, proyectado a px. */
      points: PathPoint[]
      /** Sim-time de entrada al segmento (hito vehicle.segment_entered). */
      enteredSim: number
      /** Duración analítica estimada del segmento, en segundos de sim. */
      durationSim: number
      /** Progreso [0..1] ya recorrido en el momento del hito. */
      baseProgress: number
    }

export interface VehicleVM {
  id: string
  color: number
  owned: boolean
  motion: VehicleMotion
}

export type AnyVM = RegionVM | CityVM | DepositVM | NodeVM | LinkVM | BuildingVM | VehicleVM

export interface VMByKind {
  region: RegionVM
  city: CityVM
  deposit: DepositVM
  node: NodeVM
  link: LinkVM
  building: BuildingVM
  vehicle: VehicleVM
}

// ─── Puerto WorldRenderer (FAD §6.4: Phaser vive detrás de este puerto) ─────

export interface WorldRenderer {
  /** Upsert idempotente de un view-model (el renderer hace pooling, no destruye por frame). */
  upsert<K extends EntityKind>(kind: K, vm: VMByKind[K]): void
  /** Retira una entidad del render (la libera a su pool). */
  remove(kind: EntityKind, id: string): void
  /** Viaje de cámara a una coordenada de mundo (intent 'camera:flyTo'). */
  flyTo(lon: number, lat: number): void
  /** Encuadra la cámara para abarcar un bbox de mundo (lon/lat): centra + zoom-para-ajustar. */
  frameWorld(bbox: WorldBbox): void
}

// ─── Paleta del mundo (config; espejo de settings/_tokens.scss) ─────────────

export interface WorldPalette {
  /** Fondo del lienzo (token $color-bg-deep). */
  background: string
  regionStroke: number
  regionFillByBiome: Record<string, number>
  city: number
  deposit: number
  node: number
  /** Rampa de congestión: [fluido, medio, congestionado]. */
  linkCongestion: [number, number, number]
  buildingByStatus: Record<string, number>
  buildingDefault: number
  ownedOutline: number
  vehicle: number
  vehicleOwned: number
  selection: number
  hover: number
  label: string
}

// ─── Dependencias inyectadas al juego (desde GameCanvasHost) ────────────────

export interface GameDeps {
  /** SimClock vía bridge/host: única fuente del "ahora" de simulación (P5). */
  simNow: () => number
  /** Bus tipado de intents (game/ nunca importa stores ni componentes Vue). */
  eventBus: EventBus<AppEvents>
  /** Proyección top-down v1 del kernel (único punto que conoce la fórmula). */
  projection: WorldProjection
  /** Paleta como config (prohibido importar Sass desde game/). */
  palette?: WorldPalette
  /** Notifica cambios de viewport (mundo lon/lat) para interest management. */
  onViewportChange?: (bbox: WorldBbox) => void
}
