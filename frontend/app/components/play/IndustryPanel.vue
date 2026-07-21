<script setup lang="ts">
/**
 * IndustryPanel — mis edificios con estado y cola (FAD §15.10, mandato §3).
 *
 * El progreso del lote en curso se DERIVA con el SimClock (startedAtSim +
 * batch_sim_seconds de la receta). Clic en una fila = selección espacial
 * (abre el inspector y resalta en el mapa vía mapui.store).
 */

import { computed } from 'vue'
import { t } from '~shared/i18n'
import type { Building } from '~domain/buildings'
import { buildingStatusPresentation } from '~domain/status'
import StatusBadge from '~/components/play/StatusBadge.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import { batchProgressPct } from '~/composables/useBatchProgress'
import { useSimNow } from '~/composables/useSimNow'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const buildings = useBuildingsStore()
const world = useWorldStore()
const mapui = useMapUiStore()
const panels = usePanelsStore()
const simNow = useSimNow()

const rows = computed(() =>
  buildings.buildingList.map((building) => {
    const running = buildings.runningBatchOfBuilding(building.id)
    const recipe = running === null ? null : world.getRecipe(running.recipeId)
    return {
      building,
      typeName: world.getBuildingType(building.buildingTypeId)?.name ?? '—',
      recipeName: world.getRecipe(building.activeRecipeId)?.name ?? null,
      queued: buildings.batchesOfBuilding(building.id).filter((b) => b.status === 'queued').length,
      progress: running === null ? null : batchProgressPct(running, recipe, simNow.value),
    }
  }),
)

function onInspect(building: Building): void {
  mapui.setSelection({ type: 'building', id: building.id })
}
</script>

<template>
  <FloatingPanel :title="t('panel.industry')" @close="panels.close()">
    <p v-if="rows.length === 0" class="industry__empty">{{ t('industry.empty') }}</p>

    <table v-else class="industry__table">
      <thead>
        <tr>
          <th>{{ t('industry.col.type') }}</th>
          <th>{{ t('industry.col.status') }}</th>
          <th>{{ t('industry.col.recipe') }}</th>
          <th>{{ t('industry.col.queue') }}</th>
          <th>{{ t('industry.col.progress') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="row of rows" :key="row.building.id">
          <td>{{ row.typeName }}</td>
          <td><StatusBadge :presentation="buildingStatusPresentation(row.building.status)" /></td>
          <td>{{ row.recipeName ?? t('inspector.building.recipe.none') }}</td>
          <td class="u-numeric">{{ row.queued }}</td>
          <td>
            <div v-if="row.progress !== null" class="industry__progress">
              <div class="industry__progress-bar" :style="{ width: `${row.progress}%` }" />
              <span class="u-numeric">{{ row.progress }}%</span>
            </div>
            <span v-else class="industry__muted">—</span>
          </td>
          <td>
            <button type="button" class="industry__link" @click="onInspect(row.building)">
              {{ t('industry.inspect') }}
            </button>
          </td>
        </tr>
      </tbody>
    </table>
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.industry__empty,
.industry__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.industry__table {
  width: 100%;
  border-collapse: collapse;
  font-size: s.$font-size-300;

  th {
    color: var(--color-text-muted);
    font-weight: s.$font-weight-medium;
    text-align: left;
  }

  th,
  td {
    padding: s.$space-2 s.$space-3;
    border-bottom: 1px solid var(--color-border);
  }
}

.industry__progress {
  position: relative;
  display: flex;
  align-items: center;
  gap: s.$space-2;
  min-width: 6rem;
}

.industry__progress-bar {
  height: 0.375rem;
  max-width: 4rem;
  background-color: var(--color-accent);
  border-radius: 999px;
}

.industry__link {
  border: 0;
  background: transparent;
  color: var(--color-link);
  font-size: s.$font-size-300;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    text-decoration: underline;
  }
}
</style>
