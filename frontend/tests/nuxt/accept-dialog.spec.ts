import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { t } from '~shared/i18n'
import type { AcceptanceDto, FreightContractDto } from '~network/market.api'
import type { RoutePlanDto } from '~network/logistics.api'
import AcceptDialog from '~/components/play/AcceptDialog.vue'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'
import { mon, node, product, publication, qty, uid } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_ID = uid<'Product'>(10)
const ORIGIN_NODE = uid<'Node'>(100)
const DEST_NODE = uid<'Node'>(101)
const FREIGHT_ID = uid<'FreightContract'>(185)

/** Publicación de flete: 700 unidades, declarado 60 000, plazo 48 h de juego. */
function freightPublication() {
  return publication({
    kind: 'freight',
    productId: PRODUCT_ID,
    quantityTotal: qty('700'),
    quantityRemaining: qty('700'),
    minLot: qty('1'),
    unitPrice: mon('10'),
    originNodeId: ORIGIN_NODE,
    destinationNodeId: DEST_NODE,
    declaredValue: mon('60000'),
    deliverySimSeconds: 48 * 3_600,
  })
}

const SERVED: AcceptanceDto = {
  id: uid(170),
  publication_id: uid(160),
  acceptor_account_id: uid(900),
  quantity: '300',
  quantity_served: '300',
  status: 'served',
  freight_contract_id: FREIGHT_ID,
  accepted_at: '2026-07-20T10:00:00Z',
}

const FREIGHT_DTO: FreightContractDto = {
  id: FREIGHT_ID,
  channel: 'board',
  shipper_account_id: uid(901),
  carrier_account_id: uid(900),
  origin_node_id: ORIGIN_NODE,
  destination_node_id: DEST_NODE,
  freight_price: '3000',
  declared_value: '25714',
  deadline_sim: 200_000,
  status: 'active',
  confirmed_at_sim: 2_000,
}

const PLAN: RoutePlanDto = {
  origin_node_id: ORIGIN_NODE,
  destination_node_id: DEST_NODE,
  legs: [
    { seq: 0, link_id: uid(105), mode: 'road', eta_sim_seconds: 3_600 },
    { seq: 1, link_id: uid(106), mode: 'sea', eta_sim_seconds: 200_000 },
  ],
  total_eta_sim_seconds: 203_600,
  estimated_cost: '500',
}

let stub: StubbedNuxtApp

async function mountDialog() {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([product({ id: PRODUCT_ID })])
  useLogisticsStore().applyNodesSnapshot([
    node({ id: ORIGIN_NODE, kind: 'warehouse' }),
    node({ id: DEST_NODE, kind: 'port' }),
  ])

  const wrapper = mount(AcceptDialog, {
    props: { publication: freightPublication() },
    global: { plugins: [pinia] },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/AcceptDialog — publicaciones freight', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
  })

  it('previsualiza la garantía con el floor EXACTO del servidor', async () => {
    const wrapper = await mountDialog()

    // K=300 de N=700: floor(60000×300/700)=25714; floor(25714×1000/10000)=2571.
    await wrapper.get('[data-testid="accept-quantity"]').setValue('300')
    await flushPromises()

    expect(wrapper.get('[data-testid="accept-guarantee-preview"]').text()).toContain('2.571')
    // Muestra el trayecto origen → destino de la publicación.
    expect(wrapper.get('[data-testid="accept-freight-route"]').text()).toContain('→')
  })

  it('estimar trayecto llama a planRoute con el origen/destino de la publicación y avisa si la ETA excede el plazo', async () => {
    vi.mocked(stub.apis.logistics.planRoute).mockResolvedValue(PLAN)
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="accept-quantity"]').setValue('300')
    await wrapper.get('[data-testid="accept-plan-estimate"]').trigger('click')
    await flushPromises()

    expect(stub.apis.logistics.planRoute).toHaveBeenCalledWith({
      origin_node_id: ORIGIN_NODE,
      destination_node_id: DEST_NODE,
      optimize: 'time',
      cargo_volume: '300',
    })
    // ETA 203 600 s > plazo 172 800 s ⇒ aviso.
    expect(wrapper.text()).toContain(t('market.accept.etaExceedsDeadline'))
    // No crea ninguna ruta: la estimación es solo informativa.
    expect(stub.apis.logistics.createRoute).not.toHaveBeenCalled()
  })

  it('al resultar servida con freight_contract_id hace pull del flete y lo aplica a la store', async () => {
    vi.mocked(stub.apis.market.acceptPublication).mockResolvedValue(SERVED)
    vi.mocked(stub.apis.market.getFreightContract).mockResolvedValue(FREIGHT_DTO)
    const wrapper = await mountDialog()

    await wrapper.get('[data-testid="accept-quantity"]').setValue('300')
    await wrapper.get('[data-testid="accept-submit"]').trigger('click')
    await flushPromises()

    expect(stub.apis.market.getFreightContract).toHaveBeenCalledWith(FREIGHT_ID)
    const market = useMarketStore()
    expect(market.getFreightContract(FREIGHT_ID)?.carrierAccountId).toBe(uid(900))
    expect(wrapper.text()).toContain(
      t('market.accept.freightServed', { qty: '300' }),
    )
  })
})
