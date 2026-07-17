<script setup lang="ts">
/**
 * BasePanel — superficie contenedora con título del UI kit (FAD §15.4 "Panel").
 *
 * La estructura y la superficie temable vienen del objeto global `.o-panel`
 * (capa objects, FAD §10.4); aquí solo vive la cosmética local del título y
 * el slot `actions` para controles de cabecera.
 */

interface Props {
  title?: string
}

// Sin withDefaults: `title` opcional ya es `string | undefined`.
const props = defineProps<Props>()
</script>

<template>
  <section class="o-panel">
    <header v-if="props.title !== undefined || $slots.actions" class="o-panel__header">
      <h2 v-if="props.title !== undefined" :class="$style.title">{{ props.title }}</h2>
      <slot name="actions" />
    </header>
    <div class="o-panel__body">
      <slot />
    </div>
  </section>
</template>

<style module lang="scss">
@use 'settings' as s;

.title {
  font-size: s.$font-size-500;
  font-weight: s.$font-weight-semibold;
}
</style>
