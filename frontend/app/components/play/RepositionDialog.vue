<script setup lang="ts">
/**
 * RepositionDialog — VIAJE EN VACÍO (deadhead, contrato v1.5.0).
 *
 * Un vehículo que entrega queda `idle` en el destino, donde rara vez nace
 * carga nueva: sin reposicionarlo quedaría varado. Flujo guiado: elegir nodo
 * destino → plan de ruta RESTRINGIDO al modo del vehículo (la ruta debe ser
 * íntegramente de su modo) → crear ruta on_demand → POST reposition. El
 * servidor valida idle/propiedad/combustible (403 VEHICLE_SEALED,
 * 409 VEHICLE_NOT_IDLE, 422) y la respuesta se aplica a la store.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { simTime } from '~shared/simtime'
import type { VehicleId } from '~domain/fleet'
import type { NetworkNode } from '~domain/logistics'
import type { SimClock } from '~domain/simclock'
import { mapVehicle } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { LINK_MODE_LABEL } from '~/components/play/labels'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMyNodes } from '~/composables/useMyNodes'
import { legEtaText, totalEtaText, useRoutePlanning } from '~/composables/useRoutePlanning'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'

interface Props {
  vehicleId: VehicleId
}

const props = defineProps<Props>()
const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const fleet = useFleetStore()
const logistics = useLogisticsStore()
const { myNodes, describeAnyNode } = useMyNodes()
const { messageFor } = useAppError()
const { plan, planning, planError, planBetween, createOnDemandRoute, reset } = useRoutePlanning()

const destinationNodeId = ref('')
const submitting = ref(false)
const actionError = ref<unknown>(null)

const vehicle = computed(() => fleet.getVehicle(props.vehicleId))
const vehicleType = computed(() => fleet.getVehicleType(vehicle.value?.vehicleTypeId ?? null))

/** Guard espejo del servidor: solo un vehículo `idle` detenido en nodo. */
const currentNodeId = computed(() => {
  const current = vehicle.value
  return current !== null && current.status === 'idle' && current.position.kind === 'at-node'
    ? current.position.nodeId
    : null
})

/** Destinos: todos los nodos menos el actual, los PROPIOS primero. */
const destinations = computed<readonly NetworkNode[]>(() => {
  const origin = currentNodeId.value
  const ownIds = new Set(myNodes.value.map((node) => node.id))
  return logistics.nodeList
    .filter((node) => node.id !== origin)
    .toSorted((a, b) => Number(ownIds.has(b.id)) - Number(ownIds.has(a.id)))
})

async function onPlan(): Promise<void> {
  const origin = currentNodeId.value
  const destination = destinations.value.find((node) => node.id === destinationNodeId.value)
  const mode = vehicleType.value?.mode
  if (origin === null || destination === undefined || mode === undefined) {
    return
  }
  actionError.value = null
  await planBetween(origin, destination.id, { modes: [mode] })
}

async function onReposition(): Promise<void> {
  if (plan.value === null) {
    return
  }
  submitting.value = true
  actionError.value = null
  try {
    const routeDto = await createOnDemandRoute(t('fleet.reposition.routeName'))
    const vehicleDto = await apis.fleet.repositionVehicle(props.vehicleId, routeDto.id)
    const nuxtApp = useNuxtApp() as { $simClock?: SimClock }
    const observedAt = nuxtApp.$simClock?.now() ?? simTime(0)
    fleet.applyVehicle(mapVehicle(vehicleDto, observedAt))
    emit('close')
  } catch (error) {
    actionError.value = error
  } finally {
    submitting.value = false
  }
}

function onDestinationChange(): void {
  // Un destino nuevo invalida el plan anterior (habría que replanificar).
  reset()
}
</script>

<template>
  <GameDialog :title="t('fleet.reposition.title')" @close="emit('close')">
    <div class="o-stack">
      <BaseBanner v-if="currentNodeId === null" variant="warn" data-testid="reposition-not-idle">
        {{ t('fleet.reposition.notIdle') }}
      </BaseBanner>

      <template v-else>
        <p class="reposition__muted">{{ t('fleet.reposition.hint') }}</p>

        <label class="reposition__field">
          <span>{{ t('fleet.reposition.destination') }}</span>
          <select
            v-model="destinationNodeId"
            class="reposition__select"
            data-testid="reposition-destination"
            @change="onDestinationChange"
          >
            <option value="">{{ t('fleet.reposition.destination.placeholder') }}</option>
            <option v-for="node of destinations" :key="node.id" :value="node.id">
              {{ describeAnyNode(node) }}
            </option>
          </select>
        </label>

        <div class="reposition__actions">
          <BaseButton
            variant="ghost"
            :loading="planning"
            :disabled="destinationNodeId === ''"
            data-testid="reposition-plan"
            @click="onPlan"
          >
            {{ t('fleet.dispatch.plan') }}
          </BaseButton>
        </div>

        <section v-if="plan !== null" class="reposition__plan">
          <ol class="reposition__legs">
            <li v-for="leg of plan.legs" :key="leg.seq" class="u-numeric">
              {{ t(LINK_MODE_LABEL[leg.mode]) }} · {{ legEtaText(leg.eta_sim_seconds) }}
            </li>
          </ol>
          <p class="reposition__muted">
            {{ t('fleet.dispatch.eta', { eta: totalEtaText(plan) }) }}
          </p>
        </section>

        <BaseBanner v-if="planError !== null" variant="error">
          {{ messageFor(planError) }}
        </BaseBanner>
        <BaseBanner v-if="actionError !== null" variant="error">
          {{ messageFor(actionError) }}
        </BaseBanner>

        <div class="reposition__actions">
          <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
          <BaseButton
            :disabled="plan === null"
            :loading="submitting"
            data-testid="reposition-submit"
            @click="onReposition"
          >
            {{ t('fleet.reposition.submit') }}
          </BaseButton>
        </div>
      </template>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.reposition__field {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  font-size: s.$font-size-300;
}

.reposition__select {
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.reposition__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}

.reposition__legs {
  display: flex;
  flex-direction: column;
  gap: s.$space-1;
  padding-left: s.$space-5;
  font-size: s.$font-size-300;
}

.reposition__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
