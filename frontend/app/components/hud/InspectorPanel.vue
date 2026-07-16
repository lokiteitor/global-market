<!--
  InspectorPanel — inspector polimórfico según ui.store.selection.
  Observable vs Comandable (FAD §5.3/C13): los comandos se deshabilitan
  preventivamente sobre entidades ajenas con la nota 'no es tuyo'; el servidor
  revalida (403) en cualquier caso. Solo validación de forma en los inputs.
-->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { Building, City, Contract, Publication, Vehicle } from '~/lib/api/types'
import { formatQuantity, mulByQty, parseQuantity, quantityOf, ZERO_QUANTITY } from '~/lib/kernel/money'
import { useApi } from '~/composables/useApi'
import { NOT_YOURS_NOTE, useOwnership } from '~/composables/useOwnership'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCitiesStore } from '~/stores/cities.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useShipmentsStore } from '~/stores/shipments.store'
import { useUiStore, type SelectionKind } from '~/stores/ui.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import MoneyText from '~/components/base/MoneyText.vue'
import SimTimeText from '~/components/base/SimTimeText.vue'

/** Alias del SelectionKind de ui.store (ya incluye publication/contract). */
type InspectorKind = SelectionKind

const ui = useUiStore()
const buildings = useBuildingsStore()
const fleet = useFleetStore()
const cities = useCitiesStore()
const market = useMarketStore()
const world = useWorldStore()
const shipments = useShipmentsStore()
const notifications = useNotificationsStore()
const ownership = useOwnership()
const api = useApi()

const busy = ref(false)

const kind = computed<InspectorKind | null>(() => (ui.selection?.kind as InspectorKind | undefined) ?? null)
const selectedId = computed(() => ui.selection?.id ?? null)

// ── Entidades seleccionadas (lectura de stores) ──────────────────────────────
const building = computed<Building | null>(() =>
  kind.value === 'building' && selectedId.value !== null ? (buildings.byId[selectedId.value] ?? null) : null
)
const vehicle = computed<Vehicle | null>(() =>
  kind.value === 'vehicle' && selectedId.value !== null ? (fleet.byId[selectedId.value] ?? null) : null
)
const city = computed<City | null>(() =>
  kind.value === 'city' && selectedId.value !== null ? (cities.byId[selectedId.value] ?? null) : null
)
const publication = computed<Publication | null>(() => {
  if (kind.value !== 'publication' || selectedId.value === null) return null
  return market.boardResults.find((p) => p.id === selectedId.value) ?? market.myPublications.byId[selectedId.value] ?? null
})
const contract = computed<Contract | null>(() =>
  kind.value === 'contract' && selectedId.value !== null ? (market.contractsById[selectedId.value] ?? null) : null
)
const deposit = computed(() =>
  kind.value === 'deposit' && selectedId.value !== null ? (world.deposits[selectedId.value] ?? null) : null
)

const isMine = computed(() => {
  if (building.value !== null) return ownership.isMine(building.value.owner_account_id)
  if (vehicle.value !== null) return ownership.isMine(vehicle.value.owner_account_id)
  if (publication.value !== null) return ownership.isMine(publication.value.publisher_account_id)
  return false
})

const notYoursNote = NOT_YOURS_NOTE

// ── Datos derivados ──────────────────────────────────────────────────────────
const buildingTypeName = computed(() => {
  if (building.value === null) return '—'
  return world.buildingTypes[building.value.building_type_id]?.name ?? building.value.building_type_id
})
const activeRecipeName = computed(() => {
  const id = building.value?.active_recipe_id
  if (id === undefined) return null
  return world.recipes[id]?.name ?? id
})
const buildingInventory = computed(() => (building.value !== null ? buildings.inventoryOf(building.value.id) : []))
const buildingBatches = computed(() =>
  building.value !== null ? (buildings.batchesByBuilding[building.value.id] ?? []) : []
)
const vehicleTypeName = computed(() => {
  if (vehicle.value === null) return '—'
  return world.vehicleTypes[vehicle.value.vehicle_type_id]?.name ?? vehicle.value.vehicle_type_id
})
const vehicleCargo = computed(() => (vehicle.value !== null ? (shipments.byVehicle[vehicle.value.id] ?? []) : []))
const cityDemand = computed(() => (city.value !== null ? cities.demandOf(city.value.id) : []))

function productName(productId: string): string {
  return world.products[productId]?.code ?? productId.slice(0, 8)
}

function positionText(v: Vehicle): string {
  if (v.position.at_node_id !== undefined) return `detenido en nodo ${v.position.at_node_id.slice(0, 8)}…`
  if (v.position.on_segment_id !== undefined) {
    const pct = v.position.segment_progress_pct !== undefined ? ` (${Math.round(v.position.segment_progress_pct)} %)` : ''
    return `en tramo ${v.position.on_segment_id.slice(0, 8)}…${pct}`
  }
  return 'posición desconocida'
}

const contractFillPct = computed(() => {
  const c = contract.value
  if (c === null) return '0'
  if (c.fill_bp !== undefined) return (c.fill_bp / 100).toFixed(1)
  // Derivado de presentación con BigInt (sin float sobre Quantity).
  const agreed = BigInt(c.quantity_agreed)
  if (agreed === 0n) return '0'
  return ((BigInt(c.quantity_delivered) * 1000n) / agreed / 10n).toString()
})

// ── Fetch-on-select (pull REST hacia las stores) ─────────────────────────────
watch(
  () => [kind.value, selectedId.value] as const,
  async ([k, id]) => {
    if (id === null) return
    if (k === 'building' && ownership.isMine(buildings.byId[id]?.owner_account_id)) {
      const [inv, batches] = await Promise.all([api.getBuildingInventory(id), api.listProductionBatches(id)])
      if (inv.ok) buildings.setInventory(id, inv.value.data)
      if (batches.ok) buildings.setBatches(id, batches.value.data)
    } else if (k === 'city') {
      const demand = await api.getCityDemand(id)
      if (demand.ok) cities.setDemand(id, demand.value.data)
    }
  },
  { immediate: true }
)

// ── Comandos (intenciones; el servidor decide, P1) ───────────────────────────
const queueCount = ref('1')

async function queueBatches(): Promise<void> {
  const b = building.value
  if (b === null || b.active_recipe_id === undefined) return
  const n = Number.parseInt(queueCount.value, 10)
  if (!Number.isInteger(n) || n < 1) return
  busy.value = true
  const result = await api.queueProductionBatches(b.id, b.active_recipe_id, n)
  busy.value = false
  if (result.ok) {
    buildings.upsertBatch(result.value.data)
    notifications.push({ level: 'success', text: `${n} lote(s) encolado(s) en ${buildingTypeName.value}` })
  } else {
    notifications.push({ level: 'error', text: `No se pudo encolar: ${result.error.message}` })
  }
}

async function upgradeBuilding(): Promise<void> {
  const b = building.value
  if (b === null) return
  busy.value = true
  const result = await api.upgradeBuilding(b.id)
  busy.value = false
  if (result.ok) {
    buildings.applyPatch([{ op: 'upsert', entity: 'building', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Mejora iniciada' })
  } else {
    notifications.push({ level: 'error', text: `No se pudo mejorar: ${result.error.message}` })
  }
}

const acceptQty = ref('')

async function acceptPublication(): Promise<void> {
  const p = publication.value
  if (p === null) return
  const parsed = parseQuantity(acceptQty.value.trim())
  if (!parsed.ok) return
  busy.value = true
  const result = await api.acceptPublication(p.id, parsed.value)
  busy.value = false
  if (result.ok) {
    market.upsertAcceptances([result.value.data])
    notifications.push({ level: 'success', text: 'Aceptación registrada: pendiente del sorteo' })
  } else {
    notifications.push({ level: 'error', text: `Aceptación rechazada: ${result.error.message}` })
  }
}
</script>

<template>
  <div class="hud-inspector">
    <p v-if="kind === null" class="hud-inspector__empty">Selecciona una entidad del mundo para inspeccionarla.</p>

    <!-- ── Edificio ── -->
    <div v-else-if="kind === 'building'" class="hud-inspector__section">
      <template v-if="building !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">{{ buildingTypeName }}</h3>
          <BaseBadge :status="building.status" />
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Nivel</dt><dd class="e-num">{{ building.level }}</dd></div>
          <div><dt>Condición</dt><dd class="e-num">{{ building.condition_pct }} %</dd></div>
          <div v-if="activeRecipeName !== null"><dt>Receta</dt><dd>{{ activeRecipeName }}</dd></div>
        </dl>

        <p v-if="!isMine" class="hud-inspector__note" data-test="not-yours">{{ notYoursNote }}</p>

        <template v-if="isMine">
          <h4 class="hud-inspector__subtitle">Inventario</h4>
          <ul class="hud-inspector__list" role="list">
            <li v-if="buildingInventory.length === 0" class="hud-inspector__faint">vacío</li>
            <li v-for="item in buildingInventory" :key="item.product_id" class="hud-inspector__row">
              <span>{{ productName(item.product_id) }}</span>
              <span class="e-num">{{ formatQuantity(item.quantity) }}</span>
            </li>
          </ul>

          <h4 class="hud-inspector__subtitle">Cola de lotes</h4>
          <ul class="hud-inspector__list" role="list">
            <li v-if="buildingBatches.length === 0" class="hud-inspector__faint">sin lotes</li>
            <li v-for="batch in buildingBatches" :key="batch.id" class="hud-inspector__row">
              <span class="e-num">{{ batch.batches_done }}/{{ batch.batches_queued }}</span>
              <BaseBadge :status="batch.status" />
            </li>
          </ul>
        </template>

        <div class="hud-inspector__actions">
          <BaseInput
            v-model="queueCount"
            type="number"
            label="Lotes"
            :min="1"
            :disabled="!isMine || building.active_recipe_id === undefined"
          />
          <BaseButton
            variant="primary"
            size="sm"
            data-test="cmd-queue"
            :disabled="!isMine || busy || building.active_recipe_id === undefined"
            :title="!isMine ? notYoursNote : undefined"
            @click="queueBatches"
          >
            Encolar
          </BaseButton>
          <BaseButton
            size="sm"
            data-test="cmd-upgrade"
            :disabled="!isMine || busy"
            :title="!isMine ? notYoursNote : undefined"
            @click="upgradeBuilding"
          >
            Mejorar
          </BaseButton>
        </div>
      </template>
      <p v-else class="hud-inspector__faint">Edificio no replicado todavía.</p>
    </div>

    <!-- ── Vehículo ── -->
    <div v-else-if="kind === 'vehicle'" class="hud-inspector__section">
      <template v-if="vehicle !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">{{ vehicleTypeName }}</h3>
          <BaseBadge :status="vehicle.status" />
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Desgaste</dt><dd class="e-num">{{ vehicle.wear_pct }} %</dd></div>
          <div><dt>Combustible</dt><dd class="e-num">{{ formatQuantity(vehicle.fuel) }}</dd></div>
          <div><dt>Posición</dt><dd>{{ positionText(vehicle) }}</dd></div>
          <div v-if="vehicle.route_id !== undefined"><dt>Ruta</dt><dd class="e-num">{{ vehicle.route_id.slice(0, 8) }}…</dd></div>
        </dl>
        <p v-if="!isMine" class="hud-inspector__note" data-test="not-yours">{{ notYoursNote }}</p>
        <template v-else>
          <h4 class="hud-inspector__subtitle">Carga</h4>
          <ul class="hud-inspector__list" role="list">
            <li v-if="vehicleCargo.length === 0" class="hud-inspector__faint">sin carga</li>
            <li v-for="s in vehicleCargo" :key="s.id" class="hud-inspector__row">
              <span>{{ productName(s.product_id) }}</span>
              <span class="e-num">{{ formatQuantity(s.quantity) }}</span>
            </li>
          </ul>
        </template>
      </template>
      <p v-else class="hud-inspector__faint">Vehículo no replicado todavía.</p>
    </div>

    <!-- ── Ciudad ── -->
    <div v-else-if="kind === 'city'" class="hud-inspector__section">
      <template v-if="city !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">{{ city.name }}</h3>
          <BaseBadge variant="info">nivel {{ city.level }}</BaseBadge>
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Población</dt><dd class="e-num">{{ city.population.toLocaleString('es-ES') }}</dd></div>
          <div><dt>Índice</dt><dd class="e-num">{{ city.supply_index }}</dd></div>
        </dl>
        <h4 class="hud-inspector__subtitle">Demanda por producto</h4>
        <ul class="hud-inspector__list" role="list">
          <li v-if="cityDemand.length === 0" class="hud-inspector__faint">sin datos de demanda</li>
          <li v-for="d in cityDemand" :key="d.product_id" class="hud-inspector__row">
            <span>{{ productName(d.product_id) }}</span>
            <MoneyText :amount="d.current_price" />
          </li>
        </ul>
      </template>
      <p v-else class="hud-inspector__faint">Ciudad no replicada todavía.</p>
    </div>

    <!-- ── Publicación ── -->
    <div v-else-if="kind === 'publication'" class="hud-inspector__section">
      <template v-if="publication !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">{{ publication.kind }} · {{ publication.product_id !== undefined ? productName(publication.product_id) : '—' }}</h3>
          <BaseBadge :status="publication.status" />
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Restante</dt><dd class="e-num">{{ formatQuantity(publication.quantity_remaining) }}</dd></div>
          <div><dt>Precio ud.</dt><dd><MoneyText :amount="publication.unit_price" /></dd></div>
          <div><dt>Lote mín.</dt><dd class="e-num">{{ formatQuantity(publication.min_lot) }}</dd></div>
          <div><dt>Plazo</dt><dd><SimTimeText :sim-seconds="publication.published_at_sim + publication.delivery_sim_seconds" verb="entrega" /></dd></div>
        </dl>
        <div v-if="!isMine" class="hud-inspector__actions">
          <BaseInput v-model="acceptQty" type="number" label="Cantidad" :min="1" />
          <BaseButton variant="primary" size="sm" :disabled="busy" @click="acceptPublication">Aceptar</BaseButton>
        </div>
        <p v-else class="hud-inspector__faint">Publicación propia: gestiónala en el panel de mercado.</p>
        <p v-if="!isMine && acceptQty !== ''" class="hud-inspector__faint">
          Se bloqueará ≈ <MoneyText :amount="mulByQty(publication.unit_price, parseQuantity(acceptQty.trim()).ok ? quantityOf(acceptQty.trim()) : ZERO_QUANTITY)" /> según tu rol (el servidor calcula la garantía exacta).
        </p>
      </template>
      <p v-else class="hud-inspector__faint">Publicación no disponible: consulta el tablón.</p>
    </div>

    <!-- ── Contrato ── -->
    <div v-else-if="kind === 'contract'" class="hud-inspector__section">
      <template v-if="contract !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">Contrato · {{ productName(contract.product_id) }}</h3>
          <BaseBadge :status="contract.status" />
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Fill</dt><dd class="e-num">{{ contractFillPct }} %</dd></div>
          <div>
            <dt>Entregado</dt>
            <dd class="e-num">{{ formatQuantity(contract.quantity_delivered) }} / {{ formatQuantity(contract.quantity_agreed) }}</dd>
          </div>
          <div><dt>Plazo</dt><dd><SimTimeText :sim-seconds="contract.deadline_sim" /></dd></div>
          <div><dt>Comprador</dt><dd class="e-num">{{ contract.buyer_account_id.slice(0, 8) }}…</dd></div>
          <div><dt>Vendedor</dt><dd class="e-num">{{ contract.seller_account_id.slice(0, 8) }}…</dd></div>
        </dl>
      </template>
      <p v-else class="hud-inspector__faint">Contrato no replicado (solo se replican los propios).</p>
    </div>

    <!-- ── Yacimiento ── -->
    <div v-else-if="kind === 'deposit'" class="hud-inspector__section">
      <template v-if="deposit !== null">
        <header class="hud-inspector__head">
          <h3 class="hud-inspector__title">Yacimiento · {{ productName(deposit.product_id) }}</h3>
          <BaseBadge :variant="deposit.renewable ? 'success' : 'warning'">{{ deposit.renewable ? 'renovable' : 'finito' }}</BaseBadge>
        </header>
        <dl class="hud-inspector__facts">
          <div><dt>Restante</dt><dd class="e-num">{{ formatQuantity(deposit.remaining_amount) }}</dd></div>
        </dl>
      </template>
      <p v-else class="hud-inspector__faint">Yacimiento fuera de catálogo.</p>
    </div>

    <!-- ── Nodo ── -->
    <div v-else class="hud-inspector__section">
      <p class="hud-inspector__faint">Nodo logístico {{ selectedId?.slice(0, 8) }}…</p>
    </div>
  </div>
</template>

<style lang="scss" scoped>
.hud-inspector {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
  font-size: 0.875rem;

  &__empty,
  &__faint {
    color: var(--ii-text-faint);
    font-size: 0.8125rem;
  }

  &__section {
    display: flex;
    flex-direction: column;
    gap: 0.625rem;
  }

  &__head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }

  &__title {
    font-size: 0.9375rem;
  }

  &__subtitle {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-text-muted);
  }

  &__facts {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;

    > div {
      display: flex;
      justify-content: space-between;
      gap: 0.5rem;
    }

    dt {
      color: var(--ii-text-muted);
    }
  }

  &__list {
    display: flex;
    flex-direction: column;
    gap: 0.25rem;
  }

  &__row {
    display: flex;
    justify-content: space-between;
    gap: 0.5rem;
  }

  &__note {
    color: var(--ii-warning);
    font-size: 0.8125rem;
    border: 1px dashed var(--ii-warning);
    border-radius: 3px;
    padding: 0.375rem 0.5rem;
  }

  &__actions {
    display: flex;
    align-items: flex-end;
    gap: 0.5rem;
  }
}
</style>
