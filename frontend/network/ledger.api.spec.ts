import { describe, expect, it } from 'vitest'

import { createLedgerApi } from '~network/ledger.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const LEDGER_ACCOUNT_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f040'
const PRODUCT_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f041'

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

describe('network/ledger.api — contratos de endpoint', () => {
  it('listLedgerAccounts hace GET /ledger/accounts con filtros y devuelve Page', async () => {
    const { rest, requested } = fakeRest([{ id: LEDGER_ACCOUNT_ID, balance: '1000000' }], 'c-2')
    const page = await createLedgerApi(rest).listLedgerAccounts({
      kind: 'stock_free',
      product_id: PRODUCT_ID,
      limit: 10,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/ledger/accounts',
      query: { kind: 'stock_free', product_id: PRODUCT_ID, limit: 10 },
    })
    // El balance sigue siendo string de punto fijo: esta capa jamás lo convierte (C11).
    expect(page.items).toEqual([{ id: LEDGER_ACCOUNT_ID, balance: '1000000' }])
    expect(page.nextCursor).toBe('c-2')
  })

  it('listLedgerEntries hace GET al extracto de la cuenta con rango sim-time + cursor', async () => {
    const { rest, requested } = fakeRest([])
    await createLedgerApi(rest).listLedgerEntries(LEDGER_ACCOUNT_ID, {
      from_sim: 31104000,
      to_sim: 31190400,
      cursor: 'c-1',
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: `/ledger/accounts/${LEDGER_ACCOUNT_ID}/entries`,
      query: { from_sim: 31104000, to_sim: 31190400, cursor: 'c-1' },
    })
  })

  it('propaga el AppError tipado del cliente REST (extracto ajeno → 403)', async () => {
    const error = new AppError({
      kind: 'http',
      code: 'NOT_RESOURCE_OWNER',
      message: 'la cuenta pertenece a otra corporación',
      status: 403,
    })
    const rest: RestClient = {
      request: () => Promise.reject(error),
      requestVoid: () => Promise.reject(error),
    }
    await expect(createLedgerApi(rest).listLedgerEntries(LEDGER_ACCOUNT_ID)).rejects.toBe(error)
  })
})
