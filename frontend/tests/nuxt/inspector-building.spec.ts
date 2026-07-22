import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'

import { t } from '~shared/i18n'
import type { AccountId } from '~domain/auth'
import InspectorBuilding from '~/components/play/InspectorBuilding.vue'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'
import {
  MY_ACCOUNT,
  OTHER_ACCOUNT,
  building,
  buildingType,
  inventoryItem,
  product,
  productionBatch,
  recipe,
  uid,
} from '~/stores/testing/fixtures'
import { stubNuxtApp } from './game-fakes'

const BUILDING_ID = uid<'Building'>(70)

async function mountInspector(owner: AccountId) {
  const pinia = createPinia()
  setActivePinia(pinia)

  const world = useWorldStore()
  world.applyProductsSnapshot([product()])
  world.applyBuildingTypesSnapshot([buildingType()])
  world.applyRecipesSnapshot([recipe()])

  const buildings = useBuildingsStore()
  buildings.applyBuildingsSnapshot([
    building({ id: BUILDING_ID, ownerAccountId: owner, activeRecipeId: uid(30) }),
  ])
  buildings.applyInventorySnapshot(BUILDING_ID, [inventoryItem({ buildingId: BUILDING_ID })])
  buildings.applyBuildingBatchesSnapshot(BUILDING_ID, [
    productionBatch({ buildingId: BUILDING_ID }),
  ])

  const session = useSessionStore()
  session.account = {
    id: MY_ACCOUNT,
    kind: 'human',
    name: 'Mi corporación',
    status: 'active',
    botArchetype: null,
    createdAtMs: 0,
  }

  const wrapper = mount(InspectorBuilding, {
    props: { buildingId: BUILDING_ID },
    global: { plugins: [pinia] },
  })
  await flushPromises()
  return wrapper
}

describe('components/play/InspectorBuilding', () => {
  beforeEach(() => {
    stubNuxtApp(2_000)
  })

  it('edificio PROPIO: acciones de mando habilitadas', async () => {
    const wrapper = await mountInspector(MY_ACCOUNT)

    expect(wrapper.find('[data-testid="foreign-note"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="recipe-select"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="apply-recipe"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="queue-batches"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-testid="upgrade-building"]').attributes('disabled')).toBeUndefined()
  })

  it('edificio AJENO: todo mando deshabilitado con nota y tooltip (OwnershipPolicy)', async () => {
    const wrapper = await mountInspector(OTHER_ACCOUNT)

    expect(wrapper.get('[data-testid="foreign-note"]').text()).toBe(t('ownership.foreign'))
    expect(wrapper.get('[data-testid="recipe-select"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="apply-recipe"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="queue-batches"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="upgrade-building"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-testid="apply-recipe"]').attributes('title')).toBe(
      t('ownership.foreign'),
    )
  })

  it('muestra inventario y cola con el progreso derivado del SimClock', async () => {
    // simNow=2000, startedAtSim=1000, batch de 3600 s ⇒ 27% derivado.
    const wrapper = await mountInspector(MY_ACCOUNT)

    expect(wrapper.text()).toContain('Mineral de hierro')
    expect(wrapper.text()).toContain('27%')
  })

  it('lote paused_no_fuel: badge + explicación causa/remedio (no badge mudo)', async () => {
    const wrapper = await mountInspector(MY_ACCOUNT)
    useBuildingsStore().applyBuildingBatchesSnapshot(BUILDING_ID, [
      productionBatch({ buildingId: BUILDING_ID, status: 'paused_no_fuel' }),
    ])
    await flushPromises()

    expect(wrapper.get('[data-testid="batch-status-explain"]').text()).toBe(
      t('status.explain.paused_no_fuel'),
    )
  })

  it('condición baja: clase de severidad + explicación del estado damaged', async () => {
    const wrapper = await mountInspector(MY_ACCOUNT)
    useBuildingsStore().applyBuilding(
      building({
        id: BUILDING_ID,
        ownerAccountId: MY_ACCOUNT,
        activeRecipeId: uid(30),
        status: 'damaged',
        conditionPct: 20,
      }),
    )
    await flushPromises()

    expect(wrapper.get('[data-testid="building-condition"]').classes()).toContain(
      'inspector-building__condition--danger',
    )
    expect(wrapper.get('[data-testid="building-status-explain"]').text()).toBe(
      t('status.explain.damaged'),
    )
  })
})
