<!--
  BuildPanel — suelo (concesiones) y construcción de edificios.
  SIMPLIFICACIÓN v1 (aceptada): sin dibujo en el mapa — la parcela se define
  con inputs numéricos lon/lat + lado ('clic en el mapa próximamente'); el
  footprint del edificio se genera automáticamente como cuadrado centrado en
  la parcela. El emplazamiento lo valida SOLO el servidor (PLACEMENT_INVALID).
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { Concession, GeoPolygon } from '~/lib/api/types'
import { useApi } from '~/composables/useApi'
import { useOwnership } from '~/composables/useOwnership'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useWorldStore } from '~/stores/world.store'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseSelect from '~/components/base/BaseSelect.vue'
import BaseTable, { type TableColumn } from '~/components/base/BaseTable.vue'
import MoneyText from '~/components/base/MoneyText.vue'
import SimTimeText from '~/components/base/SimTimeText.vue'

const api = useApi()
const world = useWorldStore()
const buildings = useBuildingsStore()
const notifications = useNotificationsStore()
const ownership = useOwnership()

const busy = ref(false)

/**
 * Concesiones propias: no existe store de concesiones en el andamiaje, así
 * que el panel mantiene su propio estado pull (anotado para el integrador).
 */
const concessions = ref<Concession[]>([])

onMounted(async () => {
  if (!world.loaded.regions) {
    const r = await api.listRegions()
    if (r.ok) world.setRegions(r.value.data)
  }
  if (!world.loaded.buildingTypes) {
    const r = await api.listBuildingTypes()
    if (r.ok) world.setBuildingTypes(r.value.data)
  }
  await Promise.all([refreshConcessions(), refreshBuildings()])
})

async function refreshConcessions(): Promise<void> {
  const result = await api.listConcessions()
  if (result.ok) concessions.value = result.value.data
}

async function refreshBuildings(): Promise<void> {
  const result = await api.listBuildings()
  // Pull REST como fuente 'rest' de la colección replicada (idempotente).
  if (result.ok) buildings.applySnapshot('rest', { buildings: result.value.data })
}

const regionOptions = computed(() => world.regionList.map((r) => ({ value: r.id, label: `${r.name} (${r.biome})` })))
const buildingTypeOptions = computed(() => world.buildingTypes)

function regionName(regionId: string): string {
  return world.regions[regionId]?.name ?? `${regionId.slice(0, 8)}…`
}

// ─── Geometría (solo forma; el servidor valida emplazamiento) ────────────────

/** Cuadrado (GeoPolygon) de lado `side` grados centrado en (lon, lat). */
function squareAround(lon: number, lat: number, side: number): GeoPolygon {
  const h = side / 2
  return {
    type: 'Polygon',
    coordinates: [
      [
        [lon - h, lat - h],
        [lon + h, lat - h],
        [lon + h, lat + h],
        [lon - h, lat + h],
        [lon - h, lat - h]
      ]
    ]
  }
}

/** Centro y lado aproximados de una parcela (bbox del anillo exterior). */
function parcelCenter(parcel: GeoPolygon): { lon: number; lat: number; side: number } {
  const ring = parcel.coordinates[0] ?? []
  let minLon = Infinity
  let maxLon = -Infinity
  let minLat = Infinity
  let maxLat = -Infinity
  for (const [lon, lat] of ring) {
    minLon = Math.min(minLon, lon)
    maxLon = Math.max(maxLon, lon)
    minLat = Math.min(minLat, lat)
    maxLat = Math.max(maxLat, lat)
  }
  return {
    lon: (minLon + maxLon) / 2,
    lat: (minLat + maxLat) / 2,
    side: Math.min(maxLon - minLon, maxLat - minLat)
  }
}

// ─── Obtener concesión ───────────────────────────────────────────────────────
const grantRegion = ref('')
const grantLon = ref('')
const grantLat = ref('')
const grantSide = ref('0.02')
const grantError = ref<string | undefined>(undefined)

async function requestConcession(): Promise<void> {
  grantError.value = undefined
  const lon = Number.parseFloat(grantLon.value)
  const lat = Number.parseFloat(grantLat.value)
  const side = Number.parseFloat(grantSide.value)
  if (grantRegion.value === '') {
    grantError.value = 'Elige una región'
    return
  }
  if (!Number.isFinite(lon) || !Number.isFinite(lat) || lon < -180 || lon > 180 || lat < -90 || lat > 90) {
    grantError.value = 'Coordenadas inválidas (lon −180..180, lat −90..90)'
    return
  }
  if (!Number.isFinite(side) || side <= 0) {
    grantError.value = 'Lado de parcela inválido'
    return
  }
  busy.value = true
  const result = await api.createConcession(grantRegion.value, squareAround(lon, lat, side))
  busy.value = false
  if (result.ok) {
    notifications.push({ level: 'success', text: 'Concesión otorgada (primer canon cobrado)' })
    await refreshConcessions()
  } else {
    grantError.value = `${result.error.code}: ${result.error.message}`
  }
}

async function renew(concession: Concession): Promise<void> {
  busy.value = true
  const result = await api.renewConcession(concession.id)
  busy.value = false
  if (result.ok) {
    notifications.push({ level: 'success', text: 'Concesión renovada' })
    await refreshConcessions()
  } else {
    notifications.push({ level: 'error', text: `${result.error.code}: ${result.error.message}` })
  }
}

// ─── Construir edificio ──────────────────────────────────────────────────────
const buildType = ref('')
const buildConcession = ref('')
const buildError = ref<string | undefined>(undefined)

const buildTypeOptions = computed(() =>
  Object.values(buildingTypeOptions.value).map((t) => ({ value: t.id, label: `${t.name}` }))
)
const selectedBuildType = computed(() => (buildType.value !== '' ? (world.buildingTypes[buildType.value] ?? null) : null))
const concessionOptions = computed(() =>
  concessions.value
    .filter((c) => c.status === 'active' || c.status === 'grace')
    .map((c) => {
      const center = parcelCenter(c.parcel)
      return { value: c.id, label: `${regionName(c.region_id)} · (${center.lon.toFixed(3)}, ${center.lat.toFixed(3)})` }
    })
)

async function build(): Promise<void> {
  buildError.value = undefined
  if (buildType.value === '' || buildConcession.value === '') {
    buildError.value = 'Elige tipo de edificio y concesión'
    return
  }
  const concession = concessions.value.find((c) => c.id === buildConcession.value)
  if (concession === undefined) return
  // Footprint automático v1: cuadrado centrado en la parcela (60 % del lado).
  const center = parcelCenter(concession.parcel)
  const footprint = squareAround(center.lon, center.lat, center.side * 0.6)
  busy.value = true
  const result = await api.createBuilding({
    building_type_id: buildType.value,
    concession_id: concession.id,
    footprint
  })
  busy.value = false
  if (result.ok) {
    buildings.applyPatch([{ op: 'upsert', entity: 'building', id: result.value.data.id, data: result.value.data }])
    notifications.push({ level: 'success', text: 'Construcción iniciada' })
  } else {
    buildError.value = `${result.error.code}: ${result.error.message}`
  }
}

// ─── Tablas ──────────────────────────────────────────────────────────────────
const concessionColumns: TableColumn[] = [
  { key: 'region', label: 'Región' },
  { key: 'canon_amount', label: 'Canon', align: 'right' },
  { key: 'expires', label: 'Vence' },
  { key: 'status', label: 'Estado' },
  { key: 'actions', label: '' }
]

const buildingColumns: TableColumn[] = [
  { key: 'type', label: 'Tipo' },
  { key: 'region', label: 'Región' },
  { key: 'level', label: 'Nivel', align: 'right' },
  { key: 'condition_pct', label: 'Condición', align: 'right' },
  { key: 'status', label: 'Estado' }
]

const myBuildings = computed(() => {
  const myId = ownership.myAccountId.value
  return myId !== null ? buildings.ownedBy(myId) : []
})

function buildingTypeName(id: string): string {
  return world.buildingTypes[id]?.name ?? `${id.slice(0, 8)}…`
}
</script>

<template>
  <BasePanel title="Construcción — suelo y edificios" :collapsible="false">
    <div class="p-build">
      <section class="p-build__section" aria-label="Mis concesiones">
        <h4 class="p-build__subtitle">Mis concesiones</h4>
        <BaseTable :columns="concessionColumns" :rows="concessions" :row-key="(c) => c.id" empty-text="Sin concesiones">
          <template #cell-region="{ row }">{{ regionName(row.region_id) }}</template>
          <template #cell-canon_amount="{ row }"><MoneyText :amount="row.canon_amount" /></template>
          <template #cell-expires="{ row }"><SimTimeText :sim-seconds="row.expires_at_sim" /></template>
          <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
          <template #cell-actions="{ row }">
            <BaseButton size="sm" :disabled="busy || row.status === 'reverted'" @click="renew(row)">Renovar</BaseButton>
          </template>
        </BaseTable>
      </section>

      <section class="p-build__section" aria-label="Obtener concesión">
        <h4 class="p-build__subtitle">Obtener concesión</h4>
        <form class="p-build__form" @submit.prevent="requestConcession">
          <BaseSelect v-model="grantRegion" label="Región" :options="regionOptions" placeholder="Elegir…" required />
          <BaseInput v-model="grantLon" label="Longitud" type="number" step="0.001" hint="(clic en el mapa próximamente)" required />
          <BaseInput v-model="grantLat" label="Latitud" type="number" step="0.001" required />
          <BaseInput v-model="grantSide" label="Lado (grados)" type="number" step="0.001" :min="0.001" />
          <BaseButton type="submit" variant="primary" :disabled="busy">Solicitar</BaseButton>
        </form>
        <p v-if="grantError" class="p-build__error" role="alert">{{ grantError }}</p>
        <p class="p-build__faint">
          Parcela cuadrada centrada en las coordenadas. Todo suelo es concesión renovable del sistema; el primer canon se
          cobra al conceder.
        </p>
      </section>

      <section class="p-build__section" aria-label="Construir edificio">
        <h4 class="p-build__subtitle">Construir edificio</h4>
        <form class="p-build__form" @submit.prevent="build">
          <BaseSelect v-model="buildType" label="Tipo (catálogo)" :options="buildTypeOptions" placeholder="Elegir…" required />
          <BaseSelect v-model="buildConcession" label="Concesión" :options="concessionOptions" placeholder="Elegir…" required />
          <BaseButton type="submit" variant="primary" :disabled="busy">Construir</BaseButton>
        </form>
        <p v-if="selectedBuildType !== null" class="p-build__faint">
          Coste: <MoneyText :amount="selectedBuildType.build_cost" /> · mantenimiento:
          <MoneyText :amount="selectedBuildType.maintenance_cost" /> · footprint automático: cuadrado centrado en la parcela.
          El emplazamiento lo valida el servidor (PLACEMENT_INVALID si no cumple).
        </p>
        <p v-if="buildError" class="p-build__error" role="alert">{{ buildError }}</p>
      </section>

      <section class="p-build__section" aria-label="Mis edificios">
        <h4 class="p-build__subtitle">Mis edificios</h4>
        <BaseTable :columns="buildingColumns" :rows="myBuildings" :row-key="(b) => b.id" empty-text="Sin edificios">
          <template #cell-type="{ row }">{{ buildingTypeName(row.building_type_id) }}</template>
          <template #cell-region="{ row }">{{ regionName(row.region_id) }}</template>
          <template #cell-condition_pct="{ row }"><span class="e-num">{{ row.condition_pct }} %</span></template>
          <template #cell-status="{ row }"><BaseBadge :status="row.status" /></template>
        </BaseTable>
      </section>
    </div>
  </BasePanel>
</template>

<style lang="scss" scoped>
.p-build {
  display: flex;
  flex-direction: column;
  gap: 1.25rem;

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

  &__form {
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

  &__error {
    color: var(--ii-error);
    font-size: 0.875rem;
  }
}
</style>
