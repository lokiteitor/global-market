/**
 * app/stores/fleet.store — bounded context Fleet (FAD §9.1, §20).
 *
 * Vehículos PROPIOS con su estado cinemático observado (status, at-node /
 * on-segment con progreso y `observedAtSim` — la base de la extrapolación de
 * domain/kinematics) y cargamentos propios. Estado replicado con la tríada
 * idempotente; la store NO extrapola (getter puro ≠ tiempo): el render llama
 * a domain/kinematics con el simNow del SimClock.
 */

import { computed } from 'vue'
import { defineStore } from 'pinia'
import type {
  Shipment,
  ShipmentId,
  Vehicle,
  VehicleId,
  VehicleStatus,
  VehicleType,
  VehicleTypeId,
} from '~domain/fleet'
import { VEHICLE_STATUSES } from '~domain/fleet'
import type { NodeId } from '~domain/logistics'
import type { ContractId } from '~domain/market'
import { createEntityCollection, indexBy } from './entity-collection'

export const useFleetStore = defineStore('fleet', () => {
  const vehicles = createEntityCollection<VehicleId, Vehicle>((v) => v.id)
  const shipments = createEntityCollection<ShipmentId, Shipment>((s) => s.id)
  /** Catálogo de tipos de vehículo (replicado como cualquier catálogo del mundo). */
  const vehicleTypes = createEntityCollection<VehicleTypeId, VehicleType>((vt) => vt.id)

  // Índices derivados.
  const vehicleIdsByStatus = indexBy(vehicles, (v) => v.status)
  const vehicleIdsByRoute = indexBy(vehicles, (v) => v.routeId)
  const shipmentIdsByContract = indexBy(shipments, (s) => s.contractId)
  const shipmentIdsByVehicle = indexBy(shipments, (s) => s.vehicleId)
  const shipmentIdsByNode = indexBy(shipments, (s) => s.atNodeId)

  /** Recuento por estado con TODOS los estados presentes (0 incluido). */
  const vehicleCountByStatus = computed(() => {
    const counts = Object.fromEntries(VEHICLE_STATUSES.map((s) => [s, 0])) as Record<
      VehicleStatus,
      number
    >
    for (const vehicle of vehicles.list.value) {
      counts[vehicle.status] += 1
    }
    return counts as Readonly<Record<VehicleStatus, number>>
  })

  /** Vehículos circulando por un segmento (los que el render debe extrapolar). */
  const vehiclesOnSegments = computed(() =>
    vehicles.list.value.filter((v) => v.position.kind === 'on-segment'),
  )

  /** Vehículos ociosos (candidatos a despacho de cargamentos). */
  const idleVehicles = computed(() => vehicles.list.value.filter((v) => v.status === 'idle'))

  function shipmentsForContract(contractId: ContractId): readonly Shipment[] {
    return (shipmentIdsByContract.value[contractId] ?? []).flatMap((id) => {
      const shipment = shipments.get(id)
      return shipment === null ? [] : [shipment]
    })
  }

  function shipmentsAboard(vehicleId: VehicleId): readonly Shipment[] {
    return (shipmentIdsByVehicle.value[vehicleId] ?? []).flatMap((id) => {
      const shipment = shipments.get(id)
      return shipment === null ? [] : [shipment]
    })
  }

  function shipmentsAtNode(nodeId: NodeId): readonly Shipment[] {
    return (shipmentIdsByNode.value[nodeId] ?? []).flatMap((id) => {
      const shipment = shipments.get(id)
      return shipment === null ? [] : [shipment]
    })
  }

  function clear(): void {
    vehicles.clear()
    shipments.clear()
    vehicleTypes.clear()
  }

  return {
    // Catálogo de tipos
    vehicleTypeById: vehicleTypes.byId,
    vehicleTypeList: vehicleTypes.list,
    getVehicleType: vehicleTypes.get,
    applyVehicleTypesSnapshot: vehicleTypes.applySnapshot,
    // Vehículos
    vehicleById: vehicles.byId,
    vehicleList: vehicles.list,
    getVehicle: vehicles.get,
    applyVehiclesSnapshot: vehicles.applySnapshot,
    applyVehicle: vehicles.applyOne,
    removeVehicle: vehicles.remove,
    vehicleIdsByStatus,
    vehicleIdsByRoute,
    vehicleCountByStatus,
    vehiclesOnSegments,
    idleVehicles,
    // Cargamentos
    shipmentById: shipments.byId,
    shipmentList: shipments.list,
    getShipment: shipments.get,
    applyShipmentsSnapshot: shipments.applySnapshot,
    applyShipment: shipments.applyOne,
    removeShipment: shipments.remove,
    shipmentIdsByContract,
    shipmentIdsByVehicle,
    shipmentIdsByNode,
    shipmentsForContract,
    shipmentsAboard,
    shipmentsAtNode,
    // Global
    clear,
  }
})
