<!--
  FleetPanel — flota propia y rutas.
  Compra a catálogo, asignación/retirada de ruta, mantenimiento, y creación de
  rutas desde el asistente de planes (POST /logistics/route-plans → legs + ETA
  → guardar como ruta). Las ETAs son estimaciones informativas, no garantías
  (P1): el riesgo del plazo lo asume quien lo pacta.
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { NetworkNode, Route, RoutePlan, Vehicle } from '~/lib/api/types'
import { formatQuantity } from '~/lib/kernel/money'
import { formatSimDuration } from '~/lib/kernel/simtime'
import { useApi } from '~/composables/useApi'
import { useOwnership } from '~/composables/useOwnership'
import { useFleetStore } from '~/stores/fleet.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseSelect from '~/components/base/BaseSelect.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import BaseTabs from '~/components/base/BaseTabs.vue'
import MoneyText from '~/components/base/MoneyText.vue'

const api = useApi()
const world = useWorldStore()
const fleet = useFleetStore()
const notifications = useNotificationsStore()
const ownership = useOwnership()

const tab = ref('vehicles')
const tabs = [
  { id: 'vehicles', label: 'Vehículos' },
  { id: 'buy', label: 'Comprar' },
  { id: 'routes', label: 'Rutas' }
]

const busy = ref(false)
const nodes = ref<NetworkNode[]>([])
const routes = ref<Route[]>([])

onMounted(async () => {
  if (!world.loaded.vehicleTypes) {
    const r = await api.listVehicleTypes()
    if (r.ok) world.setVehicleTypes(r.value.data)
  }
  const [v, n, rt] = await Promise.all([api.listVehicles(), api.listNetworkNodes(), api.listRoutes()])
  if (v.ok) fleet.applySnapshot('rest', { vehicles: v.value.data })
  if (n.ok) nodes.value = n.value.data
  if (rt.ok) routes.value = rt.value.data
})

const myVehicles = computed(() => {
  const myId = ownership.myAccountId.value
  return myId !== null ? fleet.ownedBy(myId) : []
})

function typeName(vehicleTypeId: string): string {
  return world.vehicleTypes[vehicleTypeId]?.name ?? `${vehicleTypeId.slice(0, 8)}…`
}

function routeName(routeId: string | undefined): string {
  if (routeId === undefined) return '—'
  return routes.value.find((r) => r.id === routeId)?.name ?? `${routeId.slice(0, 8)}…`
}

function positionText(v: Vehicle): string {
  if (v.position.at_node_id !== undefined) {
    const node = nodes.value.find((n) => n.id === v.position.at_node_id)
    return node !== undefined ? `en ${node.kind}` : 'en nodo'
  }
  if (v.position.on_segment_id !== undefined) {
    const pct = v.position.segment_progress_pct !== undefined ? ` · ${Math.round(v.position.segment_progress_pct)} %` : ''
    return `en tránsito${pct}`
  }
  return '—'
}

// ─── Comandos sobre vehículos ────────────────────────────────────────────────
const routeChoice = ref<Record<string, string>>({})

async function assignRoute(vehicle: Vehicle): Promise<void> {
  const chosen = routeChoice.value[vehicle.id] ?? ''
  busy.value = true
  const result = await api.updateVehicle(vehicle.id, { route_id: chosen === '' ? null : chosen })
  busy.value = false
  if (result.ok) {
    fleet.applyPatch([{ op: 'upsert', entity: 'vehicle', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: chosen === '' ? 'Ruta retirada' : 'Ruta asignada' })
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

async function scheduleMaintenance(vehicle: Vehicle): Promise<void> {
  busy.value = true
  const result = await api.updateVehicle(vehicle.id, { schedule_maintenance: true })
  busy.value = false
  if (result.ok) {
    fleet.applyPatch([{ op: 'upsert', entity: 'vehicle', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Mantenimiento programado' })
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

const vehicleColumns: TableColumn[] = [
  { key: 'type', label: 'Tipo' },
  { key: 'status', label: 'Estado' },
  { key: 'wear_pct', label: 'Desgaste', align: 'right' },
  { key: 'fuel', label: 'Combustible', align: 'right' },
  { key: 'position', label: 'Posición' },
  { key: 'route', label: 'Ruta' },
  { key: 'actions', label: '' }
]

const routeOptions = computed(() => routes.value.map((r) => ({ value: r.id, label: r.name })))

// ─── Comprar vehículo ────────────────────────────────────────────────────────
const buyType = ref('')
const buyNode = ref('')
const buyError = ref<string | undefined>(undefined)

const vehicleTypeOptions = computed(() =>
  Object.values(world.vehicleTypes).map((t) => ({ value: t.id, label: `${t.name} (${t.mode})` }))
)
const selectedType = computed(() => (buyType.value !== '' ? (world.vehicleTypes[buyType.value] ?? null) : null))
const nodeOptions = computed(() => nodes.value.map((n) => ({ value: n.id, label: `${n.kind} · ${n.id.slice(0, 8)}…` })))

async function buyVehicle(): Promise<void> {
  buyError.value = undefined
  if (buyType.value === '' || buyNode.value === '') {
    buyError.value = 'Elige tipo de vehículo y nodo de entrega'
    return
  }
  busy.value = true
  const result = await api.purchaseVehicle(buyType.value, buyNode.value)
  busy.value = false
  if (result.ok) {
    fleet.applyPatch([{ op: 'upsert', entity: 'vehicle', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Vehículo adquirido (idle en el nodo de entrega)' })
  } else {
    buyError.value = `${result.error.code}: ${result.error.message}`
  }
}

// ─── Rutas: crear desde plan ─────────────────────────────────────────────────
const planOrigin = ref('')
const planDestination = ref('')
const plan = ref<RoutePlan | null>(null)
const planError = ref<string | undefined>(undefined)
const newRouteName = ref('')

async function calculatePlan(): Promise<void> {
  planError.value = undefined
  plan.value = null
  if (planOrigin.value === '' || planDestination.value === '') {
    planError.value = 'Elige nodos de origen y destino'
    return
  }
  busy.value = true
  const result = await api.createRoutePlan(planOrigin.value, planDestination.value)
  busy.value = false
  if (result.ok) {
    plan.value = result.value.data
  } else {
    planError.value = `${result.error.code}: ${result.error.message}`
  }
}

async function saveRoute(): Promise<void> {
  if (plan.value === null) return
  const name = newRouteName.value.trim()
  if (name === '') {
    planError.value = 'Ponle nombre a la ruta'
    return
  }
  busy.value = true
  const result = await api.createRoute(name, 'fixed_line', plan.value.legs.map((l) => l.link_id))
  busy.value = false
  if (result.ok) {
    routes.value = [...routes.value, result.value.data]
    notifications.push({ level: 'success', text: `Ruta «${name}» creada` })
    plan.value = null
    newRouteName.value = ''
  } else {
    planError.value = `${result.error.code}: ${result.error.message}`
  }
}

const routeColumns: TableColumn[] = [
  { key: 'name', label: 'Nombre' },
  { key: 'kind', label: 'Tipo' },
  { key: 'legs', label: 'Tramos', align: 'right' },
  { key: 'active', label: 'Activa' }
]
</script>

<template>
  <BasePanel title="Flota y rutas" :collapsible="false">
    <div class="p-fleet">
      <BaseTabs v-model="tab" :tabs="tabs" />

      <!-- ── Vehículos ── -->
      <section v-if="tab === 'vehicles'" class="p-fleet__section" aria-label="Mis vehículos">
        <BaseTable :columns="vehicleColumns" :rows="myVehicles" :row-key="(v) => v.id" empty-text="Sin vehículos">
          <template #cell-type="{ row }">{{ typeName(row.vehicle_type_id) }}</template>
          <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
          <template #cell-wear_pct="{ row }"><span class="e-num">{{ row.wear_pct }} %</span></template>
          <template #cell-fuel="{ row }"><span class="e-num">{{ formatQuantity(row.fuel) }}</span></template>
          <template #cell-position="{ row }">{{ positionText(row) }}</template>
          <template #cell-route="{ row }">{{ routeName(row.route_id) }}</template>
          <template #cell-actions="{ row }">
            <div class="p-fleet__row-actions">
              <BaseSelect
                v-model="routeChoice[row.id]"
                :options="routeOptions"
                placeholder="(sin ruta)"
              />
              <BaseButton size="sm" :disabled="busy || row.status === 'sealed'" @click="assignRoute(row)">Asignar</BaseButton>
              <BaseButton size="sm" :disabled="busy || row.status === 'sealed'" @click="scheduleMaintenance(row)">Mant.</BaseButton>
            </div>
          </template>
        </BaseTable>
      </section>

      <!-- ── Comprar ── -->
      <section v-else-if="tab === 'buy'" class="p-fleet__section" aria-label="Comprar vehículo">
        <form class="p-fleet__form" @submit.prevent="buyVehicle">
          <BaseSelect v-model="buyType" label="Tipo (catálogo)" :options="vehicleTypeOptions" placeholder="Elegir…" required />
          <BaseSelect v-model="buyNode" label="Nodo de entrega" :options="nodeOptions" placeholder="Elegir…" required />
          <BaseButton type="submit" variant="primary" :disabled="busy">Comprar</BaseButton>
        </form>
        <p v-if="selectedType !== null" class="p-fleet__faint">
          Precio: <MoneyText :amount="selectedType.purchase_price" /> · capacidad:
          <span class="e-num">{{ formatQuantity(selectedType.cargo_capacity) }}</span> · coste/día:
          <MoneyText :amount="selectedType.operating_cost_per_day" />
        </p>
        <p v-if="buyError" class="p-fleet__error" role="alert">{{ buyError }}</p>
      </section>

      <!-- ── Rutas ── -->
      <section v-else class="p-fleet__section" aria-label="Rutas propias">
        <BaseTable :columns="routeColumns" :rows="routes" :row-key="(r) => r.id" empty-text="Sin rutas">
          <template #cell-legs="{ row }"><span class="e-num">{{ row.legs.length }}</span></template>
          <template #cell-active="{ row }">
            <BaseBadge :variant="row.active ? 'success' : 'neutral'">{{ row.active ? 'sí' : 'no' }}</BaseBadge>
          </template>
        </BaseTable>

        <h4 class="p-fleet__subtitle">Crear ruta desde plan</h4>
        <form class="p-fleet__form" @submit.prevent="calculatePlan">
          <BaseSelect v-model="planOrigin" label="Origen" :options="nodeOptions" placeholder="Elegir…" required />
          <BaseSelect v-model="planDestination" label="Destino" :options="nodeOptions" placeholder="Elegir…" required />
          <BaseButton type="submit" variant="primary" :disabled="busy">Calcular plan</BaseButton>
        </form>
        <p v-if="planError" class="p-fleet__error" role="alert">{{ planError }}</p>

        <div v-if="plan !== null" class="p-fleet__plan">
          <p>
            Plan sugerido — ETA total: <span class="e-num">{{ formatSimDuration(plan.total_eta_sim_seconds) }}</span> (sim)
            <template v-if="plan.estimated_cost !== undefined">
              · coste estimado: <MoneyText :amount="plan.estimated_cost" />
            </template>
          </p>
          <ol class="p-fleet__legs">
            <li v-for="leg in plan.legs" :key="leg.seq">
              <BaseBadge variant="info">{{ leg.mode }}</BaseBadge>
              tramo {{ leg.link_id.slice(0, 8) }}… · <span class="e-num">{{ formatSimDuration(leg.eta_sim_seconds) }}</span>
              <span v-if="leg.transshipment_terminal_id !== undefined"> · transbordo</span>
            </li>
          </ol>
          <p class="p-fleet__faint">ETAs con la congestión EMA vigente: estimación informativa, no garantía.</p>
          <div class="p-fleet__form">
            <BaseInput v-model="newRouteName" label="Nombre de la ruta" required />
            <BaseButton variant="primary" :disabled="busy" @click="saveRoute">Guardar como ruta</BaseButton>
          </div>
        </div>
      </section>
    </div>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-fleet {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;

  &__section {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  &__form {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 0.75rem;

    > * {
      min-width: 9rem;
    }
  }

  &__row-actions {
    display: flex;
    align-items: center;
    gap: 0.375rem;
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

  &__error {
    color: var(--ii-error);
    font-size: 0.875rem;
  }

  &__plan {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    border: 1px solid var(--ii-border-subtle);
    border-radius: 3px;
    padding: 0.75rem;
    font-size: 0.875rem;
  }

  &__legs {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
    padding-left: 1.25rem;
  }
}
</style>
