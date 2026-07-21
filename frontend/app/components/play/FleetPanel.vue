<script setup lang="ts">
/**
 * FleetPanel — flota y cargamentos propios (mandato §3 FLOTA).
 *
 * Vehículos (estado, posición, combustible; seguir/inspeccionar) y
 * cargamentos (para `in_warehouse` con contrato: flujo DESPACHAR guiado).
 * Compra de vehículo por catálogo (BuyVehicleDialog).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { formatQuantity } from '~domain/quantity'
import type { Shipment, Vehicle } from '~domain/fleet'
import { shipmentStatusPresentation, vehicleStatusPresentation } from '~domain/status'
import BaseButton from '~/components/base/BaseButton.vue'
import BuyVehicleDialog from '~/components/play/BuyVehicleDialog.vue'
import DispatchDialog from '~/components/play/DispatchDialog.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import { NODE_KIND_LABEL } from '~/components/play/labels'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const fleet = useFleetStore()
const logistics = useLogisticsStore()
const world = useWorldStore()
const mapui = useMapUiStore()
const panels = usePanelsStore()

const buying = ref(false)
const dispatching = ref<Shipment | null>(null)

function vehiclePositionText(vehicle: Vehicle): string {
  if (vehicle.position.kind === 'at-node') {
    const node = logistics.getNode(vehicle.position.nodeId)
    return t('inspector.vehicle.position.atNode', {
      node: node === null ? vehicle.position.nodeId : t(NODE_KIND_LABEL[node.kind]),
    })
  }
  return t('inspector.vehicle.position.onSegment', {
    pct: Math.round(vehicle.position.progressPct),
  })
}

function onInspect(vehicle: Vehicle): void {
  mapui.setSelection({ type: 'vehicle', id: vehicle.id })
}

function canDispatch(shipment: Shipment): boolean {
  return shipment.status === 'in_warehouse' && shipment.contractId !== null
}

const shipments = computed(() => fleet.shipmentList)
</script>

<template>
  <FloatingPanel :title="t('panel.fleet')" @close="panels.close()">
    <div class="o-stack">
      <section>
        <header class="fleet__section-head">
          <h4 class="fleet__subtitle">{{ t('fleet.vehicles.title') }}</h4>
          <BaseButton variant="ghost" data-testid="fleet-buy" @click="buying = true">
            {{ t('fleet.buy.open') }}
          </BaseButton>
        </header>

        <p v-if="fleet.vehicleList.length === 0" class="fleet__empty">
          {{ t('fleet.vehicles.empty') }}
        </p>
        <table v-else class="fleet__table">
          <thead>
            <tr>
              <th>{{ t('fleet.col.type') }}</th>
              <th>{{ t('fleet.col.status') }}</th>
              <th>{{ t('fleet.col.position') }}</th>
              <th>{{ t('fleet.col.fuel') }}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <tr v-for="vehicle of fleet.vehicleList" :key="vehicle.id">
              <td>{{ fleet.getVehicleType(vehicle.vehicleTypeId)?.name ?? '—' }}</td>
              <td><StatusBadge :presentation="vehicleStatusPresentation(vehicle.status)" /></td>
              <td>{{ vehiclePositionText(vehicle) }}</td>
              <td class="u-numeric">{{ formatQuantity(vehicle.fuel) }}</td>
              <td>
                <BaseButton variant="ghost" @click="onInspect(vehicle)">
                  {{ t('industry.inspect') }}
                </BaseButton>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section>
        <h4 class="fleet__subtitle">{{ t('fleet.shipments.title') }}</h4>
        <p v-if="shipments.length === 0" class="fleet__empty">{{ t('fleet.shipments.empty') }}</p>
        <table v-else class="fleet__table">
          <thead>
            <tr>
              <th>{{ t('market.col.product') }}</th>
              <th>{{ t('fleet.col.quantity') }}</th>
              <th>{{ t('fleet.col.status') }}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <tr v-for="shipment of shipments" :key="shipment.id" data-testid="shipment-row">
              <td>{{ world.getProduct(shipment.productId)?.name ?? shipment.productId }}</td>
              <td class="u-numeric">{{ formatQuantity(shipment.quantity) }}</td>
              <td><StatusBadge :presentation="shipmentStatusPresentation(shipment.status)" /></td>
              <td>
                <BaseButton
                  v-if="canDispatch(shipment)"
                  data-testid="shipment-dispatch"
                  @click="dispatching = shipment"
                >
                  {{ t('fleet.dispatch.open') }}
                </BaseButton>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </div>

    <BuyVehicleDialog v-if="buying" @close="buying = false" />
    <DispatchDialog v-if="dispatching !== null" :shipment="dispatching" @close="dispatching = null" />
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.fleet__section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: s.$space-3;
}

.fleet__subtitle {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.fleet__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.fleet__table {
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
</style>
