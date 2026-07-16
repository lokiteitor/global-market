<!--
  FinancePanel — cuentas del ledger agrupadas por tipo y extracto paginado.
  Solo lectura: ni un centavo cambia por decisión del cliente (P1). Los saldos
  se formatean con los helpers BigInt del kernel (C11).
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { LedgerAccount, LedgerAccountKind, LedgerEntry } from '~/lib/api/types'
import { useApi } from '~/composables/useApi'
import { useFinanceStore } from '~/stores/finance.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useWorldStore } from '~/stores/world.store'
import BaseButton from '~/components/base/BaseButton.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import MoneyText from '~/components/base/MoneyText.vue'
import SimTimeText from '~/components/base/SimTimeText.vue'

const api = useApi()
const finance = useFinanceStore()
const world = useWorldStore()
const notifications = useNotificationsStore()

const busy = ref(false)

onMounted(async () => {
  if (!world.loaded.products) {
    const r = await api.listProducts()
    if (r.ok) world.setProducts(r.value.data)
  }
  await refreshAccounts()
})

async function refreshAccounts(): Promise<void> {
  busy.value = true
  const result = await api.listLedgerAccounts()
  busy.value = false
  if (result.ok) {
    finance.applySnapshot('rest', { ledger_accounts: result.value.data })
  } else {
    notifications.push({ level: 'error', text: `Ledger: ${result.error.message}` })
  }
}

const GROUP_LABELS: Partial<Record<LedgerAccountKind, string>> = {
  cash: 'Caja',
  escrow: 'Escrow (pagos retenidos)',
  guarantee: 'Garantías bloqueadas',
  stock_free: 'Stock libre',
  stock_reserved: 'Stock reservado',
  custody: 'Custodia (fletes)'
}

const GROUP_ORDER: LedgerAccountKind[] = ['cash', 'escrow', 'guarantee', 'stock_free', 'stock_reserved', 'custody']

const groups = computed(() => {
  const byKind = new Map<LedgerAccountKind, LedgerAccount[]>()
  for (const account of finance.accountList) {
    const list = byKind.get(account.kind) ?? []
    list.push(account)
    byKind.set(account.kind, list)
  }
  return GROUP_ORDER.filter((kind) => byKind.has(kind)).map((kind) => ({
    kind,
    label: GROUP_LABELS[kind] ?? kind,
    accounts: byKind.get(kind) ?? []
  }))
})

function accountLabel(account: LedgerAccount): string {
  const parts: string[] = []
  if (account.product_id !== undefined) parts.push(world.products[account.product_id]?.code ?? account.product_id.slice(0, 8))
  if (account.warehouse_building_id !== undefined) parts.push(`alm. ${account.warehouse_building_id.slice(0, 8)}…`)
  if (account.reference_id !== undefined) parts.push(`ref. ${account.reference_id.slice(0, 8)}…`)
  return parts.length > 0 ? parts.join(' · ') : 'general'
}

/** Las cuentas de stock se muestran como cantidad; las monetarias como importe. */
function isStockKind(kind: LedgerAccountKind): boolean {
  return kind === 'stock_free' || kind === 'stock_reserved' || kind === 'custody'
}

// ─── Extracto ────────────────────────────────────────────────────────────────
const selectedAccount = ref<LedgerAccount | null>(null)
const entries = ref<LedgerEntry[]>([])
const nextCursor = ref<string | undefined>(undefined)

async function openStatement(account: LedgerAccount): Promise<void> {
  selectedAccount.value = account
  entries.value = []
  nextCursor.value = undefined
  await loadMore()
}

async function loadMore(): Promise<void> {
  const account = selectedAccount.value
  if (account === null) return
  busy.value = true
  const result = await api.listLedgerEntries(account.id, {
    limit: 25,
    ...(nextCursor.value !== undefined ? { cursor: nextCursor.value } : {})
  })
  busy.value = false
  if (result.ok) {
    entries.value = [...entries.value, ...result.value.data]
    finance.addEntries(result.value.data)
    nextCursor.value = result.value.meta.next_cursor
  } else {
    notifications.push({ level: 'error', text: `Extracto: ${result.error.message}` })
  }
}

const entryColumns: TableColumn[] = [
  { key: 'when', label: 'Sim-time' },
  { key: 'transaction_kind', label: 'Concepto' },
  { key: 'amount', label: 'Importe', align: 'right' },
  { key: 'description', label: 'Detalle' }
]
</script>

<template>
  <BasePanel title="Finanzas — ledger" :collapsible="false">
    <div class="p-fin">
      <div class="p-fin__toolbar">
        <BaseButton :disabled="busy" @click="refreshAccounts">Actualizar</BaseButton>
        <span class="p-fin__faint">Ledger de doble entrada, solo lectura: los saldos los deriva y protege el servidor.</span>
      </div>

      <section v-for="group in groups" :key="group.kind" class="p-fin__group" :aria-label="group.label">
        <h4 class="p-fin__subtitle">{{ group.label }}</h4>
        <ul class="p-fin__accounts" role="list">
          <li v-for="account in group.accounts" :key="account.id" class="p-fin__account">
            <span class="p-fin__account-label">{{ accountLabel(account) }}</span>
            <span v-if="isStockKind(account.kind)" class="e-num">{{ account.balance }}</span>
            <MoneyText v-else :amount="account.balance" signed />
            <BaseButton size="sm" variant="subtle" @click="openStatement(account)">Extracto</BaseButton>
          </li>
        </ul>
      </section>

      <p v-if="groups.length === 0" class="p-fin__faint">Sin cuentas replicadas todavía (pulsa Actualizar).</p>

      <section v-if="selectedAccount !== null" class="p-fin__statement" aria-label="Extracto de cuenta">
        <h4 class="p-fin__subtitle">
          Extracto — {{ GROUP_LABELS[selectedAccount.kind] ?? selectedAccount.kind }} · {{ accountLabel(selectedAccount) }}
        </h4>
        <BaseTable :columns="entryColumns" :rows="entries" :row-key="(e) => e.id" empty-text="Sin partidas">
          <template #cell-when="{ row }"><SimTimeText :sim-seconds="row.sim_time_at" :relative="false" /></template>
          <template #cell-amount="{ row }"><MoneyText :amount="row.amount" signed show-plus /></template>
          <template #cell-description="{ row }">{{ row.description ?? '—' }}</template>
        </BaseTable>
        <BaseButton v-if="nextCursor !== undefined" :disabled="busy" @click="loadMore">Cargar más</BaseButton>
      </section>
    </div>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-fin {
  display: flex;
  flex-direction: column;
  gap: 1rem;

  &__toolbar {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }

  &__group,
  &__statement {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }

  &__subtitle {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-text-muted);
  }

  &__accounts {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  &__account {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.75rem;
    padding: 0.25rem 0.5rem;
    border: 1px solid var(--ii-border-subtle);
    border-radius: 3px;
    font-size: 0.875rem;
  }

  &__account-label {
    flex: 1;
    color: var(--ii-text-muted);
  }

  &__faint {
    color: var(--ii-text-faint);
    font-size: 0.8125rem;
  }
}
</style>
