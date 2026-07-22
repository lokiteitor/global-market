import { describe, expect, it } from 'vitest'

import type { BuildingVM, CityVM, DepositVM, NodeVM, VehicleVM } from '../bridge/vm'
import type { HitTestVms } from './hit-test'
import { DEPOSIT_RADIUS_M, NODE_RADIUS_M, VEHICLE_RADIUS_M, hitTest } from './hit-test'

const vehicleVM = (over: Partial<VehicleVM> = {}): VehicleVM => ({
  id: 'v1',
  xM: 1_000,
  yM: 1_000,
  angleRad: 0,
  status: 'in_transit',
  own: true,
  ...over,
})

const buildingVM = (over: Partial<BuildingVM> = {}): BuildingVM => ({
  id: 'b1',
  xM: 900,
  yM: 900,
  wM: 250,
  hM: 250,
  status: 'operational',
  typeCode: null,
  own: true,
  ...over,
})

const cityVM = (over: Partial<CityVM> = {}): CityVM => ({
  id: 'c1',
  xM: 1_000,
  yM: 1_000,
  level: 2, // radio visual 250 m
  name: 'Puerto',
  influenceRadiusM: 5_000,
  ...over,
})

const depositVM = (over: Partial<DepositVM> = {}): DepositVM => ({
  id: 'd1',
  xM: 1_000,
  yM: 1_000,
  ...over,
})

const nodeVM = (over: Partial<NodeVM> = {}): NodeVM => ({
  id: 'n1',
  xM: 1_000,
  yM: 1_000,
  kind: 'warehouse',
  intermodal: false,
  ...over,
})

const vms = (over: Partial<HitTestVms> = {}): HitTestVms => ({
  vehicles: new Map(),
  buildings: new Map(),
  cities: new Map(),
  deposits: new Map(),
  nodes: new Map(),
  ...over,
})

const mapOf = <VM extends { id: string }>(...items: VM[]): ReadonlyMap<string, VM> =>
  new Map(items.map((vm) => [vm.id, vm]))

describe('game/input/hit-test — prioridad vehículo > edificio > ciudad > yacimiento > nodo', () => {
  it('con todo apilado en el mismo punto gana el vehículo', () => {
    const all = vms({
      vehicles: mapOf(vehicleVM()),
      buildings: mapOf(buildingVM()),
      cities: mapOf(cityVM()),
      deposits: mapOf(depositVM()),
      nodes: mapOf(nodeVM()),
    })
    expect(hitTest(all, 1_000, 1_000, 10)).toEqual({ type: 'vehicle', id: 'v1' })
  })

  it('sin vehículo gana el edificio; sin edificio, la ciudad; y así en cadena', () => {
    const noVehicle = vms({
      buildings: mapOf(buildingVM()),
      cities: mapOf(cityVM()),
      deposits: mapOf(depositVM()),
      nodes: mapOf(nodeVM()),
    })
    expect(hitTest(noVehicle, 1_000, 1_000, 10)?.type).toBe('building')

    const noBuilding = vms({ cities: mapOf(cityVM()), deposits: mapOf(depositVM()) })
    expect(hitTest(noBuilding, 1_000, 1_000, 10)?.type).toBe('city')

    const depositsOnly = vms({ deposits: mapOf(depositVM()), nodes: mapOf(nodeVM()) })
    expect(hitTest(depositsOnly, 1_000, 1_000, 10)?.type).toBe('deposit')

    const nodesOnly = vms({ nodes: mapOf(nodeVM()) })
    expect(hitTest(nodesOnly, 1_000, 1_000, 10)).toEqual({ type: 'node', id: 'n1' })
  })

  it('vacío o lejos de todo ⇒ null', () => {
    expect(hitTest(vms(), 1_000, 1_000, 10)).toBeNull()
    expect(hitTest(vms({ nodes: mapOf(nodeVM()) }), 5_000, 5_000, 10)).toBeNull()
  })
})

describe('game/input/hit-test — tolerancia y radios visuales', () => {
  it('el vehículo acierta dentro de su radio visual + tolerancia y falla fuera', () => {
    const only = vms({ vehicles: mapOf(vehicleVM()) })
    const justInside = 1_000 + VEHICLE_RADIUS_M + 9
    const justOutside = 1_000 + VEHICLE_RADIUS_M + 11
    expect(hitTest(only, justInside, 1_000, 10)?.id).toBe('v1')
    expect(hitTest(only, justOutside, 1_000, 10)).toBeNull()
  })

  it('el bbox del edificio se expande con la tolerancia', () => {
    const only = vms({ buildings: mapOf(buildingVM()) })
    expect(hitTest(only, 900 - 5, 900, 10)?.id).toBe('b1') // 5 m fuera, tol 10
    expect(hitTest(only, 900 - 15, 900, 10)).toBeNull()
  })

  it('nodo y yacimiento usan sus radios derivados de textura', () => {
    const only = vms({ deposits: mapOf(depositVM()), nodes: mapOf(nodeVM()) })
    // A un punto entre ambos radios: el yacimiento (125 m) acierta, el nodo (31 m) no.
    const between = 1_000 + (NODE_RADIUS_M + DEPOSIT_RADIUS_M) / 2
    expect(hitTest(only, between, 1_000, 0)?.type).toBe('deposit')
  })

  it('dentro de un tipo gana el más cercano al puntero', () => {
    const near = nodeVM({ id: 'near', xM: 1_010 })
    const far = nodeVM({ id: 'far', xM: 1_030 })
    const only = vms({ nodes: mapOf(far, near) })
    expect(hitTest(only, 1_012, 1_000, 50)?.id).toBe('near')
  })
})
