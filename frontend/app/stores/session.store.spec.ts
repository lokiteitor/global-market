import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import type { Account, AuthSession } from '~domain/auth'
import { asEntityId } from '~shared/ids'
import type { AuthApi } from '~network/auth.api'
import { AppError } from '~network/rest'
import { useSessionStore } from './session.store'

const ACCOUNT: Account = {
  id: asEntityId<'Account'>('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012'),
  kind: 'human',
  name: 'Aceros del Norte',
  status: 'active',
  botArchetype: null,
  createdAtMs: Date.parse('2026-07-01T08:00:00Z'),
}

const SESSION: AuthSession = {
  sessionId: asEntityId<'Session'>('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f099'),
  token: 'token-en-memoria',
  expiresAtMs: Date.parse('2026-07-17T12:30:00Z'),
  account: ACCOUNT,
}

interface FakeAuthApiScript {
  createSession?: () => Promise<AuthSession>
  deleteCurrentSession?: () => Promise<void>
  getCurrentAccount?: () => Promise<Account>
}

/** Doble del PUERTO AuthApi (no del módulo global): guiones por método + registro de llamadas. */
function fakeAuthApi(script: FakeAuthApiScript = {}) {
  const calls: string[] = []
  const api: AuthApi = {
    createSession(accountName, secret) {
      calls.push(`createSession:${accountName}:${secret}`)
      return (script.createSession ?? (() => Promise.resolve(SESSION)))()
    },
    deleteCurrentSession() {
      calls.push('deleteCurrentSession')
      return (script.deleteCurrentSession ?? (() => Promise.resolve()))()
    },
    getCurrentAccount() {
      calls.push('getCurrentAccount')
      return (script.getCurrentAccount ?? (() => Promise.resolve(ACCOUNT)))()
    },
  }
  return { api, calls }
}

function unauthorized(): AppError {
  return new AppError({
    kind: 'http',
    code: 'UNAUTHORIZED',
    status: 401,
    message: 'Sesión no válida',
  })
}

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/session.store — login', () => {
  it('login guarda token (SOLO memoria) y cuenta, y pasa a authenticated', async () => {
    const store = useSessionStore()
    const { api, calls } = fakeAuthApi()
    store.configure(api)

    const pending = store.login('Aceros del Norte', 's3creto')
    expect(store.status).toBe('authenticating')
    await pending

    expect(calls).toEqual(['createSession:Aceros del Norte:s3creto'])
    expect(store.token).toBe('token-en-memoria')
    expect(store.account?.name).toBe('Aceros del Norte')
    expect(store.status).toBe('authenticated')
    expect(store.isAuthenticated).toBe(true)
    expect(store.lastErrorCode).toBeNull()
  })

  it('un login fallido purga, marca error con el código y relanza el AppError', async () => {
    const store = useSessionStore()
    const { api } = fakeAuthApi({
      createSession: () => Promise.reject(unauthorized()),
    })
    store.configure(api)

    await expect(store.login('x', 'mal')).rejects.toMatchObject({ code: 'UNAUTHORIZED' })

    expect(store.status).toBe('error')
    expect(store.lastErrorCode).toBe('UNAUTHORIZED')
    expect(store.token).toBeNull()
    expect(store.account).toBeNull()
    expect(store.isAuthenticated).toBe(false)
  })

  it('sin AuthApi configurada, login falla ruidosamente (bug de arranque, no silencio)', async () => {
    const store = useSessionStore()
    await expect(store.login('a', 'b')).rejects.toThrow('AuthApi no configurado')
  })
})

describe('app/stores/session.store — logout', () => {
  it('logout invalida en servidor y purga token/cuenta a idle', async () => {
    const store = useSessionStore()
    const { api, calls } = fakeAuthApi()
    store.configure(api)
    await store.login('a', 'b')

    await store.logout()

    expect(calls).toContain('deleteCurrentSession')
    expect(store.token).toBeNull()
    expect(store.account).toBeNull()
    expect(store.status).toBe('idle')
    expect(store.isAuthenticated).toBe(false)
  })

  it('la purga local es incondicional aunque el servidor falle (best-effort)', async () => {
    const store = useSessionStore()
    const { api } = fakeAuthApi({
      deleteCurrentSession: () =>
        Promise.reject(
          new AppError({ kind: 'network', code: 'INTERNAL', status: 0, message: 'offline' }),
        ),
    })
    store.configure(api)
    await store.login('a', 'b')

    await store.logout() // no lanza

    expect(store.token).toBeNull()
    expect(store.status).toBe('idle')
  })
})

describe('app/stores/session.store — fetchMe', () => {
  it('refresca la cuenta de la sesión actual', async () => {
    const store = useSessionStore()
    const renamed: Account = { ...ACCOUNT, name: 'Aceros del Sur' }
    const { api } = fakeAuthApi({ getCurrentAccount: () => Promise.resolve(renamed) })
    store.configure(api)
    await store.login('a', 'b')

    await store.fetchMe()
    expect(store.account?.name).toBe('Aceros del Sur')
    expect(store.status).toBe('authenticated')
  })

  it('un 401 en fetchMe purga la sesión caducada y relanza', async () => {
    const store = useSessionStore()
    const { api } = fakeAuthApi({ getCurrentAccount: () => Promise.reject(unauthorized()) })
    store.configure(api)
    await store.login('a', 'b')

    await expect(store.fetchMe()).rejects.toMatchObject({ code: 'UNAUTHORIZED' })
    expect(store.token).toBeNull()
    expect(store.account).toBeNull()
    expect(store.status).toBe('idle')
  })

  it('otros errores en fetchMe NO purgan la sesión (p. ej. mantenimiento)', async () => {
    const store = useSessionStore()
    const { api } = fakeAuthApi({
      getCurrentAccount: () =>
        Promise.reject(
          new AppError({
            kind: 'http',
            code: 'MAINTENANCE_WINDOW',
            status: 503,
            message: 'mundo pausado',
            retryAfterSeconds: 900,
          }),
        ),
    })
    store.configure(api)
    await store.login('a', 'b')

    await expect(store.fetchMe()).rejects.toMatchObject({ code: 'MAINTENANCE_WINDOW' })
    expect(store.token).toBe('token-en-memoria')
    expect(store.status).toBe('authenticated')
  })
})
