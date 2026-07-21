<script setup lang="ts">
/**
 * FinancePanel — cuentas del ledger + extracto paginado (mandato §3 FINANZAS).
 *
 * El cliente REFLEJA el ledger (P1): saldos tal cual llegan, importes con
 * signo formateados vía signedAmountParts + shared/money (cero floats, C11).
 * El extracto de la cuenta elegida se pagina por cursor (finance.store).
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatSimTime } from '~shared/simtime'
import type { LedgerAccount, LedgerEntry } from '~domain/finance'
import { signedAmountParts } from '~domain/finance'
import { mapLedgerEntry } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import { LEDGER_ACCOUNT_KIND_LABEL, LEDGER_TX_KIND_LABEL } from '~/components/play/labels'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useFinanceStore } from '~/stores/finance.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const finance = useFinanceStore()
const world = useWorldStore()
const panels = usePanelsStore()
const { messageFor } = useAppError()

const selectedAccountId = ref<string | null>(null)
const loadingEntries = ref(false)
const fetchError = ref<unknown>(null)

const accounts = computed(() => finance.ledgerAccountList)

function accountProduct(account: LedgerAccount): string {
  return account.productId === null
    ? '—'
    : (world.getProduct(account.productId)?.name ?? account.productId)
}

async function loadEntries(accountId: string, cursor: string | null): Promise<void> {
  loadingEntries.value = true
  fetchError.value = null
  try {
    const page = await apis.ledger.listLedgerEntries(accountId, {
      limit: 25,
      ...(cursor === null ? {} : { cursor }),
    })
    finance.applyEntriesPage(page.items.map(mapLedgerEntry), page.nextCursor)
  } catch (error) {
    fetchError.value = error
  } finally {
    loadingEntries.value = false
  }
}

async function onSelectAccount(account: LedgerAccount): Promise<void> {
  selectedAccountId.value = account.id
  finance.resetEntries()
  await loadEntries(account.id, null)
}

async function onLoadMore(): Promise<void> {
  const accountId = selectedAccountId.value
  if (accountId === null) {
    return
  }
  await loadEntries(accountId, finance.entriesNextCursor)
}

/** Importe con signo formateado sin pasar por number (C11). */
function amountText(entry: LedgerEntry): string {
  const { negative, magnitude } = signedAmountParts(entry.amount)
  return `${negative ? '−' : '+'}${format(magnitude)}`
}
</script>

<template>
  <FloatingPanel :title="t('panel.finance')" @close="panels.close()">
    <div class="o-stack">
      <section>
        <h4 class="finance__subtitle">{{ t('finance.accounts.title') }}</h4>
        <p v-if="accounts.length === 0" class="finance__empty">{{ t('finance.accounts.empty') }}</p>
        <table v-else class="finance__table">
          <thead>
            <tr>
              <th>{{ t('finance.col.kind') }}</th>
              <th>{{ t('finance.col.product') }}</th>
              <th>{{ t('finance.col.balance') }}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="account of accounts"
              :key="account.id"
              :class="{ 'finance__row--selected': selectedAccountId === account.id }"
            >
              <td>{{ t(LEDGER_ACCOUNT_KIND_LABEL[account.kind]) }}</td>
              <td>{{ accountProduct(account) }}</td>
              <td class="u-numeric">{{ format(account.balance) }}</td>
              <td>
                <BaseButton variant="ghost" @click="onSelectAccount(account)">
                  {{ t('finance.entries.open') }}
                </BaseButton>
              </td>
            </tr>
          </tbody>
        </table>
      </section>

      <section v-if="selectedAccountId !== null">
        <h4 class="finance__subtitle">{{ t('finance.entries.title') }}</h4>
        <BaseBanner v-if="fetchError !== null" variant="error">
          {{ messageFor(fetchError) }}
        </BaseBanner>
        <p v-else-if="!finance.entriesLoaded && loadingEntries" class="finance__empty">
          {{ t('common.loading') }}
        </p>
        <p v-else-if="finance.entriesList.length === 0" class="finance__empty">
          {{ t('finance.entries.empty') }}
        </p>
        <table v-else class="finance__table">
          <thead>
            <tr>
              <th>{{ t('finance.col.simTime') }}</th>
              <th>{{ t('finance.col.concept') }}</th>
              <th>{{ t('finance.col.amount') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="entry of finance.entriesList" :key="entry.id">
              <td class="u-numeric">{{ formatSimTime(entry.simTimeAt) }}</td>
              <td>{{ t(LEDGER_TX_KIND_LABEL[entry.transactionKind]) }}</td>
              <td class="u-numeric">{{ amountText(entry) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-if="finance.hasMoreEntries" class="finance__more">
          <BaseButton variant="ghost" :loading="loadingEntries" @click="onLoadMore">
            {{ t('finance.entries.more') }}
          </BaseButton>
        </div>
      </section>
    </div>
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.finance__subtitle {
  margin-bottom: s.$space-3;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.finance__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.finance__table {
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

.finance__row--selected {
  background-color: var(--color-surface-active);
}

.finance__more {
  display: flex;
  justify-content: center;
  margin-top: s.$space-3;
}
</style>
