/**
 * game/index — ENTRYPOINT público del motor de render (FAD §11.1/§11.2).
 *
 * Única puerta de entrada permitida desde app/ (frontera en ESLint): el host
 * de /play hará `await import('~~/game')` (carga perezosa, fase UI) y llamará
 * a `createGame`. El resto de game/** es interno.
 *
 * Contrato de aislamiento (FAD §11.1): game/ no conoce Vue, Nuxt, Pinia, la
 * red ni el DOM de la UI. Entrada de estado: dependencias inyectadas + (fase
 * UI) el bridge sobre `WorldApi`. Salida: eventos tipados (`worldApi.on`).
 */

import Phaser from 'phaser'

import type { WorldBoundsM } from '~shared/geometry/grid'
import type { ChunkStats } from './map/chunks'
import type { CameraController } from './camera'
import type { GameDeps } from './deps'
import { BootScene } from './scenes/boot-scene'
import type { LayerName, WorldSceneEvents } from './scenes/world-scene'
import { WORLD_READY_EVENT, WorldScene } from './scenes/world-scene'
import { COLOR_WORLD_BG } from './textures'

export type { GameDeps } from './deps'
export type { BiomeLookup, ChunkStats } from './map/chunks'
export type { CameraController, InteractionMode, RectM } from './camera'
export type { LayerName, WorldSceneEvents, WorldSelectEvent } from './scenes/world-scene'
export { LAYER_ORDER } from './scenes/world-scene'
export type { BiomeName, BuildingStatusName } from './textures'

// ── Mundo vivo (bridge + entidades + overlays + input) ───────────────────────
export { createWorldLive } from './world-live'
export type { WorldLive, WorldLiveDeps, WorldLiveStats } from './world-live'
export type { WorldStateSource, SegmentContextInfo } from './bridge/source'
export type { BridgeStats, VisibleVms } from './bridge/bridge'
export type {
  BuildingVM,
  CityVM,
  CongestionTier,
  DepositVM,
  LinkVM,
  NodeVM,
  RegionVM,
  VehicleVM,
  WorldRectM,
} from './bridge/vm'
export type {
  BuildIntent,
  CameraView,
  InputMode,
  ParcelIntent,
  SelectableType,
  SelectionRef,
  WorldIntent,
  WorldLiveEvents,
} from './input/types'
export { OVERLAY_NAMES, DEFAULT_OVERLAYS } from './overlays/controller'
export type { OverlayName } from './overlays/controller'

/**
 * Superficie que el bridge y la UI consumen. Deliberadamente estrecha: la UI
 * no toca escenas ni game-objects; el bridge (fase UI) recibirá además la
 * escena para poblar capas vía pools (game/pools.ts).
 */
export interface WorldApi {
  /** Cámara: pan/zoom/bounds/viewport (alimenta chunking, culling y bridge). */
  readonly camera: CameraController
  /**
   * Límites del mundo derivados del catálogo de regiones (unión de sus bounds,
   * FAD §17.6). La fase UI los empuja al llegar/cambiar el catálogo; hasta
   * entonces rige el fallback Askadia.
   */
  setWorldBoundsM(bounds: WorldBoundsM): void
  /** Culling por capa: mostrar/ocultar una capa completa (overlays, etiquetas…). */
  setLayerVisible(layer: LayerName, visible: boolean): void
  /** Suscripción a eventos espaciales; devuelve la función de baja. */
  on<K extends keyof WorldSceneEvents>(
    event: K,
    handler: (payload: WorldSceneEvents[K]) => void,
  ): () => void
  /** Contadores de chunks (diagnóstico/HUD de rendimiento). */
  chunkStats(): ChunkStats
  /** Escena del mundo para el bridge (crear pools de sprites en sus capas). */
  readonly worldScene: WorldScene
}

export interface CreatedGame {
  readonly game: Phaser.Game
  readonly worldApi: WorldApi
  /** Destruye el juego y libera el contexto WebGL (host, onBeforeUnmount). */
  destroy(): void
}

/**
 * Crea la instancia Phaser dentro de `canvasParent` y resuelve cuando
 * WorldScene está lista (texturas generadas, capas y cámara creadas).
 */
export async function createGame(canvasParent: HTMLElement, deps: GameDeps): Promise<CreatedGame> {
  const world = new WorldScene({ biomeAtM: deps.biomeAtM })

  const game = new Phaser.Game({
    type: Phaser.AUTO, // WebGL con fallback Canvas (FAD §11.3)
    parent: canvasParent,
    backgroundColor: COLOR_WORLD_BG,
    banner: false,
    disableContextMenu: true, // clic derecho reservado al menú contextual (fase UI)
    audio: { noAudio: true }, // sin assets de audio en esta fase (FAD §14.4)
    render: {
      roundPixels: true,
      antialias: true,
      powerPreference: 'high-performance',
    },
    scale: {
      mode: Phaser.Scale.RESIZE, // el canvas sigue al contenedor del layout
      width: '100%',
      height: '100%',
    },
    scene: [new BootScene(), world],
  })

  await new Promise<void>((resolve) => {
    game.events.once(WORLD_READY_EVENT, () => {
      resolve()
    })
  })

  const worldApi: WorldApi = {
    camera: world.cameraController,
    setWorldBoundsM: (bounds) => {
      world.setWorldBoundsM(bounds)
    },
    setLayerVisible: (layer, visible) => {
      world.setLayerVisible(layer, visible)
    },
    on: (event, handler) => world.worldEvents.on(event, handler),
    chunkStats: () => world.chunkStats(),
    worldScene: world,
  }

  return {
    game,
    worldApi,
    destroy: () => {
      game.destroy(true)
    },
  }
}
