/**
 * game/world-live — ensamblado del MUNDO VIVO (FAD §11.6–§11.10).
 *
 * Une motor (WorldApi de game/index) + estado replicado (puerto
 * `WorldStateSource`, implementado por la app) en el mundo jugable: bridge de
 * view-models con culling y diffs, renderers por capa contra pools, overlays
 * por visibilidad y el input espacial (selección, intents, follow).
 *
 * Z-order dentro de cada capa: un Container POR RENDERER, creado en orden
 * (enlaces bajo nodos; edificios bajo bordes bajo ciudades; influencia bajo
 * bordes de región). El game-loop llama a `tick` una vez por frame (rAF):
 * coalescing natural de ≤1 recomputación de estáticos por frame (FAD §21.7).
 */

import type { WorldApi } from './index'
import { WorldStateBridge } from './bridge/bridge'
import type { BridgeStats } from './bridge/bridge'
import type { WorldStateSource } from './bridge/source'
import { BuildingsRenderer } from './entities/buildings'
import { CitiesRenderer } from './entities/cities'
import { CityLabelsRenderer } from './entities/labels'
import { LinksRenderer } from './entities/links'
import type { RenderParent } from './entities/parent'
import { PointSpritesRenderer } from './entities/points'
import { makeExtraTextures } from './entities/textures-extra'
import { VehiclesRenderer } from './entities/vehicles'
import { InputController } from './input/controller'
import type { InputMode, SelectionRef, WorldLiveEvents } from './input/types'
import type { ChunkStats } from './map/chunks'
import { InfluenceOverlay } from './overlays/influence'
import { OverlayController } from './overlays/controller'
import type { OverlayName } from './overlays/controller'
import { RegionsOverlay } from './overlays/regions'
import type { DepositVM, NodeVM, WorldRectM } from './bridge/vm'
import { TEXTURES } from './textures'

/** Evento de step de escena de Phaser (Phaser.Scenes.Events.UPDATE). */
const SCENE_UPDATE_EVENT = 'update'

export interface WorldLiveStats {
  readonly bridge: BridgeStats
  readonly chunks: ChunkStats
  /** Objetos de render ACTIVOS por renderer (verificación de culling/pooling). */
  readonly renderObjects: Readonly<Record<string, number>>
}

/** API del mundo vivo para la fase UI (selección, intents, overlays, stats). */
export interface WorldLive {
  setMode(mode: InputMode): void
  mode(): InputMode
  /** Centra la cámara en un punto del mundo (metros; clampeado a bounds). */
  centerOnM(xM: number, yM: number): void
  /** Encuadra un rectángulo del mundo (salto de región del HUD/minimapa). */
  fitRectM(rect: WorldRectM): void
  setOverlay(name: OverlayName, on: boolean): void
  overlays(): Readonly<Record<OverlayName, boolean>>
  selection(): SelectionRef | null
  /** Selección programática desde la UI (`null` deselecciona). */
  select(selection: SelectionRef | null): void
  /** Sigue un vehículo con la cámara (`null` cancela; el pan manual también). */
  setFollow(vehicleId: string | null): void
  followedVehicleId(): string | null
  /** Suscripción a eventos del mundo vivo; devuelve la función de baja. */
  on<K extends keyof WorldLiveEvents>(
    event: K,
    handler: (payload: WorldLiveEvents[K]) => void,
  ): () => void
  stats(): WorldLiveStats
  destroy(): void
}

export interface WorldLiveDeps {
  readonly worldApi: WorldApi
  readonly source: WorldStateSource
}

export function createWorldLive(deps: WorldLiveDeps): WorldLive {
  const { worldApi, source } = deps
  const scene = worldApi.worldScene
  const camera = worldApi.camera

  makeExtraTextures(scene)

  // Containers por renderer (z-order dentro de la capa = orden de creación).
  const container = (
    layer: 'links' | 'buildings' | 'labels' | 'vehicles' | 'resources' | 'overlays',
  ): RenderParent => {
    const c = scene.add.container(0, 0)
    scene.layer(layer).add(c)
    return c
  }
  const linksParent = container('links')
  const nodesParent = container('links')
  const resourcesParent = container('resources')
  const buildingsParent = container('buildings')
  const bordersParent = container('buildings')
  const citiesParent = container('buildings')
  const vehiclesParent = container('vehicles')
  const influenceContainer = scene.add.container(0, 0)
  scene.layer('overlays').add(influenceContainer)
  const regionsContainer = scene.add.container(0, 0)
  scene.layer('overlays').add(regionsContainer)
  const labelsParent = container('labels')

  // Renderers por capa (consumen diffs de VMs, reconcilian contra pools).
  const links = new LinksRenderer(scene, linksParent)
  const nodes = new PointSpritesRenderer<NodeVM>(scene, nodesParent, TEXTURES.node)
  const deposits = new PointSpritesRenderer<DepositVM>(scene, resourcesParent, TEXTURES.deposit)
  const buildings = new BuildingsRenderer(scene, buildingsParent, bordersParent)
  const cities = new CitiesRenderer(scene, citiesParent)
  const vehicles = new VehiclesRenderer(scene, vehiclesParent)
  const labels = new CityLabelsRenderer(scene, labelsParent)
  const influence = new InfluenceOverlay(scene, influenceContainer)
  const regions = new RegionsOverlay(scene, regionsContainer)

  const bridge = new WorldStateBridge({
    source,
    camera,
    sinks: {
      buildings,
      // Un mismo diff de ciudades alimenta sprite, etiqueta e influencia.
      cities: {
        apply: (diff) => {
          cities.apply(diff)
          labels.apply(diff)
          influence.apply(diff)
        },
      },
      deposits,
      nodes,
      links,
      regions,
      vehicles,
    },
  })
  bridge.start()

  const overlays = new OverlayController({
    setLayerVisible: (layer, visible) => {
      worldApi.setLayerVisible(layer, visible)
    },
    setRegionsVisible: (visible) => regionsContainer.setVisible(visible),
    setInfluenceVisible: (visible) => influenceContainer.setVisible(visible),
    setCongestionColoring: (on) => {
      links.setCongestionColoring(on)
    },
  })

  const input = new InputController(scene, camera, bridge)

  // Encuadre inicial (claridad ante todo, FAD §21): el centro geométrico del
  // mundo suele ser terreno vacío. En cuanto el estado replicado ofrece un
  // foco — primer edificio propio; si no, la primera ciudad — la cámara se
  // centra UNA sola vez; después el encuadre es del jugador.
  let framedInitialView = false
  const frameInitialView = (): void => {
    if (framedInitialView) {
      return
    }
    const own = source.ownAccountId()
    const ownBuilding =
      own === null ? undefined : source.buildings().find((b) => b.ownerAccountId === own)
    const focus = ownBuilding?.footprintM[0]?.[0] ?? source.cities()[0]?.locationM
    if (focus === undefined) {
      return
    }
    framedInitialView = true
    // Plano general (zoom 0.25, clampeado por la cámara): con el foco
    // centrado se abarca su entorno logístico (enlaces, nodos, ciudad
    // cercana), no solo el edificio aislado.
    camera.setZoom(0.25)
    camera.centerOnM(focus[0], focus[1])
  }

  const onUpdate = (): void => {
    bridge.tick()
    frameInitialView()
    labels.setZoom(camera.zoom())
    input.tick()
  }
  scene.events.on(SCENE_UPDATE_EVENT, onUpdate)
  // Primer poblado sin esperar al siguiente frame (la escena ya corre).
  onUpdate()

  return {
    setMode: (mode) => {
      input.setMode(mode)
    },
    mode: () => input.mode(),
    centerOnM: (xM, yM) => {
      camera.centerOnM(xM, yM)
    },
    fitRectM: (rect) => {
      camera.fitRectM(rect.xM, rect.yM, rect.widthM, rect.heightM)
    },
    setOverlay: (name, on) => {
      overlays.set(name, on)
    },
    overlays: () => overlays.state(),
    selection: () => input.selection(),
    select: (selection) => {
      input.select(selection)
    },
    setFollow: (vehicleId) => {
      input.setFollow(vehicleId)
    },
    followedVehicleId: () => input.followedVehicleId(),
    on: (event, handler) => input.events.on(event, handler),
    stats: () => ({
      bridge: bridge.stats(),
      chunks: worldApi.chunkStats(),
      renderObjects: {
        links: links.count(),
        nodes: nodes.count(),
        deposits: deposits.count(),
        buildings: buildings.count(),
        cities: cities.count(),
        vehicles: vehicles.count(),
        labels: labels.count(),
        influence: influence.count(),
        regions: regions.count(),
      },
    }),
    destroy: () => {
      scene.events.off(SCENE_UPDATE_EVENT, onUpdate)
      input.destroy()
      bridge.destroy()
      links.destroy()
      nodes.destroy()
      deposits.destroy()
      buildings.destroy()
      cities.destroy()
      vehicles.destroy()
      labels.destroy()
      influence.destroy()
      regions.destroy()
    },
  }
}
