<!--
  BaseButton — botón del UI kit propio (ADR-FE-006: cero librerías CSS).
  Variantes semánticas; foco visible heredado del sistema (:focus-visible).
-->
<script setup lang="ts">
withDefaults(
  defineProps<{
    variant?: 'primary' | 'ghost' | 'danger' | 'subtle'
    size?: 'sm' | 'md'
    type?: 'button' | 'submit'
    disabled?: boolean
    /** Motivo del deshabilitado (tooltip, UX honesta §5.3). */
    title?: string
  }>(),
  { variant: 'ghost', size: 'md', type: 'button', disabled: false, title: undefined }
)

defineEmits<{ click: [event: MouseEvent] }>()
</script>

<template>
  <button
    class="b-button"
    :class="[`b-button--${variant}`, `b-button--${size}`]"
    :type="type"
    :disabled="disabled"
    :aria-disabled="disabled || undefined"
    :title="title"
    @click="$emit('click', $event)"
  >
    <slot />
  </button>
</template>

<style lang="scss" scoped>
.b-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 0.375rem;
  border-radius: 3px;
  font-weight: 600;
  white-space: nowrap;
  transition: background-color 120ms ease, border-color 120ms ease;

  &--md {
    padding: 0.375rem 0.875rem;
    font-size: 0.875rem;
  }

  &--sm {
    padding: 0.125rem 0.5rem;
    font-size: 0.75rem;
  }

  &--primary {
    background-color: var(--ii-accent);
    color: var(--ii-bg-deep);

    &:hover:not(:disabled) {
      background-color: var(--ii-accent-strong);
    }
  }

  &--ghost {
    border: 1px solid var(--ii-border);
    color: var(--ii-text);

    &:hover:not(:disabled) {
      border-color: var(--ii-accent);
      color: var(--ii-accent);
    }
  }

  &--danger {
    border: 1px solid var(--ii-error);
    color: var(--ii-error);

    &:hover:not(:disabled) {
      background-color: color-mix(in srgb, var(--ii-error) 15%, transparent);
    }
  }

  &--subtle {
    color: var(--ii-text-muted);

    &:hover:not(:disabled) {
      color: var(--ii-text);
    }
  }

  &:disabled {
    opacity: 0.45;
    cursor: not-allowed;
  }
}
</style>
