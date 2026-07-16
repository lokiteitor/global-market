<!--
  BaseInput — campo de texto/número del UI kit propio.
  Solo validación de FORMA en la UI (P1): `error` lo aporta el llamador.
-->
<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    label?: string
    type?: 'text' | 'password' | 'number'
    placeholder?: string
    hint?: string
    error?: string
    disabled?: boolean
    required?: boolean
    min?: number | string
    max?: number | string
    step?: number | string
    autocomplete?: string
    name?: string
  }>(),
  { label: undefined, type: 'text', placeholder: undefined, hint: undefined, error: undefined, disabled: false, required: false, min: undefined, max: undefined, step: undefined, autocomplete: undefined, name: undefined }
)

// Binding manual (no v-model): Vue auto-castea a number en type="number" y
// aquí el modelo es SIEMPRE string (dinero/stock en punto fijo, C11).
const model = defineModel<string>({ default: '' })

function onInput(event: Event): void {
  model.value = (event.target as HTMLInputElement).value
}

const inputId = `b-input-${Math.random().toString(36).slice(2, 9)}`
const describedBy = computed(() => {
  if (props.error !== undefined) return `${inputId}-error`
  if (props.hint !== undefined) return `${inputId}-hint`
  return undefined
})
</script>

<template>
  <div class="b-field">
    <label v-if="label" class="b-field__label" :for="inputId">{{ label }}</label>
    <input
      :id="inputId"
      :value="model"
      class="b-field__input"
      :class="{ 'b-field__input--error': error !== undefined }"
      :type="type"
      :placeholder="placeholder"
      :disabled="disabled"
      :required="required"
      :min="min"
      :max="max"
      :step="step"
      :autocomplete="autocomplete"
      :name="name"
      :aria-invalid="error !== undefined || undefined"
      :aria-describedby="describedBy"
      @input="onInput"
    />
    <p v-if="error" :id="`${inputId}-error`" class="b-field__error" role="alert">{{ error }}</p>
    <p v-else-if="hint" :id="`${inputId}-hint`" class="b-field__hint">{{ hint }}</p>
  </div>
</template>

<style lang="scss" scoped>
.b-field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  min-width: 0;

  &__label {
    font-size: 0.75rem;
    color: var(--ii-text-muted);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  &__input {
    background-color: var(--ii-bg-deep);
    border: 1px solid var(--ii-border);
    border-radius: 3px;
    padding: 0.375rem 0.5rem;
    font-size: 0.875rem;
    width: 100%;

    &:focus-visible {
      border-color: var(--ii-accent);
      outline: none;
    }

    &:disabled {
      opacity: 0.5;
    }

    &--error {
      border-color: var(--ii-error);
    }
  }

  &__hint {
    font-size: 0.75rem;
    color: var(--ii-text-faint);
  }

  &__error {
    font-size: 0.75rem;
    color: var(--ii-error);
  }
}
</style>
