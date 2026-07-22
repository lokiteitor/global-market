import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { asEntityId } from '~shared/ids'
import { t } from '~shared/i18n'
import type { FreightContractDto } from '~network/market.api'
import MyFreights from '~/components/play/MyFreights.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'
import {
  MY_ACCOUNT,
  OTHER_ACCOUNT,
  freightContract,
  product,
  qty,
  shipment,
  st,
  uid,
} from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_ID = uid<'Product'>(10)
/** Flete donde soy CARGADOR (el fixture usa MY_ACCOUNT como shipper). */
const AS_SHIPPER = freightContract()
/** Flete donde soy TRANSPORTISTA. */
const AS_CARRIER = freightContract({
  id: uid(186),
  shipperAccountId: OTHER_ACCOUNT,
  carrierAccountId: MY_ACCOUNT,
  confirmedAtSim: st(3_000),
})

let stub: StubbedNuxtApp

async function mountPanel() {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([product({ id: PRODUCT_ID, name: 'Acero' })])
  const session = useSessionStore()
  session.account = {
    id: MY_ACCOUNT,
    kind: 'human',
    name: 'Demo',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }
  useMarketStore().applyFreightsSnapshot([AS_SHIPPER, AS_CARRIER])
  // Carga física del flete como transportista, ya replicada.
  useFleetStore().applyShipmentsSnapshot([
    shipment({
      id: uid(150),
      productId: PRODUCT_ID,
      quantity: qty('300'),
      freightContractId: AS_CARRIER.id,
      status: 'in_warehouse',
    }),
  ])

  const wrapper = mount(MyFreights, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/MyFreights', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
  })

  it('lista los fletes con el rol correcto y la carga del Shipment ligado', async () => {
    const wrapper = await mountPanel()
    const rows = wrapper.findAll('[data-testid="freight-row"]')
    expect(rows).toHaveLength(2)

    const text = wrapper.text()
    expect(text).toContain(t('market.freight.role.shipper'))
    expect(text).toContain(t('market.freight.role.carrier'))
    // La carga (producto + cantidad) sale del Shipment, no del FreightContract.
    expect(text).toContain('Acero · 300')
  })

  it('el filtro por rol reduce las filas', async () => {
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="freight-filter-role"]').setValue('carrier')
    expect(wrapper.findAll('[data-testid="freight-row"]')).toHaveLength(1)
    expect(wrapper.text()).toContain(t('market.freight.role.carrier'))
  })

  it('refrescar re-consulta AMBOS roles y aplica el snapshot deduplicado', async () => {
    const updated: FreightContractDto = {
      id: AS_SHIPPER.id,
      channel: 'board',
      shipper_account_id: MY_ACCOUNT,
      carrier_account_id: OTHER_ACCOUNT,
      origin_node_id: asEntityId<'Node'>(AS_SHIPPER.originNodeId),
      destination_node_id: asEntityId<'Node'>(AS_SHIPPER.destinationNodeId),
      freight_price: '5000',
      declared_value: '60000',
      deadline_sim: 200_000,
      status: 'settled',
      fill_bp: 10_000,
      confirmed_at_sim: 2_000,
      settled_at_sim: 250_000,
    }
    vi.mocked(stub.apis.market.listFreightContracts).mockResolvedValue({
      items: [updated],
      nextCursor: null,
    })
    const wrapper = await mountPanel()

    await wrapper.get('[data-testid="freight-refresh"]').trigger('click')
    await flushPromises()

    expect(stub.apis.market.listFreightContracts).toHaveBeenCalledWith(
      expect.objectContaining({ role: 'shipper' }),
    )
    expect(stub.apis.market.listFreightContracts).toHaveBeenCalledWith(
      expect.objectContaining({ role: 'carrier' }),
    )
    // Snapshot: reemplaza (el flete carrier desapareció de la respuesta).
    const market = useMarketStore()
    expect(market.freightList).toHaveLength(1)
    expect(market.getFreightContract(AS_SHIPPER.id)?.status).toBe('settled')
  })
})
