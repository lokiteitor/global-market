import { describe, expect, it } from 'vitest'

import { createAuthApi } from '~network/auth.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const ACCOUNT_DTO = {
  id: '01981C5E-7D2A-7F3B-9E41-A2C4D6E8F012', // mayúsculas a propósito: el mapper canonicaliza
  kind: 'human',
  name: 'Aceros del Norte',
  status: 'active',
  created_at: '2026-07-01T08:00:00Z',
}

const SESSION_DTO = {
  session_id: '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f099',
  token: 'token-una-sola-vez',
  expires_at: '2026-07-17T12:30:00Z',
  account: ACCOUNT_DTO,
}

/** Doble del PUERTO RestClient (no del módulo): sirve data programada y registra specs. */
function fakeRest(data: unknown) {
  const requested: RequestSpec[] = []
  const voided: RequestSpec[] = []
  const rest: RestClient = {
    request<TData>(spec: RequestSpec): Promise<Enveloped<TData>> {
      requested.push(spec)
      return Promise.resolve({ data: data as TData, meta: META })
    },
    requestVoid(spec: RequestSpec): Promise<void> {
      voided.push(spec)
      return Promise.resolve()
    },
  }
  return { rest, requested, voided }
}

describe('network/auth.api — contratos de endpoint', () => {
  it('createSession hace POST /auth/sessions con el DTO snake_case del contrato', async () => {
    const { rest, requested } = fakeRest(SESSION_DTO)
    await createAuthApi(rest).createSession('Aceros del Norte', 's3creto')

    expect(requested).toHaveLength(1)
    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: '/auth/sessions',
      body: { account_name: 'Aceros del Norte', secret: 's3creto' },
    })
  })

  it('deleteCurrentSession hace DELETE /auth/sessions/current sin cuerpo (204)', async () => {
    const { rest, voided } = fakeRest(null)
    await createAuthApi(rest).deleteCurrentSession()

    expect(voided).toHaveLength(1)
    expect(voided[0]).toMatchObject({ method: 'DELETE', path: '/auth/sessions/current' })
  })

  it('getCurrentAccount hace GET /auth/me', async () => {
    const { rest, requested } = fakeRest(ACCOUNT_DTO)
    await createAuthApi(rest).getCurrentAccount()

    expect(requested[0]).toMatchObject({ method: 'GET', path: '/auth/me' })
  })
})

describe('network/auth.api — mappers DTO → dominio (FAD §9.5, O5)', () => {
  it('mapea SessionCreated a AuthSession de dominio (ids canónicos, fechas en ms)', async () => {
    const { rest } = fakeRest(SESSION_DTO)
    const session = await createAuthApi(rest).createSession('a', 'b')

    expect(session.sessionId).toBe('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f099')
    expect(session.token).toBe('token-una-sola-vez')
    expect(session.expiresAtMs).toBe(Date.parse('2026-07-17T12:30:00Z'))
    expect(session.account.id).toBe('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012') // canonicalizado
    expect(session.account.kind).toBe('human')
    expect(session.account.botArchetype).toBeNull()
    expect(session.account.createdAtMs).toBe(Date.parse('2026-07-01T08:00:00Z'))
  })

  it('conserva bot_archetype cuando la cuenta es un bot', async () => {
    const { rest } = fakeRest({ ...ACCOUNT_DTO, kind: 'bot', bot_archetype: 'freighter' })
    const account = await createAuthApi(rest).getCurrentAccount()
    expect(account.botArchetype).toBe('freighter')
  })

  it('un UUID inválido del servidor es AppError protocol (nunca estado corrupto)', async () => {
    const { rest } = fakeRest({ ...ACCOUNT_DTO, id: 'no-es-un-uuid' })
    const failure = createAuthApi(rest).getCurrentAccount()

    await expect(failure).rejects.toBeInstanceOf(AppError)
    await expect(failure).rejects.toMatchObject({ kind: 'protocol' })
  })

  it('un enum fuera de contrato es AppError protocol', async () => {
    const { rest } = fakeRest({ ...ACCOUNT_DTO, kind: 'alien' })
    await expect(createAuthApi(rest).getCurrentAccount()).rejects.toMatchObject({
      kind: 'protocol',
    })
  })

  it('una fecha no parseable es AppError protocol', async () => {
    const { rest } = fakeRest({ ...SESSION_DTO, expires_at: 'mañana' })
    await expect(createAuthApi(rest).createSession('a', 'b')).rejects.toMatchObject({
      kind: 'protocol',
    })
  })
})
