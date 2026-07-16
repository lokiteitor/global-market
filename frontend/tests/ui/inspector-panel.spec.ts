// @vitest-environment happy-dom
/**
 * InspectorPanel — Observable vs Comandable (FAD §5.3/C13): los comandos se
 * deshabilitan preventivamente sobre entidades ajenas con la nota 'no es tuyo'.
 */
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import InspectorPanel from '~/components/hud/InspectorPanel.vue'
import { API_CLIENT_KEY, type ApiClient } from '~/composables/useApiClient'
import type { Building, SessionCreated } from '~/lib/api/types'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useSessionStore } from '~/stores/session.store'
import { useUiStore } from '~/stores/ui.store'

const MY_ID = '00000000-0000-7000-8000-0000000000aa'
const OTHER_ID = '00000000-0000-7000-8000-0000000000bb'
const BUILDING_ID = '00000000-0000-7000-8000-0000000000cc'

const stubClient: ApiClient = {
  // eslint-disable-next-line @typescript-eslint/require-await
  async request<T>() {
    return {
      ok: true as const,
      value: { data: [] as T, meta: { sim_time: '1-001-00:00', server_time: '2026-07-15T10:00:00Z' } }
    }
  }
} as ApiClient

function makeBuilding(owner: string): Building {
  return {
    id: BUILDING_ID,
    owner_account_id: owner,
    region_id: '00000000-0000-7000-8000-0000000000dd',
    concession_id: '00000000-0000-7000-8000-0000000000ee',
    building_type_id: '00000000-0000-7000-8000-0000000000ff',
    footprint: { type: 'Polygon', coordinates: [[[0, 0], [1, 0], [1, 1], [0, 1], [0, 0]]] },
    level: 2,
    status: 'operational',
    active_recipe_id: '00000000-0000-7000-8000-000000000011',
    condition_pct: 90,
    fuel_stock: '0'
  } as unknown as Building
}

const session = {
  session_id: '00000000-0000-7000-8000-000000000001',
  token: 'tok',
  expires_at: '2026-07-16T10:00:00Z',
  account: { id: MY_ID, kind: 'human', name: 'Aurora Corp', status: 'active', created_at: '2026-01-01T00:00:00Z' }
} as unknown as SessionCreated

function mountInspector(pinia: Pinia) {
  return mount(InspectorPanel, {
    global: {
      plugins: [pinia],
      provide: { [API_CLIENT_KEY as symbol]: stubClient }
    }
  })
}

describe('InspectorPanel', () => {
  let pinia: Pinia

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    useSessionStore().setSession(session)
  })

  it('deshabilita los comandos sobre un edificio ajeno y muestra la nota', async () => {
    useBuildingsStore().applySnapshot('viewport:test', { buildings: [makeBuilding(OTHER_ID)] })
    useUiStore().select('building', BUILDING_ID)

    const wrapper = mountInspector(pinia)
    await flushPromises()

    expect(wrapper.find('[data-test="not-yours"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="not-yours"]').text()).toContain('No es de tu corporación')
    expect(wrapper.find('[data-test="cmd-queue"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-test="cmd-upgrade"]').attributes('disabled')).toBeDefined()
  })

  it('habilita los comandos sobre un edificio propio (sin nota)', async () => {
    useBuildingsStore().applySnapshot('corp:mine', { buildings: [makeBuilding(MY_ID)] })
    useUiStore().select('building', BUILDING_ID)

    const wrapper = mountInspector(pinia)
    await flushPromises()

    expect(wrapper.find('[data-test="not-yours"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="cmd-queue"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="cmd-upgrade"]').attributes('disabled')).toBeUndefined()
  })
})
