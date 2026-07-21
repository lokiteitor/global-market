import { describe, expect, it } from 'vitest'

import { createWorldApi } from '~network/world.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const REGION_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f001'
const BUILDING_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f002'
const RECIPE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f003'
const BATCH_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f004'

/** Doble del PUERTO RestClient (no del módulo): sirve data programada y registra specs. */
function fakeRest(data: unknown, nextCursor: string | null = null) {
  const requested: RequestSpec[] = []
  const voided: RequestSpec[] = []
  const rest: RestClient = {
    request<TData>(spec: RequestSpec): Promise<Enveloped<TData>> {
      requested.push(spec)
      return Promise.resolve({ data: data as TData, meta: { ...META, nextCursor } })
    },
    requestVoid(spec: RequestSpec): Promise<void> {
      voided.push(spec)
      return Promise.resolve()
    },
  }
  return { rest, requested, voided }
}

/** Doble que rechaza siempre: la propagación del AppError es responsabilidad del cliente. */
function failingRest(error: AppError): RestClient {
  return {
    request: () => Promise.reject(error),
    requestVoid: () => Promise.reject(error),
  }
}

describe('network/world.api — contratos de endpoint', () => {
  it('listBuildings hace GET /world/buildings con filtros + cursor y devuelve Page', async () => {
    const { rest, requested } = fakeRest([{ id: BUILDING_ID }], 'cursor-siguiente')
    const page = await createWorldApi(rest).listBuildings({
      region_id: REGION_ID,
      status: 'operational',
      cursor: 'cursor-anterior',
      limit: 50,
    })

    expect(requested).toHaveLength(1)
    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/world/buildings',
      query: {
        region_id: REGION_ID,
        status: 'operational',
        cursor: 'cursor-anterior',
        limit: 50,
      },
    })
    expect(page.items).toEqual([{ id: BUILDING_ID }])
    expect(page.nextCursor).toBe('cursor-siguiente')
  })

  it('queueProductionBatches hace POST a la cola del edificio con el DTO snake_case', async () => {
    const { rest, requested } = fakeRest({ id: BATCH_ID })
    await createWorldApi(rest).queueProductionBatches(BUILDING_ID, {
      recipe_id: RECIPE_ID,
      batches_queued: 3,
    })

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: `/world/buildings/${BUILDING_ID}/production-batches`,
      body: { recipe_id: RECIPE_ID, batches_queued: 3 },
    })
  })

  it('cancelProductionBatch hace DELETE /world/production-batches/{batchId} y desenvuelve data', async () => {
    const { rest, requested } = fakeRest({ id: BATCH_ID, status: 'cancelled' })
    const batch = await createWorldApi(rest).cancelProductionBatch(BATCH_ID)

    expect(requested[0]).toMatchObject({
      method: 'DELETE',
      path: `/world/production-batches/${BATCH_ID}`,
    })
    expect(batch).toEqual({ id: BATCH_ID, status: 'cancelled' })
  })

  it('propaga el AppError tipado del cliente REST sin transformarlo', async () => {
    const error = new AppError({
      kind: 'http',
      code: 'PLACEMENT_INVALID',
      message: 'emplazamiento inválido',
      status: 422,
    })
    const api = createWorldApi(failingRest(error))
    await expect(api.getBuilding(BUILDING_ID)).rejects.toBe(error)
  })
})
