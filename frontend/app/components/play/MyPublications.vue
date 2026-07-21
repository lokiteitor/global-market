<script setup lang="ts">
/**
 * MyPublications — publicaciones propias replicadas (bootstrap desde el
 * tablón + respuestas de comandos + eventos WS publication.*; el contrato no
 * expone listado propio — hueco documentado en useGameSync).
 *
 * Cancelar respeta el cooldown anti-parpadeo: el 409 CANCEL_COOLDOWN_ACTIVE
 * del servidor se muestra tipado.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatQuantity } from '~domain/quantity'
import type { Publication } from '~domain/market'
import { isLivePublicationStatus } from '~domain/market'
import { isMine } from '~domain/ownership'
import { publicationStatusPresentation } from '~domain/status'
import { mapPublication } from '~network/mappers/domain.mapper'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import { PUBLICATION_KIND_LABEL } from '~/components/play/labels'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const market = useMarketStore()
const world = useWorldStore()
const session = useSessionStore()
const { messageFor } = useAppError()

const mine = computed(() =>
  market.publicationList.filter((publication) =>
    isMine(publication.publisherAccountId, session.account?.id ?? null),
  ),
)

const cancellingId = ref<string | null>(null)
const actionError = ref<unknown>(null)

async function onCancel(publication: Publication): Promise<void> {
  cancellingId.value = publication.id
  actionError.value = null
  try {
    const dto = await apis.market.cancelPublication(publication.id)
    market.applyPublication(mapPublication(dto))
  } catch (error) {
    actionError.value = error
  } finally {
    cancellingId.value = null
  }
}

function productName(publication: Publication): string {
  const productId = publication.productId
  return productId === null
    ? t('market.kind.freight')
    : (world.getProduct(productId)?.name ?? productId)
}
</script>

<template>
  <div class="o-stack">
    <BaseBanner v-if="actionError !== null" variant="error">{{ messageFor(actionError) }}</BaseBanner>

    <p v-if="mine.length === 0" class="mypubs__empty">{{ t('market.mine.empty') }}</p>

    <table v-else class="mypubs__table">
      <thead>
        <tr>
          <th>{{ t('market.col.kind') }}</th>
          <th>{{ t('market.col.product') }}</th>
          <th>{{ t('market.col.price') }}</th>
          <th>{{ t('market.col.remaining') }}</th>
          <th>{{ t('market.col.status') }}</th>
          <th />
        </tr>
      </thead>
      <tbody>
        <tr v-for="publication of mine" :key="publication.id">
          <td>{{ t(PUBLICATION_KIND_LABEL[publication.kind]) }}</td>
          <td>{{ productName(publication) }}</td>
          <td class="u-numeric">{{ format(publication.unitPrice) }}</td>
          <td class="u-numeric">
            {{ formatQuantity(publication.quantityRemaining) }} /
            {{ formatQuantity(publication.quantityTotal) }}
          </td>
          <td><StatusBadge :presentation="publicationStatusPresentation(publication.status)" /></td>
          <td>
            <BaseButton
              v-if="isLivePublicationStatus(publication.status)"
              variant="danger"
              :loading="cancellingId === publication.id"
              @click="onCancel(publication)"
            >
              {{ t('market.mine.cancel') }}
            </BaseButton>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.mypubs__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.mypubs__table {
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
