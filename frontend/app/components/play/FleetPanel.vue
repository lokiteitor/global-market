<script setup lang="ts">
/**
 * FleetPanel — flota y cargamentos propios (mandato §3 FLOTA).
 *
 * Vehículos (estado, posición, combustible; seguir/inspeccionar; los `idle`
 * ofrecen REPOSICIONAR en vacío, contrato v1.5.0) y cargamentos: el flujo
 * DESPACHAR se ofrece según la regla del servidor (canDispatchShipment) — un
 * cargamento de bienes lo despacha su dueño; uno de flete, el TRANSPORTISTA
 * (el cargador lo ve etiquetado, sin botón). Compra por BuyVehicleDialog.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { formatQuantity } from '~domain/quantity'
import type { Shipment, Vehicle } from '~domain/fleet'
import { canDispatchShipment, isVehicleCommandable } from '~domain/ownership'
import { shipmentStatusPresentation, vehicleStatusPresentation } from '~domain/status'
import BaseButton from '~/components/base/BaseButton.vue'
import BuyVehicleDialog from '~/components/play/BuyVehicleDialog.vue'
import DispatchDialog from '~/components/play/DispatchDialog.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import { NODE_KIND_LABEL } from '~/components/play/labels'
import RepositionDialog from '~/components/play/RepositionDialog.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useMarketStore } from '~/stores/market.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const fleet = useFleetStore()
const logistics = useLogisticsStore()
const world = useWorldStore()
const market = useMarketStore()
const session = useSessionStore()
const mapui = useMapUiStore()
const panels = usePanelsStore()

const buying = ref(false)
const dispatching = ref<Shipment | null>(null)
const repositioning = ref<Vehicle | null>(null)

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

/**
 * Regla de despacho del servidor: cargamento parado (in_warehouse o
 * at_terminal) ligado a un contrato, y comandado por quien corresponde
 * (dueño en bienes; TRANSPORTISTA en fletes).
 */
function canDispatch(shipment: Shipment): boolean {
  if (shipment.status !== 'in_warehouse' && shipment.status !== 'at_terminal') {
    return false
  }
  if (shipment.contractId === null && shipment.freightContractId === null) {
    return false
  }
  return canDispatchShipment(
    shipment,
    market.getFreightContract(shipment.freightContractId),
    session.account?.id ?? null,
  )
}

/** Cargamento de flete que veo pero NO despacho (soy el cargador). */
function isCarrierOnly(shipment: Shipment): boolean {
  return (
    shipment.freightContractId !== null &&
    (shipment.status === 'in_warehouse' || shipment.status === 'at_terminal') &&
    !canDispatch(shipment)
  )
}

function canReposition(vehicle: Vehicle): boolean {
  return (
    isVehicleCommandable(vehicle, session.account?.id ?? null) &&
    vehicle.status === 'idle' &&
    vehicle.position.kind === 'at-node'
  )
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
              <td class="fleet__row-actions">
                <BaseButton variant="ghost" @click="onInspect(vehicle)">
                  {{ t('industry.inspect') }}
                </BaseButton>
                <BaseButton
                  v-if="canReposition(vehicle)"
                  variant="ghost"
                  data-testid="vehicle-reposition"
                  @click="repositioning = vehicle"
                >
                  {{ t('fleet.reposition.open') }}
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
              <td>
                {{ world.getProduct(shipment.productId)?.name ?? shipment.productId }}
                <span
                  v-if="shipment.freightContractId !== null"
                  class="fleet__freight-tag"
                  data-testid="shipment-freight-tag"
                >
                  {{ t('market.kind.freight') }}
                </span>
              </td>
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
                <span
                  v-else-if="isCarrierOnly(shipment)"
                  class="fleet__empty"
                  :title="t('fleet.dispatch.carrierOnly')"
                >
                  {{ t('fleet.dispatch.carrierOnly.short') }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </section>
    </div>

    <BuyVehicleDialog v-if="buying" @close="buying = false" />
    <DispatchDialog v-if="dispatching !== null" :shipment="dispatching" @close="dispatching = null" />
    <RepositionDialog
      v-if="repositioning !== null"
      :vehicle-id="repositioning.id"
      @close="repositioning = null"
    />
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

.fleet__row-actions {
  display: flex;
  gap: s.$space-2;
}

.fleet__freight-tag {
  margin-left: s.$space-2;
  padding: 0 s.$space-2;
  border: 1px solid var(--color-border);
  border-radius: 0.25rem;
  color: var(--color-text-muted);
  font-size: s.$font-size-100;
  text-transform: uppercase;
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
