import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Router } from 'vue-router'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'

import type { Account, AuthSession } from '~domain/auth'
import { asEntityId } from '~shared/ids'
import { t } from '~shared/i18n'
import type { AuthApi } from '~network/auth.api'
import { AppError } from '~network/rest'
import LoginPage from '~/pages/login.vue'
import { useSessionStore } from '~/stores/session.store'

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

/** Doble del puerto AuthApi con registro de llamadas (mismo patrón que la store). */
function fakeAuthApi(createSession?: () => Promise<AuthSession>) {
  const calls: string[] = []
  const api: AuthApi = {
    createSession(accountName, secret) {
      calls.push(`createSession:${accountName}:${secret}`)
      return (createSession ?? (() => Promise.resolve(SESSION)))()
    },
    deleteCurrentSession() {
      calls.push('deleteCurrentSession')
      return Promise.resolve()
    },
    getCurrentAccount() {
      calls.push('getCurrentAccount')
      return Promise.resolve(ACCOUNT)
    },
  }
  return { api, calls }
}

function makeRouter(): Router {
  return createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }],
  })
}

async function mountLogin(options: { createSession?: () => Promise<AuthSession>; url?: string }) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useSessionStore()
  const { api, calls } = fakeAuthApi(options.createSession)
  store.configure(api)

  const router = makeRouter()
  await router.push(options.url ?? '/login')
  await router.isReady()

  const wrapper = mount(LoginPage, { global: { plugins: [pinia, router] } })
  return { wrapper, store, calls, router }
}

async function fillAndSubmit(wrapper: Awaited<ReturnType<typeof mountLogin>>['wrapper']) {
  await wrapper.get('input[type="text"]').setValue('Aceros del Norte')
  await wrapper.get('input[type="password"]').setValue('s3creto')
  await wrapper.get('form').trigger('submit.prevent')
  await flushPromises()
}

describe('pages/login', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('submit válido llama a session.login y navega a /lobby', async () => {
    const { wrapper, store, calls, router } = await mountLogin({})

    await fillAndSubmit(wrapper)

    expect(calls).toEqual(['createSession:Aceros del Norte:s3creto'])
    expect(store.isAuthenticated).toBe(true)
    expect(router.currentRoute.value.fullPath).toBe('/lobby')
  })

  it('respeta ?redirect= interno tras el login', async () => {
    const { wrapper, router } = await mountLogin({ url: '/login?redirect=/settings' })

    await fillAndSubmit(wrapper)

    expect(router.currentRoute.value.fullPath).toBe('/settings')
  })

  it('un AppError 401 muestra el banner de credenciales inválidas y no navega', async () => {
    const { wrapper, store, router } = await mountLogin({
      createSession: () =>
        Promise.reject(
          new AppError({
            kind: 'http',
            code: 'UNAUTHORIZED',
            status: 401,
            message: 'credenciales inválidas',
          }),
        ),
    })

    await fillAndSubmit(wrapper)

    const banner = wrapper.get('[role="alert"]')
    expect(banner.text()).toContain(t('login.error.invalidCredentials'))
    expect(store.isAuthenticated).toBe(false)
    expect(router.currentRoute.value.path).toBe('/login')
  })

  it('la validación de FORMA bloquea el envío con campos vacíos', async () => {
    const { wrapper, calls } = await mountLogin({})

    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(calls).toEqual([])
    const errors = wrapper.findAll('[role="alert"]')
    expect(errors.length).toBe(2)
    expect(errors[0]?.text()).toBe(t('validation.required'))
  })
})
