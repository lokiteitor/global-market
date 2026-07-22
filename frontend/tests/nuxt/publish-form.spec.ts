import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { t } from '~shared/i18n'
import type { PublicationDto } from '~network/market.api'
import PublishForm from '~/components/play/PublishForm.vue'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useWorldStore } from '~/stores/world.store'
import { building, node, product, uid } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_ID = uid<'Product'>(10)
const BUILDING_ID = uid<'Building'>(70)
const NODE_ID = uid<'Node'>(100)

const CREATED: PublicationDto = {
  id: uid(160),
  kind: 'sell',
  publisher_account_id: uid(900),
  channel: 'board',
  product_id: PRODUCT_ID,
  quantity_total: '500',
  quantity_remaining: '500',
  unit_price: '120',
  min_lot: '50',
  origin_node_id: NODE_ID,
  delivery_sim_seconds: 172_800,
  status: 'draw_window',
  published_at_sim: 1_000,
}

let stub: StubbedNuxtApp

async function mountForm() {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([product({ id: PRODUCT_ID })])
  // "Mis nodos" = nodos ligados a MIS edificios (buildings.store replica solo propios).
  useBuildingsStore().applyBuildingsSnapshot([building({ id: BUILDING_ID })])
  useLogisticsStore().applyNodesSnapshot([node({ id: NODE_ID, buildingId: BUILDING_ID })])

  const wrapper = mount(PublishForm, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/PublishForm', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
    vi.mocked(stub.apis.market.createPublication).mockResolvedValue(CREATED)
  })

  it('valida la FORMA: campos requeridos sin llamar a la API', async () => {
    const wrapper = await mountForm()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t('validation.required'))
  })

  it('sell: envía el payload EXACTO del contrato (plazo en sim-seconds, origen propio)', async () => {
    const wrapper = await mountForm()

    await wrapper.get('[data-testid="publish-kind"]').setValue('sell')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('500')
    await wrapper.get('[data-testid="publish-price"]').setValue('120')
    await wrapper.get('[data-testid="publish-min-lot"]').setValue('50')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('[data-testid="publish-delivery"]').setValue('48')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).toHaveBeenCalledWith({
      kind: 'sell',
      channel: 'board',
      product_id: PRODUCT_ID,
      quantity_total: '500',
      unit_price: '120',
      min_lot: '50',
      origin_node_id: NODE_ID,
      delivery_sim_seconds: 48 * 3_600,
    })
    expect(wrapper.text()).toContain(t('market.publish.success'))
  })

  it('buy: el nodo propio viaja como destination_node_id', async () => {
    const wrapper = await mountForm()

    await wrapper.get('[data-testid="publish-kind"]').setValue('buy')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('200')
    await wrapper.get('[data-testid="publish-price"]').setValue('90')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('[data-testid="publish-delivery"]').setValue('24')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).toHaveBeenCalledWith(
      expect.objectContaining({
        kind: 'buy',
        destination_node_id: NODE_ID,
        delivery_sim_seconds: 24 * 3_600,
      }),
    )
    const call = vi.mocked(stub.apis.market.createPublication).mock.calls.at(-1)?.[0]
    expect(call).not.toHaveProperty('origin_node_id')
  })

  it('freight: previsualiza el escrow (cantidad × tarifa) y envía origen+destino+valor declarado', async () => {
    const OTHER_NODE = uid<'Node'>(101)
    const wrapper = await mountForm()
    // Un nodo AJENO más en el grafo: destino válido de flete.
    useLogisticsStore().applyNode(node({ id: OTHER_NODE, kind: 'port' }))
    await flushPromises()

    await wrapper.get('[data-testid="publish-kind"]').setValue('freight')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('500')
    await wrapper.get('[data-testid="publish-price"]').setValue('120')
    await flushPromises()

    // Preview de escrow con la MISMA aritmética del ledger: 500 × 120 = 60 000.
    expect(wrapper.get('[data-testid="publish-escrow-preview"]').text()).toContain('60.000')

    await wrapper.get('[data-testid="publish-declared-value"]').setValue('90000')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('[data-testid="publish-destination"]').setValue(OTHER_NODE)
    await wrapper.get('[data-testid="publish-delivery"]').setValue('72')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).toHaveBeenCalledWith({
      kind: 'freight',
      channel: 'board',
      product_id: PRODUCT_ID,
      quantity_total: '500',
      unit_price: '120',
      min_lot: '1',
      origin_node_id: NODE_ID,
      destination_node_id: OTHER_NODE,
      declared_value: '90000',
      delivery_sim_seconds: 72 * 3_600,
    })
  })

  it('freight: sin valor declarado o sin destino no llama a la API', async () => {
    const wrapper = await mountForm()

    await wrapper.get('[data-testid="publish-kind"]').setValue('freight')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('500')
    await wrapper.get('[data-testid="publish-price"]').setValue('120')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t('validation.required'))
  })

  it('canal privado: exige counterparty UUID válido y lo envía en el body', async () => {
    const COUNTERPARTY = '01981c5e-84b6-7c2a-8d3f-5b7a9c1e3f99'
    const wrapper = await mountForm()

    await wrapper.get('[data-testid="publish-kind"]').setValue('sell')
    await wrapper.get('[data-testid="publish-channel"]').setValue('private')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('500')
    await wrapper.get('[data-testid="publish-price"]').setValue('120')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('[data-testid="publish-delivery"]').setValue('48')

    // UUID inválido: error de forma, sin llamada.
    await wrapper.get('[data-testid="publish-counterparty"]').setValue('no-es-un-uuid')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(stub.apis.market.createPublication).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t('validation.uuid'))

    await wrapper.get('[data-testid="publish-counterparty"]').setValue(COUNTERPARTY)
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).toHaveBeenCalledWith(
      expect.objectContaining({ channel: 'private', counterparty_account_id: COUNTERPARTY }),
    )
  })

  it('cantidad con decimales = error de forma, sin llamada', async () => {
    const wrapper = await mountForm()

    await wrapper.get('[data-testid="publish-kind"]').setValue('sell')
    await wrapper.get('[data-testid="publish-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="publish-quantity"]').setValue('10.5')
    await wrapper.get('[data-testid="publish-price"]').setValue('120')
    await wrapper.get('[data-testid="publish-node"]').setValue(NODE_ID)
    await wrapper.get('[data-testid="publish-delivery"]').setValue('48')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.createPublication).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain(t('validation.quantity'))
  })
})
