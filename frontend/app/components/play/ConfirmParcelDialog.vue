<script setup lang="ts">
/**
 * ConfirmParcelDialog — confirmación de solicitud de CONCESIÓN (mandato §3).
 *
 * Recibe el rectángulo del modo `parcel` del mapa, detecta la región que lo
 * contiene (por bounds; con una única región, ella) y muestra el CANON BASE
 * de la región como estimación — el canon real lo fija el servidor al
 * conceder. Confirmar envía el rectángulo como polígono GeoJSON-like.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { polygonContainsPointM, rectRingM } from '~domain/geo'
import type { Region } from '~domain/world'
import type { ParcelIntent } from '~~/game'
import { mapConcession } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useCadastreStore } from '~/stores/cadastre.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  intent: ParcelIntent
}

const props = defineProps<Props>()
const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const world = useWorldStore()
const cadastre = useCadastreStore()
const mapui = useMapUiStore()
const { messageFor } = useAppError()

/** Región que contiene el centro del rectángulo (bounds; única región = ella). */
const region = computed<Region | null>(() => {
  const rect = props.intent.rectM
  const centerX = rect.xM + rect.widthM / 2
  const centerY = rect.yM + rect.heightM / 2
  for (const candidate of world.regionList) {
    if (candidate.boundsM !== null && polygonContainsPointM(candidate.boundsM, centerX, centerY)) {
      return candidate
    }
  }
  return world.regionList.length === 1 ? (world.regionList[0] ?? null) : null
})

const areaText = computed(() => {
  const rect = props.intent.rectM
  return `${String(Math.round(rect.widthM))} × ${String(Math.round(rect.heightM))} m`
})

const submitting = ref(false)
const submitError = ref<unknown>(null)

async function onConfirm(): Promise<void> {
  const target = region.value
  if (target === null) {
    return
  }
  submitting.value = true
  submitError.value = null
  try {
    const rect = props.intent.rectM
    const ring = rectRingM(rect.xM, rect.yM, rect.widthM, rect.heightM)
    const dto = await apis.world.createConcession({
      region_id: target.id,
      parcel: { type: 'Polygon', coordinates: [ring.map(([x, y]) => [x, y])] },
    })
    cadastre.applyConcession(mapConcession(dto))
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
  <GameDialog :title="t('parcel.confirm.title')" @close="emit('close')">
    <div class="o-stack">
      <dl class="parcel__facts">
        <div>
          <dt>{{ t('parcel.confirm.region') }}</dt>
          <dd>{{ region?.name ?? t('parcel.confirm.noRegion') }}</dd>
        </div>
        <div>
          <dt>{{ t('parcel.confirm.area') }}</dt>
          <dd class="u-numeric">{{ areaText }}</dd>
        </div>
        <div>
          <dt>{{ t('parcel.confirm.canon') }}</dt>
          <dd class="u-numeric">{{ region === null ? '—' : format(region.canonBase) }}</dd>
        </div>
      </dl>

      <p class="parcel__note">{{ t('parcel.confirm.note') }}</p>

      <BaseBanner v-if="submitError !== null" variant="error">
        {{ messageFor(submitError) }}
      </BaseBanner>

      <div class="parcel__actions">
        <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
        <BaseButton
          :disabled="region === null"
          :loading="submitting"
          data-testid="parcel-confirm"
          @click="onConfirm"
        >
          {{ t('parcel.confirm.submit') }}
        </BaseButton>
      </div>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.parcel__facts {
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

.parcel__note {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.parcel__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}
</style>
