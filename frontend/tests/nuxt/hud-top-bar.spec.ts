import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'

import { asEntityId } from '~shared/ids'
import { t } from '~shared/i18n'
import HudTopBar from '~/components/play/HudTopBar.vue'
import { useFinanceStore } from '~/stores/finance.store'
import { useSessionStore } from '~/stores/session.store'
import { ledgerAccount, mon } from '~/stores/testing/fixtures'
import { stubNuxtApp } from './game-fakes'

/** Año 2, día 5, 07:05 de juego → "002-005-07:05". */
const SIM_NOW_SECONDS = 31_104_000 + 4 * 86_400 + 7 * 3_600 + 5 * 60

async function mountTopBar(connection: 'connecting' | 'open' | 'reconnecting' | 'closed') {
  const pinia = createPinia()
  setActivePinia(pinia)

  const finance = useFinanceStore()
  finance.applyLedgerAccountsSnapshot([ledgerAccount({ kind: 'cash', balance: mon('1234567') })])

  const session = useSessionStore()
  session.account = {
    id: asEntityId<'Account'>('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012'),
    kind: 'human',
    name: 'Aceros del Norte',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }

  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }],
  })
  await router.push('/play')
  await router.isReady()

  const wrapper = mount(HudTopBar, {
    props: { connection },
    global: {
      plugins: [pinia, router],
      stubs: { NuxtLink: { template: '<a><slot /></a>' } },
    },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/HudTopBar', () => {
  beforeEach(() => {
    stubNuxtApp(SIM_NOW_SECONDS)
  })

  it('muestra el saldo cash del ledger formateado con shared/money', async () => {
    const wrapper = await mountTopBar('open')

    expect(wrapper.get('[data-testid="hud-cash"]').text()).toBe('1.234.567')
    expect(wrapper.text()).toContain(t('hud.cash.label'))
  })

  it('muestra el reloj del mundo con el formato del kernel', async () => {
    const wrapper = await mountTopBar('open')

    expect(wrapper.get('[data-testid="hud-sim-time"]').text()).toBe('002-005-07:05')
  })

  it('refleja el estado de la conexión WS', async () => {
    const wrapper = await mountTopBar('reconnecting')

    expect(wrapper.text()).toContain(t('hud.connection.reconnecting'))
    expect(wrapper.get('[data-testid="hud-connection"]').classes()).toContain(
      'topbar__dot--reconnecting',
    )
  })

  it('marca el estado stale durante un resync', async () => {
    const pinia = createPinia()
    setActivePinia(pinia)
    useFinanceStore()
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }],
    })
    await router.push('/play')
    await router.isReady()

    const wrapper = mount(HudTopBar, {
      props: { connection: 'open', stale: true },
      global: { plugins: [pinia, router], stubs: { NuxtLink: { template: '<a><slot /></a>' } } },
    })

    expect(wrapper.text()).toContain(t('common.stale'))
  })
})
