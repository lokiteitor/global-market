import { ref } from 'vue'
import type { Ref } from 'vue'
import { createPinia, setActivePinia } from 'pinia'
import type { Pinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import type { Router } from 'vue-router'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import type { Account } from '~domain/auth'
import { asEntityId } from '~shared/ids'
import { t } from '~shared/i18n'
import type { SimTime } from '~shared/simtime'
import { simTime } from '~shared/simtime'
import type { AuthApi } from '~network/auth.api'
import LobbyPage from '~/pages/lobby.vue'
import { useSessionStore } from '~/stores/session.store'

const ACCOUNT: Account = {
  id: asEntityId<'Account'>('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012'),
  kind: 'human',
  name: 'Aceros del Norte',
  status: 'active',
  botArchetype: null,
  createdAtMs: Date.parse('2026-07-01T08:00:00Z'),
}

/** Año 2, día 5, 07:05 de juego → "002-005-07:05" (formatSimTime del kernel). */
const SIM_NOW = simTime(31_104_000 + 4 * 86_400 + 7 * 3_600 + 5 * 60)

/** Dobles del reloj del plugin sim-clock.client ($simNow/$simFrozen). */
let simNowRef: Ref<SimTime | null>
let simFrozenRef: Ref<boolean>

function fakeAuthApi() {
  const calls: string[] = []
  const api: AuthApi = {
    createSession() {
      calls.push('createSession')
      return Promise.reject(new Error('no usado en este spec'))
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

async function mountLobby() {
  const pinia: Pinia = createPinia()
  setActivePinia(pinia)
  const store = useSessionStore()
  const { api, calls } = fakeAuthApi()
  store.configure(api)
  // Sesión autenticada preexistente (el middleware auth no corre en unitarios).
  store.token = 'token-en-memoria'
  store.status = 'authenticated'
  store.account = ACCOUNT

  const router = makeRouter()
  await router.push('/lobby')
  await router.isReady()

  const wrapper = mount(LobbyPage, {
    global: {
      plugins: [pinia, router],
      stubs: { NuxtLink: { template: '<a><slot /></a>' } },
    },
  })
  await flushPromises()
  return { wrapper, store, calls, router }
}

describe('pages/lobby', () => {
  beforeEach(() => {
    simNowRef = ref<SimTime | null>(SIM_NOW)
    simFrozenRef = ref(false)
    vi.stubGlobal('useNuxtApp', () => ({
      $simNow: simNowRef,
      $simFrozen: simFrozenRef,
    }))
  })

  it('muestra la corporación de /auth/me: nombre, kind y status', async () => {
    const { wrapper, calls } = await mountLobby()

    expect(calls).toContain('getCurrentAccount')
    expect(wrapper.text()).toContain(t('lobby.welcome', { corporationName: 'Aceros del Norte' }))
    expect(wrapper.text()).toContain(t('account.kind.human'))
    expect(wrapper.text()).toContain(t('account.status.active'))
  })

  it('muestra el reloj del mundo: sim-time formateado + ratio ×24', async () => {
    const { wrapper } = await mountLobby()

    expect(wrapper.get('[data-testid="sim-time"]').text()).toBe('002-005-07:05')
    expect(wrapper.text()).toContain(t('lobby.clock.ratio'))
    // El wall-clock local también se muestra (valor dependiente del entorno).
    expect(wrapper.get('[data-testid="wall-time"]').text()).not.toBe('')
  })

  it('sin ancla del servidor, el sim-time indica sincronización pendiente', async () => {
    simNowRef.value = null
    const { wrapper } = await mountLobby()

    expect(wrapper.get('[data-testid="sim-time"]').text()).toBe(t('lobby.clock.unanchored'))
  })

  it('mundo vivo: sección de estado sin banner de mantenimiento', async () => {
    const { wrapper } = await mountLobby()

    expect(wrapper.text()).toContain(t('lobby.worldStatus.live'))
    expect(wrapper.text()).not.toContain(t('maintenance.title'))
  })

  it('cliente frozen: banner de mantenimiento en el estado del mundo', async () => {
    simFrozenRef.value = true
    const { wrapper } = await mountLobby()

    expect(wrapper.text()).toContain(t('maintenance.title'))
    expect(wrapper.text()).toContain(t('maintenance.body'))
    expect(wrapper.text()).not.toContain(t('lobby.worldStatus.live'))
  })

  it('logout purga la sesión y vuelve a /login', async () => {
    const { wrapper, store, calls, router } = await mountLobby()

    const logoutButton = wrapper
      .findAll('button')
      .find((candidate) => candidate.text() === t('login.logout'))
    expect(logoutButton).toBeDefined()
    await logoutButton?.trigger('click')
    await flushPromises()

    expect(calls).toContain('deleteCurrentSession')
    expect(store.token).toBeNull()
    expect(store.isAuthenticated).toBe(false)
    expect(router.currentRoute.value.path).toBe('/login')
  })
})
