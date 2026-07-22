<script setup lang="ts">
/**
 * InspectorPanel — inspector contextual polimórfico (FAD §15.12).
 *
 * Refleja la selección espacial de mapui.store (escrita por el mundo vivo o
 * por listas de paneles) y despacha al detalle por tipo: edificio, vehículo,
 * ciudad; yacimiento y nodo se muestran inline (solo lectura — sin dueño no
 * hay mando posible, OwnershipPolicy).
 */

import { computed } from 'vue'
import { t } from '~shared/i18n'
import { formatQuantity } from '~domain/quantity'
import type { BuildingId } from '~domain/buildings'
import type { VehicleId } from '~domain/fleet'
import type { NodeId } from '~domain/logistics'
import type { CityId } from '~domain/world'
import { asEntityId } from '~shared/ids'
import BasePanel from '~/components/base/BasePanel.vue'
import InspectorBuilding from '~/components/play/InspectorBuilding.vue'
import InspectorCity from '~/components/play/InspectorCity.vue'
import InspectorTerminal from '~/components/play/InspectorTerminal.vue'
import InspectorVehicle from '~/components/play/InspectorVehicle.vue'
import { NODE_KIND_LABEL } from '~/components/play/labels'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useWorldStore } from '~/stores/world.store'

const mapui = useMapUiStore()
const world = useWorldStore()
const logistics = useLogisticsStore()

const selection = computed(() => mapui.selection)

const title = computed(() => {
  switch (selection.value?.type) {
    case 'building':
      return t('inspector.building.title')
    case 'vehicle':
      return t('inspector.vehicle.title')
    case 'city':
      return t('inspector.city.title')
    case 'deposit':
      return t('inspector.deposit.title')
    case 'node':
      return t('inspector.node.title')
    default:
      return t('inspector.title')
  }
})

const deposit = computed(() => {
  const current = selection.value
  return current?.type === 'deposit' ? world.getDeposit(asEntityId<'Deposit'>(current.id)) : null
})

const node = computed(() => {
  const current = selection.value
  return current?.type === 'node' ? logistics.getNode(asEntityId<'Node'>(current.id)) : null
})

function buildingId(id: string): BuildingId {
  return asEntityId<'Building'>(id)
}

function vehicleId(id: string): VehicleId {
  return asEntityId<'Vehicle'>(id)
}

function cityId(id: string): CityId {
  return asEntityId<'City'>(id)
}

function nodeId(id: string): NodeId {
  return asEntityId<'Node'>(id)
}
</script>

<template>
  <div v-if="selection !== null" class="inspector">
    <BasePanel :title="title">
      <template #actions>
        <button
          class="inspector__close"
          type="button"
          :aria-label="t('common.close')"
          @click="mapui.setSelection(null)"
        >
          ×
        </button>
      </template>

      <InspectorBuilding v-if="selection.type === 'building'" :building-id="buildingId(selection.id)" />
      <InspectorVehicle v-else-if="selection.type === 'vehicle'" :vehicle-id="vehicleId(selection.id)" />
      <InspectorCity v-else-if="selection.type === 'city'" :city-id="cityId(selection.id)" />

      <div v-else-if="selection.type === 'deposit'" class="o-stack">
        <template v-if="deposit !== null">
          <dl class="inspector__facts">
            <div>
              <dt>{{ t('inspector.deposit.product') }}</dt>
              <dd>{{ world.getProduct(deposit.productId)?.name ?? deposit.productId }}</dd>
            </div>
            <div>
              <dt>{{ t('inspector.deposit.remaining') }}</dt>
              <dd class="u-numeric">{{ formatQuantity(deposit.remainingAmount) }}</dd>
            </div>
            <div>
              <dt>{{ t('inspector.deposit.initial') }}</dt>
              <dd class="u-numeric">{{ formatQuantity(deposit.initialAmount) }}</dd>
            </div>
            <div>
              <dt>{{ t('inspector.deposit.renewable') }}</dt>
              <dd>{{ deposit.renewable ? t('common.yes') : t('common.no') }}</dd>
            </div>
          </dl>
          <p class="inspector__muted">{{ t('ownership.systemResource') }}</p>
        </template>
      </div>

      <div v-else-if="selection.type === 'node'" class="o-stack">
        <template v-if="node !== null">
          <dl class="inspector__facts">
            <div>
              <dt>{{ t('inspector.node.kind') }}</dt>
              <dd>{{ t(NODE_KIND_LABEL[node.kind]) }}</dd>
            </div>
            <div>
              <dt>{{ t('inspector.node.location') }}</dt>
              <dd class="u-numeric">
                {{ Math.round(node.locationM[0]) }}, {{ Math.round(node.locationM[1]) }} m
              </dd>
            </div>
            <div>
              <dt>{{ t('inspector.node.links') }}</dt>
              <dd class="u-numeric">{{ logistics.linksAtNode(nodeId(selection.id)).length }}</dd>
            </div>
          </dl>
          <p class="inspector__muted">{{ t('ownership.systemResource') }}</p>

          <!-- Nodo con terminal intermodal (v1.7.0): cola y slots de prioridad. -->
          <InspectorTerminal v-if="node.terminalId !== null" :node-id="nodeId(selection.id)" />
        </template>
      </div>
    </BasePanel>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.inspector {
  position: absolute;
  top: 3.25rem;
  right: s.$space-4;
  bottom: s.$space-5;
  z-index: s.$z-hud;
  width: 21rem;
  overflow-y: auto;
}

.inspector__close {
  border: 0;
  background: transparent;
  color: var(--color-text-muted);
  font-size: s.$font-size-600;
  line-height: 1;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    color: var(--color-text-strong);
  }
}

.inspector__facts {
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

.inspector__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
