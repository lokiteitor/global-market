<!--
  CitiesPanel — ciudades (nivel/población/índice) y demanda por producto.
  Datos de cities.store (viewport WS y/o pull REST) + fetch de demanda bajo
  demanda. Precio actual vs base del producto: el cálculo es del Economy
  Balancer server-side; aquí solo se compara visualmente (P1).
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { City } from '~/lib/api/types'
import { cmpMoney, type Money } from '~/lib/kernel/money'
import { useApi } from '~/composables/useApi'
import { useCitiesStore } from '~/stores/cities.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import MoneyText from '~/components/base/MoneyText.vue'

const api = useApi()
const cities = useCitiesStore()
const world = useWorldStore()
const notifications = useNotificationsStore()

const busy = ref(false)
const selectedCityId = ref<string | null>(null)

onMounted(async () => {
  if (!world.loaded.products) {
    const r = await api.listProducts()
    if (r.ok) world.setProducts(r.value.data)
  }
  if (cities.list.length === 0) {
    const r = await api.listCities()
    if (r.ok) cities.applySnapshot('rest', { cities: r.value.data })
  }
})

const selectedCity = computed(() => (selectedCityId.value !== null ? (cities.byId[selectedCityId.value] ?? null) : null))
const demand = computed(() => (selectedCityId.value !== null ? cities.demandOf(selectedCityId.value) : []))

async function selectCity(city: City): Promise<void> {
  selectedCityId.value = city.id
  busy.value = true
  const result = await api.getCityDemand(city.id)
  busy.value = false
  if (result.ok) {
    cities.setDemand(city.id, result.value.data)
  } else {
    notifications.push({ level: 'error', text: `Demanda: ${result.error.message}` })
  }
}

const cityColumns: TableColumn[] = [
  { key: 'name', label: 'Ciudad' },
  { key: 'level', label: 'Nivel', align: 'right' },
  { key: 'population', label: 'Población', align: 'right' },
  { key: 'supply_index', label: 'Índice', align: 'right' },
  { key: 'actions', label: '' }
]

const demandColumns: TableColumn[] = [
  { key: 'product', label: 'Producto' },
  { key: 'd0', label: 'Demanda/día', align: 'right' },
  { key: 'current_price', label: 'Precio actual', align: 'right' },
  { key: 'base_price', label: 'Precio base', align: 'right' },
  { key: 'trend', label: 'vs base' },
  { key: 'saturation', label: 'Saturación', align: 'right' }
]

function productCode(productId: string): string {
  return world.products[productId]?.code ?? `${productId.slice(0, 8)}…`
}

function basePrice(productId: string): string | null {
  return world.products[productId]?.base_price ?? null
}

function trend(productId: string, currentPrice: Money): 'above' | 'below' | 'equal' | null {
  const base = world.products[productId]?.base_price
  if (base === undefined) return null
  const c = cmpMoney(currentPrice, base)
  return c > 0 ? 'above' : c < 0 ? 'below' : 'equal'
}
</script>

<template>
  <BasePanel title="Ciudades y demanda" :collapsible="false">
    <div class="p-cities">
      <BaseTable :columns="cityColumns" :rows="cities.list" :row-key="(c) => c.id" empty-text="Sin ciudades replicadas">
        <template #cell-population="{ row }"><span class="e-num">{{ row.population.toLocaleString('es-ES') }}</span></template>
        <template #cell-level="{ row }"><span class="e-num">{{ row.level }}</span></template>
        <template #cell-supply_index="{ row }"><span class="e-num">{{ row.supply_index }}</span></template>
        <template #cell-actions="{ row }">
          <BaseButton size="sm" :disabled="busy" @click="selectCity(row)">Demanda</BaseButton>
        </template>
      </BaseTable>

      <section v-if="selectedCity !== null" class="p-cities__demand" aria-label="Demanda de la ciudad">
        <h4 class="p-cities__subtitle">Demanda — {{ selectedCity.name }} (nivel {{ selectedCity.level }})</h4>
        <BaseTable :columns="demandColumns" :rows="demand" :row-key="(d) => d.product_id" empty-text="Sin curvas activas">
          <template #cell-product="{ row }">{{ productCode(row.product_id) }}</template>
          <template #cell-d0="{ row }"><span class="e-num">{{ row.d0_per_sim_day }}</span></template>
          <template #cell-current_price="{ row }"><MoneyText :amount="row.current_price" /></template>
          <template #cell-base_price="{ row }">
            <MoneyText v-if="basePrice(row.product_id) !== null" :amount="world.products[row.product_id]!.base_price" />
            <span v-else>—</span>
          </template>
          <template #cell-trend="{ row }">
            <BaseBadge
              v-if="trend(row.product_id, row.current_price) !== null"
              :variant="trend(row.product_id, row.current_price) === 'above' ? 'success' : trend(row.product_id, row.current_price) === 'below' ? 'danger' : 'neutral'"
            >
              {{ trend(row.product_id, row.current_price) === 'above' ? '▲ sobre base' : trend(row.product_id, row.current_price) === 'below' ? '▼ bajo base' : '= base' }}
            </BaseBadge>
          </template>
          <template #cell-saturation="{ row }"><span class="e-num">{{ row.saturation_factor }}</span></template>
        </BaseTable>
        <p class="p-cities__faint">
          Precio acotado por los clamps del producto; la curva la recalcula el Economy Balancer (server-side).
        </p>
      </section>
    </div>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-cities {
  display: flex;
  flex-direction: column;
  gap: 1rem;

  &__demand {
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

  &__faint {
    color: var(--ii-text-faint);
    font-size: 0.8125rem;
  }
}
</style>
