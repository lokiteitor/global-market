<script setup lang="ts">
/**
 * BaseInput — campo de texto del UI kit (FAD §15, ADR-FE-006).
 *
 * Componente controlado (v-model). Los atributos no declarados (id, name,
 * placeholder, autocomplete, required, disabled, aria-describedby…) caen al
 * <input> nativo por herencia de atributos; `invalid` marca `aria-invalid`
 * y el estilo de error. Se acopla a BaseFormField vía sus slot props.
 */

interface Props {
  type?: 'text' | 'password'
  invalid?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  type: 'text',
  invalid: false,
})

const model = defineModel<string>({ default: '' })
</script>

<template>
  <input
    v-model="model"
    :type="props.type"
    :class="[$style.input, props.invalid ? $style.invalid : undefined]"
    :aria-invalid="props.invalid || undefined"
  />
</template>

<style module lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.input {
  width: 100%;
  padding: s.$space-3 s.$space-4;
  background-color: var(--color-bg-raised);
  border: 1px solid var(--color-border-strong);
  border-radius: 4px;
  color: var(--color-text);
  font-size: s.$font-size-400;
  line-height: s.$line-height-base;
  transition: border-color s.$motion-duration-fast s.$motion-ease-out;

  @include t.focus-ring(1px);

  &::placeholder {
    color: var(--color-text-muted);
  }

  &:disabled {
    cursor: not-allowed;
    opacity: 0.55;
  }
}

.invalid {
  border-color: var(--color-danger);
}
</style>
