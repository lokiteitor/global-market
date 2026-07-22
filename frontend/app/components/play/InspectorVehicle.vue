<script setup lang="ts">
/**
 * InspectorVehicle — detalle de un vehículo (FAD §15.12).
 *
 * Estado, combustible, desgaste, ruta asignada y posición observada (nodo o
 * segmento con progreso). El seguimiento de cámara escribe en mapui.store.
 * Un vehículo propio `idle` en nodo ofrece REPOSICIONAR en vacío (v1.5.0).
 * Un vehículo ajeno (o `sealed`) es solo lectura con nota (OwnershipPolicy).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { formatQuantity } from '~domain/quantity'
import type { VehicleId } from '~domain/fleet'
import { isCommandable, isVehicleCommandable } from '~domain/ownership'
import { vehicleStatusPresentation } from '~domain/status'
import BaseButton from '~/components/base/BaseButton.vue'
import { NODE_KIND_LABEL } from '~/components/play/labels'
import RepositionDialog from '~/components/play/RepositionDialog.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useSessionStore } from '~/stores/session.store'

interface Props {
  vehicleId: VehicleId
}

const props = defineProps<Props>()

const fleet = useFleetStore()
const logistics = useLogisticsStore()
const session = useSessionStore()
const mapui = useMapUiStore()

const vehicle = computed(() => fleet.getVehicle(props.vehicleId))
const vehicleType = computed(() => fleet.getVehicleType(vehicle.value?.vehicleTypeId ?? null))
const own = computed(() => {
  const current = vehicle.value
  return current !== null && isCommandable(current, session.account?.id ?? null)
})
const route = computed(() => logistics.getRoute(vehicle.value?.routeId ?? null))

const positionText = computed(() => {
  const current = vehicle.value
  if (current === null) {
    return ''
  }
  if (current.position.kind === 'at-node') {
    const node = logistics.getNode(current.position.nodeId)
    return t('inspector.vehicle.position.atNode', {
      node: node === null ? current.position.nodeId : t(NODE_KIND_LABEL[node.kind]),
    })
  }
  return t('inspector.vehicle.position.onSegment', {
    pct: Math.round(current.position.progressPct),
  })
})

const following = computed(() => mapui.followedVehicleId === props.vehicleId)

function onFollow(): void {
  mapui.setFollow(following.value ? null : props.vehicleId)
}

// ── Reposicionamiento en vacío (solo propio, no sellado) ─────────────────────
const repositioning = ref(false)

const commandable = computed(() => {
  const current = vehicle.value
  return current !== null && isVehicleCommandable(current, session.account?.id ?? null)
})

/** El servidor exige idle + detenido en nodo; deshabilitado con tooltip si no. */
const repositionReady = computed(() => {
  const current = vehicle.value
  return (
    commandable.value &&
    current !== null &&
    current.status === 'idle' &&
    current.position.kind === 'at-node'
  )
})
</script>

<template>
  <div v-if="vehicle !== null" class="inspector-vehicle o-stack">
    <header class="inspector-vehicle__head">
      <strong>{{ vehicleType?.name ?? t('inspector.vehicle.title') }}</strong>
      <StatusBadge :presentation="vehicleStatusPresentation(vehicle.status)" />
    </header>

    <p v-if="!own" class="inspector-vehicle__foreign">{{ t('ownership.foreign') }}</p>

    <dl class="inspector-vehicle__facts">
      <div>
        <dt>{{ t('inspector.vehicle.fuel') }}</dt>
        <dd class="u-numeric">{{ formatQuantity(vehicle.fuel) }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.vehicle.wear') }}</dt>
        <dd class="u-numeric">{{ vehicle.wearPct }}%</dd>
      </div>
      <div>
        <dt>{{ t('inspector.vehicle.route') }}</dt>
        <dd>{{ route?.name ?? t('inspector.vehicle.route.none') }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.vehicle.position') }}</dt>
        <dd>{{ positionText }}</dd>
      </div>
    </dl>

    <BaseButton variant="ghost" @click="onFollow">
      {{ following ? t('inspector.vehicle.unfollow') : t('inspector.vehicle.follow') }}
    </BaseButton>

    <BaseButton
      v-if="own"
      variant="ghost"
      :disabled="!repositionReady"
      :title="repositionReady ? undefined : t('fleet.reposition.notIdle')"
      data-testid="vehicle-reposition"
      @click="repositioning = true"
    >
      {{ t('fleet.reposition.open') }}
    </BaseButton>

    <RepositionDialog
      v-if="repositioning"
      :vehicle-id="props.vehicleId"
      @close="repositioning = false"
    />
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.inspector-vehicle__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: s.$space-3;
}

.inspector-vehicle__foreign {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.inspector-vehicle__facts {
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
</style>
