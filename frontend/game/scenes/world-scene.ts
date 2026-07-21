/**
 * game/scenes/world-scene — escena principal del mundo (FAD §11.5/§16, ADR-019).
 *
 * Contenedores POR CAPA en orden fijo: el orden de dibujo por capas ES el
 * z-order (top-down cenital estricto: sin depth sorting por entidad, ADR-019).
 *
 * SIMPLIFICACIÓN CONSCIENTE vs FAD §11.5: en v1 NO hay OverlayScene paralela
 * (ni Effects/MinimapScene); overlays y efectos son capas de esta escena. El
 * beneficio de la escena paralela (alternar overlays sin repintar el mundo) se
 * conserva a nivel de capa (`setLayerVisible`); si el coste de repintado lo
 * exige, extraer la capa a una escena paralela no cambia la API pública.
 */

import Phaser from 'phaser'

import type { TileCoord } from '~shared/geometry/grid'
import { mToTile } from '~shared/geometry/grid'

import { CameraController } from '../camera'
import { TypedEmitter } from '../events'
import { ChunkManager, type BiomeLookup, type ChunkStats } from '../map/chunks'
import { COLOR_WORLD_BG } from '../textures'

export const WORLD_SCENE_KEY = 'World'

/** Evento de game.events: WorldScene lista (create() completado). */
export const WORLD_READY_EVENT = 'imperio:world-ready'

/** Orden FIJO de capas = z-order (ADR-019 §3; FAD §16.4 sin capa de agua propia: los biomas ocean/coast son terreno). */
export const LAYER_ORDER = [
  'terrain',
  'links',
  'resources',
  'buildings',
  'vehicles',
  'effects',
  'overlays',
  'labels',
] as const

export type LayerName = (typeof LAYER_ORDER)[number]

/** Selección espacial: clic (sin arrastre) sobre el mundo. */
export interface WorldSelectEvent {
  readonly xM: number
  readonly yM: number
  readonly tile: TileCoord
  readonly screenX: number
  readonly screenY: number
}

export type WorldSceneEvents = {
  select: WorldSelectEvent
}

export interface WorldSceneDeps {
  /** Bioma por bounds de región (inyectado por la fase UI desde el estado replicado). */
  readonly biomeAtM: BiomeLookup
}

/** Umbral de "clic": por debajo de este desplazamiento no es arrastre. */
const CLICK_EPS_PX = 4

export class WorldScene extends Phaser.Scene {
  readonly worldEvents = new TypedEmitter<WorldSceneEvents>()
  cameraController!: CameraController
  private layers!: Map<LayerName, Phaser.GameObjects.Layer>
  private chunkManager!: ChunkManager
  private downAt: { x: number; y: number } | null = null

  constructor(private readonly deps: WorldSceneDeps) {
    super({ key: WORLD_SCENE_KEY })
  }

  create(): void {
    this.cameras.main.setBackgroundColor(COLOR_WORLD_BG)

    // Capas en orden de creación = orden de dibujo (z-order por capa).
    this.layers = new Map(LAYER_ORDER.map((name) => [name, this.add.layer()]))

    this.chunkManager = new ChunkManager({
      scene: this,
      terrainLayer: this.layer('terrain'),
      biomeAtM: this.deps.biomeAtM,
    })

    this.cameraController = new CameraController({
      scene: this,
      onViewChanged: (viewPx) => {
        this.chunkManager.update(viewPx)
      },
    })
    // Primer poblado de chunks (la cámara arranca centrada en el mundo).
    this.chunkManager.update(this.cameraController.viewRectPx())

    this.scale.on('resize', this.onResize, this)
    this.input.on('pointerdown', this.onPointerDown, this)
    this.input.on('pointerup', this.onPointerUp, this)

    this.events.once('shutdown', this.onShutdown, this)

    this.game.events.emit(WORLD_READY_EVENT, this)
  }

  override update(_time: number, delta: number): void {
    this.cameraController.update(delta)
  }

  layer(name: LayerName): Phaser.GameObjects.Layer {
    const layer = this.layers.get(name)
    if (!layer) {
      throw new Error(`WorldScene: capa desconocida "${name}"`)
    }
    return layer
  }

  /** Culling por capa (FAD §16.5): una capa oculta no consume draw calls. */
  setLayerVisible(name: LayerName, visible: boolean): void {
    this.layer(name).setVisible(visible)
  }

  chunkStats(): ChunkStats {
    return this.chunkManager.stats()
  }

  private onResize(): void {
    this.cameraController.refresh()
  }

  private onPointerDown(pointer: Phaser.Input.Pointer): void {
    if (pointer.leftButtonDown()) {
      this.downAt = { x: pointer.x, y: pointer.y }
    }
  }

  private onPointerUp(pointer: Phaser.Input.Pointer): void {
    const downAt = this.downAt
    this.downAt = null
    if (!downAt || Math.hypot(pointer.x - downAt.x, pointer.y - downAt.y) > CLICK_EPS_PX) {
      return
    }
    const { xM, yM } = this.cameraController.screenToM(pointer.x, pointer.y)
    this.worldEvents.emit('select', {
      xM,
      yM,
      tile: mToTile(xM, yM),
      screenX: pointer.x,
      screenY: pointer.y,
    })
  }

  private onShutdown(): void {
    // Limpieza anti-fugas (FAD §11.13): input/listeners/objetos GL.
    this.scale.off('resize', this.onResize, this)
    this.input.off('pointerdown', this.onPointerDown, this)
    this.input.off('pointerup', this.onPointerUp, this)
    this.cameraController.destroy()
    this.chunkManager.destroy()
    this.worldEvents.removeAll()
  }
}
