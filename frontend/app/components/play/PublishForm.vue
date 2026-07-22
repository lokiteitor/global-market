<script setup lang="ts">
/**
 * PublishForm — publicar oferta/solicitud CCRI (FAD §9.4, ADR-014).
 *
 * `sell`: exige nodo ORIGEN propio (el stock queda congelado allí).
 * `buy`: exige nodo DESTINO propio (la entrega exigirá transporte físico).
 * `freight` (GDD §5.3.2): solicitud de TRANSPORTE del cargador — origen
 *   propio (la carga sale de un almacén con stock), destino CUALQUIER nodo,
 *   tarifa por unidad y valor declarado (base de la garantía del carrier);
 *   antes de confirmar se previsualiza el escrow (cantidad × tarifa) que el
 *   servidor bloqueará.
 * Validación de FORMA en cliente (C7 — requeridos y patrones de punto fijo);
 * la validación real y el bloqueo de garantías son del servidor: los errores
 * tipados (INSUFFICIENT_COLLATERAL con detalles) se muestran tal cual.
 */

import { computed, ref } from 'vue'
import { t } from '~shared/i18n'
import { format, isMoney, multiplyByUnits } from '~shared/money'
import { isQuantity } from '~domain/quantity'
import { SIM_SECONDS_PER_HOUR } from '~shared/simtime'
import { parseEntityId } from '~shared/ids'
import type { ContractChannel, PublicationKind } from '~domain/market'
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
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

const apis = useGameApis()
const world = useWorldStore()
const market = useMarketStore()
const session = useSessionStore()
const logistics = useLogisticsStore()
const { myNodes, describeNode, describeAnyNode } = useMyNodes()
const { messageFor } = useAppError()

const kind = ref<PublicationKind>('sell')
const channel = ref<ContractChannel>('board')
const counterpartyId = ref('')
const productId = ref('')
const quantity = ref('')
const unitPrice = ref('')
const minLot = ref('1')
const nodeId = ref('')
const destinationNodeId = ref('')
const declaredValue = ref('')
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

/** Un flete pide origen (propio) Y destino (cualquiera); sell/buy solo uno. */
const isFreight = computed(() => kind.value === 'freight')

/** Canal privado: exige la cuenta contraparte (UUID). */
const isPrivate = computed(() => channel.value === 'private')

const counterpartyError = computed<string | null>(() => {
  if (!isPrivate.value) {
    return null
  }
  const required = requiredError(counterpartyId.value)
  if (required !== null) {
    return required
  }
  if (counterpartyId.value !== '' && !parseEntityId(counterpartyId.value).ok) {
    return t('validation.uuid')
  }
  return null
})

/**
 * Sugerencias de contraparte: cuentas ya conocidas por contratos/fletes
 * previos (no existe directorio de cuentas en el contrato — UX honesta:
 * input de UUID con datalist de lo que la sesión ya vio).
 */
const knownCounterparties = computed<readonly string[]>(() => {
  const mine = session.account?.id ?? null
  const ids = new Set<string>()
  for (const contract of market.contractList) {
    ids.add(contract.buyerAccountId)
    ids.add(contract.sellerAccountId)
  }
  for (const freight of market.freightList) {
    ids.add(freight.shipperAccountId)
    ids.add(freight.carrierAccountId)
  }
  if (mine !== null) {
    ids.delete(mine)
  }
  return [...ids]
})

const destinationError = computed<string | null>(() =>
  isFreight.value ? requiredError(destinationNodeId.value) : null,
)

const declaredValueError = computed<string | null>(() => {
  if (!isFreight.value) {
    return null
  }
  const required = requiredError(declaredValue.value)
  if (required !== null) {
    return required
  }
  if (declaredValue.value !== '' && !isMoney(declaredValue.value)) {
    return t('validation.money')
  }
  return null
})

const nodeLabel = computed(() => {
  if (kind.value === 'buy') {
    return t('market.publish.destinationNode')
  }
  return t('market.publish.originNode')
})

const priceLabel = computed(() =>
  isFreight.value ? t('market.publish.freightRate') : t('market.publish.unitPrice'),
)

/**
 * Escrow que el servidor bloqueará al publicar el flete: cantidad × tarifa
 * (misma fórmula del ledger; preview de forma, la verdad es del servidor).
 */
const escrowPreview = computed<string | null>(() => {
  if (!isFreight.value || !isQuantity(quantity.value) || !isMoney(unitPrice.value)) {
    return null
  }
  return format(multiplyByUnits(unitPrice.value, quantity.value))
})

const hasErrors = computed(
  () =>
    productError.value !== null ||
    nodeError.value !== null ||
    destinationError.value !== null ||
    declaredValueError.value !== null ||
    counterpartyError.value !== null ||
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
      channel: channel.value,
      ...(channel.value === 'private' ? { counterparty_account_id: counterpartyId.value } : {}),
      product_id: productId.value,
      quantity_total: quantity.value,
      unit_price: unitPrice.value,
      min_lot: minLot.value === '' ? '1' : minLot.value,
      delivery_sim_seconds: Number.parseInt(deliveryHours.value, 10) * SIM_SECONDS_PER_HOUR,
      ...(kind.value === 'sell' ? { origin_node_id: nodeId.value } : {}),
      ...(kind.value === 'buy' ? { destination_node_id: nodeId.value } : {}),
      ...(kind.value === 'freight'
        ? {
            origin_node_id: nodeId.value,
            destination_node_id: destinationNodeId.value,
            declared_value: declaredValue.value,
          }
        : {}),
    }
    const dto = await apis.market.createPublication(body)
    market.applyPublication(mapPublication(dto))
    successText.value = t('market.publish.success')
    submitted.value = false
    quantity.value = ''
    unitPrice.value = ''
    declaredValue.value = ''
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
          <option value="freight">{{ t('market.kind.freight') }}</option>
        </select>
      </template>
    </BaseFormField>

    <BaseFormField :label="t('market.publish.channel')" required>
      <template #default="{ id }">
        <select :id="id" v-model="channel" class="publish__select" data-testid="publish-channel">
          <option value="board">{{ t('market.publish.channel.board') }}</option>
          <option value="private">{{ t('market.publish.channel.private') }}</option>
        </select>
      </template>
    </BaseFormField>

    <BaseFormField
      v-if="isPrivate"
      :label="t('market.publish.counterparty')"
      :hint="t('market.publish.counterparty.hint')"
      :error="counterpartyError"
      required
    >
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="counterpartyId"
          :aria-describedby="describedBy"
          :invalid="invalid"
          list="publish-counterparty-suggestions"
          data-testid="publish-counterparty"
        />
        <datalist id="publish-counterparty-suggestions">
          <option v-for="accountId of knownCounterparties" :key="accountId" :value="accountId" />
        </datalist>
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

    <BaseFormField :label="priceLabel" :error="priceError" required>
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
      v-if="isFreight"
      :label="t('market.publish.declaredValue')"
      :hint="t('market.publish.declaredValue.hint')"
      :error="declaredValueError"
      required
    >
      <template #default="{ id, describedBy, invalid }">
        <BaseInput
          :id="id"
          v-model="declaredValue"
          :aria-describedby="describedBy"
          :invalid="invalid"
          inputmode="numeric"
          data-testid="publish-declared-value"
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

    <BaseFormField
      :label="nodeLabel"
      :hint="isFreight ? t('market.publish.freightOrigin.hint') : undefined"
      :error="nodeError"
      required
    >
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
      v-if="isFreight"
      :label="t('market.publish.destinationNode')"
      :hint="t('market.publish.destinationNodeAny.hint')"
      :error="destinationError"
      required
    >
      <template #default="{ id }">
        <select
          :id="id"
          v-model="destinationNodeId"
          class="publish__select"
          data-testid="publish-destination"
        >
          <option value="">{{ t('market.publish.node.placeholder') }}</option>
          <option v-for="node of logistics.nodeList" :key="node.id" :value="node.id">
            {{ describeAnyNode(node) }}
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

    <p v-if="escrowPreview !== null" class="publish__escrow" data-testid="publish-escrow-preview">
      {{ t('market.publish.escrowPreview', { amount: escrowPreview }) }}
    </p>

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

.publish__escrow {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}
</style>
