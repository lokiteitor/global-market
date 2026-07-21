<script setup lang="ts">
/**
 * InspectorBuilding — detalle y mando de un edificio (FAD §15.12).
 *
 * OwnershipPolicy aplicada (domain/ownership): sobre edificio PROPIO ofrece
 * cambiar receta activa, encolar lotes y mejorar nivel; sobre ajeno, todo
 * deshabilitado con nota/tooltip (UX honesta — el servidor revalida con 403).
 * El progreso del lote en curso se DERIVA con el SimClock (P1). Toda acción
 * aplica la RESPUESTA del servidor a la store (nunca predicción optimista).
 */

import { computed, ref, watch } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatQuantity } from '~domain/quantity'
import type { BuildingId } from '~domain/buildings'
import { isCommandable } from '~domain/ownership'
import { batchStatusPresentation, buildingStatusPresentation } from '~domain/status'
import type { ProductId } from '~domain/world'
import {
  mapBuilding,
  mapInventoryItem,
  mapProductionBatch,
} from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { batchProgressPct } from '~/composables/useBatchProgress'
import { useGameApis } from '~/composables/useGameApis'
import { useSimNow } from '~/composables/useSimNow'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  buildingId: BuildingId
}

const props = defineProps<Props>()

const apis = useGameApis()
const buildings = useBuildingsStore()
const world = useWorldStore()
const session = useSessionStore()
const simNow = useSimNow()
const { messageFor } = useAppError()

const building = computed(() => buildings.getBuilding(props.buildingId))
const buildingType = computed(() => world.getBuildingType(building.value?.buildingTypeId ?? null))
const own = computed(() => {
  const current = building.value
  return current !== null && isCommandable(current, session.account?.id ?? null)
})
const recipes = computed(() => {
  const typeId = building.value?.buildingTypeId
  return typeId === undefined ? [] : world.recipesForBuildingType(typeId)
})
const activeRecipe = computed(() => world.getRecipe(building.value?.activeRecipeId ?? null))
const inventory = computed(() => buildings.inventoryOf(props.buildingId))
const queue = computed(() => buildings.batchesOfBuilding(props.buildingId))

/** Tooltip de acción deshabilitada por titularidad (OwnershipPolicy). */
const foreignTitle = computed(() => (own.value ? undefined : t('ownership.foreign')))

const busy = ref(false)
const actionError = ref<unknown>(null)
const errorText = computed(() => (actionError.value === null ? null : messageFor(actionError.value)))

// — Cambiar receta —
const selectedRecipeId = ref<string>('')
watch(
  () => building.value?.activeRecipeId ?? null,
  (value) => {
    selectedRecipeId.value = value ?? ''
  },
  { immediate: true },
)

async function run(action: () => Promise<void>): Promise<void> {
  busy.value = true
  actionError.value = null
  try {
    await action()
  } catch (error) {
    actionError.value = error
  } finally {
    busy.value = false
  }
}

async function onApplyRecipe(): Promise<void> {
  await run(async () => {
    const dto = await apis.world.updateBuilding(props.buildingId, {
      active_recipe_id: selectedRecipeId.value === '' ? null : selectedRecipeId.value,
    })
    buildings.applyBuilding(mapBuilding(dto))
  })
}

// — Encolar lotes —
const batchCount = ref('1')

async function onQueueBatches(): Promise<void> {
  const count = Number.parseInt(batchCount.value, 10)
  const recipeId = selectedRecipeId.value
  if (!Number.isSafeInteger(count) || count <= 0 || recipeId === '') {
    actionError.value = null
    return
  }
  await run(async () => {
    const dto = await apis.world.queueProductionBatches(props.buildingId, {
      recipe_id: recipeId,
      batches_queued: count,
    })
    buildings.applyBatch(mapProductionBatch(dto))
    // La cola y el inventario del edificio pueden reordenarse: re-pull acotado.
    const inventoryDto = await apis.world.getBuildingInventory(props.buildingId)
    buildings.applyInventorySnapshot(props.buildingId, inventoryDto.map(mapInventoryItem))
  })
}

// — Mejorar —
const canUpgrade = computed(() => {
  const current = building.value
  const type = buildingType.value
  return current !== null && type !== null && current.level < type.maxLevel
})

async function onUpgrade(): Promise<void> {
  await run(async () => {
    const dto = await apis.world.upgradeBuilding(props.buildingId)
    buildings.applyBuilding(mapBuilding(dto))
  })
}

function productName(productId: ProductId): string {
  return world.getProduct(productId)?.name ?? productId
}
</script>

<template>
  <div v-if="building !== null" class="inspector-building o-stack">
    <header class="inspector-building__head">
      <strong>{{ buildingType?.name ?? t('inspector.building.title') }}</strong>
      <StatusBadge :presentation="buildingStatusPresentation(building.status)" />
    </header>

    <p v-if="!own" class="inspector-building__foreign" data-testid="foreign-note">
      {{ t('ownership.foreign') }}
    </p>

    <dl class="inspector-building__facts">
      <div>
        <dt>{{ t('inspector.building.level') }}</dt>
        <dd class="u-numeric">{{ building.level }} / {{ buildingType?.maxLevel ?? '—' }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.building.condition') }}</dt>
        <dd class="u-numeric">{{ building.conditionPct }}%</dd>
      </div>
      <div>
        <dt>{{ t('inspector.building.fuel') }}</dt>
        <dd class="u-numeric">{{ formatQuantity(building.fuelStock) }}</dd>
      </div>
    </dl>

    <section>
      <h4 class="inspector-building__subtitle">{{ t('inspector.building.inventory') }}</h4>
      <p v-if="inventory.length === 0" class="inspector-building__empty">
        {{ t('inspector.building.inventory.empty') }}
      </p>
      <ul v-else class="inspector-building__list">
        <li v-for="item of inventory" :key="item.productId" class="inspector-building__row">
          <span>{{ productName(item.productId) }}</span>
          <span class="u-numeric">{{ formatQuantity(item.quantity) }}</span>
        </li>
      </ul>
    </section>

    <section>
      <h4 class="inspector-building__subtitle">{{ t('inspector.building.recipe') }}</h4>
      <p class="inspector-building__muted">
        {{ activeRecipe?.name ?? t('inspector.building.recipe.none') }}
      </p>
      <div class="inspector-building__controls">
        <select
          v-model="selectedRecipeId"
          class="inspector-building__select"
          data-testid="recipe-select"
          :disabled="!own || busy"
          :title="foreignTitle"
        >
          <option value="">{{ t('inspector.building.recipe.none') }}</option>
          <option v-for="recipe of recipes" :key="recipe.id" :value="recipe.id">
            {{ recipe.name }}
          </option>
        </select>
        <BaseButton
          variant="ghost"
          :disabled="!own"
          :loading="busy"
          :title="foreignTitle"
          data-testid="apply-recipe"
          @click="onApplyRecipe"
        >
          {{ t('inspector.building.recipe.apply') }}
        </BaseButton>
      </div>
    </section>

    <section>
      <h4 class="inspector-building__subtitle">{{ t('inspector.building.queue') }}</h4>
      <p v-if="queue.length === 0" class="inspector-building__empty">
        {{ t('inspector.building.queue.empty') }}
      </p>
      <ul v-else class="inspector-building__list">
        <li v-for="batch of queue" :key="batch.id" class="inspector-building__row">
          <span>{{ world.getRecipe(batch.recipeId)?.name ?? batch.recipeId }}</span>
          <span class="u-numeric">{{ batch.batchesDone }}/{{ batch.batchesQueued }}</span>
          <StatusBadge :presentation="batchStatusPresentation(batch.status)" />
          <span
            v-if="batchProgressPct(batch, world.getRecipe(batch.recipeId), simNow) !== null"
            class="u-numeric inspector-building__muted"
          >
            {{ batchProgressPct(batch, world.getRecipe(batch.recipeId), simNow) }}%
          </span>
        </li>
      </ul>
      <div class="inspector-building__controls">
        <input
          v-model="batchCount"
          class="inspector-building__count"
          type="text"
          inputmode="numeric"
          :disabled="!own || busy"
          :title="foreignTitle"
          :aria-label="t('inspector.building.queue.count')"
          data-testid="batch-count"
        />
        <BaseButton
          variant="ghost"
          :disabled="!own || selectedRecipeId === ''"
          :loading="busy"
          :title="foreignTitle"
          data-testid="queue-batches"
          @click="onQueueBatches"
        >
          {{ t('inspector.building.queue.add') }}
        </BaseButton>
      </div>
    </section>

    <section class="inspector-building__controls">
      <BaseButton
        :disabled="!own || !canUpgrade"
        :loading="busy"
        :title="foreignTitle"
        data-testid="upgrade-building"
        @click="onUpgrade"
      >
        {{ t('inspector.building.upgrade') }}
      </BaseButton>
      <span v-if="buildingType !== null" class="inspector-building__muted">
        {{ t('inspector.building.upgrade.cost', { cost: format(buildingType.buildCost) }) }}
      </span>
    </section>

    <BaseBanner v-if="errorText !== null" variant="error">{{ errorText }}</BaseBanner>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.inspector-building__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: s.$space-3;
}

.inspector-building__foreign {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.inspector-building__facts {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;

  div {
    display: flex;
    justify-content: space-between;
  }

  dt {
    color: var(--color-text-muted);
  }
}

.inspector-building__subtitle {
  margin-bottom: s.$space-2;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.inspector-building__list {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  list-style: none;
}

.inspector-building__row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: s.$space-3;
  font-size: s.$font-size-300;
}

.inspector-building__controls {
  display: flex;
  align-items: center;
  gap: s.$space-3;
  margin-top: s.$space-3;
}

.inspector-building__select,
.inspector-building__count {
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
  font-size: s.$font-size-300;
}

.inspector-building__count {
  width: 4.5rem;
}

.inspector-building__empty,
.inspector-building__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
