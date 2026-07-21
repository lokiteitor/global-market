import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { VEHICLE_STATUSES } from '~domain/fleet'
import { useFleetStore } from './fleet.store'
import { shipment, st, uid, vehicle } from './testing/fixtures'

const NODE_1 = uid<'Node'>(100)
const VEHICLE_1 = uid<'Vehicle'>(140)
const VEHICLE_2 = uid<'Vehicle'>(141)
const CONTRACT_1 = uid<'Contract'>(180)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/fleet.store — vehículos', () => {
  it('tríada idempotente y actualización del estado cinemático observado', () => {
    const store = useFleetStore()
    const atNode = vehicle({ id: VEHICLE_1, observedAtSim: st(1_000) })
    store.applyVehiclesSnapshot([atNode])
    store.applyVehiclesSnapshot([atNode])
    expect(store.vehicleList).toHaveLength(1)

    // Nueva observación: en tránsito sobre un segmento, con progreso y sim-time.
    const moving = vehicle({
      id: VEHICLE_1,
      status: 'in_transit',
      position: {
        kind: 'on-segment',
        segmentId: uid<'Segment'>(110),
        progressPct: 40,
        locationM: [4_000, 0],
      },
      observedAtSim: st(2_000),
    })
    store.applyVehicle(moving)
    store.applyVehicle(moving)

    expect(store.vehicleList).toHaveLength(1)
    const observed = store.getVehicle(VEHICLE_1)
    expect(observed?.status).toBe('in_transit')
    expect(observed?.observedAtSim).toBe(2_000)
    expect(observed?.position).toEqual(moving.position)
  })

  it('vehicleCountByStatus cubre TODOS los estados (0 incluido)', () => {
    const store = useFleetStore()
    store.applyVehiclesSnapshot([
      vehicle({ id: VEHICLE_1, status: 'idle' }),
      vehicle({ id: VEHICLE_2, status: 'broken' }),
    ])

    expect(Object.keys(store.vehicleCountByStatus)).toHaveLength(VEHICLE_STATUSES.length)
    expect(store.vehicleCountByStatus.idle).toBe(1)
    expect(store.vehicleCountByStatus.broken).toBe(1)
    expect(store.vehicleCountByStatus.in_transit).toBe(0)
    expect(store.vehicleCountByStatus.sealed).toBe(0)
  })

  it('vehiclesOnSegments e idleVehicles derivan de posición/estado', () => {
    const store = useFleetStore()
    const idle = vehicle({ id: VEHICLE_1, status: 'idle' })
    const moving = vehicle({
      id: VEHICLE_2,
      status: 'in_transit',
      position: {
        kind: 'on-segment',
        segmentId: uid<'Segment'>(110),
        progressPct: 10,
        locationM: null,
      },
    })
    store.applyVehiclesSnapshot([idle, moving])

    expect(store.vehiclesOnSegments).toEqual([moving])
    expect(store.idleVehicles).toEqual([idle])
    expect(store.vehicleIdsByStatus['in_transit']).toEqual([VEHICLE_2])
  })
})

describe('app/stores/fleet.store — cargamentos', () => {
  it('índices por contrato, vehículo y nodo', () => {
    const store = useFleetStore()
    const inWarehouse = shipment({ id: uid<'Shipment'>(150), atNodeId: NODE_1 })
    const aboard = shipment({
      id: uid<'Shipment'>(151),
      status: 'in_transit',
      vehicleId: VEHICLE_1,
      atNodeId: null,
      contractId: CONTRACT_1,
    })
    store.applyShipmentsSnapshot([inWarehouse, aboard])

    expect(store.shipmentsAtNode(NODE_1)).toEqual([inWarehouse])
    expect(store.shipmentsAboard(VEHICLE_1)).toEqual([aboard])
    expect(store.shipmentsForContract(CONTRACT_1)).toEqual([aboard])
    expect(store.shipmentsForContract(uid<'Contract'>(999))).toEqual([])
  })

  it('remove es no-op sobre inexistentes y clear purga todo', () => {
    const store = useFleetStore()
    store.applyVehicle(vehicle())
    store.applyShipment(shipment())
    store.removeShipment(uid<'Shipment'>(999))
    expect(store.shipmentList).toHaveLength(1)

    store.clear()
    expect(store.vehicleList).toHaveLength(0)
    expect(store.shipmentList).toHaveLength(0)
  })
})
