<script setup lang="ts">
/**
 * BaseFormField — envoltorio accesible de un control de formulario (FAD §15).
 *
 * Cablea label/hint/error con el control del slot vía slot props:
 * `{ id, describedBy, invalid }` — el control debe enlazar `:id`,
 * `:aria-describedby` y su marca de error. El error mostrado aquí es de FORMA
 * (validación de UX, C7): la validación real siempre es del servidor.
 */

import { computed, useId } from 'vue'

interface Props {
  label: string
  // `| undefined` explícito: permite el default undefined bajo exactOptionalPropertyTypes.
  hint?: string | undefined
  error?: string | null
  required?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  hint: undefined,
  error: null,
  required: false,
})

const fieldId = useId()
const hintId = `${fieldId}-hint`
const errorId = `${fieldId}-error`

const hasError = computed(
  () => props.error !== null && props.error !== undefined && props.error !== '',
)
const describedBy = computed<string | undefined>(() => {
  if (hasError.value) {
    return errorId
  }
  return props.hint !== undefined ? hintId : undefined
})
</script>

<template>
  <div :class="$style.field">
    <label :class="$style.label" :for="fieldId">
      {{ props.label
      }}<span v-if="props.required" :class="$style.required" aria-hidden="true"> *</span>
    </label>
    <!-- v-bind objeto: la slot prop viaja como `describedBy` (camelCase literal). -->
    <slot :id="fieldId" v-bind="{ describedBy }" :invalid="hasError" />
    <p v-if="props.hint !== undefined && !hasError" :id="hintId" :class="$style.hint">
      {{ props.hint }}
    </p>
    <p v-if="hasError" :id="errorId" :class="$style.error" role="alert">{{ props.error }}</p>
  </div>
</template>

<style module lang="scss">
@use 'settings' as s;

.field {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
}

.label {
  color: var(--color-text-strong);
  font-size: s.$font-size-300;
  font-weight: s.$font-weight-medium;
}

.required {
  color: var(--color-danger);
}

.hint {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.error {
  color: var(--color-danger);
  font-size: s.$font-size-200;
}
</style>
