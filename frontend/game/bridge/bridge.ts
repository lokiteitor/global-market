/**
 * game/bridge/bridge — WorldStateBridge (FAD §11.6/§11.7): stores → VMs → diffs.
 *
 * Orquestador SIN Phaser y SIN Vue (testeable con dobles): observa el puerto
 * `WorldStateSource` y el viewport de la cámara, deriva view-models de lo
 * VISIBLE (derive.ts) y difunde DIFFS (diff.ts) a los sinks del renderer.
 *
 * Frecuencia (coalescing, FAD §11.6/§21.7): los cambios de store solo marcan
 * `dirty`; `tick()` — llamado UNA vez por frame (rAF del game-loop) — hace
 * como máximo UNA recomputación de estáticos por frame, cuando hay dirty o el
 * viewport cambió. Los VEHÍCULOS se recomputan CADA frame (posición analítica
 * extrapolada con el sim-now del SimClock, FAD §11.7): su diff sale barato
 * (pocos vehículos, VMs planos) y el renderer solo toca sprites que cambian.
 */

import type { NetworkNode, NodeId } from '~domain/logistics'

import { deriveStatics, deriveVehicles, CULL_MARGIN_M } from './derive'
import type { StaticVms } from './derive'
import type { VmDiff } from './diff'
import { diffVms } from './diff'
import type { WorldStateSource } from './source'
import type {
  BuildingVM,
  CityVM,
  DepositVM,
  LinkVM,
  NodeVM,
  RegionVM,
  VehicleVM,
  WorldRectM,
} from './vm'

/** Puerto mínimo de cámara que el bridge necesita (CameraController lo cumple). */
export interface CameraPort {
  viewRectM(): WorldRectM
  zoom(): number
}

/** Consumidor de diffs de una colección de VMs (renderers de game/entities). */
export interface EntitySink<VM> {
  apply(diff: VmDiff<VM>): void
}

export interface BridgeSinks {
  readonly buildings: EntitySink<BuildingVM>
  readonly cities: EntitySink<CityVM>
  readonly deposits: EntitySink<DepositVM>
  readonly nodes: EntitySink<NodeVM>
  readonly links: EntitySink<LinkVM>
  readonly regions: EntitySink<RegionVM>
  readonly vehicles: EntitySink<VehicleVM>
}

/** VMs visibles actuales (fuente del hit-testing de game/input). */
export interface VisibleVms extends StaticVms {
  readonly vehicles: ReadonlyMap<string, VehicleVM>
}

export interface BridgeStats {
  readonly buildings: number
  readonly cities: number
  readonly deposits: number
  readonly nodes: number
  readonly links: number
  readonly regions: number
  readonly vehicles: number
  /** Recomputaciones de estáticos realizadas (diagnóstico de coalescing). */
  readonly staticRecomputes: number
}

export interface WorldStateBridgeOptions {
  readonly source: WorldStateSource
  readonly camera: CameraPort
  readonly sinks: BridgeSinks
  /** Margen de culling en metros (default CULL_MARGIN_M). */
  readonly marginM?: number
}

const EMPTY_STATICS: StaticVms = {
  buildings: new Map(),
  cities: new Map(),
  deposits: new Map(),
  nodes: new Map(),
  links: new Map(),
  regions: new Map(),
}

export class WorldStateBridge {
  private statics: StaticVms = EMPTY_STATICS
  private vehicles: ReadonlyMap<string, VehicleVM> = new Map()
  private nodeMap = new Map<string, NetworkNode>()
  private lastView: WorldRectM | null = null
  private dirty = true
  private staticRecomputes = 0
  private unsubscribe: (() => void) | null = null
  private readonly marginM: number

  constructor(private readonly options: WorldStateBridgeOptions) {
    this.marginM = options.marginM ?? CULL_MARGIN_M
  }

  /** Se suscribe al estado replicado (cambios ⇒ dirty; recomputa en `tick`). */
  start(): void {
    this.unsubscribe ??= this.options.source.subscribe(() => {
      this.dirty = true
    })
  }

  /**
   * Tick del game-loop (≤1 por rAF): recomputa estáticos SI hay dirty o el
   * viewport cambió, y los vehículos SIEMPRE (posición analítica del frame).
   */
  tick(): void {
    const view = this.options.camera.viewRectM()
    if (this.dirty || !this.viewEquals(view)) {
      this.recomputeStatics(view)
      this.lastView = view
      this.dirty = false
    }
    this.recomputeVehicles(view)
  }

  /** VMs visibles actuales (hit-testing y marcador de selección). */
  visible(): VisibleVms {
    return { ...this.statics, vehicles: this.vehicles }
  }

  stats(): BridgeStats {
    return {
      buildings: this.statics.buildings.size,
      cities: this.statics.cities.size,
      deposits: this.statics.deposits.size,
      nodes: this.statics.nodes.size,
      links: this.statics.links.size,
      regions: this.statics.regions.size,
      vehicles: this.vehicles.size,
      staticRecomputes: this.staticRecomputes,
    }
  }

  destroy(): void {
    this.unsubscribe?.()
    this.unsubscribe = null
  }

  private viewEquals(view: WorldRectM): boolean {
    const last = this.lastView
    return (
      last !== null &&
      last.xM === view.xM &&
      last.yM === view.yM &&
      last.widthM === view.widthM &&
      last.heightM === view.heightM
    )
  }

  private recomputeStatics(view: WorldRectM): void {
    const source = this.options.source
    const nodes = source.nodes()
    // Cache de nodos para la derivación de vehículos por frame (fallbacks).
    this.nodeMap = new Map(nodes.map((n) => [n.id as string, n]))

    const next = deriveStatics(
      {
        regions: source.regions(),
        cities: source.cities(),
        deposits: source.deposits(),
        nodes,
        links: source.links(),
        buildings: source.buildings(),
        buildingTypeCode: source.buildingTypeCode,
        ownAccountId: source.ownAccountId(),
      },
      view,
      this.marginM,
    )

    const sinks = this.options.sinks
    this.applyIfChanged(sinks.buildings, this.statics.buildings, next.buildings)
    this.applyIfChanged(sinks.cities, this.statics.cities, next.cities)
    this.applyIfChanged(sinks.deposits, this.statics.deposits, next.deposits)
    this.applyIfChanged(sinks.nodes, this.statics.nodes, next.nodes)
    this.applyIfChanged(sinks.links, this.statics.links, next.links)
    this.applyIfChanged(sinks.regions, this.statics.regions, next.regions)

    this.statics = next
    this.staticRecomputes += 1
  }

  private recomputeVehicles(view: WorldRectM): void {
    const source = this.options.source
    const next = deriveVehicles(
      {
        vehicles: source.vehicles(),
        segmentContext: source.segmentContext,
        nodeById: (id: NodeId) => this.nodeMap.get(id) ?? null,
        ownAccountId: source.ownAccountId(),
        simNow: source.simNow(),
      },
      view,
      this.marginM,
    )
    this.applyIfChanged(this.options.sinks.vehicles, this.vehicles, next)
    this.vehicles = next
  }

  private applyIfChanged<VM extends object>(
    sink: EntitySink<VM>,
    prev: ReadonlyMap<string, VM>,
    next: ReadonlyMap<string, VM>,
  ): void {
    const diff = diffVms(prev, next)
    if (diff.upserts.length > 0 || diff.removes.length > 0) {
      sink.apply(diff)
    }
  }
}
