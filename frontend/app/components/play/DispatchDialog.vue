<script setup lang="ts">
/**
 * DispatchDialog — flujo guiado de DESPACHO de un cargamento `in_warehouse`
 * (mandato §3 FLOTA): elegir vehículo `idle` en el nodo del cargamento →
 * planRoute(origen → destino del contrato) con legs/ETA informativos →
 * crear ruta `on_demand` con esos legs → POST dispatch en un paso.
 *
 * El servidor valida capacidad/autonomía de una vez; los 422 se muestran
 * tipados. Las respuestas se aplican a las stores (nunca estado inventado).
 */

import { computed, onMounted, ref } from 'vue'
import { t } from '~shared/i18n'
import { simTime } from '~shared/simtime'
import { formatQuantity } from '~domain/quantity'
import type { Shipment } from '~domain/fleet'
import type { NodeId } from '~domain/logistics'
import {
  mapContract,
  mapFreightContract,
  mapShipment,
  mapVehicle,
} from '~network/mappers/domain.mapper'
import type { SimClock } from '~domain/simclock'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { LINK_MODE_LABEL } from '~/components/play/labels'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { legEtaText, totalEtaText, useRoutePlanning } from '~/composables/useRoutePlanning'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  shipment: Shipment
}

const props = defineProps<Props>()
const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const fleet = useFleetStore()
const market = useMarketStore()
const world = useWorldStore()
const { messageFor } = useAppError()
const { plan, planning, planError, planBetween, createOnDemandRoute } = useRoutePlanning()

const vehicleId = ref('')
const dispatching = ref(false)
const actionError = ref<unknown>(null)
const anyError = computed(() => actionError.value ?? planError.value)

/** Vehículos propios `idle` estacionados en el nodo del cargamento. */
const candidateVehicles = computed(() =>
  fleet.idleVehicles.filter(
    (vehicle) =>
      vehicle.position.kind === 'at-node' && vehicle.position.nodeId === props.shipment.atNodeId,
  ),
)

const contract = computed(() => market.getContract(props.shipment.contractId))
const freight = computed(() => market.getFreightContract(props.shipment.freightContractId))
/** Destino: del contrato de bienes o, en un cargamento de flete, del CCRI-Flete. */
const destinationNodeId = computed<NodeId | null>(
  () => contract.value?.destinationNodeId ?? freight.value?.destinationNodeId ?? null,
)

onMounted(() => {
  // El contrato/flete del cargamento puede no estar replicado aún: pull puntual.
  const contractId = props.shipment.contractId
  if (contractId !== null && contract.value === null) {
    apis.market
      .getContract(contractId)
      .then((dto) => {
        market.applyContract(mapContract(dto))
      })
      .catch((error: unknown) => {
        actionError.value = error
      })
  }
  const freightContractId = props.shipment.freightContractId
  if (freightContractId !== null && freight.value === null) {
    apis.market
      .getFreightContract(freightContractId)
      .then((dto) => {
        market.applyFreightContract(mapFreightContract(dto))
      })
      .catch((error: unknown) => {
        actionError.value = error
      })
  }
})

function vehicleLabel(vehicle: (typeof candidateVehicles.value)[number]): string {
  const type = fleet.getVehicleType(vehicle.vehicleTypeId)
  return `${type?.name ?? t('inspector.vehicle.title')} · ${t('fleet.dispatch.fuel', { fuel: formatQuantity(vehicle.fuel) })}`
}

async function onPlan(): Promise<void> {
  const origin = props.shipment.atNodeId
  const destination = destinationNodeId.value
  if (origin === null || destination === null) {
    return
  }
  actionError.value = null
  await planBetween(origin, destination, { cargoVolume: props.shipment.quantity })
}

async function onDispatch(): Promise<void> {
  if (plan.value === null || vehicleId.value === '') {
    return
  }
  dispatching.value = true
  actionError.value = null
  try {
    const routeDto = await createOnDemandRoute(t('fleet.dispatch.routeName'))
    const shipmentDto = await apis.fleet.dispatchShipment(
      props.shipment.id,
      vehicleId.value,
      routeDto.id,
    )
    fleet.applyShipment(mapShipment(shipmentDto))
    // El vehículo cambió de estado (loading/in_transit): re-pull puntual.
    const nuxtApp = useNuxtApp() as { $simClock?: SimClock }
    const observedAt = nuxtApp.$simClock?.now() ?? simTime(0)
    fleet.applyVehicle(mapVehicle(await apis.fleet.getVehicle(vehicleId.value), observedAt))
    emit('close')
  } catch (error) {
    actionError.value = error
  } finally {
    dispatching.value = false
  }
}

const productName = computed(
  () => world.getProduct(props.shipment.productId)?.name ?? props.shipment.productId,
)

/** ETA total del plan como duración sim legible (días/horas de juego). */
const etaText = computed<string | null>(() =>
  plan.value === null ? null : totalEtaText(plan.value),
)
</script>

<template>
  <GameDialog :title="t('fleet.dispatch.title')" @close="emit('close')">
    <div class="o-stack">
      <p class="dispatch__summary">
        {{ productName }} · {{ formatQuantity(props.shipment.quantity) }}
      </p>

      <BaseBanner v-if="destinationNodeId === null" variant="warn">
        {{ t('fleet.dispatch.noContract') }}
      </BaseBanner>

      <template v-else>
        <label class="dispatch__field">
          <span>{{ t('fleet.dispatch.vehicle') }}</span>
          <select v-model="vehicleId" class="dispatch__select" data-testid="dispatch-vehicle">
            <option value="">{{ t('fleet.dispatch.vehicle.placeholder') }}</option>
            <option v-for="vehicle of candidateVehicles" :key="vehicle.id" :value="vehicle.id">
              {{ vehicleLabel(vehicle) }}
            </option>
          </select>
        </label>
        <p v-if="candidateVehicles.length === 0" class="dispatch__muted">
          {{ t('fleet.dispatch.noVehicles') }}
        </p>

        <div class="dispatch__actions">
          <BaseButton
            variant="ghost"
            :loading="planning"
            :disabled="vehicleId === ''"
            data-testid="dispatch-plan"
            @click="onPlan"
          >
            {{ t('fleet.dispatch.plan') }}
          </BaseButton>
        </div>

        <section v-if="plan !== null" class="dispatch__plan">
          <h4 class="dispatch__subtitle">{{ t('fleet.dispatch.plan.title') }}</h4>
          <ol class="dispatch__legs">
            <li v-for="leg of plan.legs" :key="leg.seq" class="u-numeric">
              {{ t(LINK_MODE_LABEL[leg.mode]) }} · {{ legEtaText(leg.eta_sim_seconds) }}
            </li>
          </ol>
          <p v-if="etaText !== null" class="dispatch__muted">
            {{ t('fleet.dispatch.eta', { eta: etaText }) }}
          </p>
        </section>

        <BaseBanner v-if="anyError !== null" variant="error">
          {{ messageFor(anyError) }}
        </BaseBanner>

        <div class="dispatch__actions">
          <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
          <BaseButton
            :disabled="plan === null || vehicleId === ''"
            :loading="dispatching"
            data-testid="dispatch-submit"
            @click="onDispatch"
          >
            {{ t('fleet.dispatch.submit') }}
          </BaseButton>
        </div>
      </template>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.dispatch__summary {
  color: var(--color-text-strong);
}

.dispatch__field {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  font-size: s.$font-size-300;
}

.dispatch__select {
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.dispatch__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}

.dispatch__subtitle {
  margin-bottom: s.$space-2;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.dispatch__legs {
  display: flex;
  flex-direction: column;
  gap: s.$space-1;
  padding-left: s.$space-5;
  font-size: s.$font-size-300;
}

.dispatch__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
