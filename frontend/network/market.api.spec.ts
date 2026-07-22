import { describe, expect, it } from 'vitest'

import { createMarketApi } from '~network/market.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const PRODUCT_ID = '01981c5e-84b6-7c2a-8d3f-5b7a9c1e3f04'
const PUBLICATION_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f010'
const NODE_ID = '01981c5e-91d8-7e4b-a2c6-4d8e0f6a2b93'

/** Doble del PUERTO RestClient: sirve data programada y registra specs. */
function fakeRest(data: unknown, nextCursor: string | null = null) {
  const requested: RequestSpec[] = []
  const rest: RestClient = {
    request<TData>(spec: RequestSpec): Promise<Enveloped<TData>> {
      requested.push(spec)
      return Promise.resolve({ data: data as TData, meta: { ...META, nextCursor } })
    },
    requestVoid(spec: RequestSpec): Promise<void> {
      requested.push(spec)
      return Promise.resolve()
    },
  }
  return { rest, requested }
}

describe('network/market.api — contratos de endpoint', () => {
  it('queryBoard hace GET /contracts/board con filtros + cursor y devuelve Page', async () => {
    const { rest, requested } = fakeRest([{ id: PUBLICATION_ID }], 'cursor-2')
    const page = await createMarketApi(rest).queryBoard({
      kind: 'sell',
      product_id: PRODUCT_ID,
      max_unit_price: '150',
      sort: 'unit_price_asc',
      cursor: 'cursor-1',
      limit: 25,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/contracts/board',
      query: {
        kind: 'sell',
        product_id: PRODUCT_ID,
        max_unit_price: '150',
        sort: 'unit_price_asc',
        cursor: 'cursor-1',
        limit: 25,
      },
    })
    expect(page.items).toEqual([{ id: PUBLICATION_ID }])
    expect(page.nextCursor).toBe('cursor-2')
  })

  it('acceptPublication hace POST a /acceptances con quantity y origin_node_id opcional', async () => {
    const { rest, requested } = fakeRest({ id: PUBLICATION_ID })
    const api = createMarketApi(rest)

    await api.acceptPublication(PUBLICATION_ID, '100', NODE_ID)
    await api.acceptPublication(PUBLICATION_ID, '50')

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: `/contracts/publications/${PUBLICATION_ID}/acceptances`,
      body: { quantity: '100', origin_node_id: NODE_ID },
    })
    // Sin originNodeId, la clave se OMITE (no viaja como undefined/null).
    expect(requested[1]?.body).toEqual({ quantity: '50' })
  })

  it('getMarketOhlc hace GET /market/ohlc con product_id requerido y desenvuelve el array', async () => {
    const { rest, requested } = fakeRest([{ product_id: PRODUCT_ID }])
    const candles = await createMarketApi(rest).getMarketOhlc({
      product_id: PRODUCT_ID,
      bucket_sim_secs: 3600,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/market/ohlc',
      query: { product_id: PRODUCT_ID, bucket_sim_secs: 3600 },
    })
    expect(candles).toEqual([{ product_id: PRODUCT_ID }])
  })

  it('listFreightContracts hace GET /contracts/freight-contracts con role/status y devuelve Page', async () => {
    const FREIGHT_ID = '01981c5e-a3f1-7d5c-b4e8-6f0a2c4e6081'
    const { rest, requested } = fakeRest([{ id: FREIGHT_ID }], 'cursor-f2')
    const page = await createMarketApi(rest).listFreightContracts({
      role: 'carrier',
      status: 'active',
      cursor: 'cursor-f1',
      limit: 50,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/contracts/freight-contracts',
      query: { role: 'carrier', status: 'active', cursor: 'cursor-f1', limit: 50 },
    })
    expect(page.items).toEqual([{ id: FREIGHT_ID }])
    expect(page.nextCursor).toBe('cursor-f2')
  })

  it('getFreightContract hace GET por id', async () => {
    const FREIGHT_ID = '01981c5e-a3f1-7d5c-b4e8-6f0a2c4e6081'
    const { rest, requested } = fakeRest({ id: FREIGHT_ID })
    const dto = await createMarketApi(rest).getFreightContract(FREIGHT_ID)

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: `/contracts/freight-contracts/${FREIGHT_ID}`,
    })
    expect(dto).toEqual({ id: FREIGHT_ID })
  })

  it('propaga el AppError tipado del cliente REST (p. ej. INSUFFICIENT_COLLATERAL)', async () => {
    const error = new AppError({
      kind: 'http',
      code: 'INSUFFICIENT_COLLATERAL',
      message: 'garantía insuficiente',
      status: 422,
    })
    const rest: RestClient = {
      request: () => Promise.reject(error),
      requestVoid: () => Promise.reject(error),
    }
    const failure = createMarketApi(rest).createPublication({
      kind: 'sell',
      channel: 'board',
      product_id: PRODUCT_ID,
      quantity_total: '500',
      unit_price: '120',
      min_lot: '50',
      origin_node_id: NODE_ID,
      delivery_sim_seconds: 172800,
    })
    await expect(failure).rejects.toBe(error)
    await expect(failure).rejects.toBeInstanceOf(AppError)
  })
})
