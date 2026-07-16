<!--
  MarketPanel — tablón CCRI (pull explícito, C10), publicaciones propias,
  contratos y OHLC. El cliente solo envía intenciones y refleja resultados
  (P1): garantías, sorteo y liquidación son del servidor. Validación de FORMA
  únicamente (campos requeridos, mínimos de UI).
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Contract, ContractDelivery, NetworkNode, OhlcCandle, Publication } from '~/lib/api/types'
import { cmpMoney, formatQuantity, mulByQty, parseMoney, parseQuantity, ZERO_MONEY, type Quantity } from '~/lib/kernel/money'
import { formatSimTime } from '~/lib/kernel/simtime'
import { gameHoursToSimSeconds, useApi } from '~/composables/useApi'
import { useOwnership } from '~/composables/useOwnership'
import { useMarketStore, type BoardFilters } from '~/stores/market.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useSimStore } from '~/stores/sim.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import BaseModal from '~/components/base/BaseModal.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseSelect from '~/components/base/BaseSelect.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import BaseTabs from '~/components/base/BaseTabs.vue'
import CountdownText from '~/components/base/CountdownText.vue'
import MoneyText from '~/components/base/MoneyText.vue'
import SimTimeText from '~/components/base/SimTimeText.vue'

const emit = defineEmits<{
  /** Intent de aceptación confirmado por el jugador (cantidad validada en forma). */
  'intent:accept': [payload: { publicationId: string; quantity: string }]
}>()

const api = useApi()
const market = useMarketStore()
const world = useWorldStore()
const buildings = useBuildingsStore()
const sim = useSimStore()
const notifications = useNotificationsStore()
const ownership = useOwnership()

const tab = ref('board')
const tabs = computed(() => [
  { id: 'board', label: 'Tablón' },
  { id: 'mine', label: 'Mis publicaciones', badge: market.myOpenPublications.length },
  { id: 'contracts', label: 'Contratos', badge: market.activeContracts.length },
  { id: 'publish', label: 'Publicar' },
  { id: 'ohlc', label: 'OHLC' }
])

const busy = ref(false)

// ─── Catálogos mínimos (pull una vez) ────────────────────────────────────────
onMounted(async () => {
  if (!world.loaded.products) {
    const r = await api.listProducts()
    if (r.ok) world.setProducts(r.value.data)
  }
  if (!world.loaded.regions) {
    const r = await api.listRegions()
    if (r.ok) world.setRegions(r.value.data)
  }
})

const productOptions = computed(() => world.productList.map((p) => ({ value: p.id, label: `${p.code} — ${p.name}` })))
const regionOptions = computed(() => world.regionList.map((r) => ({ value: r.id, label: r.name })))

function productLabel(productId: string | undefined): string {
  if (productId === undefined) return '—'
  return world.products[productId]?.code ?? `${productId.slice(0, 8)}…`
}

// ─── Tablón (pull con filtros + auto-refresh opcional) ───────────────────────
const filterKind = ref('')
const filterProduct = ref('')
const filterRegion = ref('')
const filterMaxPrice = ref('')
const priceError = ref<string | undefined>(undefined)

async function refreshBoard(): Promise<void> {
  priceError.value = undefined
  const filters: BoardFilters = {}
  if (filterKind.value !== '') filters.kind = filterKind.value as 'sell' | 'buy'
  if (filterProduct.value !== '') filters.productId = filterProduct.value
  if (filterRegion.value !== '') filters.regionId = filterRegion.value
  if (filterMaxPrice.value.trim() !== '') {
    const parsed = parseMoney(filterMaxPrice.value.trim())
    if (!parsed.ok) {
      priceError.value = 'Importe inválido (entero de punto fijo)'
      return
    }
    filters.maxPrice = parsed.value
  }
  busy.value = true
  const result = await api.queryBoard({
    ...(filters.kind !== undefined ? { kind: filters.kind } : {}),
    ...(filters.productId !== undefined ? { product_id: filters.productId } : {}),
    ...(filters.regionId !== undefined ? { origin_region_id: filters.regionId } : {}),
    ...(filters.maxPrice !== undefined ? { max_unit_price: filters.maxPrice } : {}),
    limit: 100
  })
  busy.value = false
  if (result.ok) {
    market.setBoardResults(filters, result.value.data, result.value.meta.sim_time_seconds)
  } else {
    notifications.push({ level: 'error', text: `Tablón: ${result.error.message}` })
  }
}

const autoRefresh = ref(false)
let autoTimer: ReturnType<typeof setInterval> | null = null
watch(autoRefresh, (enabled) => {
  if (autoTimer !== null) {
    clearInterval(autoTimer)
    autoTimer = null
  }
  if (enabled) autoTimer = setInterval(() => void refreshBoard(), 30_000)
})
onBeforeUnmount(() => {
  if (autoTimer !== null) clearInterval(autoTimer)
})

const boardColumns: TableColumn[] = [
  { key: 'kind', label: 'Tipo' },
  { key: 'product', label: 'Producto' },
  { key: 'quantity_remaining', label: 'Restante', align: 'right' },
  { key: 'unit_price', label: 'Precio ud.', align: 'right' },
  { key: 'min_lot', label: 'Lote mín.', align: 'right' },
  { key: 'window', label: 'Sorteo' },
  { key: 'actions', label: '' }
]

// ─── Aceptar (modal con countdown de la ventana de sorteo) ───────────────────
const acceptTarget = ref<Publication | null>(null)
const acceptQty = ref('')
const acceptError = ref<string | undefined>(undefined)

function openAccept(pub: Publication): void {
  acceptTarget.value = pub
  acceptQty.value = pub.min_lot
  acceptError.value = undefined
}

/** Validación de FORMA: entero, ≥ min_lot, ≤ restante. El servidor revalida. */
function validateAcceptQty(): Quantity | null {
  const target = acceptTarget.value
  if (target === null) return null
  const parsed = parseQuantity(acceptQty.value.trim())
  if (!parsed.ok) {
    acceptError.value = 'Cantidad inválida (entero positivo)'
    return null
  }
  if (BigInt(parsed.value) < BigInt(target.min_lot)) {
    acceptError.value = `Mínimo de aceptación: ${formatQuantity(target.min_lot)}`
    return null
  }
  if (BigInt(parsed.value) > BigInt(target.quantity_remaining)) {
    acceptError.value = `Solo quedan ${formatQuantity(target.quantity_remaining)}`
    return null
  }
  acceptError.value = undefined
  return parsed.value
}

const acceptBlockNotice = computed(() => {
  const target = acceptTarget.value
  if (target === null) return null
  const parsed = parseQuantity(acceptQty.value.trim())
  if (!parsed.ok) return null
  const value = mulByQty(target.unit_price, parsed.value)
  // Aviso de qué se bloqueará: importes indicativos; la garantía exacta la calcula el servidor.
  if (target.kind === 'sell') return { role: 'comprador', text: 'se retendrá en escrow el 100 % del pago', amount: value }
  return { role: 'vendedor', text: 'se congelará el stock ofrecido y una garantía monetaria (~10 % del valor)', amount: value }
})

async function confirmAccept(): Promise<void> {
  const target = acceptTarget.value
  const quantity = validateAcceptQty()
  if (target === null || quantity === null) return
  emit('intent:accept', { publicationId: target.id, quantity })
  busy.value = true
  const result = await api.acceptPublication(target.id, quantity)
  busy.value = false
  if (result.ok) {
    market.upsertAcceptances([result.value.data])
    notifications.push({ level: 'success', text: 'Aceptación registrada: pendiente del sorteo' })
    acceptTarget.value = null
  } else {
    acceptError.value = `${result.error.code}: ${result.error.message}`
  }
}

// ─── Mis publicaciones (cancelar con aviso de cooldown) ──────────────────────
const cancelTarget = ref<Publication | null>(null)

const cancelCooldownActive = computed(() => {
  const t = cancelTarget.value
  if (t === null || t.cancel_cooldown_until === undefined) return false
  return Date.parse(t.cancel_cooldown_until) > Date.now()
})

async function confirmCancel(): Promise<void> {
  const target = cancelTarget.value
  if (target === null) return
  busy.value = true
  const result = await api.cancelPublication(target.id)
  busy.value = false
  if (result.ok) {
    market.applyPatch([{ op: 'upsert', entity: 'publication', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Publicación cancelada; garantía restante liberada' })
    cancelTarget.value = null
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

const mineColumns: TableColumn[] = [
  { key: 'kind', label: 'Tipo' },
  { key: 'product', label: 'Producto' },
  { key: 'quantity_remaining', label: 'Restante', align: 'right' },
  { key: 'unit_price', label: 'Precio ud.', align: 'right' },
  { key: 'status', label: 'Estado' },
  { key: 'actions', label: '' }
]

// ─── Contratos ───────────────────────────────────────────────────────────────
const contractColumns: TableColumn[] = [
  { key: 'product', label: 'Producto' },
  { key: 'fill', label: 'Fill', align: 'right' },
  { key: 'deadline', label: 'Plazo' },
  { key: 'role', label: 'Rol' },
  { key: 'status', label: 'Estado' },
  { key: 'actions', label: '' }
]

const contractList = computed(() => Object.values(market.contractsById))

function contractFill(c: Contract): string {
  if (c.fill_bp !== undefined) return `${(c.fill_bp / 100).toFixed(1)} %`
  const agreed = BigInt(c.quantity_agreed)
  if (agreed === 0n) return '0 %'
  return `${((BigInt(c.quantity_delivered) * 1000n) / agreed / 10n).toString()} %`
}

function contractRole(c: Contract): string {
  return ownership.isMine(c.buyer_account_id) ? 'comprador' : 'vendedor'
}

const deliveriesFor = ref<string | null>(null)
const deliveries = ref<ContractDelivery[]>([])

async function loadDeliveries(contract: Contract): Promise<void> {
  deliveriesFor.value = contract.id
  const result = await api.listContractDeliveries(contract.id)
  deliveries.value = result.ok ? result.value.data : []
}

async function refreshContracts(): Promise<void> {
  const result = await api.listContracts()
  if (result.ok) market.applySnapshot('rest:contracts', { contracts: result.value.data })
}

watch(tab, (t) => {
  if (t === 'contracts') void refreshContracts()
})

// ─── Publicar ────────────────────────────────────────────────────────────────
const pubKind = ref<'sell' | 'buy'>('sell')
const pubProduct = ref('')
const pubQuantity = ref('')
const pubPrice = ref('')
const pubMinLot = ref('1')
const pubPlazoHoras = ref('48')
const pubOrigin = ref('')
const pubDestination = ref('')
const pubError = ref<string | undefined>(undefined)

const nodes = ref<NetworkNode[]>([])
watch(tab, async (t) => {
  if (t === 'publish' && nodes.value.length === 0) {
    const result = await api.listNetworkNodes()
    if (result.ok) nodes.value = result.value.data
  }
})

/** Nodos de edificios propios (almacenes de origen para sell). */
const myBuildingNodeOptions = computed(() => {
  const myId = ownership.myAccountId.value
  if (myId === null) return []
  const mine = new Set(buildings.ownedBy(myId).map((b) => b.id))
  return nodes.value
    .filter((n) => n.building_id !== undefined && mine.has(n.building_id))
    .map((n) => ({ value: n.id, label: `${n.kind} · ${n.id.slice(0, 8)}…` }))
})

const nodeOptions = computed(() => nodes.value.map((n) => ({ value: n.id, label: `${n.kind} · ${n.id.slice(0, 8)}…` })))

async function submitPublication(): Promise<void> {
  pubError.value = undefined
  const qty = parseQuantity(pubQuantity.value.trim())
  const price = parseMoney(pubPrice.value.trim())
  const minLot = parseQuantity(pubMinLot.value.trim())
  const hours = Number.parseFloat(pubPlazoHoras.value)
  if (pubProduct.value === '') {
    pubError.value = 'Elige un producto'
    return
  }
  if (!qty.ok || BigInt(qty.value) === 0n) {
    pubError.value = 'Cantidad inválida'
    return
  }
  if (!price.ok || cmpMoney(price.value, ZERO_MONEY) <= 0) {
    pubError.value = 'Precio inválido'
    return
  }
  if (!minLot.ok) {
    pubError.value = 'Lote mínimo inválido'
    return
  }
  if (!Number.isFinite(hours) || hours <= 0) {
    pubError.value = 'Plazo inválido (horas de juego)'
    return
  }
  if (pubKind.value === 'sell' && pubOrigin.value === '') {
    pubError.value = 'Elige el almacén de origen (el stock debe existir físicamente)'
    return
  }
  if (pubKind.value === 'buy' && pubDestination.value === '') {
    pubError.value = 'Elige el nodo de destino'
    return
  }

  busy.value = true
  const result = await api.createPublication({
    kind: pubKind.value,
    product_id: pubProduct.value,
    quantity_total: qty.value,
    unit_price: price.value,
    min_lot: minLot.value,
    // Plazo del formulario en horas de JUEGO → sim_seconds (kernel del panel).
    delivery_sim_seconds: gameHoursToSimSeconds(hours),
    ...(pubKind.value === 'sell' ? { origin_node_id: pubOrigin.value } : { destination_node_id: pubDestination.value })
  })
  busy.value = false
  if (result.ok) {
    market.applyPatch([{ op: 'upsert', entity: 'publication', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Publicada: garantía bloqueada y ventana de sorteo abierta' })
    pubQuantity.value = ''
    pubPrice.value = ''
  } else {
    pubError.value = `${result.error.code}: ${result.error.message}`
  }
}

// ─── OHLC ────────────────────────────────────────────────────────────────────
const ohlcProduct = ref('')
const ohlcRegion = ref('')
const ohlcBucket = ref('3600')
const candles = ref<OhlcCandle[]>([])

async function refreshOhlc(): Promise<void> {
  if (ohlcProduct.value === '') return
  const result = await api.getMarketOhlc({
    product_id: ohlcProduct.value,
    ...(ohlcRegion.value !== '' ? { region_id: ohlcRegion.value } : {}),
    bucket_sim_secs: Number.parseInt(ohlcBucket.value, 10),
    limit: 48
  })
  candles.value = result.ok ? result.value.data : []
}

const ohlcColumns: TableColumn[] = [
  { key: 'bucket', label: 'Bucket (sim)' },
  { key: 'open_price', label: 'Open', align: 'right' },
  { key: 'high_price', label: 'High', align: 'right' },
  { key: 'low_price', label: 'Low', align: 'right' },
  { key: 'close_price', label: 'Close', align: 'right' },
  { key: 'volume', label: 'Vol.', align: 'right' }
]
</script>

<template>
  <BasePanel title="Mercado — tablón CCRI" :collapsible="false">
    <div class="p-market">
      <BaseTabs v-model="tab" :tabs="tabs" />

      <!-- ── Tablón ── -->
      <section v-if="tab === 'board'" class="p-market__section" aria-label="Tablón global">
        <div class="p-market__filters">
          <BaseSelect v-model="filterKind" label="Tipo" :options="[{ value: 'sell', label: 'Venta' }, { value: 'buy', label: 'Compra' }]" placeholder="(todos)" />
          <BaseSelect v-model="filterProduct" label="Producto" :options="productOptions" placeholder="(todos)" />
          <BaseSelect v-model="filterRegion" label="Región origen" :options="regionOptions" placeholder="(todas)" />
          <BaseInput v-model="filterMaxPrice" label="Precio máx." type="number" :min="1" :error="priceError" />
          <BaseButton variant="primary" :disabled="busy" data-test="refresh-board" @click="refreshBoard">Actualizar</BaseButton>
          <label class="p-market__auto">
            <input v-model="autoRefresh" type="checkbox" /> auto 30 s
          </label>
        </div>

        <BaseTable :columns="boardColumns" :rows="market.boardResults" :row-key="(p) => p.id" data-test="board-table">
          <template #cell-kind="{ row }">
            <BaseBadge :variant="row.kind === 'sell' ? 'success' : 'info'">{{ row.kind === 'sell' ? 'venta' : 'compra' }}</BaseBadge>
          </template>
          <template #cell-product="{ row }">{{ productLabel(row.product_id) }}</template>
          <template #cell-quantity_remaining="{ row }"><span class="e-num">{{ formatQuantity(row.quantity_remaining) }}</span></template>
          <template #cell-unit_price="{ row }"><MoneyText :amount="row.unit_price" /></template>
          <template #cell-min_lot="{ row }"><span class="e-num">{{ formatQuantity(row.min_lot) }}</span></template>
          <template #cell-window="{ row }">
            <CountdownText v-if="row.window_closes_at !== undefined" :until="row.window_closes_at" />
            <span v-else class="p-market__faint">—</span>
          </template>
          <template #cell-actions="{ row }">
            <BaseButton
              size="sm"
              variant="primary"
              data-test="accept-btn"
              :disabled="ownership.isMine(row.publisher_account_id)"
              :title="ownership.isMine(row.publisher_account_id) ? 'Es tu propia publicación' : undefined"
              @click="openAccept(row)"
            >
              Aceptar
            </BaseButton>
          </template>
        </BaseTable>
        <p v-if="market.boardFetchedAtSim !== null" class="p-market__faint">
          Última consulta: día {{ formatSimTime(market.boardFetchedAtSim) }} (pull explícito; el tablón no es push)
        </p>
      </section>

      <!-- ── Mis publicaciones ── -->
      <section v-else-if="tab === 'mine'" class="p-market__section" aria-label="Mis publicaciones">
        <BaseTable :columns="mineColumns" :rows="market.myPublicationList" :row-key="(p) => p.id">
          <template #cell-kind="{ row }">{{ row.kind === 'sell' ? 'venta' : 'compra' }}</template>
          <template #cell-product="{ row }">{{ productLabel(row.product_id) }}</template>
          <template #cell-quantity_remaining="{ row }">
            <span class="e-num">{{ formatQuantity(row.quantity_remaining) }} / {{ formatQuantity(row.quantity_total) }}</span>
          </template>
          <template #cell-unit_price="{ row }"><MoneyText :amount="row.unit_price" /></template>
          <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
          <template #cell-actions="{ row }">
            <BaseButton
              v-if="row.status === 'open' || row.status === 'draw_window' || row.status === 'micro_window'"
              size="sm"
              variant="danger"
              @click="cancelTarget = row"
            >
              Cancelar
            </BaseButton>
          </template>
        </BaseTable>
      </section>

      <!-- ── Contratos ── -->
      <section v-else-if="tab === 'contracts'" class="p-market__section" aria-label="Contratos CCRI">
        <BaseTable :columns="contractColumns" :rows="contractList" :row-key="(c) => c.id">
          <template #cell-product="{ row }">{{ productLabel(row.product_id) }}</template>
          <template #cell-fill="{ row }"><span class="e-num">{{ contractFill(row) }}</span></template>
          <template #cell-deadline="{ row }"><SimTimeText :sim-seconds="row.deadline_sim" /></template>
          <template #cell-role="{ row }">{{ contractRole(row) }}</template>
          <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
          <template #cell-actions="{ row }">
            <BaseButton size="sm" @click="loadDeliveries(row)">Entregas</BaseButton>
          </template>
        </BaseTable>

        <div v-if="deliveriesFor !== null" class="p-market__deliveries">
          <h4 class="p-market__subtitle">Entregas del contrato {{ deliveriesFor.slice(0, 8) }}…</h4>
          <ul role="list">
            <li v-if="deliveries.length === 0" class="p-market__faint">sin entregas confirmadas</li>
            <li v-for="d in deliveries" :key="d.id" class="p-market__delivery">
              <span class="e-num">{{ formatQuantity(d.quantity) }}</span>
              <span>· día {{ formatSimTime(d.delivered_at_sim) }}</span>
              <BaseBadge :variant="d.on_time ? 'success' : 'danger'">{{ d.on_time ? 'a tiempo' : 'fuera de plazo' }}</BaseBadge>
            </li>
          </ul>
        </div>
      </section>

      <!-- ── Publicar ── -->
      <form v-else-if="tab === 'publish'" class="p-market__section p-market__form" aria-label="Publicar en el tablón" @submit.prevent="submitPublication">
        <div class="p-market__filters">
          <BaseSelect v-model="pubKind" label="Tipo" :options="[{ value: 'sell', label: 'Venta (ofrezco stock)' }, { value: 'buy', label: 'Compra (solicito)' }]" />
          <BaseSelect v-model="pubProduct" label="Producto" :options="productOptions" placeholder="Elegir…" required />
        </div>
        <div class="p-market__filters">
          <BaseInput v-model="pubQuantity" label="Cantidad" type="number" :min="1" required />
          <BaseInput v-model="pubPrice" label="Precio unitario" type="number" :min="1" required hint="unidades menores (punto fijo)" />
          <BaseInput v-model="pubMinLot" label="Lote mínimo" type="number" :min="1" />
          <BaseInput v-model="pubPlazoHoras" label="Plazo (horas de juego)" type="number" :min="1" hint="se convierte a sim-seconds" />
        </div>
        <div class="p-market__filters">
          <BaseSelect
            v-if="pubKind === 'sell'"
            v-model="pubOrigin"
            label="Almacén de origen"
            :options="myBuildingNodeOptions"
            placeholder="Nodo con tu stock…"
          />
          <BaseSelect v-else v-model="pubDestination" label="Nodo de destino" :options="nodeOptions" placeholder="Elegir…" />
          <BaseButton type="submit" variant="primary" :disabled="busy">Publicar</BaseButton>
        </div>
        <p v-if="pubError" class="p-market__error" role="alert">{{ pubError }}</p>
        <p class="p-market__faint">
          Al publicar se bloquea la garantía íntegra (sell: stock + 10 %; buy: 100 % en escrow) y se abre la ventana de sorteo.
          El servidor valida y decide (P1).
        </p>
      </form>

      <!-- ── OHLC ── -->
      <section v-else class="p-market__section" aria-label="Historial OHLC">
        <div class="p-market__filters">
          <BaseSelect v-model="ohlcProduct" label="Producto" :options="productOptions" placeholder="Elegir…" />
          <BaseSelect v-model="ohlcRegion" label="Región" :options="regionOptions" placeholder="(todas)" />
          <BaseSelect
            v-model="ohlcBucket"
            label="Bucket"
            :options="[{ value: '3600', label: '1 h de juego' }, { value: '86400', label: '1 día de juego' }]"
          />
          <BaseButton variant="primary" :disabled="ohlcProduct === ''" @click="refreshOhlc">Consultar</BaseButton>
        </div>
        <BaseTable :columns="ohlcColumns" :rows="candles" :row-key="(c) => `${c.product_id}:${c.bucket_start_sim}`">
          <template #cell-bucket="{ row }"><span class="e-num">{{ formatSimTime(row.bucket_start_sim) }}</span></template>
          <template #cell-open_price="{ row }"><MoneyText :amount="row.open_price" /></template>
          <template #cell-high_price="{ row }"><MoneyText :amount="row.high_price" /></template>
          <template #cell-low_price="{ row }"><MoneyText :amount="row.low_price" /></template>
          <template #cell-close_price="{ row }"><MoneyText :amount="row.close_price" /></template>
          <template #cell-volume="{ row }"><span class="e-num">{{ formatQuantity(row.volume) }}</span></template>
        </BaseTable>
      </section>
    </div>

    <!-- ── Modal de aceptación ── -->
    <BaseModal :open="acceptTarget !== null" title="Aceptar publicación" @close="acceptTarget = null">
      <div v-if="acceptTarget !== null" class="p-market__accept">
        <p>
          {{ acceptTarget.kind === 'sell' ? 'Comprar' : 'Vender' }} <strong>{{ productLabel(acceptTarget.product_id) }}</strong>
          a <MoneyText :amount="acceptTarget.unit_price" />/ud.
        </p>
        <p v-if="acceptTarget.window_closes_at !== undefined">
          Ventana de sorteo: cierra en <CountdownText :until="acceptTarget.window_closes_at" /> — las aceptaciones concurren y
          se sortean (la latencia no otorga ventaja).
        </p>
        <BaseInput
          v-model="acceptQty"
          data-test="accept-qty"
          label="Cantidad"
          type="number"
          :min="1"
          :error="acceptError"
          :hint="`mínimo ${formatQuantity(acceptTarget.min_lot)}, restante ${formatQuantity(acceptTarget.quantity_remaining)}`"
        />
        <p v-if="acceptBlockNotice !== null" class="p-market__notice">
          Como {{ acceptBlockNotice.role }}, {{ acceptBlockNotice.text }} — valor del lote:
          <MoneyText :amount="acceptBlockNotice.amount" />. El bloqueo exacto lo asienta el servidor.
        </p>
      </div>
      <template #footer>
        <BaseButton @click="acceptTarget = null">Cerrar</BaseButton>
        <BaseButton variant="primary" data-test="accept-confirm" :disabled="busy" @click="confirmAccept">Confirmar aceptación</BaseButton>
      </template>
    </BaseModal>

    <!-- ── Modal de cancelación ── -->
    <BaseModal :open="cancelTarget !== null" title="Cancelar publicación" @close="cancelTarget = null">
      <div v-if="cancelTarget !== null" class="p-market__accept">
        <p>Se cancelará la cantidad restante y se liberará su garantía.</p>
        <p v-if="cancelCooldownActive" class="p-market__notice">
          Cooldown anti-parpadeo activo: termina en
          <CountdownText v-if="cancelTarget.cancel_cooldown_until !== undefined" :until="cancelTarget.cancel_cooldown_until" />.
          El servidor rechazará la cancelación hasta entonces (409 CANCEL_COOLDOWN_ACTIVE).
        </p>
      </div>
      <template #footer>
        <BaseButton @click="cancelTarget = null">Volver</BaseButton>
        <BaseButton variant="danger" :disabled="busy" @click="confirmCancel">Cancelar publicación</BaseButton>
      </template>
    </BaseModal>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-market {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;

  &__section {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }

  &__filters {
    display: flex;
    flex-wrap: wrap;
    align-items: flex-end;
    gap: 0.75rem;

    > * {
      min-width: 8rem;
    }
  }

  &__auto {
    display: inline-flex;
    align-items: center;
    gap: 0.375rem;
    font-size: 0.8125rem;
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

  &__notice {
    color: var(--ii-warning);
    font-size: 0.8125rem;
    border: 1px dashed var(--ii-warning);
    border-radius: 3px;
    padding: 0.375rem 0.5rem;
  }

  &__subtitle {
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-text-muted);
  }

  &__deliveries {
    display: flex;
    flex-direction: column;
    gap: 0.375rem;
  }

  &__delivery {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    font-size: 0.875rem;
  }

  &__accept {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
    font-size: 0.875rem;
  }

  &__form {
    max-width: 44rem;
  }
}
</style>
