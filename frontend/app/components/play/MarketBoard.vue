<script setup lang="ts">
/**
 * MarketBoard — tablón CCRI global (FAD C10: pull con filtros, nunca push).
 *
 * Consulta GET /contracts/board con filtros de producto/kind/precio máximo,
 * aplica el snapshot EFÍMERO a market.store (con su sim-time de consulta para
 * el marcado de frescura) y emite `accept` con la publicación elegida — el
 * diálogo de aceptación (cantidad + nodo origen) lo abre el panel padre.
 * Aceptar la publicación propia se deshabilita con tooltip (OwnershipPolicy).
 */

import { computed, onMounted, ref } from 'vue'
import { t } from '~shared/i18n'
import { format, isMoney } from '~shared/money'
import { formatSimTime, simTime } from '~shared/simtime'
import { formatQuantity } from '~domain/quantity'
import type { Publication } from '~domain/market'
import { isLivePublicationStatus, isPublicationKind } from '~domain/market'
import { isMine } from '~domain/ownership'
import type { SimClock } from '~domain/simclock'
import type { BoardQuery } from '~network/market.api'
import { mapPublication } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import { PUBLICATION_KIND_LABEL } from '~/components/play/labels'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const emit = defineEmits<{ accept: [publication: Publication] }>()

const apis = useGameApis()
const market = useMarketStore()
const world = useWorldStore()
const session = useSessionStore()
const { messageFor } = useAppError()

const filterProductId = ref('')
const filterKind = ref('')
const filterMaxPrice = ref('')

const loading = ref(false)
const fetchError = ref<unknown>(null)

const board = computed(() => market.board)
const fetchedAtText = computed(() => {
  const at = board.value.fetchedAtSim
  return at === null ? null : formatSimTime(at)
})

function nowSim(): ReturnType<SimClock['now']> {
  const { $simClock } = useNuxtApp() as { $simClock?: SimClock }
  return $simClock?.now() ?? null
}

async function refresh(): Promise<void> {
  loading.value = true
  fetchError.value = null
  try {
    const kind = filterKind.value
    const maxPrice = filterMaxPrice.value
    const query: BoardQuery = {
      limit: 100,
      ...(kind !== '' && isPublicationKind(kind) ? { kind } : {}),
      ...(filterProductId.value === '' ? {} : { product_id: filterProductId.value }),
      ...(maxPrice !== '' && isMoney(maxPrice) ? { max_unit_price: maxPrice } : {}),
    }
    const page = await apis.market.queryBoard(query)
    market.applyBoardSnapshot(page.items.map(mapPublication), nowSim() ?? simTime(0))
  } catch (error) {
    fetchError.value = error
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void refresh()
})

const myAccountId = computed(() => session.account?.id ?? null)

function isOwn(publication: Publication): boolean {
  return isMine(publication.publisherAccountId, myAccountId.value)
}

function acceptDisabledTitle(publication: Publication): string | undefined {
  if (isOwn(publication)) {
    return t('market.board.ownPublication')
  }
  if (!isLivePublicationStatus(publication.status)) {
    return t('market.board.notLive')
  }
  return undefined
}

function productName(publication: Publication): string {
  if (publication.productId === null) {
    return t('market.kind.freight')
  }
  return world.getProduct(publication.productId)?.name ?? publication.productId
}

/** Plazo de entrega en horas de juego (presentación). */
function deliveryHours(publication: Publication): number {
  return Math.round(publication.deliverySimSeconds / 3_600)
}
</script>

<template>
  <div class="board o-stack">
    <form class="board__filters" @submit.prevent="refresh">
      <select v-model="filterProductId" :aria-label="t('market.filter.product')" data-testid="filter-product">
        <option value="">{{ t('market.filter.product.all') }}</option>
        <option v-for="product of world.productList" :key="product.id" :value="product.id">
          {{ product.name }}
        </option>
      </select>
      <select v-model="filterKind" :aria-label="t('market.filter.kind')" data-testid="filter-kind">
        <option value="">{{ t('market.filter.kind.all') }}</option>
        <option value="sell">{{ t('market.kind.sell') }}</option>
        <option value="buy">{{ t('market.kind.buy') }}</option>
      </select>
      <input
        v-model="filterMaxPrice"
        type="text"
        inputmode="numeric"
        :placeholder="t('market.filter.maxPrice')"
        :aria-label="t('market.filter.maxPrice')"
        data-testid="filter-max-price"
      />
      <BaseButton type="submit" variant="ghost" :loading="loading" data-testid="board-refresh">
        {{ t('market.filter.search') }}
      </BaseButton>
    </form>

    <p v-if="fetchedAtText !== null" class="board__meta">
      {{ t('market.board.fetchedAt', { simTime: fetchedAtText }) }}
    </p>

    <BaseBanner v-if="fetchError !== null" variant="error">{{ messageFor(fetchError) }}</BaseBanner>
    <BaseSpinner v-else-if="loading && board.publications.length === 0" />
    <p v-else-if="board.publications.length === 0" class="board__empty">
      {{ t('market.board.empty') }}
    </p>

    <table v-else class="board__table">
      <thead>
        <tr>
          <th>{{ t('market.col.kind') }}</th>
          <th>{{ t('market.col.product') }}</th>
          <th>{{ t('market.col.price') }}</th>
          <th>{{ t('market.col.remaining') }}</th>
          <th>{{ t('market.col.minLot') }}</th>
          <th>{{ t('market.col.delivery') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="publication of board.publications" :key="publication.id" data-testid="board-row">
          <td>{{ t(PUBLICATION_KIND_LABEL[publication.kind]) }}</td>
          <td>{{ productName(publication) }}</td>
          <td class="u-numeric">{{ format(publication.unitPrice) }}</td>
          <td class="u-numeric">{{ formatQuantity(publication.quantityRemaining) }}</td>
          <td class="u-numeric">{{ formatQuantity(publication.minLot) }}</td>
          <td class="u-numeric">{{ t('market.col.deliveryHours', { hours: deliveryHours(publication) }) }}</td>
          <td>
            <BaseButton
              variant="ghost"
              :disabled="acceptDisabledTitle(publication) !== undefined"
              :title="acceptDisabledTitle(publication)"
              data-testid="board-accept"
              @click="emit('accept', publication)"
            >
              {{ t('market.board.accept') }}
            </BaseButton>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.board__filters {
  display: flex;
  flex-wrap: wrap;
  gap: s.$space-3;

  select,
  input {
    padding: s.$space-2 s.$space-3;
    background-color: var(--color-surface);
    border: 1px solid var(--color-border);
    border-radius: 0.375rem;
    color: var(--color-text);
    font-size: s.$font-size-300;
  }
}

.board__meta,
.board__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.board__table {
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
</style>
