import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'

import type { AccountId } from '~domain/auth'
import FleetPanel from '~/components/play/FleetPanel.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'
import {
  MY_ACCOUNT,
  OTHER_ACCOUNT,
  freightContract,
  product,
  shipment,
  uid,
  vehicle,
  vehicleType,
} from '~/stores/testing/fixtures'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_ID = uid<'Product'>(10)
const FREIGHT_ID = uid<'FreightContract'>(185)

async function mountPanel(myAccount: AccountId, carrierAccount: AccountId) {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([product({ id: PRODUCT_ID })])
  const session = useSessionStore()
  session.account = {
    id: myAccount,
    kind: 'human',
    name: 'Demo',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }
  const fleet = useFleetStore()
  fleet.applyVehicleTypesSnapshot([vehicleType()])
  fleet.applyVehiclesSnapshot([vehicle()])
  // Cargamento de flete parado en terminal: el dueño es el CARGADOR (shipper),
  // pero lo despacha el TRANSPORTISTA del freight contract.
  fleet.applyShipmentsSnapshot([
    shipment({
      productId: PRODUCT_ID,
      ownerAccountId: MY_ACCOUNT,
      freightContractId: FREIGHT_ID,
      status: 'at_terminal',
      contractId: null,
    }),
  ])
  useMarketStore().applyFreightsSnapshot([
    freightContract({
      id: FREIGHT_ID,
      shipperAccountId: MY_ACCOUNT,
      carrierAccountId: carrierAccount,
    }),
  ])

  const wrapper = mount(FleetPanel, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/FleetPanel — cargamentos de flete', () => {
  beforeEach(() => {
    stubNuxtApp(1_000)
  })

  it('el TRANSPORTISTA ve el botón despachar en un cargamento de flete at_terminal', async () => {
    // Yo soy OTHER_ACCOUNT (carrier); el dueño del cargamento es MY_ACCOUNT.
    const wrapper = await mountPanel(OTHER_ACCOUNT, OTHER_ACCOUNT)
    expect(wrapper.find('[data-testid="shipment-dispatch"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="shipment-freight-tag"]').exists()).toBe(true)
  })

  it('el CARGADOR ve la fila etiquetada pero SIN botón (despacha el transportista)', async () => {
    const wrapper = await mountPanel(MY_ACCOUNT, OTHER_ACCOUNT)
    expect(wrapper.find('[data-testid="shipment-row"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="shipment-dispatch"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('Despacha el transportista')
  })

  it('un vehículo idle propio ofrece reposicionar', async () => {
    const wrapper = await mountPanel(MY_ACCOUNT, OTHER_ACCOUNT)
    expect(wrapper.find('[data-testid="vehicle-reposition"]').exists()).toBe(true)
  })
})
