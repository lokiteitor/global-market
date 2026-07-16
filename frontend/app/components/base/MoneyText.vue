<!--
  MoneyText — importe monetario del ledger.
  Money es un string de punto fijo (unidades menores): se formatea SOLO con el
  helper BigInt del kernel (C11: prohibido parseFloat/Number sobre Money).
  Números tabulares (monospace) alineados a la derecha.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { formatMoney, isNegative, type Money } from '~/lib/kernel/money'

const props = withDefaults(
  defineProps<{
    amount: Money
    /** Colorea negativo en rojo (extractos del ledger). */
    signed?: boolean
    /** Antepone '+' a los importes positivos (extractos). */
    showPlus?: boolean
  }>(),
  { signed: false, showPlus: false }
)

const negative = computed(() => isNegative(props.amount))
const text = computed(() => {
  const formatted = formatMoney(props.amount)
  return props.showPlus && !negative.value ? `+${formatted}` : formatted
})
</script>

<template>
  <span class="b-money e-num" :class="{ 'b-money--negative': signed && negative, 'b-money--positive': signed && !negative }">
    {{ text }}
  </span>
</template>

<style lang="scss" scoped>
.b-money {
  display: inline-block;
  text-align: right;
  font-variant-numeric: tabular-nums;

  &--negative {
    color: var(--ii-error);
  }

  &--positive {
    color: var(--ii-success);
  }
}
</style>
