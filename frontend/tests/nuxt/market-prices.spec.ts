import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { OhlcCandleDto } from '~network/market.api'
import MarketPrices from '~/components/play/MarketPrices.vue'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'
import { product, region, uid } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const PRODUCT_A = uid<'Product'>(10)
const PRODUCT_B = uid<'Product'>(11)
const REGION_ID = uid<'Region'>(1)

const CANDLES: readonly OhlcCandleDto[] = [
  {
    product_id: PRODUCT_A,
    region_id: REGION_ID,
    bucket_start_sim: 0,
    bucket_sim_secs: 86_400,
    open_price: '100',
    high_price: '130',
    low_price: '90',
    close_price: '120',
    volume: '1000',
    contract_count: 4,
  },
]

let stub: StubbedNuxtApp

async function mountPrices() {
  const pinia = createPinia()
  setActivePinia(pinia)

  useWorldStore().applyProductsSnapshot([
    product({ id: PRODUCT_A }),
    product({ id: PRODUCT_B, name: 'Carbón' }),
  ])
  useWorldStore().applyRegionsSnapshot([region({ id: REGION_ID })])

  const wrapper = mount(MarketPrices, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/MarketPrices', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
    vi.mocked(stub.apis.market.getMarketOhlc).mockResolvedValue(CANDLES)
  })

  it('pull al abrir con el primer producto y aplica el snapshot a la store', async () => {
    const wrapper = await mountPrices()

    expect(stub.apis.market.getMarketOhlc).toHaveBeenCalledWith(
      expect.objectContaining({ product_id: PRODUCT_A, bucket_sim_secs: 86_400, limit: 200 }),
    )
    const market = useMarketStore()
    expect(market.candlesFor(PRODUCT_A)).toHaveLength(1)
    expect(wrapper.get('[data-testid="prices-last-close"]').text()).toContain('120')
  })

  it('cambiar producto o bucket re-consulta (pull bajo demanda, sin timers)', async () => {
    const wrapper = await mountPrices()
    vi.mocked(stub.apis.market.getMarketOhlc).mockClear()

    await wrapper.get('[data-testid="prices-product"]').setValue(PRODUCT_B)
    await flushPromises()
    expect(stub.apis.market.getMarketOhlc).toHaveBeenCalledWith(
      expect.objectContaining({ product_id: PRODUCT_B }),
    )

    vi.mocked(stub.apis.market.getMarketOhlc).mockClear()
    await wrapper.get('[data-testid="prices-bucket"]').setValue('3600')
    await flushPromises()
    expect(stub.apis.market.getMarketOhlc).toHaveBeenCalledWith(
      expect.objectContaining({ bucket_sim_secs: 3_600 }),
    )
  })

  it('el filtro de región viaja como region_id', async () => {
    const wrapper = await mountPrices()
    vi.mocked(stub.apis.market.getMarketOhlc).mockClear()

    await wrapper.get('[data-testid="prices-region"]').setValue(REGION_ID)
    await flushPromises()

    expect(stub.apis.market.getMarketOhlc).toHaveBeenCalledWith(
      expect.objectContaining({ region_id: REGION_ID }),
    )
  })
})
