<script setup lang="ts">
/**
 * BuyVehicleDialog — compra de vehículo a precio de catálogo con entrega en
 * un nodo propio (mandato §3 FLOTA). La respuesta se aplica a la store; los
 * errores tipados (INSUFFICIENT_FUNDS…) se muestran tal cual.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { simTime } from '~shared/simtime'
import type { SimClock } from '~domain/simclock'
import { mapVehicle } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { LINK_MODE_LABEL } from '~/components/play/labels'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMyNodes } from '~/composables/useMyNodes'
import { useFleetStore } from '~/stores/fleet.store'

const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const fleet = useFleetStore()
const { myNodes, describeNode } = useMyNodes()
const { messageFor } = useAppError()

const vehicleTypeId = ref('')
const deliveryNodeId = ref('')
const submitting = ref(false)
const submitError = ref<unknown>(null)

const selectedType = computed(
  () => fleet.vehicleTypeList.find((type) => type.id === vehicleTypeId.value) ?? null,
)

async function onSubmit(): Promise<void> {
  if (vehicleTypeId.value === '' || deliveryNodeId.value === '') {
    return
  }
  submitting.value = true
  submitError.value = null
  try {
    const dto = await apis.fleet.purchaseVehicle({
      vehicle_type_id: vehicleTypeId.value,
      delivery_node_id: deliveryNodeId.value,
    })
    const nuxtApp = useNuxtApp() as { $simClock?: SimClock }
    fleet.applyVehicle(mapVehicle(dto, nuxtApp.$simClock?.now() ?? simTime(0)))
    emit('close')
  } catch (error) {
    submitError.value = error
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <GameDialog :title="t('fleet.buy.title')" @close="emit('close')">
    <div class="o-stack">
      <label class="buy__field">
        <span>{{ t('fleet.buy.type') }}</span>
        <select v-model="vehicleTypeId" class="buy__select">
          <option value="">{{ t('fleet.buy.type.placeholder') }}</option>
          <option v-for="type of fleet.vehicleTypeList" :key="type.id" :value="type.id">
            {{ type.name }} ({{ t(LINK_MODE_LABEL[type.mode]) }}) — {{ format(type.purchasePrice) }}
          </option>
        </select>
      </label>

      <p v-if="selectedType !== null" class="buy__muted">
        {{
          t('fleet.buy.specs', {
            capacity: selectedType.cargoCapacity,
            speed: selectedType.speedKmh,
            autonomy: selectedType.autonomyKm,
          })
        }}
      </p>

      <label class="buy__field">
        <span>{{ t('fleet.buy.node') }}</span>
        <select v-model="deliveryNodeId" class="buy__select">
          <option value="">{{ t('fleet.buy.node.placeholder') }}</option>
          <option v-for="node of myNodes" :key="node.id" :value="node.id">
            {{ describeNode(node) }}
          </option>
        </select>
      </label>

      <BaseBanner v-if="submitError !== null" variant="error">
        {{ messageFor(submitError) }}
      </BaseBanner>

      <div class="buy__actions">
        <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
        <BaseButton
          :disabled="vehicleTypeId === '' || deliveryNodeId === ''"
          :loading="submitting"
          @click="onSubmit"
        >
          {{ t('fleet.buy.submit') }}
        </BaseButton>
      </div>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.buy__field {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  font-size: s.$font-size-300;
}

.buy__select {
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.buy__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.buy__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}
</style>
