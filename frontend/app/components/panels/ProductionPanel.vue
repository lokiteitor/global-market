<!--
  ProductionPanel — producción por edificio propio: receta activa, cola de
  lotes, inventario físico + partición libre/reservado del ledger
  (finance.store). El cliente encola INTENCIONES; insumos, combustible y
  salarios los valida/consume el servidor (P1) — aquí solo se muestran los
  avisos paused_no_fuel / paused_no_workers.
-->
<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { formatMoney, formatQuantity, type Money } from '~/lib/kernel/money'
import { useApi } from '~/composables/useApi'
import { useOwnership } from '~/composables/useOwnership'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useFinanceStore } from '~/stores/finance.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseSelect from '~/components/base/BaseSelect.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import SimTimeText from '~/components/base/SimTimeText.vue'

const api = useApi()
const world = useWorldStore()
const buildings = useBuildingsStore()
const finance = useFinanceStore()
const notifications = useNotificationsStore()
const ownership = useOwnership()

const busy = ref(false)
const selectedBuildingId = ref('')

onMounted(async () => {
  if (!world.loaded.recipes) {
    const r = await api.listRecipes()
    if (r.ok) world.setRecipes(r.value.data)
  }
  if (!world.loaded.buildingTypes) {
    const r = await api.listBuildingTypes()
    if (r.ok) world.setBuildingTypes(r.value.data)
  }
  if (!world.loaded.products) {
    const r = await api.listProducts()
    if (r.ok) world.setProducts(r.value.data)
  }
  const b = await api.listBuildings()
  if (b.ok) buildings.applySnapshot('rest', { buildings: b.value.data })
  // Ledger para cruzar libre/reservado con el inventario físico.
  const accounts = await api.listLedgerAccounts()
  if (accounts.ok) finance.applySnapshot('rest', { ledger_accounts: accounts.value.data })
})

const myBuildings = computed(() => {
  const myId = ownership.myAccountId.value
  return myId !== null ? buildings.ownedBy(myId) : []
})

const buildingOptions = computed(() =>
  myBuildings.value.map((b) => ({
    value: b.id,
    label: `${world.buildingTypes[b.building_type_id]?.name ?? b.building_type_id.slice(0, 8)} · nv ${b.level} (${b.status})`
  }))
)

const building = computed(() => (selectedBuildingId.value !== '' ? (buildings.byId[selectedBuildingId.value] ?? null) : null))

watch(selectedBuildingId, async (id) => {
  if (id === '') return
  const [inv, batches] = await Promise.all([api.getBuildingInventory(id), api.listProductionBatches(id)])
  if (inv.ok) buildings.setInventory(id, inv.value.data)
  if (batches.ok) buildings.setBatches(id, batches.value.data)
})

// ─── Receta activa ───────────────────────────────────────────────────────────
const recipeOptions = computed(() => {
  if (building.value === null) return []
  return (world.recipesByBuildingType[building.value.building_type_id] ?? []).map((r) => ({ value: r.id, label: r.name }))
})

const recipeChoice = ref('')
watch(building, (b) => {
  recipeChoice.value = b?.active_recipe_id ?? ''
})

async function applyRecipe(): Promise<void> {
  if (building.value === null) return
  busy.value = true
  const result = await api.updateBuilding(building.value.id, {
    active_recipe_id: recipeChoice.value === '' ? null : recipeChoice.value
  })
  busy.value = false
  if (result.ok) {
    buildings.applyPatch([{ op: 'upsert', entity: 'building', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Receta actualizada (aplica su changeover)' })
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

// ─── Cola de lotes ───────────────────────────────────────────────────────────
const queueRecipe = ref('')
const queueCount = ref('1')

const batches = computed(() => (building.value !== null ? (buildings.batchesByBuilding[building.value.id] ?? []) : []))

const pausedWarnings = computed(() =>
  batches.value.filter((b) => b.status === 'paused_no_fuel' || b.status === 'paused_no_workers')
)

async function queueBatches(): Promise<void> {
  if (building.value === null) return
  const recipeId = queueRecipe.value !== '' ? queueRecipe.value : (building.value.active_recipe_id ?? '')
  const n = Number.parseInt(queueCount.value, 10)
  if (recipeId === '' || !Number.isInteger(n) || n < 1) {
    notifications.push({ level: 'warning', text: 'Elige receta y nº de lotes (≥ 1)' })
    return
  }
  busy.value = true
  const result = await api.queueProductionBatches(building.value.id, recipeId, n)
  busy.value = false
  if (result.ok) {
    buildings.upsertBatch(result.value.data)
    notifications.push({ level: 'success', text: `${n} lote(s) encolado(s)` })
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

async function cancelBatch(batchId: string): Promise<void> {
  busy.value = true
  const result = await api.cancelProductionBatch(batchId)
  busy.value = false
  if (result.ok) {
    buildings.upsertBatch(result.value.data)
    notifications.push({ level: 'success', text: 'Orden cancelada (lo producido queda asentado)' })
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

const batchColumns: TableColumn[] = [
  { key: 'recipe', label: 'Receta' },
  { key: 'progress', label: 'Lotes', align: 'right' },
  { key: 'progress_pct', label: 'Progreso', align: 'right' },
  { key: 'eta', label: 'ETA' },
  { key: 'status', label: 'Estado' },
  { key: 'actions', label: '' }
]

function recipeName(id: string): string {
  return world.recipes[id]?.name ?? `${id.slice(0, 8)}…`
}

// ─── Inventario físico + ledger (libre/reservado) ────────────────────────────
const inventory = computed(() => (building.value !== null ? buildings.inventoryOf(building.value.id) : []))

/** Getter local derivado (anotar al integrador: candidato a finance.store). */
function stockFree(productId: string): string | null {
  const b = building.value
  if (b === null) return null
  const account = finance.accountList.find(
    (a) => a.kind === 'stock_free' && a.product_id === productId && a.warehouse_building_id === b.id
  )
  return account !== undefined ? formatMoney(account.balance) : null
}

function stockReserved(productId: string): string | null {
  const total = finance.accountList
    .filter((a) => a.kind === 'stock_reserved' && a.product_id === productId)
    .reduce((sum, a) => sum + BigInt(a.balance), 0n)
  return total > 0n ? formatMoney(total.toString() as Money) : null
}

const inventoryColumns: TableColumn[] = [
  { key: 'product', label: 'Producto' },
  { key: 'quantity', label: 'Físico', align: 'right' },
  { key: 'free', label: 'Libre (ledger)', align: 'right' },
  { key: 'reserved', label: 'Reservado (ledger)', align: 'right' }
]

function productName(productId: string): string {
  return world.products[productId]?.code ?? `${productId.slice(0, 8)}…`
}
</script>

<template>
  <BasePanel title="Producción" :collapsible="false">
    <div class="p-prod">
      <BaseSelect v-model="selectedBuildingId" label="Edificio propio" :options="buildingOptions" placeholder="Elegir…" />

      <p v-if="building === null" class="p-prod__faint">Selecciona un edificio para gestionar su producción.</p>

      <template v-else>
        <div v-if="pausedWarnings.length > 0" class="p-prod__warning" role="alert">
          <strong>Producción pausada:</strong>
          <span v-for="b in pausedWarnings" :key="b.id">
            {{ b.status === 'paused_no_fuel' ? 'sin combustible' : 'sin trabajadores (salarios impagados)' }} ·
          </span>
          El motor la reanudará cuando haya insumos/salarios (cascada de insolvencia sin deuda).
        </div>

        <section class="p-prod__section" aria-label="Receta activa">
          <h4 class="p-prod__subtitle">Receta activa</h4>
          <div class="p-prod__row">
            <BaseSelect v-model="recipeChoice" :options="recipeOptions" placeholder="(sin receta — línea parada)" />
            <BaseButton variant="primary" :disabled="busy" @click="applyRecipe">Cambiar receta</BaseButton>
          </div>
        </section>

        <section class="p-prod__section" aria-label="Cola de lotes">
          <h4 class="p-prod__subtitle">Cola de lotes</h4>
          <div class="p-prod__row">
            <BaseSelect v-model="queueRecipe" label="Receta" :options="recipeOptions" placeholder="(receta activa)" />
            <BaseInput v-model="queueCount" label="Lotes" type="number" :min="1" />
            <BaseButton variant="primary" :disabled="busy" @click="queueBatches">Encolar</BaseButton>
          </div>
          <BaseTable :columns="batchColumns" :rows="batches" :row-key="(b) => b.id" empty-text="Cola vacía">
            <template #cell-recipe="{ row }">{{ recipeName(row.recipe_id) }}</template>
            <template #cell-progress="{ row }"><span class="e-num">{{ row.batches_done }}/{{ row.batches_queued }}</span></template>
            <template #cell-progress_pct="{ row }">
              <span class="e-num">{{ row.progress_pct !== undefined ? `${Math.round(row.progress_pct)} %` : '—' }}</span>
            </template>
            <template #cell-eta="{ row }">
              <SimTimeText v-if="row.eta_sim !== undefined" :sim-seconds="row.eta_sim" verb="termina" />
              <span v-else class="p-prod__faint">—</span>
            </template>
            <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
            <template #cell-actions="{ row }">
              <BaseButton
                v-if="row.status === 'queued' || row.status === 'running' || row.status === 'paused_no_fuel' || row.status === 'paused_no_workers'"
                size="sm"
                variant="danger"
                :disabled="busy"
                @click="cancelBatch(row.id)"
              >
                Cancelar
              </BaseButton>
            </template>
          </BaseTable>
        </section>

        <section class="p-prod__section" aria-label="Inventario">
          <h4 class="p-prod__subtitle">Inventario físico · partición contable</h4>
          <BaseTable :columns="inventoryColumns" :rows="inventory" :row-key="(i) => i.product_id" empty-text="Inventario vacío">
            <template #cell-product="{ row }">{{ productName(row.product_id) }}</template>
            <template #cell-quantity="{ row }"><span class="e-num">{{ formatQuantity(row.quantity) }}</span></template>
            <template #cell-free="{ row }">
              <span class="e-num">{{ stockFree(row.product_id) ?? '—' }}</span>
            </template>
            <template #cell-reserved="{ row }">
              <span class="e-num">{{ stockReserved(row.product_id) ?? '—' }}</span>
            </template>
          </BaseTable>
          <p class="p-prod__faint">
            Físico = inventario del edificio; libre/reservado = cuentas de stock del ledger (la reconciliación es periódica,
            server-side).
          </p>
        </section>
      </template>
    </div>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-prod {
  display: flex;
  flex-direction: column;
  gap: 1rem;

  &__section {
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

  &__row {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 0.75rem;

    > * {
      min-width: 9rem;
    }
  }

  &__faint {
    color: var(--ii-text-faint);
    font-size: 0.8125rem;
  }

  &__warning {
    color: var(--ii-warning);
    border: 1px dashed var(--ii-warning);
    border-radius: 3px;
    padding: 0.5rem 0.75rem;
    font-size: 0.875rem;
  }
}
</style>
