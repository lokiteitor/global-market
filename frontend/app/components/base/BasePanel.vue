<!--
  BasePanel — panel con cabecera y cuerpo colapsable (UI kit propio).
  Cabecera accesible: botón con aria-expanded/aria-controls.
-->
<script setup lang="ts">
import { computed, ref } from 'vue'

const props = withDefaults(
  defineProps<{
    title: string
    collapsible?: boolean
    defaultOpen?: boolean
  }>(),
  { collapsible: true, defaultOpen: true }
)

let nextId = 0
const bodyId = `b-panel-body-${++nextId}-${Math.floor(Math.random() * 1e6)}`
const open = ref(props.defaultOpen)
const expanded = computed(() => !props.collapsible || open.value)

function toggle(): void {
  if (props.collapsible) open.value = !open.value
}
</script>

<template>
  <section class="b-panel">
    <header class="b-panel__header">
      <button
        v-if="collapsible"
        class="b-panel__toggle"
        type="button"
        :aria-expanded="expanded"
        :aria-controls="bodyId"
        @click="toggle"
      >
        <span class="b-panel__chevron" :class="{ 'b-panel__chevron--open': expanded }" aria-hidden="true">▸</span>
        <span class="b-panel__title">{{ title }}</span>
      </button>
      <span v-else class="b-panel__title">{{ title }}</span>
      <div class="b-panel__actions">
        <slot name="actions" />
      </div>
    </header>

    <div v-show="expanded" :id="bodyId" class="b-panel__body">
      <slot />
    </div>
  </section>
</template>

<style lang="scss" scoped>
.b-panel {
  background-color: var(--ii-bg-raised);
  border: 1px solid var(--ii-border);
  border-radius: 6px;
  display: flex;
  flex-direction: column;
  min-height: 0;

  &__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--ii-border-subtle);
  }

  &__toggle {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    color: inherit;
  }

  &__chevron {
    display: inline-block;
    color: var(--ii-text-faint);
    transition: transform 120ms ease;

    &--open {
      transform: rotate(90deg);
    }
  }

  &__title {
    font-size: 0.875rem;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--ii-text-muted);
  }

  &__actions {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
  }

  &__body {
    padding: 0.75rem;
    overflow: auto;
    min-height: 0;
  }
}
</style>
