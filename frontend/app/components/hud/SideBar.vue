<!--
  SideBar — lanzador de paneles de gestión (estado en ui.store).
  Cada botón conmuta ui.store.activePanel; el host del HUD decide qué panel
  montar según ese estado.
-->
<script setup lang="ts">
import { useUiStore, type PanelName } from '~/stores/ui.store'

interface LauncherItem {
  panel: PanelName
  label: string
  icon: string
}

/** Mapa tarea→panel: construcción usa el context 'concessions', producción 'industry'. */
const items: LauncherItem[] = [
  { panel: 'market', label: 'Mercado', icon: '⚖' },
  { panel: 'concessions', label: 'Construcción', icon: '⛏' },
  { panel: 'industry', label: 'Producción', icon: '⚙' },
  { panel: 'fleet', label: 'Flota', icon: '🚚' },
  { panel: 'finance', label: 'Finanzas', icon: '𝄃𝄃' },
  { panel: 'city', label: 'Ciudades', icon: '🏙' },
  { panel: 'logistics', label: 'Logística', icon: '⛓' }
]

const ui = useUiStore()
</script>

<template>
  <nav class="hud-side" aria-label="Paneles de gestión">
    <button
      v-for="item in items"
      :key="item.panel"
      class="hud-side__item"
      :class="{ 'hud-side__item--active': ui.activePanel === item.panel }"
      type="button"
      :aria-pressed="ui.activePanel === item.panel"
      @click="ui.togglePanel(item.panel)"
    >
      <span class="hud-side__icon" aria-hidden="true">{{ item.icon }}</span>
      <span class="hud-side__label">{{ item.label }}</span>
    </button>
  </nav>
</template>

<style lang="scss" scoped>
.hud-side {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;

  &__item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.625rem;
    border-radius: 3px;
    color: var(--ii-text-muted);
    font-size: 0.875rem;
    text-align: left;

    &:hover {
      color: var(--ii-text);
      background-color: var(--ii-bg-overlay);
    }

    &--active {
      color: var(--ii-accent);
      background-color: var(--ii-bg-overlay);
      box-shadow: inset 2px 0 0 var(--ii-accent);
    }
  }

  &__icon {
    width: 1.25rem;
    text-align: center;
  }
}
</style>
