import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { RouteDto, RoutePlanDto } from '~network/logistics.api'
import type { ShipmentDto, VehicleDto } from '~network/fleet.api'
import DispatchDialog from '~/components/play/DispatchDialog.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'
import { MY_ACCOUNT, contract, product, shipment, uid, vehicle } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const ORIGIN_NODE = uid<'Node'>(100)
const DEST_NODE = uid<'Node'>(101)
const CONTRACT_ID = uid<'Contract'>(180)
const SHIPMENT_ID = uid<'Shipment'>(150)
const VEHICLE_ID = uid<'Vehicle'>(140)
const LINK_ID = uid<'Link'>(120)
const ROUTE_ID = uid<'Route'>(131)

const PLAN: RoutePlanDto = {
  origin_node_id: ORIGIN_NODE,
  destination_node_id: DEST_NODE,
  legs: [{ seq: 0, link_id: LINK_ID, mode: 'road', eta_sim_seconds: 7_200 }],
  total_eta_sim_seconds: 7_200,
}

const ROUTE_CREATED: RouteDto = {
  id: ROUTE_ID,
  owner_account_id: MY_ACCOUNT,
  name: 'Despacho bajo demanda',
  kind: 'on_demand',
  active: true,
  legs: [{ leg_index: 0, link_id: LINK_ID }],
}

const SHIPMENT_DISPATCHED: ShipmentDto = {
  id: SHIPMENT_ID,
  owner_account_id: MY_ACCOUNT,
  product_id: uid(10),
  quantity: '200',
  contract_id: CONTRACT_ID,
  vehicle_id: VEHICLE_ID,
  status: 'in_transit',
}

const VEHICLE_AFTER: VehicleDto = {
  id: VEHICLE_ID,
  vehicle_type_id: uid(141),
  owner_account_id: MY_ACCOUNT,
  status: 'loading',
  wear_pct: 10,
  fuel: '100',
  position: { at_node_id: ORIGIN_NODE },
}

let stub: StubbedNuxtApp

async function mountDialog() {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([product()])
  const fleet = useFleetStore()
  const inWarehouse = shipment({ id: SHIPMENT_ID, contractId: CONTRACT_ID, atNodeId: ORIGIN_NODE })
  fleet.applyShipmentsSnapshot([inWarehouse])
  fleet.applyVehiclesSnapshot([vehicle({ id: VEHICLE_ID })])
  useMarketStore().applyContractsSnapshot([
    contract({ id: CONTRACT_ID, originNodeId: ORIGIN_NODE, destinationNodeId: DEST_NODE }),
  ])

  const wrapper = mount(DispatchDialog, {
    props: { shipment: inWarehouse },
    global: { plugins: [pinia] },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/DispatchDialog', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
    vi.mocked(stub.apis.logistics.planRoute).mockResolvedValue(PLAN)
    vi.mocked(stub.apis.logistics.createRoute).mockResolvedValue(ROUTE_CREATED)
    vi.mocked(stub.apis.fleet.dispatchShipment).mockResolvedValue(SHIPMENT_DISPATCHED)
    vi.mocked(stub.apis.fleet.getVehicle).mockResolvedValue(VEHICLE_AFTER)
  })

  it('planifica: planRoute con origen del cargamento y destino del contrato', async () => {
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="dispatch-vehicle"]').setValue(VEHICLE_ID)
    await wrapper.get('[data-testid="dispatch-plan"]').trigger('click')
    await flushPromises()

    expect(stub.apis.logistics.planRoute).toHaveBeenCalledWith({
      origin_node_id: ORIGIN_NODE,
      destination_node_id: DEST_NODE,
      optimize: 'time',
      cargo_volume: '200',
    })
    // El plan se muestra (legs + ETA) antes de poder despachar.
    expect(wrapper.text()).toContain('Carretera')
  })

  it('despacha: createRoute con los legs del plan y dispatch con ids correctos', async () => {
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="dispatch-vehicle"]').setValue(VEHICLE_ID)
    await wrapper.get('[data-testid="dispatch-plan"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="dispatch-submit"]').trigger('click')
    await flushPromises()

    expect(stub.apis.logistics.createRoute).toHaveBeenCalledWith({
      name: 'Despacho bajo demanda',
      kind: 'on_demand',
      legs: [LINK_ID],
    })
    expect(stub.apis.fleet.dispatchShipment).toHaveBeenCalledWith(
      SHIPMENT_ID,
      VEHICLE_ID,
      ROUTE_ID,
    )
    // La respuesta del servidor se aplica a la store (thin client).
    const fleet = useFleetStore()
    expect(fleet.getShipment(SHIPMENT_ID)?.status).toBe('in_transit')
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('sin plan calculado, el despacho está deshabilitado', async () => {
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="dispatch-vehicle"]').setValue(VEHICLE_ID)

    expect(wrapper.get('[data-testid="dispatch-submit"]').attributes('disabled')).toBeDefined()
    expect(stub.apis.fleet.dispatchShipment).not.toHaveBeenCalled()
  })
})
