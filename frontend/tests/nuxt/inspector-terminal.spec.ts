import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { t } from '~shared/i18n'
import type { TerminalDto, TerminalSlotDto } from '~network/fleet.api'
import { AppError } from '~network/rest'
import InspectorTerminal from '~/components/play/InspectorTerminal.vue'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useSessionStore } from '~/stores/session.store'
import { MY_ACCOUNT, OTHER_ACCOUNT, node, uid } from '~/stores/testing/fixtures'
import type { StubbedNuxtApp } from './game-fakes'
import { stubNuxtApp } from './game-fakes'

const NODE_ID = uid<'Node'>(100)
const TERMINAL_ID = uid<'Terminal'>(120)
const SLOT_FREE = uid<'TerminalSlot'>(125)
const SLOT_MINE = uid<'TerminalSlot'>(126)

const TERMINAL: TerminalDto = {
  id: TERMINAL_ID,
  node_id: NODE_ID,
  owner_account_id: OTHER_ACCOUNT,
  transshipment_per_hour: 40,
  queue_length: 3,
  updated_at_sim: 1_000,
}

const SLOTS: readonly TerminalSlotDto[] = [
  { id: SLOT_FREE, terminal_id: TERMINAL_ID, priority_tier: 1, price: '30000' },
  {
    id: SLOT_MINE,
    terminal_id: TERMINAL_ID,
    priority_tier: 2,
    price: '20000',
    holder_account_id: MY_ACCOUNT,
    valid_until_sim: 100_000,
  },
]

let stub: StubbedNuxtApp

async function mountInspector() {
  const pinia = createPinia()
  setActivePinia(pinia)

  const session = useSessionStore()
  session.account = {
    id: MY_ACCOUNT,
    kind: 'human',
    name: 'Demo',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }
  useLogisticsStore().applyNodesSnapshot([
    node({ id: NODE_ID, kind: 'port', terminalId: TERMINAL_ID }),
  ])

  const wrapper = mount(InspectorTerminal, {
    props: { nodeId: NODE_ID },
    global: { plugins: [pinia] },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/InspectorTerminal', () => {
  beforeEach(() => {
    stub = stubNuxtApp(1_000)
    vi.mocked(stub.apis.fleet.getTerminal).mockResolvedValue(TERMINAL)
    vi.mocked(stub.apis.fleet.listTerminalSlots).mockResolvedValue(SLOTS)
  })

  it('pull al montar: cola, capacidad y slots ordenados por tier; el mío marcado', async () => {
    const wrapper = await mountInspector()

    expect(stub.apis.fleet.getTerminal).toHaveBeenCalledWith(TERMINAL_ID)
    expect(stub.apis.fleet.listTerminalSlots).toHaveBeenCalledWith(TERMINAL_ID)
    expect(wrapper.get('[data-testid="terminal-queue"]').text()).toBe('3')
    expect(wrapper.findAll('[data-testid="terminal-slot-row"]')).toHaveLength(2)
    // El slot con mi titularidad vigente se marca "Tuyo".
    expect(wrapper.get('[data-testid="slot-own"]').text()).toBe(
      t('inspector.terminal.slot.own'),
    )
  })

  it('compra con confirmación: aplica la respuesta a la fila (thin client)', async () => {
    const purchased: TerminalSlotDto = {
      id: SLOT_FREE,
      terminal_id: TERMINAL_ID,
      priority_tier: 1,
      price: '30000',
      holder_account_id: MY_ACCOUNT,
      valid_until_sim: 2_593_000,
    }
    vi.mocked(stub.apis.fleet.purchaseTerminalSlot).mockResolvedValue(purchased)
    const wrapper = await mountInspector()

    await wrapper.get('[data-testid="slot-buy"]').trigger('click')
    expect(wrapper.get('[data-testid="slot-confirm-box"]').text()).toContain('30.000')

    await wrapper.get('[data-testid="slot-confirm"]').trigger('click')
    await flushPromises()

    expect(stub.apis.fleet.purchaseTerminalSlot).toHaveBeenCalledWith(SLOT_FREE)
    // Ahora ambos slots son míos y no queda ninguno comprable.
    expect(wrapper.findAll('[data-testid="slot-own"]')).toHaveLength(2)
    expect(wrapper.find('[data-testid="slot-buy"]').exists()).toBe(false)
  })

  it('422 INSUFFICIENT_FUNDS se muestra tipado', async () => {
    vi.mocked(stub.apis.fleet.purchaseTerminalSlot).mockRejectedValue(
      new AppError({
        kind: 'http',
        code: 'INSUFFICIENT_FUNDS',
        message: 'sin fondos',
        status: 422,
      }),
    )
    const wrapper = await mountInspector()

    await wrapper.get('[data-testid="slot-buy"]').trigger('click')
    await wrapper.get('[data-testid="slot-confirm"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain(t('error.INSUFFICIENT_FUNDS'))
  })
})
