<script setup lang="ts">
/**
 * BaseButton — botón del UI kit (FAD §15, ADR-FE-006).
 *
 * Variantes: `primary` (acción principal), `ghost` (secundaria) y `danger`
 * (destructiva). `loading` deshabilita el botón, anuncia `aria-busy` y muestra
 * un spinner decorativo — el estado de carga es del llamador (thin client:
 * el botón presenta, no decide). El click nativo cae por atributos heredados.
 */

import BaseSpinner from './BaseSpinner.vue'

export type ButtonVariant = 'primary' | 'ghost' | 'danger'

interface Props {
  variant?: ButtonVariant
  type?: 'button' | 'submit'
  disabled?: boolean
  loading?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'primary',
  type: 'button',
  disabled: false,
  loading: false,
})
</script>

<template>
  <button
    :type="props.type"
    :class="[$style.button, $style[props.variant]]"
    :disabled="props.disabled || props.loading"
    :aria-busy="props.loading || undefined"
  >
    <BaseSpinner v-if="props.loading" decorative size="sm" />
    <span :class="$style.label"><slot /></span>
  </button>
</template>

<style module lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: s.$space-3;
  padding: s.$space-3 s.$space-5;
  border: 1px solid transparent;
  border-radius: 4px;
  font-size: s.$font-size-400;
  font-weight: s.$font-weight-medium;
  line-height: s.$line-height-tight;
  cursor: pointer;
  transition:
    background-color s.$motion-duration-fast s.$motion-ease-out,
    border-color s.$motion-duration-fast s.$motion-ease-out;

  @include t.focus-ring;

  &:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
}

.label {
  min-width: 0;
}

.primary {
  background-color: var(--color-accent);
  color: var(--color-accent-contrast);

  &:not(:disabled):hover {
    background-color: var(--color-accent-hover);
  }
}

.ghost {
  background-color: transparent;
  border-color: var(--color-border-strong);
  color: var(--color-text);

  &:not(:disabled):hover {
    background-color: var(--color-surface-hover);
  }
}

.danger {
  background-color: var(--color-danger);
  border-color: var(--color-danger);
  color: s.$color-gray-050;

  &:not(:disabled):hover {
    filter: brightness(1.08);
  }
}
</style>
