<script setup lang="ts">
/**
 * InspectorCity — detalle de una ciudad (FAD §15.12).
 *
 * Nivel, población, índice de suministro y radio de influencia del estado
 * replicado; la CURVA DE DEMANDA vigente es pull bajo demanda (C10): se
 * consulta al montar/cambiar la selección y no se almacena en store.
 */

import { computed, ref, watch } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatQuantity } from '~domain/quantity'
import type { CityDemand, CityId } from '~domain/world'
import { mapCityDemand } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  cityId: CityId
}

const props = defineProps<Props>()

const apis = useGameApis()
const world = useWorldStore()
const { messageFor } = useAppError()

const city = computed(() => world.getCity(props.cityId))

const demand = ref<readonly CityDemand[]>([])
const loading = ref(false)
const fetchError = ref<unknown>(null)

watch(
  () => props.cityId,
  (cityId) => {
    loading.value = true
    fetchError.value = null
    demand.value = []
    apis.world
      .getCityDemand(cityId)
      .then((rows) => {
        demand.value = rows.map(mapCityDemand)
      })
      .catch((error: unknown) => {
        fetchError.value = error
      })
      .finally(() => {
        loading.value = false
      })
  },
  { immediate: true },
)
</script>

<template>
  <div v-if="city !== null" class="inspector-city o-stack">
    <header class="inspector-city__head">
      <strong>{{ city.name }}</strong>
    </header>

    <dl class="inspector-city__facts">
      <div>
        <dt>{{ t('inspector.city.level') }}</dt>
        <dd class="u-numeric">{{ city.level }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.city.population') }}</dt>
        <dd class="u-numeric">{{ city.population.toLocaleString('es-ES') }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.city.supplyIndex') }}</dt>
        <dd class="u-numeric">{{ city.supplyIndex.toFixed(2) }}</dd>
      </div>
      <div>
        <dt>{{ t('inspector.city.influence') }}</dt>
        <dd class="u-numeric">{{ (city.influenceRadiusM / 1000).toFixed(1) }} km</dd>
      </div>
    </dl>

    <section>
      <h4 class="inspector-city__subtitle">{{ t('inspector.city.demand') }}</h4>
      <BaseSpinner v-if="loading" size="sm" />
      <BaseBanner v-else-if="fetchError !== null" variant="error">
        {{ messageFor(fetchError) }}
      </BaseBanner>
      <p v-else-if="demand.length === 0" class="inspector-city__muted">
        {{ t('inspector.city.demand.empty') }}
      </p>
      <table v-else class="inspector-city__table">
        <thead>
          <tr>
            <th>{{ t('inspector.city.demand.product') }}</th>
            <th>{{ t('inspector.city.demand.d0') }}</th>
            <th>{{ t('inspector.city.demand.price') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="row of demand" :key="row.productId">
            <td>{{ world.getProduct(row.productId)?.name ?? row.productId }}</td>
            <td class="u-numeric">{{ formatQuantity(row.d0PerSimDay) }}</td>
            <td class="u-numeric">{{ format(row.currentPrice) }}</td>
          </tr>
        </tbody>
      </table>
    </section>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.inspector-city__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.inspector-city__facts {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;

  div {
    display: flex;
    justify-content: space-between;
  }

  dt {
    color: var(--color-text-muted);
  }
}

.inspector-city__subtitle {
  margin-bottom: s.$space-2;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
}

.inspector-city__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.inspector-city__table {
  width: 100%;
  border-collapse: collapse;
  font-size: s.$font-size-200;

  th {
    color: var(--color-text-muted);
    font-weight: s.$font-weight-medium;
    text-align: left;
  }

  th,
  td {
    padding: s.$space-1 s.$space-2;
    border-bottom: 1px solid var(--color-border);
  }
}
</style>
