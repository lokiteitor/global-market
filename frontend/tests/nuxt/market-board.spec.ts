import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { t } from '~shared/i18n'
import type { PublicationDto } from '~network/market.api'
import MarketBoard from '~/components/play/MarketBoard.vue'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'
import { MY_ACCOUNT, OTHER_ACCOUNT, product, uid } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_ID = uid<'Product'>(10)
const NODE_ID = uid<'Node'>(100)

function publicationDto(over: Partial<PublicationDto> = {}): PublicationDto {
  return {
    id: uid(160),
    kind: 'sell',
    publisher_account_id: OTHER_ACCOUNT,
    channel: 'board',
    product_id: PRODUCT_ID,
    quantity_total: '500',
    quantity_remaining: '300',
    unit_price: '120',
    min_lot: '50',
    origin_node_id: NODE_ID,
    delivery_sim_seconds: 172_800,
    status: 'open',
    published_at_sim: 1_000,
    ...over,
  }
}

let stub: StubbedNuxtApp

async function mountBoard() {
  const pinia = createPinia()
  setActivePinia(pinia)

  const world = useWorldStore()
  world.applyProductsSnapshot([product({ id: PRODUCT_ID, name: 'Mineral de hierro' })])
  const session = useSessionStore()
  session.account = {
    id: MY_ACCOUNT,
    kind: 'human',
    name: 'Mi corporación',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }

  const wrapper = mount(MarketBoard, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/MarketBoard', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
    vi.mocked(stub.apis.market.queryBoard).mockResolvedValue({
      items: [publicationDto()],
      nextCursor: null,
    })
  })

  it('consulta el tablón al montar y pinta las filas', async () => {
    const wrapper = await mountBoard()

    expect(stub.apis.market.queryBoard).toHaveBeenCalledTimes(1)
    const rows = wrapper.findAll('[data-testid="board-row"]')
    expect(rows).toHaveLength(1)
    expect(rows[0]?.text()).toContain('Mineral de hierro')
    expect(rows[0]?.text()).toContain('120')
  })

  it('filtra: producto, kind y precio máximo van EXACTOS en la query', async () => {
    const wrapper = await mountBoard()

    await wrapper.get('[data-testid="filter-product"]').setValue(PRODUCT_ID)
    await wrapper.get('[data-testid="filter-kind"]').setValue('sell')
    await wrapper.get('[data-testid="filter-max-price"]').setValue('150')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(stub.apis.market.queryBoard).toHaveBeenLastCalledWith({
      limit: 100,
      kind: 'sell',
      product_id: PRODUCT_ID,
      max_unit_price: '150',
    })
  })

  it('emite accept con la publicación de dominio de la fila', async () => {
    const wrapper = await mountBoard()

    await wrapper.get('[data-testid="board-accept"]').trigger('click')

    const emitted = wrapper.emitted('accept')
    expect(emitted).toHaveLength(1)
    const publication = emitted?.[0]?.[0] as { id: string; unitPrice: string }
    expect(publication.id).toBe(uid(160))
    expect(publication.unitPrice).toBe('120')
  })

  it('la publicación PROPIA no es aceptable (tooltip de titularidad)', async () => {
    vi.mocked(stub.apis.market.queryBoard).mockResolvedValue({
      items: [publicationDto({ publisher_account_id: MY_ACCOUNT })],
      nextCursor: null,
    })
    const wrapper = await mountBoard()

    const accept = wrapper.get('[data-testid="board-accept"]')
    expect(accept.attributes('disabled')).toBeDefined()
    expect(accept.attributes('title')).toBe(t('market.board.ownPublication'))
  })
})
