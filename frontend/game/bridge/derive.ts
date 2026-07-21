/**
 * game/bridge/derive — derivación PURA de view-models de lo VISIBLE (FAD §11.6/§11.7).
 *
 * Funciones deterministas: dado el estado replicado (entidades de dominio), el
 * viewport en metros y el sim-now, producen los VMs planos de las entidades
 * que intersecan el viewport expandido (culling por rect en METROS — el coste
 * de render es proporcional a lo visible, no al mundo).
 *
 * La posición de los vehículos `on-segment` es ANALÍTICA (FAD §11.7): se
 * extrapola con domain/kinematics al sim-now del frame y se interpola a lo
 * largo del path del enlace (el progreso del contrato es DENTRO del segmento;
 * aquí se traduce a fracción del path completo vía las longitudes por `seq`).
 * Dirección asumida: la del path (fromNode → toNode), la misma en que el
 * contrato ordena los segmentos.
 *
 * Sin Phaser, sin Vue: matemática y proyecciones puras (testeable).
 */

import type { SimTime } from '~shared/simtime'
import type { AccountId } from '~domain/auth'
import type { Building } from '~domain/buildings'
import type { Vehicle } from '~domain/fleet'
import type { WorldPathM, WorldPointM, WorldPolygonM } from '~domain/geo'
import { extrapolateProgressPct, progressPctToFraction } from '~domain/kinematics'
import type { NetworkLink, NetworkNode, NodeId, SegmentId } from '~domain/logistics'
import type { BuildingTypeId, City, Region, ResourceDeposit } from '~domain/world'

import type { SegmentContextInfo } from './source'
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
import { cityRadiusM, congestionTier } from './vm'

/** Margen de culling por defecto (metros): evita el "pop" en los bordes. */
export const CULL_MARGIN_M = 500

// ── Geometría de culling (pura) ──────────────────────────────────────────────

export function expandRectM(rect: WorldRectM, marginM: number): WorldRectM {
  return {
    xM: rect.xM - marginM,
    yM: rect.yM - marginM,
    widthM: rect.widthM + 2 * marginM,
    heightM: rect.heightM + 2 * marginM,
  }
}

export function rectContainsM(rect: WorldRectM, xM: number, yM: number): boolean {
  return (
    xM >= rect.xM && xM <= rect.xM + rect.widthM && yM >= rect.yM && yM <= rect.yM + rect.heightM
  )
}

export function rectsIntersectM(a: WorldRectM, b: WorldRectM): boolean {
  return (
    a.xM <= b.xM + b.widthM &&
    b.xM <= a.xM + a.widthM &&
    a.yM <= b.yM + b.heightM &&
    b.yM <= a.yM + a.heightM
  )
}

/** Bbox del anillo exterior de un polígono; `null` si no hay vértices. */
export function polygonBoundsM(polygon: WorldPolygonM): WorldRectM | null {
  const ring = polygon[0]
  if (!ring || ring.length === 0) {
    return null
  }
  return pathBoundsM(ring)
}

/** Bbox de una polilínea; `null` si no hay vértices. */
export function pathBoundsM(path: WorldPathM): WorldRectM | null {
  const first = path[0]
  if (!first) {
    return null
  }
  let minX = first[0]
  let maxX = first[0]
  let minY = first[1]
  let maxY = first[1]
  for (const [x, y] of path) {
    minX = Math.min(minX, x)
    maxX = Math.max(maxX, x)
    minY = Math.min(minY, y)
    maxY = Math.max(maxY, y)
  }
  return { xM: minX, yM: minY, widthM: maxX - minX, heightM: maxY - minY }
}

// ── Pose sobre polilínea (posición + orientación del tramo) ─────────────────

export interface PathPose {
  readonly point: WorldPointM
  /** Ángulo del tramo que contiene el punto (radianes, atan2(dy, dx)). */
  readonly angleRad: number
}

/**
 * Punto y orientación a la fracción `fraction` ∈ [0, 1] de una polilínea, por
 * longitud acumulada (misma semántica que domain/kinematics.pointAlongPath,
 * añadiendo el ángulo del tramo). Camino vacío → `null`; de un punto o
 * degenerado → ese punto con ángulo 0.
 */
export function poseAlongPath(path: WorldPathM, fraction: number): PathPose | null {
  const first = path[0]
  if (!first) {
    return null
  }
  if (path.length === 1) {
    return { point: [first[0], first[1]], angleRad: 0 }
  }
  const clamped = Math.min(1, Math.max(0, Number.isFinite(fraction) ? fraction : 0))

  const lengths: number[] = []
  let total = 0
  for (let i = 0; i < path.length - 1; i += 1) {
    const a = path[i] as WorldPointM
    const b = path[i + 1] as WorldPointM
    const length = Math.hypot(b[0] - a[0], b[1] - a[1])
    lengths.push(length)
    total += length
  }
  if (total === 0) {
    return { point: [first[0], first[1]], angleRad: 0 }
  }

  const target = clamped * total
  let travelled = 0
  for (let i = 0; i < lengths.length; i += 1) {
    const length = lengths[i] as number
    if (travelled + length >= target && length > 0) {
      const a = path[i] as WorldPointM
      const b = path[i + 1] as WorldPointM
      const t = (target - travelled) / length
      return {
        point: [a[0] + (b[0] - a[0]) * t, a[1] + (b[1] - a[1]) * t],
        angleRad: Math.atan2(b[1] - a[1], b[0] - a[0]),
      }
    }
    travelled += length
  }

  // fraction = 1 (o error flotante acumulado): último vértice, ángulo del último tramo.
  const a = path[path.length - 2] as WorldPointM
  const b = path[path.length - 1] as WorldPointM
  return { point: [b[0], b[1]], angleRad: Math.atan2(b[1] - a[1], b[0] - a[0]) }
}

// ── Derivación de estáticos ──────────────────────────────────────────────────

/** Entrada de la derivación estática (snapshots + lookups del puerto). */
export interface StaticsInput {
  readonly regions: readonly Region[]
  readonly cities: readonly City[]
  readonly deposits: readonly ResourceDeposit[]
  readonly nodes: readonly NetworkNode[]
  readonly links: readonly NetworkLink[]
  readonly buildings: readonly Building[]
  readonly buildingTypeCode: (id: BuildingTypeId) => string | null
  readonly ownAccountId: AccountId | null
}

export interface StaticVms {
  readonly buildings: ReadonlyMap<string, BuildingVM>
  readonly cities: ReadonlyMap<string, CityVM>
  readonly deposits: ReadonlyMap<string, DepositVM>
  readonly nodes: ReadonlyMap<string, NodeVM>
  readonly links: ReadonlyMap<string, LinkVM>
  readonly regions: ReadonlyMap<string, RegionVM>
}

/** Trazado de un enlace: su `path`, o la recta fromNode → toNode si no llegó. */
export function linkPath(
  link: NetworkLink,
  nodeById: (id: NodeId) => NetworkNode | null,
): WorldPathM | null {
  if (link.pathM && link.pathM.length >= 2) {
    return link.pathM
  }
  const from = nodeById(link.fromNodeId)
  const to = nodeById(link.toNodeId)
  if (!from || !to) {
    return null
  }
  return [from.locationM, to.locationM]
}

/** Tier de congestión del enlace: el del PEOR segmento (legibilidad > detalle). */
export function linkCongestionTier(link: NetworkLink): ReturnType<typeof congestionTier> {
  let worst = 0
  for (const segment of link.segments) {
    worst = Math.max(worst, segment.congestionEma)
  }
  return congestionTier(worst)
}

export function deriveStatics(
  input: StaticsInput,
  viewM: WorldRectM,
  marginM = CULL_MARGIN_M,
): StaticVms {
  const view = expandRectM(viewM, marginM)
  const nodeMap = new Map<string, NetworkNode>()
  for (const node of input.nodes) {
    nodeMap.set(node.id, node)
  }
  const nodeById = (id: NodeId): NetworkNode | null => nodeMap.get(id) ?? null

  const buildings = new Map<string, BuildingVM>()
  for (const building of input.buildings) {
    const bounds = polygonBoundsM(building.footprintM)
    if (!bounds || !rectsIntersectM(view, bounds)) {
      continue
    }
    buildings.set(building.id, {
      id: building.id,
      xM: bounds.xM,
      yM: bounds.yM,
      wM: bounds.widthM,
      hM: bounds.heightM,
      status: building.status,
      typeCode: input.buildingTypeCode(building.buildingTypeId),
      own: input.ownAccountId !== null && building.ownerAccountId === input.ownAccountId,
    })
  }

  const cities = new Map<string, CityVM>()
  for (const city of input.cities) {
    // Culling por el círculo mayor (visual o influencia): el overlay de
    // influencia debe verse aunque el centro quede justo fuera del viewport.
    const reach = Math.max(cityRadiusM(city.level), city.influenceRadiusM)
    if (!rectContainsM(expandRectM(view, reach), city.locationM[0], city.locationM[1])) {
      continue
    }
    cities.set(city.id, {
      id: city.id,
      xM: city.locationM[0],
      yM: city.locationM[1],
      level: city.level,
      name: city.name,
      influenceRadiusM: city.influenceRadiusM,
    })
  }

  const deposits = new Map<string, DepositVM>()
  for (const deposit of input.deposits) {
    if (!rectContainsM(view, deposit.locationM[0], deposit.locationM[1])) {
      continue
    }
    deposits.set(deposit.id, {
      id: deposit.id,
      xM: deposit.locationM[0],
      yM: deposit.locationM[1],
    })
  }

  const nodes = new Map<string, NodeVM>()
  for (const node of input.nodes) {
    if (!rectContainsM(view, node.locationM[0], node.locationM[1])) {
      continue
    }
    nodes.set(node.id, { id: node.id, xM: node.locationM[0], yM: node.locationM[1] })
  }

  const links = new Map<string, LinkVM>()
  for (const link of input.links) {
    const points = linkPath(link, nodeById)
    if (!points) {
      continue
    }
    const bounds = pathBoundsM(points)
    if (!bounds || !rectsIntersectM(view, bounds)) {
      continue
    }
    links.set(link.id, { id: link.id, points, congestionTier: linkCongestionTier(link) })
  }

  const regions = new Map<string, RegionVM>()
  for (const region of input.regions) {
    if (region.boundsM === null) {
      continue
    }
    const bounds = polygonBoundsM(region.boundsM)
    if (!bounds || !rectsIntersectM(view, bounds)) {
      continue
    }
    regions.set(region.id, {
      id: region.id,
      name: region.name,
      xM: bounds.xM,
      yM: bounds.yM,
      wM: bounds.widthM,
      hM: bounds.heightM,
    })
  }

  return { buildings, cities, deposits, nodes, links, regions }
}

// ── Derivación de vehículos (analítica, por frame) ───────────────────────────

export interface VehiclesInput {
  readonly vehicles: readonly Vehicle[]
  readonly segmentContext: (segmentId: SegmentId) => SegmentContextInfo | null
  readonly nodeById: (id: NodeId) => NetworkNode | null
  readonly ownAccountId: AccountId | null
  /** Sim-now del frame; `null` (reloj sin anclar) ⇒ sin extrapolar (posición observada). */
  readonly simNow: SimTime | null
}

/**
 * Pose analítica de un vehículo (metros + orientación) en `simNow`, o `null`
 * si aún no es posicionable (grafo local incompleto y sin coordenadas
 * derivadas del servidor).
 */
export function vehiclePose(
  vehicle: Vehicle,
  segmentContext: (segmentId: SegmentId) => SegmentContextInfo | null,
  nodeById: (id: NodeId) => NetworkNode | null,
  simNow: SimTime | null,
): PathPose | null {
  const position = vehicle.position
  if (position.kind === 'at-node') {
    const location = position.locationM ?? nodeById(position.nodeId)?.locationM ?? null
    return location ? { point: location, angleRad: 0 } : null
  }

  const context = segmentContext(position.segmentId)
  if (!context) {
    // Grafo local incompleto: coordenadas derivadas del servidor si llegaron.
    return position.locationM ? { point: position.locationM, angleRad: 0 } : null
  }

  const { link, segment } = context
  const progressPct = extrapolateProgressPct(
    {
      progressPct0: position.progressPct,
      simTimeObserved: vehicle.observedAtSim,
      lengthM: segment.lengthM,
      baseSpeedKmh: link.baseSpeedKmh,
      congestionEma: segment.congestionEma,
    },
    simNow ?? vehicle.observedAtSim,
  )

  // Progreso DENTRO del segmento → fracción del path COMPLETO del enlace,
  // usando las longitudes de los segmentos ordenados por seq (dominio).
  let prefix = 0
  let total = 0
  for (const s of link.segments) {
    if (s.seq < segment.seq) {
      prefix += s.lengthM
    }
    total += s.lengthM
  }
  const within = progressPctToFraction(progressPct) * segment.lengthM
  const fraction = total > 0 ? (prefix + within) / total : progressPctToFraction(progressPct)

  const points = linkPath(link, nodeById)
  if (!points) {
    return position.locationM ? { point: position.locationM, angleRad: 0 } : null
  }
  return poseAlongPath(points, fraction)
}

export function deriveVehicles(
  input: VehiclesInput,
  viewM: WorldRectM,
  marginM = CULL_MARGIN_M,
): ReadonlyMap<string, VehicleVM> {
  const view = expandRectM(viewM, marginM)
  const out = new Map<string, VehicleVM>()
  for (const vehicle of input.vehicles) {
    const pose = vehiclePose(vehicle, input.segmentContext, input.nodeById, input.simNow)
    if (!pose || !rectContainsM(view, pose.point[0], pose.point[1])) {
      continue
    }
    out.set(vehicle.id, {
      id: vehicle.id,
      xM: pose.point[0],
      yM: pose.point[1],
      angleRad: pose.angleRad,
      status: vehicle.status,
      own: input.ownAccountId !== null && vehicle.ownerAccountId === input.ownAccountId,
    })
  }
  return out
}
