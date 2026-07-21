/**
 * domain/fleet — vehículos y cargamentos (bounded context Fleet, FAD §9.1).
 *
 * El vehículo lleva su ESTADO CINEMÁTICO observado: posición analítica
 * (`at-node` XOR `on-segment` con progreso) más `observedAtSim`, el sim-time
 * de la última observación del servidor — la base de la extrapolación pura de
 * domain/kinematics (el cliente interpola, el servidor decide los hitos).
 */

import type { EntityId } from '~shared/ids'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { WorldPointM } from './geo'
import type { LinkMode, NodeId, RouteId, SegmentId } from './logistics'
import type { Money } from '~shared/money'
import type { ContractId, FreightContractId } from './market'
import type { Quantity } from './quantity'
import type { ProductId } from './world'

export type VehicleId = EntityId<'Vehicle'>
export type VehicleTypeId = EntityId<'VehicleType'>
export type ShipmentId = EntityId<'Shipment'>

/**
 * `broken`: avería = espera + reparación (la carga espera a bordo).
 * `sealed`: SELLADO durante handoff entre shards — visible pero no comandable.
 */
export const VEHICLE_STATUSES = [
  'idle',
  'loading',
  'in_transit',
  'unloading',
  'broken',
  'in_maintenance',
  'sealed',
] as const
export type VehicleStatus = (typeof VEHICLE_STATUSES)[number]

export const SHIPMENT_STATUSES = [
  'in_warehouse',
  'in_transit',
  'at_terminal',
  'delivered',
  'released_in_situ',
] as const
export type ShipmentStatus = (typeof SHIPMENT_STATUSES)[number]

/** Tipo de vehículo del catálogo (camión/tren/barco): compra a precio fijo. */
export interface VehicleType {
  readonly id: VehicleTypeId
  readonly code: string
  readonly name: string
  readonly mode: LinkMode
  readonly cargoCapacity: Quantity
  readonly speedKmh: number
  readonly fuelProductId: ProductId
  readonly fuelPer100km: Quantity
  readonly autonomyKm: number
  readonly purchasePrice: Money
  readonly operatingCostPerDay: Money
}

/** Detenido en un nodo (XOR con `on-segment`, como en el contrato). */
export interface VehicleAtNode {
  readonly kind: 'at-node'
  readonly nodeId: NodeId
  /** Coordenadas derivadas por el servidor para render; `null` si no llegaron. */
  readonly locationM: WorldPointM | null
}

/** Circulando por un segmento: progreso observado, a extrapolar con SimClock. */
export interface VehicleOnSegment {
  readonly kind: 'on-segment'
  readonly segmentId: SegmentId
  /** Avance observado dentro del segmento en `observedAtSim`, 0–100. */
  readonly progressPct: number
  readonly locationM: WorldPointM | null
}

export type VehiclePosition = VehicleAtNode | VehicleOnSegment

export interface Vehicle {
  readonly id: VehicleId
  readonly vehicleTypeId: VehicleTypeId
  readonly ownerAccountId: AccountId
  readonly status: VehicleStatus
  readonly wearPct: number
  readonly fuel: Quantity
  readonly routeId: RouteId | null
  readonly routeLegIndex: number | null
  readonly position: VehiclePosition
  /** Fin de la reparación si está `broken`. */
  readonly repairUntilSim: SimTime | null
  /**
   * Sim-time de la ÚLTIMA observación autoritativa de esta entidad (respuesta
   * REST o evento WS) — base de la extrapolación de domain/kinematics.
   */
  readonly observedAtSim: SimTime
}

export interface Shipment {
  readonly id: ShipmentId
  readonly ownerAccountId: AccountId
  readonly productId: ProductId
  readonly quantity: Quantity
  /** CCRI cuyo stock reservado transporta (sigue reservado en tránsito). */
  readonly contractId: ContractId | null
  /** CCRI-Flete bajo cuya custodia viaja. */
  readonly freightContractId: FreightContractId | null
  /** Vehículo a bordo del cual viaja (XOR con `atNodeId`). */
  readonly vehicleId: VehicleId | null
  /** Nodo donde reposa (almacén o terminal). */
  readonly atNodeId: NodeId | null
  readonly status: ShipmentStatus
  readonly updatedAtSim: SimTime | null
}

export function isVehicleStatus(value: string): value is VehicleStatus {
  return (VEHICLE_STATUSES as readonly string[]).includes(value)
}

export function isShipmentStatus(value: string): value is ShipmentStatus {
  return (SHIPMENT_STATUSES as readonly string[]).includes(value)
}
