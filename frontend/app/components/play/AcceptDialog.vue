<script setup lang="ts">
/**
 * AcceptDialog — aceptar K de N de una publicación en ventana de sorteo
 * (ADR-011: la latencia no otorga ventaja; el resultado llega tras el sorteo).
 *
 * Cantidad (≥ min_lot) y, al aceptar una publicación `buy` siendo vendedor,
 * el nodo ORIGEN propio del que sale el stock. Tras el POST muestra el estado
 * `pending_draw` con refresco manual del resultado (GET /acceptances/{id});
 * la resolución también llega sola por WS (acceptance.resolved → refetch).
 *
 * En publicaciones `freight` el aceptante es el TRANSPORTISTA (GDD §5.3.2):
 * antes de confirmar se previsualiza la GARANTÍA estimada que bloqueará el
 * servidor — floor(floor(declared × K/N) × bp/10000), bp default del servidor
 * (estimación rotulada como tal) — y puede pedirse una estimación del
 * trayecto (route-plan informativo, sin crear ruta) con aviso si la ETA
 * excede el plazo pactado.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { applyBasisPoints, format, isMoney, prorate } from '~shared/money'
import { formatQuantity, isQuantity } from '~domain/quantity'
import type { Acceptance, Publication } from '~domain/market'
import { DEFAULT_FREIGHT_GUARANTEE_BP } from '~domain/market'
import { acceptanceStatusPresentation } from '~domain/status'
import {
  mapAcceptance,
  mapContract,
  mapFreightContract,
} from '~network/mappers/domain.mapper'
import { AppError } from '~network/rest'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseFormField from '~/components/base/BaseFormField.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import GameDialog from '~/components/play/GameDialog.vue'
import { LINK_MODE_LABEL } from '~/components/play/labels'
import StatusBadge from '~/components/play/StatusBadge.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMyNodes } from '~/composables/useMyNodes'
import { legEtaText, totalEtaText, useRoutePlanning } from '~/composables/useRoutePlanning'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'

interface Props {
  publication: Publication
}

const props = defineProps<Props>()
const emit = defineEmits<{ close: [] }>()

const apis = useGameApis()
const market = useMarketStore()
const world = useWorldStore()
const { myNodes, describeNode, describeAnyNode } = useMyNodes()
const logistics = useLogisticsStore()
const { messageFor } = useAppError()

const quantity = ref<string>(props.publication.minLot)
const originNodeId = ref('')
const { plan, planning, planError, planBetween } = useRoutePlanning()

/** En `buy` soy el VENDEDOR aceptante: debo aportar el nodo origen del stock. */
const needsOrigin = computed(() => props.publication.kind === 'buy')

/** En `freight` soy el TRANSPORTISTA: garantía sobre el valor declarado. */
const isFreight = computed(() => props.publication.kind === 'freight')

/**
 * Garantía estimada a bloquear: réplica exacta del cálculo del servidor
 * (floor anidado, pro-rata a lo aceptado). ESTIMACIÓN: el bp real es
 * configuración del servidor no expuesta por la API.
 */
const guaranteePreview = computed<string | null>(() => {
  const declared = props.publication.declaredValue
  if (
    !isFreight.value ||
    declared === null ||
    !isQuantity(quantity.value) ||
    quantity.value === '0'
  ) {
    return null
  }
  const portion = prorate(declared, quantity.value, props.publication.quantityTotal)
  return format(applyBasisPoints(portion, DEFAULT_FREIGHT_GUARANTEE_BP))
})

/** Estimación informativa del trayecto origen → destino de la publicación. */
async function onEstimateRoute(): Promise<void> {
  const origin = props.publication.originNodeId
  const destination = props.publication.destinationNodeId
  if (origin === null || destination === null) {
    return
  }
  await planBetween(origin, destination, {
    ...(isQuantity(quantity.value) ? { cargoVolume: quantity.value } : {}),
  })
}

/** ¿La ETA estimada excede el plazo de entrega pactado en la publicación? */
const etaExceedsDeadline = computed(() => {
  const currentPlan = plan.value
  return currentPlan !== null && currentPlan.total_eta_sim_seconds > props.publication.deliverySimSeconds
})

const quantityError = computed<string | null>(() => {
  if (quantity.value === '') {
    return t('validation.required')
  }
  return isQuantity(quantity.value) ? null : t('validation.quantity')
})

const originError = computed<string | null>(() =>
  needsOrigin.value && originNodeId.value === '' ? t('validation.required') : null,
)

const submitting = ref(false)
const submitError = ref<unknown>(null)
const result = ref<Acceptance | null>(null)
const refreshing = ref(false)

const errorText = computed(() => (submitError.value === null ? null : messageFor(submitError.value)))

/** Detalle tipado de INSUFFICIENT_COLLATERAL: requerido vs disponible. */
const collateralDetails = computed<string | null>(() => {
  const error = submitError.value
  if (!(error instanceof AppError) || error.code !== 'INSUFFICIENT_COLLATERAL') {
    return null
  }
  const required = error.details?.['required']
  const available = error.details?.['available']
  if (typeof required !== 'string' || typeof available !== 'string') {
    return null
  }
  return t('market.accept.collateralDetail', {
    required: isMoney(required) ? format(required) : required,
    available: isMoney(available) ? format(available) : available,
  })
})

async function onSubmit(): Promise<void> {
  if (quantityError.value !== null || originError.value !== null) {
    return
  }
  submitting.value = true
  submitError.value = null
  try {
    const dto = await apis.market.acceptPublication(
      props.publication.id,
      quantity.value,
      needsOrigin.value ? originNodeId.value : undefined,
    )
    const acceptance = mapAcceptance(dto)
    market.applyAcceptance(acceptance)
    result.value = acceptance
    if (acceptance.freightContractId !== null) {
      market.applyFreightContract(
        mapFreightContract(await apis.market.getFreightContract(acceptance.freightContractId)),
      )
    }
  } catch (error) {
    submitError.value = error
  } finally {
    submitting.value = false
  }
}

async function onRefreshResult(): Promise<void> {
  const current = result.value
  if (current === null) {
    return
  }
  refreshing.value = true
  try {
    const acceptance = mapAcceptance(await apis.market.getAcceptance(current.id))
    market.applyAcceptance(acceptance)
    result.value = acceptance
    if (acceptance.contractId !== null) {
      market.applyContract(mapContract(await apis.market.getContract(acceptance.contractId)))
    }
    if (acceptance.freightContractId !== null) {
      market.applyFreightContract(
        mapFreightContract(await apis.market.getFreightContract(acceptance.freightContractId)),
      )
    }
  } catch (error) {
    submitError.value = error
  } finally {
    refreshing.value = false
  }
}

const productName = computed(() => {
  const productId = props.publication.productId
  return productId === null ? '' : (world.getProduct(productId)?.name ?? productId)
})

/** Trayecto de la publicación de flete (origen → destino), legible. */
const freightRouteText = computed<string | null>(() => {
  if (!isFreight.value) {
    return null
  }
  const origin = logistics.getNode(props.publication.originNodeId)
  const destination = logistics.getNode(props.publication.destinationNodeId)
  if (origin === null || destination === null) {
    return null
  }
  return `${describeAnyNode(origin)} → ${describeAnyNode(destination)}`
})
</script>

<template>
  <GameDialog :title="t('market.accept.title')" @close="emit('close')">
    <div class="o-stack">
      <p class="accept__summary">
        {{ productName }} · {{ format(props.publication.unitPrice) }} ·
        {{ t('market.accept.remaining', { qty: formatQuantity(props.publication.quantityRemaining) }) }}
      </p>
      <p v-if="freightRouteText !== null" class="accept__muted" data-testid="accept-freight-route">
        {{ freightRouteText }}
      </p>

      <template v-if="result === null">
        <BaseFormField
          :label="t('market.accept.quantity')"
          :hint="t('market.accept.minLot', { qty: formatQuantity(props.publication.minLot) })"
          :error="quantityError"
          required
        >
          <template #default="{ id, describedBy, invalid }">
            <BaseInput
              :id="id"
              v-model="quantity"
              :aria-describedby="describedBy"
              :invalid="invalid"
              inputmode="numeric"
              data-testid="accept-quantity"
            />
          </template>
        </BaseFormField>

        <BaseFormField
          v-if="needsOrigin"
          :label="t('market.accept.originNode')"
          :error="originError"
          required
        >
          <template #default="{ id }">
            <select :id="id" v-model="originNodeId" class="accept__select" data-testid="accept-origin">
              <option value="">{{ t('market.accept.originNode.placeholder') }}</option>
              <option v-for="node of myNodes" :key="node.id" :value="node.id">
                {{ describeNode(node) }}
              </option>
            </select>
          </template>
        </BaseFormField>

        <p
          v-if="guaranteePreview !== null"
          class="accept__guarantee"
          data-testid="accept-guarantee-preview"
        >
          {{ t('market.accept.guaranteePreview', { amount: guaranteePreview }) }}
        </p>

        <template v-if="isFreight">
          <div class="accept__actions accept__actions--start">
            <BaseButton
              variant="ghost"
              :loading="planning"
              data-testid="accept-plan-estimate"
              @click="onEstimateRoute"
            >
              {{ t('market.accept.estimateRoute') }}
            </BaseButton>
          </div>
          <section v-if="plan !== null" class="accept__plan">
            <ol class="accept__legs">
              <li v-for="leg of plan.legs" :key="leg.seq" class="u-numeric">
                {{ t(LINK_MODE_LABEL[leg.mode]) }} · {{ legEtaText(leg.eta_sim_seconds) }}
              </li>
            </ol>
            <p class="accept__muted">
              {{ t('fleet.dispatch.eta', { eta: totalEtaText(plan) }) }}
            </p>
            <BaseBanner v-if="etaExceedsDeadline" variant="warn">
              {{ t('market.accept.etaExceedsDeadline') }}
            </BaseBanner>
          </section>
          <BaseBanner v-if="planError !== null" variant="error">
            {{ messageFor(planError) }}
          </BaseBanner>
        </template>

        <BaseBanner v-if="errorText !== null" variant="error">
          {{ errorText }}
          <p v-if="collateralDetails !== null">{{ collateralDetails }}</p>
        </BaseBanner>

        <div class="accept__actions">
          <BaseButton variant="ghost" @click="emit('close')">{{ t('common.cancel') }}</BaseButton>
          <BaseButton :loading="submitting" data-testid="accept-submit" @click="onSubmit">
            {{ t('market.accept.submit') }}
          </BaseButton>
        </div>
      </template>

      <template v-else>
        <div class="accept__result">
          <StatusBadge :presentation="acceptanceStatusPresentation(result.status)" />
          <p v-if="result.status === 'pending_draw'" class="accept__muted">
            {{ t('market.accept.pendingDraw') }}
          </p>
          <p v-else-if="result.status === 'served' && result.freightContractId !== null" class="accept__muted">
            {{ t('market.accept.freightServed', { qty: formatQuantity(result.quantityServed) }) }}
          </p>
          <p v-else-if="result.status === 'served'" class="accept__muted">
            {{ t('market.accept.served', { qty: formatQuantity(result.quantityServed) }) }}
          </p>
          <p v-else class="accept__muted">{{ t('market.accept.released') }}</p>
        </div>
        <div class="accept__actions">
          <BaseButton
            v-if="result.status === 'pending_draw'"
            variant="ghost"
            :loading="refreshing"
            @click="onRefreshResult"
          >
            {{ t('market.accept.refresh') }}
          </BaseButton>
          <BaseButton @click="emit('close')">{{ t('common.close') }}</BaseButton>
        </div>
      </template>
    </div>
  </GameDialog>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.accept__summary {
  color: var(--color-text-strong);
}

.accept__select {
  width: 100%;
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
}

.accept__actions {
  display: flex;
  justify-content: flex-end;
  gap: s.$space-3;
}

.accept__actions--start {
  justify-content: flex-start;
}

.accept__guarantee {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.accept__legs {
  display: flex;
  flex-direction: column;
  gap: s.$space-1;
  padding-left: s.$space-5;
  font-size: s.$font-size-300;
}

.accept__result {
  display: flex;
  align-items: center;
  gap: s.$space-3;
}

.accept__muted {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
