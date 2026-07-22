<script setup lang="ts">
/**
 * MyFreights — contratos de flete propios (CCRI-Flete, GDD §5.3.2).
 *
 * Rol (cargador/transportista), contraparte, trayecto, carga física (vía el
 * Shipment ligado — el schema FreightContract no lleva producto ni cantidad),
 * flete total y valor declarado, fill, estado y deadline con cuenta atrás.
 * Replicado (bootstrap ambos roles + eventos WS freight./acceptance./
 * shipment.); el refresco manual re-consulta ambos roles.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatQuantity } from '~domain/quantity'
import type { Shipment } from '~domain/fleet'
import type { FreightContract } from '~domain/market'
import { isMine } from '~domain/ownership'
import { contractStatusPresentation, shipmentStatusPresentation } from '~domain/status'
import { mapFreightContract } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import SimTimeCell from '~/components/play/SimTimeCell.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMyNodes } from '~/composables/useMyNodes'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const market = useMarketStore()
const fleet = useFleetStore()
const logistics = useLogisticsStore()
const world = useWorldStore()
const session = useSessionStore()
const { describeAnyNode } = useMyNodes()
const { messageFor } = useAppError()

type RoleFilter = 'all' | 'shipper' | 'carrier'
const roleFilter = ref<RoleFilter>('all')

const refreshing = ref(false)
const fetchError = ref<unknown>(null)

const myAccountId = computed(() => session.account?.id ?? null)

function isShipper(freight: FreightContract): boolean {
  return isMine(freight.shipperAccountId, myAccountId.value)
}

const freights = computed(() => {
  const sorted = [...market.freightList].toSorted((a, b) => b.confirmedAtSim - a.confirmedAtSim)
  switch (roleFilter.value) {
    case 'shipper':
      return sorted.filter((f) => isShipper(f))
    case 'carrier':
      return sorted.filter((f) => !isShipper(f))
    default:
      return sorted
  }
})

/** Filas con su carga física resuelta (evita dobles lookups en la plantilla). */
const rows = computed(() =>
  freights.value.map((freight) => ({
    freight,
    cargo: fleet.shipmentsForFreight(freight.id)[0] ?? null,
  })),
)

function roleText(freight: FreightContract): string {
  return isShipper(freight)
    ? t('market.freight.role.shipper')
    : t('market.freight.role.carrier')
}

function counterpartyId(freight: FreightContract): string {
  return isShipper(freight) ? freight.carrierAccountId : freight.shipperAccountId
}

/** UUID abreviado para la celda (el completo va en el title). */
function shortId(id: string): string {
  return id.slice(0, 8)
}

function routeText(freight: FreightContract): string {
  const origin = logistics.getNode(freight.originNodeId)
  const destination = logistics.getNode(freight.destinationNodeId)
  const from = origin === null ? shortId(freight.originNodeId) : describeAnyNode(origin)
  const to = destination === null ? shortId(freight.destinationNodeId) : describeAnyNode(destination)
  return `${from} → ${to}`
}

/** Texto de la carga física (el schema FreightContract no lleva producto). */
function cargoText(shipment: Shipment): string {
  const productName = world.getProduct(shipment.productId)?.name ?? shipment.productId
  return `${productName} · ${formatQuantity(shipment.quantity)}`
}

function fillText(freight: FreightContract): string {
  return freight.fillBp === null ? '—' : `${(freight.fillBp / 100).toFixed(0)}%`
}

async function onRefresh(): Promise<void> {
  refreshing.value = true
  fetchError.value = null
  try {
    const [asShipper, asCarrier] = await Promise.all([
      apis.market.listFreightContracts({ role: 'shipper', limit: 200 }),
      apis.market.listFreightContracts({ role: 'carrier', limit: 200 }),
    ])
    const byId = new Map(
      [...asShipper.items, ...asCarrier.items].map((dto) => [dto.id, dto]),
    )
    market.applyFreightsSnapshot([...byId.values()].map(mapFreightContract))
  } catch (error) {
    fetchError.value = error
  } finally {
    refreshing.value = false
  }
}
</script>

<template>
  <div class="o-stack">
    <div class="freights__toolbar">
      <label class="freights__filter">
        <span>{{ t('market.freight.filter.role') }}</span>
        <select v-model="roleFilter" class="freights__select" data-testid="freight-filter-role">
          <option value="all">{{ t('market.freight.filter.all') }}</option>
          <option value="shipper">{{ t('market.freight.role.shipper') }}</option>
          <option value="carrier">{{ t('market.freight.role.carrier') }}</option>
        </select>
      </label>
      <BaseButton variant="ghost" :loading="refreshing" data-testid="freight-refresh" @click="onRefresh">
        {{ t('common.refresh') }}
      </BaseButton>
    </div>

    <BaseBanner v-if="fetchError !== null" variant="error">{{ messageFor(fetchError) }}</BaseBanner>

    <p v-if="freights.length === 0" class="freights__empty">{{ t('market.freight.empty') }}</p>

    <table v-else class="freights__table">
      <thead>
        <tr>
          <th>{{ t('market.freight.col.role') }}</th>
          <th>{{ t('market.freight.col.counterparty') }}</th>
          <th>{{ t('market.freight.col.route') }}</th>
          <th>{{ t('market.freight.col.cargo') }}</th>
          <th>{{ t('market.freight.col.price') }}</th>
          <th>{{ t('market.freight.col.declared') }}</th>
          <th>{{ t('market.freight.col.fill') }}</th>
          <th>{{ t('market.freight.col.deadline') }}</th>
          <th>{{ t('market.col.status') }}</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="{ freight, cargo } of rows" :key="freight.id" data-testid="freight-row">
          <td>{{ roleText(freight) }}</td>
          <td>
            <span class="u-numeric" :title="counterpartyId(freight)">
              {{ shortId(counterpartyId(freight)) }}
            </span>
          </td>
          <td>{{ routeText(freight) }}</td>
          <td>
            <template v-if="cargo !== null">
              {{ cargoText(cargo) }}
              <StatusBadge :presentation="shipmentStatusPresentation(cargo.status)" />
            </template>
            <span v-else class="freights__muted">—</span>
          </td>
          <td class="u-numeric">{{ format(freight.freightPrice) }}</td>
          <td class="u-numeric">{{ format(freight.declaredValue) }}</td>
          <td class="u-numeric">{{ fillText(freight) }}</td>
          <td><SimTimeCell :at="freight.deadlineSim" countdown /></td>
          <td><StatusBadge :presentation="contractStatusPresentation(freight.status)" /></td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.freights__toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: s.$space-3;
}

.freights__filter {
  display: flex;
  align-items: center;
  gap: s.$space-2;
  font-size: s.$font-size-200;
  color: var(--color-text-muted);
}

.freights__select {
  padding: s.$space-1 s.$space-2;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.freights__empty,
.freights__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.freights__table {
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
