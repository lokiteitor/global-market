import { describe, expect, it } from 'vitest'

import { createLogisticsApi } from '~network/logistics.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const REGION_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f020'
const ORIGIN_NODE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f021'
const DESTINATION_NODE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f022'
const LINK_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f023'
const ROUTE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f024'

/** Doble del PUERTO RestClient: sirve data programada y registra specs. */
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

describe('network/logistics.api — contratos de endpoint', () => {
  it('listNetworkLinks hace GET /logistics/network/links con filtros y devuelve Page', async () => {
    const { rest, requested } = fakeRest([{ id: LINK_ID }], 'cursor-links')
    const page = await createLogisticsApi(rest).listNetworkLinks({
      region_id: REGION_ID,
      mode: 'road',
      limit: 100,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/logistics/network/links',
      query: { region_id: REGION_ID, mode: 'road', limit: 100 },
    })
    expect(page.items).toEqual([{ id: LINK_ID }])
    expect(page.nextCursor).toBe('cursor-links')
  })

  it('planRoute hace POST /logistics/route-plans con el DTO del contrato y desenvuelve el plan', async () => {
    const plan = { origin_node_id: ORIGIN_NODE_ID, legs: [], total_eta_sim_seconds: 7200 }
    const { rest, requested } = fakeRest(plan)
    const result = await createLogisticsApi(rest).planRoute({
      origin_node_id: ORIGIN_NODE_ID,
      destination_node_id: DESTINATION_NODE_ID,
      optimize: 'time',
      cargo_volume: '500',
    })

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: '/logistics/route-plans',
      body: {
        origin_node_id: ORIGIN_NODE_ID,
        destination_node_id: DESTINATION_NODE_ID,
        optimize: 'time',
        cargo_volume: '500',
      },
    })
    expect(result).toEqual(plan)
  })

  it('el CRUD de rutas usa las rutas y métodos del contrato (POST/PATCH/DELETE)', async () => {
    const { rest, requested, voided } = fakeRest({ id: ROUTE_ID })
    const api = createLogisticsApi(rest)

    await api.createRoute({ name: 'Línea norte', kind: 'fixed_line', legs: [LINK_ID] })
    await api.updateRoute(ROUTE_ID, { active: false })
    await api.deleteRoute(ROUTE_ID)

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: '/logistics/routes',
      body: { name: 'Línea norte', kind: 'fixed_line', legs: [LINK_ID] },
    })
    expect(requested[1]).toMatchObject({
      method: 'PATCH',
      path: `/logistics/routes/${ROUTE_ID}`,
      body: { active: false },
    })
    expect(voided[0]).toMatchObject({ method: 'DELETE', path: `/logistics/routes/${ROUTE_ID}` })
  })

  it('propaga el AppError tipado del cliente REST (p. ej. NO_ROUTE_FOUND)', async () => {
    const error = new AppError({
      kind: 'http',
      code: 'NO_ROUTE_FOUND',
      message: 'sin ruta ejecutable',
      status: 422,
    })
    const rest: RestClient = {
      request: () => Promise.reject(error),
      requestVoid: () => Promise.reject(error),
    }
    const failure = createLogisticsApi(rest).planRoute({
      origin_node_id: ORIGIN_NODE_ID,
      destination_node_id: DESTINATION_NODE_ID,
      optimize: 'time',
    })
    await expect(failure).rejects.toBe(error)
  })
})
