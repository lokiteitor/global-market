import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useMarketStore } from './market.store'
import {
  MY_ACCOUNT,
  OTHER_ACCOUNT,
  acceptance,
  candle,
  contract,
  publication,
  st,
  uid,
} from './testing/fixtures'

const PRODUCT_IRON = uid<'Product'>(10)
const PUBLICATION_1 = uid<'Publication'>(160)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/market.store — tablón efímero', () => {
  it('applyBoardSnapshot guarda la lista con su sim-time; clearBoard la vacía', () => {
    const store = useMarketStore()
    expect(store.board.fetchedAtSim).toBeNull()

    const offers = [publication({ id: PUBLICATION_1, publisherAccountId: OTHER_ACCOUNT })]
    store.applyBoardSnapshot(offers, st(5_000))
    expect(store.board.publications).toEqual(offers)
    expect(store.board.fetchedAtSim).toBe(5_000)

    // La consulta siguiente REEMPLAZA (efímero, no fusión).
    store.applyBoardSnapshot([], st(6_000))
    expect(store.board.publications).toEqual([])
    expect(store.board.fetchedAtSim).toBe(6_000)

    store.clearBoard()
    expect(store.board.fetchedAtSim).toBeNull()
  })

  it('el tablón NO contamina las publicaciones propias', () => {
    const store = useMarketStore()
    store.applyBoardSnapshot([publication({ id: PUBLICATION_1 })], st(5_000))
    expect(store.publicationList).toHaveLength(0)
  })
})

describe('app/stores/market.store — publicaciones propias', () => {
  it('tríada idempotente, índices y livePublications', () => {
    const store = useMarketStore()
    const open = publication({ id: uid<'Publication'>(160), status: 'open' })
    const draw = publication({ id: uid<'Publication'>(161), status: 'draw_window', kind: 'buy' })
    const exhausted = publication({ id: uid<'Publication'>(162), status: 'exhausted' })
    store.applyPublicationsSnapshot([open, draw, exhausted])
    store.applyPublicationsSnapshot([open, draw, exhausted])

    expect(store.publicationList).toHaveLength(3)
    expect(store.publicationIdsByStatus['open']).toEqual([open.id])
    expect(store.publicationIdsByKind['buy']).toEqual([draw.id])
    expect(store.livePublications).toEqual([open, draw])

    store.applyPublication(publication({ id: open.id, status: 'cancelled' }))
    expect(store.livePublications).toEqual([draw])
  })
})

describe('app/stores/market.store — aceptaciones y contratos propios', () => {
  it('acceptancesForPublication y pendingAcceptances', () => {
    const store = useMarketStore()
    const pending = acceptance({ id: uid<'Acceptance'>(170), publicationId: PUBLICATION_1 })
    const served = acceptance({
      id: uid<'Acceptance'>(171),
      publicationId: PUBLICATION_1,
      status: 'served',
    })
    const other = acceptance({
      id: uid<'Acceptance'>(172),
      publicationId: uid<'Publication'>(999),
    })
    store.applyAcceptancesSnapshot([pending, served, other])

    expect(store.acceptancesForPublication(PUBLICATION_1)).toEqual([pending, served])
    expect(store.pendingAcceptances).toEqual([pending, other])
  })

  it('activeContracts y roles comprador/vendedor', () => {
    const store = useMarketStore()
    const selling = contract({
      id: uid<'Contract'>(180),
      sellerAccountId: MY_ACCOUNT,
      buyerAccountId: OTHER_ACCOUNT,
    })
    const buying = contract({
      id: uid<'Contract'>(181),
      sellerAccountId: OTHER_ACCOUNT,
      buyerAccountId: MY_ACCOUNT,
    })
    const settled = contract({
      id: uid<'Contract'>(182),
      status: 'settled',
      sellerAccountId: MY_ACCOUNT,
      buyerAccountId: OTHER_ACCOUNT,
    })
    store.applyContractsSnapshot([selling, buying, settled])

    expect(store.activeContracts).toEqual([selling, buying])
    expect(store.contractsAsSeller(MY_ACCOUNT)).toEqual([selling, settled])
    expect(store.contractsAsBuyer(MY_ACCOUNT)).toEqual([buying])
    expect(store.contractIdsByStatus['settled']).toEqual([settled.id])
  })
})

describe('app/stores/market.store — velas OHLC', () => {
  it('applyOhlcSnapshot deduplica por bucket, ordena ascendente y es idempotente', () => {
    const store = useMarketStore()
    const early = candle({ bucketStartSim: st(0) })
    const late = candle({ bucketStartSim: st(86_400) })
    const lateRevised = candle({ bucketStartSim: st(86_400), closePrice: early.closePrice })

    // Desordenadas y con bucket duplicado: gana la última versión del bucket.
    store.applyOhlcSnapshot(PRODUCT_IRON, [late, early, lateRevised])
    store.applyOhlcSnapshot(PRODUCT_IRON, [late, early, lateRevised])

    expect(store.candlesFor(PRODUCT_IRON)).toEqual([early, lateRevised])
    expect(store.lastCloseOf(PRODUCT_IRON)).toBe(lateRevised.closePrice)
  })

  it('candlesFor/lastCloseOf sin serie devuelven vacío/null', () => {
    const store = useMarketStore()
    expect(store.candlesFor(PRODUCT_IRON)).toEqual([])
    expect(store.lastCloseOf(PRODUCT_IRON)).toBeNull()
  })

  it('clear purga tablón, colecciones propias y velas', () => {
    const store = useMarketStore()
    store.applyBoardSnapshot([publication()], st(1_000))
    store.applyPublication(publication())
    store.applyAcceptance(acceptance())
    store.applyContract(contract())
    store.applyOhlcSnapshot(PRODUCT_IRON, [candle()])
    store.clear()

    expect(store.board.fetchedAtSim).toBeNull()
    expect(store.publicationList).toHaveLength(0)
    expect(store.acceptanceList).toHaveLength(0)
    expect(store.contractList).toHaveLength(0)
    expect(store.candlesFor(PRODUCT_IRON)).toEqual([])
  })
})
