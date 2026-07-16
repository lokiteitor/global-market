<!--
  BaseSelect — selector del UI kit propio (select nativo: teclado y a11y gratis).
-->
<script setup lang="ts">
export interface SelectOption {
  value: string
  label: string
  disabled?: boolean
}

withDefaults(
  defineProps<{
    label?: string
    options: SelectOption[]
    placeholder?: string
    disabled?: boolean
    required?: boolean
  }>(),
  { label: undefined, placeholder: undefined, disabled: false, required: false }
)

const model = defineModel<string>({ default: '' })

const selectId = `b-select-${Math.random().toString(36).slice(2, 9)}`
</script>

<template>
  <div class="b-field">
    <label v-if="label" class="b-field__label" :for="selectId">{{ label }}</label>
    <select :id="selectId" v-model="model" class="b-field__select" :disabled="disabled" :required="required">
      <option v-if="placeholder" value="" disabled>{{ placeholder }}</option>
      <option v-for="opt in options" :key="opt.value" :value="opt.value" :disabled="opt.disabled">
        {{ opt.label }}
      </option>
    </select>
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

  &__select {
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
  }
}
</style>
