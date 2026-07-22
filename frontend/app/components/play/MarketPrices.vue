<script setup lang="ts">
/**
 * MarketPrices — historial de precios OHLC por producto/región (FAD §15.8).
 *
 * PULL bajo demanda (C10/§13.3, como el tablón — las velas nacen de contratos
 * LIQUIDADOS, no de órdenes vivas): consulta al abrir y al cambiar cualquier
 * selector, más refresco manual; sin timers. La serie se aplica a
 * market.store (applyOhlcSnapshot: reemplaza, dedup por bucket) y el gráfico
 * es Canvas 2D propio (charts/OhlcChart).
 */

import { computed, ref, watch } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import type { ProductId } from '~domain/world'
import { mapOhlcCandle } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import OhlcChart from '~/components/play/charts/OhlcChart.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const market = useMarketStore()
const world = useWorldStore()
const { messageFor } = useAppError()

/** Buckets ofrecidos (segundos de sim-time): 1 h / 6 h / 1 día de juego. */
const BUCKETS = [
  { seconds: 3_600, label: 'market.prices.bucket.hour' },
  { seconds: 21_600, label: 'market.prices.bucket.sixHours' },
  { seconds: 86_400, label: 'market.prices.bucket.day' },
] as const

const productId = ref('')
const regionId = ref('')
const bucketSimSecs = ref(86_400)

const loading = ref(false)
const fetchError = ref<unknown>(null)

const selectedProductId = computed<ProductId | null>(() => {
  const product = world.productList.find((p) => p.id === productId.value)
  return product?.id ?? null
})

const candles = computed(() =>
  selectedProductId.value === null ? [] : market.candlesFor(selectedProductId.value),
)

const lastClose = computed(() => {
  const id = selectedProductId.value
  const close = id === null ? null : market.lastCloseOf(id)
  return close === null ? null : format(close)
})

async function refresh(): Promise<void> {
  const id = selectedProductId.value
  if (id === null) {
    return
  }
  loading.value = true
  fetchError.value = null
  try {
    const region = world.regionList.find((r) => r.id === regionId.value)
    const dtos = await apis.market.getMarketOhlc({
      product_id: id,
      ...(region === undefined ? {} : { region_id: region.id }),
      bucket_sim_secs: bucketSimSecs.value,
      limit: 200,
    })
    market.applyOhlcSnapshot(id, dtos.map(mapOhlcCandle))
  } catch (error) {
    fetchError.value = error
  } finally {
    loading.value = false
  }
}

// Pull al elegir producto o cambiar región/bucket (sin timers, C10).
watch([productId, regionId, bucketSimSecs], () => {
  void refresh()
})

// Producto por defecto: el primero del catálogo (dispara el primer pull).
watch(
  () => world.productList,
  (products) => {
    if (productId.value === '' && products.length > 0) {
      productId.value = products[0]?.id ?? ''
    }
  },
  { immediate: true },
)
</script>

<template>
  <div class="prices o-stack">
    <div class="prices__toolbar">
      <label class="prices__field">
        <span>{{ t('market.publish.product') }}</span>
        <select v-model="productId" class="prices__select" data-testid="prices-product">
          <option v-for="product of world.productList" :key="product.id" :value="product.id">
            {{ product.name }}
          </option>
        </select>
      </label>

      <label class="prices__field">
        <span>{{ t('market.filter.region') }}</span>
        <select v-model="regionId" class="prices__select" data-testid="prices-region">
          <option value="">{{ t('market.filter.region.all') }}</option>
          <option v-for="region of world.regionList" :key="region.id" :value="region.id">
            {{ region.name }}
          </option>
        </select>
      </label>

      <label class="prices__field">
        <span>{{ t('market.prices.bucket') }}</span>
        <select v-model.number="bucketSimSecs" class="prices__select" data-testid="prices-bucket">
          <option v-for="bucket of BUCKETS" :key="bucket.seconds" :value="bucket.seconds">
            {{ t(bucket.label) }}
          </option>
        </select>
      </label>

      <BaseButton variant="ghost" :loading="loading" data-testid="prices-refresh" @click="refresh">
        {{ t('common.refresh') }}
      </BaseButton>
    </div>

    <p v-if="lastClose !== null" class="prices__last" data-testid="prices-last-close">
      {{ t('market.prices.lastClose', { price: lastClose }) }}
    </p>

    <BaseBanner v-if="fetchError !== null" variant="error">{{ messageFor(fetchError) }}</BaseBanner>
    <BaseSpinner v-else-if="loading && candles.length === 0" size="sm" />
    <OhlcChart v-else :candles="candles" />
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.prices__toolbar {
  display: flex;
  align-items: flex-end;
  gap: s.$space-3;
  flex-wrap: wrap;
}

.prices__field {
  display: flex;
  flex-direction: column;
  gap: s.$space-1;
  font-size: s.$font-size-200;
  color: var(--color-text-muted);
}

.prices__select {
  padding: s.$space-1 s.$space-2;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.prices__last {
  color: var(--color-text-strong);
  font-size: s.$font-size-300;
}
</style>
