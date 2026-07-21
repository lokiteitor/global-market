<script setup lang="ts">
/**
 * MyContracts — contratos CCRI propios (FAD §15.8/§15.12).
 *
 * Estado, rol (comprador/vendedor), fill acumulado, deadline en sim-time con
 * cuenta atrás (SimTimeCell) y entregas parciales bajo demanda
 * (GET /contracts/{id}/deliveries — no se replican).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatSimTime, simTime } from '~shared/simtime'
import { formatQuantity, isQuantity } from '~domain/quantity'
import type { Contract } from '~domain/market'
import { isMine } from '~domain/ownership'
import { contractStatusPresentation } from '~domain/status'
import type { ContractDeliveryDto } from '~network/market.api'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import SimTimeCell from '~/components/play/SimTimeCell.vue'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const market = useMarketStore()
const world = useWorldStore()
const session = useSessionStore()
const { messageFor } = useAppError()

const contracts = computed(() =>
  [...market.contractList].toSorted((a, b) => b.confirmedAtSim - a.confirmedAtSim),
)

const expandedId = ref<string | null>(null)
const deliveries = ref<readonly ContractDeliveryDto[]>([])
const loadingDeliveries = ref(false)
const fetchError = ref<unknown>(null)

async function onToggleDeliveries(contract: Contract): Promise<void> {
  if (expandedId.value === contract.id) {
    expandedId.value = null
    deliveries.value = []
    return
  }
  expandedId.value = contract.id
  loadingDeliveries.value = true
  fetchError.value = null
  try {
    deliveries.value = await apis.market.listContractDeliveries(contract.id)
  } catch (error) {
    fetchError.value = error
  } finally {
    loadingDeliveries.value = false
  }
}

function roleText(contract: Contract): string {
  return isMine(contract.sellerAccountId, session.account?.id ?? null)
    ? t('market.contracts.role.seller')
    : t('market.contracts.role.buyer')
}

function productName(contract: Contract): string {
  return world.getProduct(contract.productId)?.name ?? contract.productId
}

function fillText(contract: Contract): string {
  return contract.fillBp === null ? '—' : `${(contract.fillBp / 100).toFixed(0)}%`
}

function simTimeText(seconds: number): string {
  return formatSimTime(simTime(seconds))
}

function quantityText(raw: string): string {
  return isQuantity(raw) ? formatQuantity(raw) : raw
}
</script>

<template>
  <div class="o-stack">
    <p v-if="contracts.length === 0" class="contracts__empty">{{ t('market.contracts.empty') }}</p>

    <table v-else class="contracts__table">
      <thead>
        <tr>
          <th>{{ t('market.contracts.role') }}</th>
          <th>{{ t('market.col.product') }}</th>
          <th>{{ t('market.contracts.progress') }}</th>
          <th>{{ t('market.col.price') }}</th>
          <th>{{ t('market.contracts.deadline') }}</th>
          <th>{{ t('market.contracts.fill') }}</th>
          <th>{{ t('market.col.status') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <template v-for="contract of contracts" :key="contract.id">
          <tr>
            <td>{{ roleText(contract) }}</td>
            <td>{{ productName(contract) }}</td>
            <td class="u-numeric">
              {{ formatQuantity(contract.quantityDelivered) }} /
              {{ formatQuantity(contract.quantityAgreed) }}
            </td>
            <td class="u-numeric">{{ format(contract.unitPrice) }}</td>
            <td><SimTimeCell :at="contract.deadlineSim" countdown /></td>
            <td class="u-numeric">{{ fillText(contract) }}</td>
            <td><StatusBadge :presentation="contractStatusPresentation(contract.status)" /></td>
            <td>
              <BaseButton variant="ghost" @click="onToggleDeliveries(contract)">
                {{
                  expandedId === contract.id
                    ? t('market.contracts.deliveries.hide')
                    : t('market.contracts.deliveries.show')
                }}
              </BaseButton>
            </td>
          </tr>
          <tr v-if="expandedId === contract.id">
            <td colspan="8">
              <BaseBanner v-if="fetchError !== null" variant="error">
                {{ messageFor(fetchError) }}
              </BaseBanner>
              <p v-else-if="loadingDeliveries" class="contracts__empty">{{ t('common.loading') }}</p>
              <p v-else-if="deliveries.length === 0" class="contracts__empty">
                {{ t('market.contracts.deliveries.empty') }}
              </p>
              <ul v-else class="contracts__deliveries">
                <li v-for="delivery of deliveries" :key="delivery.id">
                  <span class="u-numeric">{{ simTimeText(delivery.delivered_at_sim) }}</span>
                  ·
                  <span class="u-numeric">{{ quantityText(delivery.quantity) }}</span>
                  ·
                  <span>
                    {{
                      delivery.on_time
                        ? t('market.contracts.deliveries.onTime')
                        : t('market.contracts.deliveries.late')
                    }}
                  </span>
                </li>
              </ul>
            </td>
          </tr>
        </template>
      </tbody>
    </table>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.contracts__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.contracts__table {
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

.contracts__deliveries {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  list-style: none;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
