import { describe, expect, it } from 'vitest'

import { isUuid, uuidv7TimestampMs } from '~shared/ids'
import type { HttpReply, HttpRequest, HttpTransport } from '~network/rest/http'
import type { ResponseMeta } from '~network/rest/envelope'
import { AppError } from '~network/rest/errors'
import { createRestClient } from '~network/rest/client'

/** Envelope de éxito mínimo conforme al contrato. */
function envelope(data: unknown, metaOverrides: Record<string, unknown> = {}) {
  return {
    data,
    meta: {
      sim_time: '360-045-12:30',
      sim_time_seconds: 31_104_000,
      server_time: '2026-07-16T12:30:00Z',
      ...metaOverrides,
    },
  }
}

function reply(status: number, body: unknown, headers: Record<string, string> = {}): HttpReply {
  return {
    status,
    body,
    getHeader: (name) => headers[name.toLowerCase()] ?? null,
  }
}

/** Doble del puerto HttpTransport: registra peticiones y sirve respuestas programadas. */
function fakeTransport(...replies: HttpReply[]) {
  const requests: HttpRequest[] = []
  const queue = [...replies]
  const transport: HttpTransport = (request) => {
    requests.push(request)
    const next = queue.shift()
    if (next === undefined) {
      throw new Error('fakeTransport: sin respuesta programada')
    }
    return Promise.resolve(next)
  }
  return { transport, requests }
}

interface ClientOverrides {
  token?: string | null
  onMeta?: (meta: ResponseMeta) => void
  onMaintenance?: (retryAfterSeconds: number | null) => void
}

function makeClient(transport: HttpTransport, overrides: ClientOverrides = {}) {
  return createRestClient({
    baseUrl: '/api/v1',
    transport,
    tokenProvider: () => overrides.token ?? null,
    ...(overrides.onMeta !== undefined ? { onMeta: overrides.onMeta } : {}),
    ...(overrides.onMaintenance !== undefined ? { onMaintenance: overrides.onMaintenance } : {}),
  })
}

describe('network/rest/client — unwrap del envelope {data, meta}', () => {
  it('devuelve data cruda y meta mapeada a dominio', async () => {
    const { transport } = fakeTransport(reply(200, envelope({ id: 'x' }, { next_cursor: 'abc' })))
    const client = makeClient(transport)

    const result = await client.request<{ id: string }>({ method: 'GET', path: '/auth/me' })

    expect(result.data).toEqual({ id: 'x' })
    expect(result.meta.simTimeSeconds).toBe(31_104_000)
    expect(result.meta.simTimeLabel).toBe('360-045-12:30')
    expect(result.meta.serverTimeMs).toBe(Date.parse('2026-07-16T12:30:00Z'))
    expect(result.meta.nextCursor).toBe('abc')
  })

  it('notifica la meta de CADA éxito por el callback onMeta (alimenta el SimClock)', async () => {
    const seen: ResponseMeta[] = []
    const { transport } = fakeTransport(reply(200, envelope([])), reply(200, envelope([])))
    const client = makeClient(transport, { onMeta: (meta) => seen.push(meta) })

    await client.request({ method: 'GET', path: '/world/regions' })
    await client.request({ method: 'GET', path: '/world/products' })

    expect(seen).toHaveLength(2)
    expect(seen[0]?.simTimeSeconds).toBe(31_104_000)
  })

  it('meta sin sim_time_seconds (campo opcional) llega como null, sin romper', async () => {
    const seen: ResponseMeta[] = []
    const { transport } = fakeTransport(reply(200, envelope(null, { sim_time_seconds: undefined })))
    const client = makeClient(transport, { onMeta: (meta) => seen.push(meta) })

    await client.request({ method: 'GET', path: '/auth/me' })
    expect(seen[0]?.simTimeSeconds).toBeNull()
  })

  it('un 2xx sin envelope válido es AppError protocol (violación de contrato)', async () => {
    const { transport } = fakeTransport(reply(200, { foo: 1 }))
    const client = makeClient(transport)

    const failure = client.request({ method: 'GET', path: '/auth/me' })
    await expect(failure).rejects.toBeInstanceOf(AppError)
    await expect(failure).rejects.toMatchObject({ kind: 'protocol' })
  })

  it('requestVoid acepta 204 sin cuerpo (DELETE /auth/sessions/current)', async () => {
    const seen: ResponseMeta[] = []
    const { transport, requests } = fakeTransport(reply(204, undefined))
    const client = makeClient(transport, { onMeta: (meta) => seen.push(meta) })

    await client.requestVoid({ method: 'DELETE', path: '/auth/sessions/current' })

    expect(requests[0]?.method).toBe('DELETE')
    expect(seen).toHaveLength(0) // sin envelope, no hay meta que notificar
  })
})

describe('network/rest/client — construcción de la petición', () => {
  it('compone base + path + query, omitiendo parámetros undefined', async () => {
    const { transport, requests } = fakeTransport(reply(200, envelope([])))
    const client = makeClient(transport)

    await client.request({
      method: 'GET',
      path: '/contracts/board',
      query: { kind: 'sell', limit: 50, cursor: undefined },
    })

    expect(requests[0]?.url).toBe('/api/v1/contracts/board?kind=sell&limit=50')
  })

  it('inyecta Authorization: Bearer cuando el token provider tiene sesión', async () => {
    const { transport, requests } = fakeTransport(reply(200, envelope(null)))
    const client = makeClient(transport, { token: 'secreto-en-memoria' })

    await client.request({ method: 'GET', path: '/auth/me' })
    expect(requests[0]?.headers['authorization']).toBe('Bearer secreto-en-memoria')
  })

  it('no envía Authorization sin sesión (login)', async () => {
    const { transport, requests } = fakeTransport(reply(201, envelope({ token: 't' })))
    const client = makeClient(transport)

    await client.request({ method: 'POST', path: '/auth/sessions', body: { account_name: 'a' } })
    expect(requests[0]?.headers['authorization']).toBeUndefined()
  })
})

describe('network/rest/client — Idempotency-Key (FAD §12.8, P6)', () => {
  it.each(['POST', 'PATCH', 'DELETE', 'PUT'] as const)(
    'todo %s lleva Idempotency-Key UUIDv7 generada por el kernel',
    async (method) => {
      const { transport, requests } = fakeTransport(reply(200, envelope(null)))
      const client = makeClient(transport)

      await client.request({ method, path: '/x', body: {} })

      const key = requests[0]?.headers['idempotency-key']
      expect(key).toBeDefined()
      expect(isUuid(key as string)).toBe(true)
      // UUIDv7: el timestamp embebido es coherente con el reloj actual.
      expect(Math.abs(uuidv7TimestampMs(key as string) - Date.now())).toBeLessThan(60_000)
    },
  )

  it('un GET no lleva Idempotency-Key', async () => {
    const { transport, requests } = fakeTransport(reply(200, envelope(null)))
    const client = makeClient(transport)

    await client.request({ method: 'GET', path: '/auth/me' })
    expect(requests[0]?.headers['idempotency-key']).toBeUndefined()
  })

  it('el reintento de un comando reutiliza la clave suministrada', async () => {
    const { transport, requests } = fakeTransport(
      reply(200, envelope(null)),
      reply(200, envelope(null)),
    )
    const client = makeClient(transport)
    const retryKey = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012'

    await client.request({ method: 'POST', path: '/x', body: {}, idempotencyKey: retryKey })
    await client.request({ method: 'POST', path: '/x', body: {}, idempotencyKey: retryKey })

    expect(requests[0]?.headers['idempotency-key']).toBe(retryKey)
    expect(requests[1]?.headers['idempotency-key']).toBe(retryKey)
  })

  it('dos comandos distintos reciben claves distintas', async () => {
    const { transport, requests } = fakeTransport(
      reply(200, envelope(null)),
      reply(200, envelope(null)),
    )
    const client = makeClient(transport)

    await client.request({ method: 'POST', path: '/x', body: {} })
    await client.request({ method: 'POST', path: '/x', body: {} })

    expect(requests[0]?.headers['idempotency-key']).not.toBe(
      requests[1]?.headers['idempotency-key'],
    )
  })
})

describe('network/rest/client — errores y mantenimiento (FAD §12.9, §13.7)', () => {
  it('un 4xx con envelope se propaga como AppError tipado', async () => {
    const { transport } = fakeTransport(
      reply(422, {
        error: { code: 'BELOW_MIN_LOT', message: 'lote mínimo', details: { min_lot: '10' } },
      }),
    )
    const client = makeClient(transport)

    const failure = client.request({ method: 'POST', path: '/x', body: {} })
    await expect(failure).rejects.toMatchObject({
      code: 'BELOW_MIN_LOT',
      status: 422,
      kind: 'http',
    })
  })

  it('503 MAINTENANCE_WINDOW dispara onMaintenance con el Retry-After (estado frozen)', async () => {
    const maintenance: Array<number | null> = []
    const { transport } = fakeTransport(
      reply(
        503,
        { error: { code: 'MAINTENANCE_WINDOW', message: 'mundo pausado' } },
        { 'retry-after': '900' },
      ),
    )
    const client = makeClient(transport, {
      onMaintenance: (seconds) => maintenance.push(seconds),
    })

    const failure = client.request({ method: 'GET', path: '/world/cities' })
    await expect(failure).rejects.toMatchObject({ code: 'MAINTENANCE_WINDOW' })
    await failure.catch((error: unknown) => {
      expect((error as AppError).isMaintenance).toBe(true)
    })
    expect(maintenance).toEqual([900])
  })

  it('los errores no-mantenimiento NO disparan onMaintenance', async () => {
    const maintenance: Array<number | null> = []
    const { transport } = fakeTransport(reply(404, { error: { code: 'NOT_FOUND', message: 'x' } }))
    const client = makeClient(transport, {
      onMaintenance: (seconds) => maintenance.push(seconds),
    })

    await expect(client.request({ method: 'GET', path: '/x' })).rejects.toBeInstanceOf(AppError)
    expect(maintenance).toHaveLength(0)
  })

  it('el fallo del transporte (sin respuesta) se mapea a AppError kind network', async () => {
    const cause = new TypeError('fetch failed')
    const transport: HttpTransport = () => Promise.reject(cause)
    const client = makeClient(transport)

    const failure = client.request({ method: 'GET', path: '/x' })
    await expect(failure).rejects.toMatchObject({ kind: 'network', status: 0 })
  })
})
