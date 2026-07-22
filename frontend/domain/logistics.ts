/**
 * domain/logistics — grafo logístico y rutas (bounded context Logistics, FAD §9.1).
 *
 * Nodos, enlaces (con su `path` en metros de mundo y congestión EMA por
 * segmento — el insumo de la extrapolación cinemática de domain/kinematics) y
 * rutas propias. Mismas convenciones que domain/world.
 */

import type { EntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { BuildingId } from './buildings'
import type { WorldPathM, WorldPointM } from './geo'
import type { CityId, RegionId } from './world'

export type NodeId = EntityId<'Node'>
export type LinkId = EntityId<'Link'>
export type SegmentId = EntityId<'Segment'>
export type RouteId = EntityId<'Route'>
export type TerminalId = EntityId<'Terminal'>
export type TerminalSlotId = EntityId<'TerminalSlot'>

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
  /** Terminal intermodal que opera en el nodo, si la tiene (v1.7.0). */
  readonly terminalId: TerminalId | null
}

/**
 * Terminal intermodal (GDD §7.3): infraestructura con dueño que sirve la cola
 * de transbordo a `transshipmentPerHour` y vende slots de prioridad. Es
 * información ambiental compartida: se consulta por pull al inspeccionar
 * (C10), no se replica en store.
 */
export interface Terminal {
  readonly id: TerminalId
  readonly nodeId: NodeId
  readonly ownerAccountId: AccountId
  readonly transshipmentPerHour: number
  readonly queueLength: number
  readonly updatedAtSim: SimTime | null
}

/** Slot de prioridad de una terminal (menor `priorityTier` = antes en cola). */
export interface TerminalSlot {
  readonly id: TerminalSlotId
  readonly terminalId: TerminalId
  readonly priorityTier: number
  readonly price: Money
  /** Titular actual; `null` = a la venta. */
  readonly holderAccountId: AccountId | null
  readonly validUntilSim: SimTime | null
}

/**
 * ¿El slot tiene titular vigente en `simNow`? Espejo de la regla de compra del
 * servidor: con titular y sin vencimiento (o vencimiento futuro) el slot no es
 * comprable (409 SLOT_HELD).
 */
export function isSlotHeld(slot: TerminalSlot, simNow: SimTime): boolean {
  return slot.holderAccountId !== null && (slot.validUntilSim === null || slot.validUntilSim >= simNow)
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
