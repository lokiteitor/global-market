/**
 * domain/logistics — grafo logístico y rutas (bounded context Logistics, FAD §9.1).
 *
 * Nodos, enlaces (con su `path` en metros de mundo y congestión EMA por
 * segmento — el insumo de la extrapolación cinemática de domain/kinematics) y
 * rutas propias. Mismas convenciones que domain/world.
 */

import type { EntityId } from '~shared/ids'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { BuildingId } from './buildings'
import type { WorldPathM, WorldPointM } from './geo'
import type { CityId, RegionId } from './world'

export type NodeId = EntityId<'Node'>
export type LinkId = EntityId<'Link'>
export type SegmentId = EntityId<'Segment'>
export type RouteId = EntityId<'Route'>

export const NODE_KINDS = [
  'mine',
  'factory',
  'warehouse',
  'port',
  'station',
  'distribution_center',
  'junction',
  'city_gate',
] as const
export type NodeKind = (typeof NODE_KINDS)[number]

/** Modo de transporte (el aéreo es expansión futura). */
export const LINK_MODES = ['road', 'rail', 'sea'] as const
export type LinkMode = (typeof LINK_MODES)[number]

export const ROUTE_KINDS = ['fixed_line', 'on_demand'] as const
export type RouteKind = (typeof ROUTE_KINDS)[number]

export interface NetworkNode {
  readonly id: NodeId
  readonly kind: NodeKind
  readonly regionId: RegionId
  readonly buildingId: BuildingId | null
  readonly cityId: CityId | null
  readonly locationM: WorldPointM
}

export interface LinkSegment {
  readonly id: SegmentId
  /** Shard que simula la congestión de este segmento. */
  readonly regionId: RegionId
  readonly seq: number
  readonly lengthM: number
  /** Congestión EMA (1 = fluido; mayor = más lento). Peso del pathfinding y de la extrapolación. */
  readonly congestionEma: number
  readonly updatedAtSim: SimTime
}

export interface NetworkLink {
  readonly id: LinkId
  readonly mode: LinkMode
  readonly fromNodeId: NodeId
  readonly toNodeId: NodeId
  /** Trazado en metros de mundo; `null` si el contrato no lo envió (render: recta from→to). */
  readonly pathM: WorldPathM | null
  readonly lengthM: number
  readonly capacityPerHour: number
  readonly baseSpeedKmh: number
  /** Segmentos ordenados por `seq` (los mappers garantizan el orden). */
  readonly segments: readonly LinkSegment[]
}

export interface RouteLeg {
  readonly legIndex: number
  readonly linkId: LinkId
}

export interface Route {
  readonly id: RouteId
  readonly ownerAccountId: AccountId
  readonly name: string
  readonly kind: RouteKind
  readonly active: boolean
  /** Tramos ordenados por `legIndex` (los mappers garantizan el orden). */
  readonly legs: readonly RouteLeg[]
}

export function isNodeKind(value: string): value is NodeKind {
  return (NODE_KINDS as readonly string[]).includes(value)
}

export function isLinkMode(value: string): value is LinkMode {
  return (LINK_MODES as readonly string[]).includes(value)
}

export function isRouteKind(value: string): value is RouteKind {
  return (ROUTE_KINDS as readonly string[]).includes(value)
}
