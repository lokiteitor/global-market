/**
 * game/input/hit-test — picking espacial PURO (FAD §11.10).
 *
 * Hit test de un punto del mundo (metros) contra los VMs VISIBLES del bridge
 * (solo lo visible es clicable: coste ∝ visible). Prioridad del mandato:
 * vehículo > edificio > ciudad > yacimiento > nodo; dentro de cada tipo gana
 * el más cercano al puntero. La tolerancia llega en METROS (la calcula el
 * controller desde px de pantalla / zoom) y los radios visuales se derivan de
 * las MISMAS constantes que las texturas (render ↔ picking consistentes).
 */

import { PX_PER_M } from '~shared/geometry/grid'

import type { BuildingVM, CityVM, DepositVM, NodeVM, VehicleVM } from '../bridge/vm'
import { cityRadiusM } from '../bridge/vm'
import { ENTITY_BASE_PX, NODE_PX, VEHICLE_W_PX } from '../textures'
import type { SelectionRef } from './types'

/** Radio visual (metros) de las entidades puntuales, derivado de sus texturas. */
export const VEHICLE_RADIUS_M = VEHICLE_W_PX / 2 / PX_PER_M
export const DEPOSIT_RADIUS_M = ENTITY_BASE_PX / 2 / PX_PER_M
export const NODE_RADIUS_M = NODE_PX / 2 / PX_PER_M

/** VMs visibles que participan en el picking (subset de VisibleVms del bridge). */
export interface HitTestVms {
  readonly vehicles: ReadonlyMap<string, VehicleVM>
  readonly buildings: ReadonlyMap<string, BuildingVM>
  readonly cities: ReadonlyMap<string, CityVM>
  readonly deposits: ReadonlyMap<string, DepositVM>
  readonly nodes: ReadonlyMap<string, NodeVM>
}

interface Candidate {
  readonly ref: SelectionRef
  readonly distance: number
}

function nearestPoint<VM extends { readonly id: string; readonly xM: number; readonly yM: number }>(
  vms: Iterable<VM>,
  type: SelectionRef['type'],
  xM: number,
  yM: number,
  radiusOf: (vm: VM) => number,
): Candidate | null {
  let best: Candidate | null = null
  for (const vm of vms) {
    const distance = Math.hypot(vm.xM - xM, vm.yM - yM)
    if (distance <= radiusOf(vm) && (best === null || distance < best.distance)) {
      best = { ref: { type, id: vm.id }, distance }
    }
  }
  return best
}

function nearestBuilding(
  vms: Iterable<BuildingVM>,
  xM: number,
  yM: number,
  toleranceM: number,
): Candidate | null {
  let best: Candidate | null = null
  for (const vm of vms) {
    const inside =
      xM >= vm.xM - toleranceM &&
      xM <= vm.xM + vm.wM + toleranceM &&
      yM >= vm.yM - toleranceM &&
      yM <= vm.yM + vm.hM + toleranceM
    if (!inside) {
      continue
    }
    const distance = Math.hypot(vm.xM + vm.wM / 2 - xM, vm.yM + vm.hM / 2 - yM)
    if (best === null || distance < best.distance) {
      best = { ref: { type: 'building', id: vm.id }, distance }
    }
  }
  return best
}

/**
 * Hit test con prioridad por tipo. `toleranceM` amplía todas las dianas (dedo
 * gordo / zoom lejano); el primero de la cadena de prioridad que acierte gana.
 */
export function hitTest(
  vms: HitTestVms,
  xM: number,
  yM: number,
  toleranceM: number,
): SelectionRef | null {
  const vehicle = nearestPoint(
    vms.vehicles.values(),
    'vehicle',
    xM,
    yM,
    () => VEHICLE_RADIUS_M + toleranceM,
  )
  if (vehicle) {
    return vehicle.ref
  }

  const building = nearestBuilding(vms.buildings.values(), xM, yM, toleranceM)
  if (building) {
    return building.ref
  }

  const city = nearestPoint(
    vms.cities.values(),
    'city',
    xM,
    yM,
    (vm) => cityRadiusM(vm.level) + toleranceM,
  )
  if (city) {
    return city.ref
  }

  const deposit = nearestPoint(
    vms.deposits.values(),
    'deposit',
    xM,
    yM,
    () => DEPOSIT_RADIUS_M + toleranceM,
  )
  if (deposit) {
    return deposit.ref
  }

  const node = nearestPoint(vms.nodes.values(), 'node', xM, yM, () => NODE_RADIUS_M + toleranceM)
  return node ? node.ref : null
}
