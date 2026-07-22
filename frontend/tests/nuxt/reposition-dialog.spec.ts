import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { t } from '~shared/i18n'
import type { VehicleDto } from '~network/fleet.api'
import type { RouteDto, RoutePlanDto } from '~network/logistics.api'
import RepositionDialog from '~/components/play/RepositionDialog.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { node, uid, vehicle, vehicleType } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const VEHICLE_ID = uid<'Vehicle'>(140)
const TYPE_ID = uid<'VehicleType'>(141)
const ORIGIN = uid<'Node'>(100)
const DEST = uid<'Node'>(101)
const ROUTE_ID = uid<'Route'>(130)

const PLAN: RoutePlanDto = {
  origin_node_id: ORIGIN,
  destination_node_id: DEST,
  legs: [{ seq: 0, link_id: uid(120), mode: 'road', eta_sim_seconds: 7_200 }],
  total_eta_sim_seconds: 7_200,
  estimated_cost: '100',
}

const ROUTE: RouteDto = {
  id: ROUTE_ID,
  owner_account_id: uid(900),
  name: 'Reposicionamiento',
  kind: 'on_demand',
  active: true,
  legs: [{ leg_index: 0, link_id: uid(120) }],
}

const REPOSITIONED: VehicleDto = {
  id: VEHICLE_ID,
  vehicle_type_id: TYPE_ID,
  owner_account_id: uid(900),
  status: 'in_transit',
  wear_pct: 10,
  fuel: '90',
  route_id: ROUTE_ID,
  route_leg_index: 0,
  position: { at_node_id: ORIGIN, location: { type: 'Point', coordinates: [1_000, 1_000] } },
}

let stub: StubbedNuxtApp

async function mountDialog(vehicleOverrides: Parameters<typeof vehicle>[0] = {}) {
  const pinia = createPinia()
  setActivePinia(pinia)

  const fleet = useFleetStore()
  fleet.applyVehicleTypesSnapshot([vehicleType({ id: TYPE_ID, mode: 'road' })])
  fleet.applyVehiclesSnapshot([vehicle({ id: VEHICLE_ID, vehicleTypeId: TYPE_ID, ...vehicleOverrides })])
  useLogisticsStore().applyNodesSnapshot([
    node({ id: ORIGIN, kind: 'warehouse' }),
    node({ id: DEST, kind: 'port' }),
  ])

  const wrapper = mount(RepositionDialog, {
    props: { vehicleId: VEHICLE_ID },
    global: { plugins: [pinia] },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/RepositionDialog', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
  })

  it('guard: un vehículo no ocioso muestra el aviso y no ofrece el flujo', async () => {
    const wrapper = await mountDialog({ status: 'in_transit' })
    expect(wrapper.find('[data-testid="reposition-not-idle"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="reposition-destination"]').exists()).toBe(false)
  })

  it('planifica RESTRINGIDO al modo del vehículo desde su nodo actual', async () => {
    vi.mocked(stub.apis.logistics.planRoute).mockResolvedValue(PLAN)
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="reposition-destination"]').setValue(DEST)
    await wrapper.get('[data-testid="reposition-plan"]').trigger('click')
    await flushPromises()

    expect(stub.apis.logistics.planRoute).toHaveBeenCalledWith({
      origin_node_id: ORIGIN,
      destination_node_id: DEST,
      optimize: 'time',
      modes: ['road'],
    })
  })

  it('happy path: crea la ruta on_demand, hace POST reposition y aplica el vehículo', async () => {
    vi.mocked(stub.apis.logistics.planRoute).mockResolvedValue(PLAN)
    vi.mocked(stub.apis.logistics.createRoute).mockResolvedValue(ROUTE)
    vi.mocked(stub.apis.fleet.repositionVehicle).mockResolvedValue(REPOSITIONED)
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="reposition-destination"]').setValue(DEST)
    await wrapper.get('[data-testid="reposition-plan"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="reposition-submit"]').trigger('click')
    await flushPromises()

    expect(stub.apis.logistics.createRoute).toHaveBeenCalledWith({
      name: t('fleet.reposition.routeName'),
      kind: 'on_demand',
      legs: [uid(120)],
    })
    expect(stub.apis.fleet.repositionVehicle).toHaveBeenCalledWith(VEHICLE_ID, ROUTE_ID)
    expect(useFleetStore().getVehicle(VEHICLE_ID)?.status).toBe('in_transit')
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})
