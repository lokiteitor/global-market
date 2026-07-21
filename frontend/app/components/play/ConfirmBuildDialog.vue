<script setup lang="ts">
/**
 * ConfirmBuildDialog — confirmación del flujo CONSTRUIR (mandato §4).
 *
 * Recibe el intent de emplazamiento del mundo vivo (punto en metros) y el
 * tipo elegido; propone un footprint CUADRADO de lado sqrt(footprint_cells)
 * × 100 m centrado en el punto, detecta la concesión PROPIA que contiene el
 * punto y muestra el coste de catálogo. El servidor es quien valida
 * (PLACEMENT_INVALID / INSUFFICIENT_FUNDS visibles con detalle). Sin
 * predicción optimista: se aplica la respuesta y el evento WS
 * building.created refresca por refetch (idempotente).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { polygonContainsPointM, rectRingM } from '~domain/geo'
import type { BuildIntent } from '~~/game'
import { mapBuilding } from '~network/mappers/domain.mapper'
import { AppError } from '~network/rest'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCadastreStore } from '~/stores/cadastre.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  intent: BuildIntent
}

const props = defineProps<Props>()
const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const world = useWorldStore()
const cadastre = useCadastreStore()
const buildings = useBuildingsStore()
const mapui = useMapUiStore()
const panels = usePanelsStore()
const { messageFor } = useAppError()

const buildingType = computed(() => world.getBuildingType(panels.buildTypeId))

/** Lado del footprint cuadrado: sqrt(celdas) × 100 m (mandato §4). */
const sideM = computed(() => {
  const cells = buildingType.value?.footprintCells ?? 1
  return Math.sqrt(cells) * 100
})

/** Concesión PROPIA que contiene el punto del intent (detección en cliente). */
const concession = computed(
  () =>
    cadastre.concessionList.find((candidate) =>
      polygonContainsPointM(candidate.parcelM, props.intent.xM, props.intent.yM),
    ) ?? null,
)

const submitting = ref(false)
const submitError = ref<unknown>(null)

const errorText = computed(() => {
  const error = submitError.value
  if (error === null) {
    return null
  }
  const base = messageFor(error)
  if (error instanceof AppError && error.details !== null) {
    const reason = error.details['reason']
    if (typeof reason === 'string') {
      return `${base} (${reason})`
    }
  }
  return base
})

async function onConfirm(): Promise<void> {
  const type = buildingType.value
  const owned = concession.value
  if (type === null || owned === null) {
    return
  }
  submitting.value = true
  submitError.value = null
  try {
    const half = sideM.value / 2
    const ring = rectRingM(props.intent.xM - half, props.intent.yM - half, sideM.value, sideM.value)
    const dto = await apis.world.createBuilding({
      building_type_id: type.id,
      concession_id: owned.id,
      footprint: { type: 'Polygon', coordinates: [ring.map(([x, y]) => [x, y])] },
    })
    buildings.applyBuilding(mapBuilding(dto))
    panels.setBuildType(null)
    mapui.setMode('select')
    emit('close')
  } catch (error) {
    submitError.value = error
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <GameDialog :title="t('build.confirm.title')" @close="emit('close')">
    <div class="o-stack">
      <dl class="confirm__facts">
        <div>
          <dt>{{ t('build.confirm.type') }}</dt>
          <dd>{{ buildingType?.name ?? '—' }}</dd>
        </div>
        <div>
          <dt>{{ t('build.confirm.cost') }}</dt>
          <dd class="u-numeric">
            {{ buildingType === null ? '—' : format(buildingType.buildCost) }}
          </dd>
        </div>
        <div>
          <dt>{{ t('build.confirm.side') }}</dt>
          <dd class="u-numeric">{{ Math.round(sideM) }} m</dd>
        </div>
        <div>
          <dt>{{ t('build.confirm.concession') }}</dt>
          <dd>
            {{
              concession === null
                ? t('build.confirm.noConcession')
                : (world.getRegion(concession.regionId)?.name ?? concession.id)
            }}
          </dd>
        </div>
      </dl>

      <BaseBanner v-if="concession === null" variant="warn">
        {{ t('build.confirm.noConcession.help') }}
      </BaseBanner>

      <BaseBanner v-if="errorText !== null" variant="error">{{ errorText }}</BaseBanner>

      <div class="confirm__actions">
        <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
        <BaseButton
          :disabled="buildingType === null || concession === null"
          :loading="submitting"
          data-testid="build-confirm"
          @click="onConfirm"
        >
          {{ t('build.confirm.submit') }}
        </BaseButton>
      </div>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.confirm__facts {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;

  div {
    display: flex;
    justify-content: space-between;
    gap: s.$space-3;
  }

  dt {
    color: var(--color-text-muted);
  }
}

.confirm__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}
</style>
