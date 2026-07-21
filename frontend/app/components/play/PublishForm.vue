<script setup lang="ts">
/**
 * PublishForm — publicar oferta/solicitud CCRI (FAD §9.4, ADR-014).
 *
 * `sell`: exige nodo ORIGEN propio (el stock queda congelado allí).
 * `buy`: exige nodo DESTINO propio (la entrega exigirá transporte físico).
 * Validación de FORMA en cliente (C7 — requeridos y patrones de punto fijo);
 * la validación real y el bloqueo de garantías son del servidor: los errores
 * tipados (INSUFFICIENT_COLLATERAL con detalles) se muestran tal cual.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format, isMoney } from '~shared/money'
import { isQuantity } from '~domain/quantity'
import { SIM_SECONDS_PER_HOUR } from '~shared/simtime'
import type { PublicationCreateDto } from '~network/market.api'
import { mapPublication } from '~network/mappers/domain.mapper'
import { AppError } from '~network/rest'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseFormField from '~/components/base/BaseFormField.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameApis } from '~/composables/useGameApis'
import { useMyNodes } from '~/composables/useMyNodes'
import { useMarketStore } from '~/stores/market.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const world = useWorldStore()
const market = useMarketStore()
const { myNodes, describeNode } = useMyNodes()
const { messageFor } = useAppError()

const kind = ref<'sell' | 'buy'>('sell')
const productId = ref('')
const quantity = ref('')
const unitPrice = ref('')
const minLot = ref('1')
const nodeId = ref('')
const deliveryHours = ref('48')

const submitted = ref(false)
const submitting = ref(false)
const submitError = ref<unknown>(null)
const successText = ref<string | null>(null)

function requiredError(value: string): string | null {
  return submitted.value && value === '' ? t('validation.required') : null
}

const productError = computed(() => requiredError(productId.value))
const nodeError = computed(() => requiredError(nodeId.value))
const quantityError = computed<string | null>(() => {
  const required = requiredError(quantity.value)
  if (required !== null) {
    return required
  }
  if (quantity.value !== '' && !isQuantity(quantity.value)) {
    return t('validation.quantity')
  }
  return null
})
const priceError = computed<string | null>(() => {
  const required = requiredError(unitPrice.value)
  if (required !== null) {
    return required
  }
  if (unitPrice.value !== '' && !isMoney(unitPrice.value)) {
    return t('validation.money')
  }
  return null
})
const minLotError = computed<string | null>(() =>
  minLot.value !== '' && !isQuantity(minLot.value) ? t('validation.quantity') : null,
)
const deliveryError = computed<string | null>(() => {
  const parsed = Number.parseInt(deliveryHours.value, 10)
  return Number.isSafeInteger(parsed) && parsed > 0 ? null : t('validation.hours')
})

const nodeLabel = computed(() =>
  kind.value === 'sell' ? t('market.publish.originNode') : t('market.publish.destinationNode'),
)

const hasErrors = computed(
  () =>
    productError.value !== null ||
    nodeError.value !== null ||
    quantityError.value !== null ||
    priceError.value !== null ||
    minLotError.value !== null ||
    deliveryError.value !== null,
)

/** Detalle de INSUFFICIENT_COLLATERAL (requerido vs disponible, en Money). */
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
  submitted.value = true
  successText.value = null
  if (hasErrors.value) {
    return
  }
  submitting.value = true
  submitError.value = null
  try {
    const body: PublicationCreateDto = {
      kind: kind.value,
      channel: 'board',
      product_id: productId.value,
      quantity_total: quantity.value,
      unit_price: unitPrice.value,
      min_lot: minLot.value === '' ? '1' : minLot.value,
      delivery_sim_seconds: Number.parseInt(deliveryHours.value, 10) * SIM_SECONDS_PER_HOUR,
      ...(kind.value === 'sell'
        ? { origin_node_id: nodeId.value }
        : { destination_node_id: nodeId.value }),
    }
    const dto = await apis.market.createPublication(body)
    market.applyPublication(mapPublication(dto))
    successText.value = t('market.publish.success')
    submitted.value = false
    quantity.value = ''
    unitPrice.value = ''
  } catch (error) {
    submitError.value = error
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <form class="publish o-stack" novalidate @submit.prevent="onSubmit">
    <BaseFormField :label="t('market.publish.kind')" required>
      <template #default="{ id }">
        <select :id="id" v-model="kind" class="publish__select" data-testid="publish-kind">
          <option value="sell">{{ t('market.kind.sell') }}</option>
          <option value="buy">{{ t('market.kind.buy') }}</option>
        </select>
      </template>
    </BaseFormField>

    <BaseFormField :label="t('market.publish.product')" :error="productError" required>
      <template #default="{ id }">
        <select :id="id" v-model="productId" class="publish__select" data-testid="publish-product">
          <option value="">{{ t('market.publish.product.placeholder') }}</option>
          <option v-for="product of world.productList" :key="product.id" :value="product.id">
            {{ product.name }}
          </option>
        </select>
      </template>
    </BaseFormField>

    <BaseFormField :label="t('market.publish.quantity')" :error="quantityError" required>
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="quantity"
          :aria-describedby="describedBy"
          :invalid="invalid"
          inputmode="numeric"
          data-testid="publish-quantity"
        />
      </template>
    </BaseFormField>

    <BaseFormField :label="t('market.publish.unitPrice')" :error="priceError" required>
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="unitPrice"
          :aria-describedby="describedBy"
          :invalid="invalid"
          inputmode="numeric"
          data-testid="publish-price"
        />
      </template>
    </BaseFormField>

    <BaseFormField
      :label="t('market.publish.minLot')"
      :hint="t('market.publish.minLot.hint')"
      :error="minLotError"
    >
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="minLot"
          :aria-describedby="describedBy"
          :invalid="invalid"
          inputmode="numeric"
          data-testid="publish-min-lot"
        />
      </template>
    </BaseFormField>

    <BaseFormField :label="nodeLabel" :error="nodeError" required>
      <template #default="{ id }">
        <select :id="id" v-model="nodeId" class="publish__select" data-testid="publish-node">
          <option value="">{{ t('market.publish.node.placeholder') }}</option>
          <option v-for="node of myNodes" :key="node.id" :value="node.id">
            {{ describeNode(node) }}
          </option>
        </select>
      </template>
    </BaseFormField>

    <BaseFormField
      :label="t('market.publish.deliveryHours')"
      :hint="t('market.publish.deliveryHours.hint')"
      :error="deliveryError"
      required
    >
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="deliveryHours"
          :aria-describedby="describedBy"
          :invalid="invalid"
          inputmode="numeric"
          data-testid="publish-delivery"
        />
      </template>
    </BaseFormField>

    <BaseBanner v-if="submitError !== null" variant="error">
      {{ messageFor(submitError) }}
      <p v-if="collateralDetails !== null">{{ collateralDetails }}</p>
    </BaseBanner>
    <BaseBanner v-else-if="successText !== null" variant="info">{{ successText }}</BaseBanner>

    <div class="publish__actions">
      <BaseButton type="submit" :loading="submitting" data-testid="publish-submit">
        {{ t('market.publish.submit') }}
      </BaseButton>
    </div>
  </form>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.publish__select {
  width: 100%;
  padding: s.$space-2 s.$space-3;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.375rem;
  color: var(--color-text);
  font-size: s.$font-size-300;
}

.publish__actions {
  display: flex;
  justify-content: flex-end;
}
</style>
