<!--
  BaseTabs — pestañas del UI kit propio.
  Accesibilidad: role=tablist/tab, aria-selected, flechas ←/→ para moverse.
  El contenido lo gestiona el padre (v-show/v-if por pestaña activa).
-->
<script setup lang="ts">
export interface TabItem {
  id: string
  label: string
  badge?: string | number
}

const props = defineProps<{ tabs: TabItem[] }>()

const model = defineModel<string>({ required: true })

function onKeydown(event: KeyboardEvent): void {
  const idx = props.tabs.findIndex((t) => t.id === model.value)
  if (idx === -1) return
  let next = -1
  if (event.key === 'ArrowRight') next = (idx + 1) % props.tabs.length
  else if (event.key === 'ArrowLeft') next = (idx - 1 + props.tabs.length) % props.tabs.length
  else return
  event.preventDefault()
  const tab = props.tabs[next]
  if (tab !== undefined) {
    model.value = tab.id
    const el = (event.currentTarget as HTMLElement).querySelectorAll<HTMLElement>('[role="tab"]')[next]
    el?.focus()
  }
}
</script>

<template>
  <div class="b-tabs" role="tablist" @keydown="onKeydown">
    <button
      v-for="tab in tabs"
      :key="tab.id"
      class="b-tabs__tab"
      :class="{ 'b-tabs__tab--active': tab.id === model }"
      type="button"
      role="tab"
      :aria-selected="tab.id === model"
      :tabindex="tab.id === model ? 0 : -1"
      @click="model = tab.id"
    >
      {{ tab.label }}
      <span v-if="tab.badge !== undefined" class="b-tabs__badge">{{ tab.badge }}</span>
    </button>
  </div>
</template>

<style lang="scss" scoped>
.b-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
  border-bottom: 1px solid var(--ii-border);

  &__tab {
    padding: 0.375rem 0.75rem;
    font-size: 0.875rem;
    color: var(--ii-text-muted);
    border-bottom: 2px solid transparent;
    margin-bottom: -1px;

    &:hover {
      color: var(--ii-text);
    }

    &--active {
      color: var(--ii-accent);
      border-bottom-color: var(--ii-accent);
    }
  }

  &__badge {
    display: inline-block;
    min-width: 1.25rem;
    text-align: center;
    background-color: var(--ii-bg-overlay);
    border: 1px solid var(--ii-border);
    border-radius: 999px;
    font-size: 0.6875rem;
    padding: 0 0.25rem;
    margin-left: 0.25rem;
  }
}
</style>
