<script setup lang="ts">
/**
 * HudSidebar — herramientas, overlays y lanzador de paneles (FAD §15.10, v1).
 *
 * - Herramientas de interacción del mapa (select/pan/build/parcel): escriben
 *   el modo en mapui.store; `bindWorldLive` lo aplica al motor.
 * - Toggles de overlays (red, recursos, regiones, influencia, congestión).
 * - Salto de región (mundo multi-región, GDD §9): lista del catálogo ordenada
 *   por rejilla; clic = comando de cámara `requestFitRect` (acceso rápido
 *   accesible por teclado, complementario al minimapa).
 * - Botones de paneles flotantes (uno visible a la vez, panels.store).
 */

import { computed } from 'vue'
import { t } from '~shared/i18n'
import type { MessageKey } from '~shared/i18n'
import type { Region } from '~domain/world'
import { polygonBoundsM } from '~domain/world'
import type { InputMode, OverlayName } from '~~/game'
import type { GamePanelName } from '~/stores/panels.store'
import { useConcessionAlerts } from '~/composables/useConcessionAlerts'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const mapui = useMapUiStore()
const panels = usePanelsStore()
const world = useWorldStore()
const concessionAlerts = useConcessionAlerts()

/** Regiones con bounds (saltables), en orden de rejilla (filas, columnas). */
const jumpableRegions = computed(() =>
  world.regionList
    .filter((region) => region.boundsM !== null)
    .toSorted((a, b) => a.gridY - b.gridY || a.gridX - b.gridX),
)

function onJumpToRegion(region: Region): void {
  if (region.boundsM === null) {
    return
  }
  const box = polygonBoundsM(region.boundsM)
  if (box === null) {
    return
  }
  mapui.requestFitRect({
    xM: box.minXM,
    yM: box.minYM,
    widthM: box.maxXM - box.minXM,
    heightM: box.maxYM - box.minYM,
  })
}

const TOOLS: readonly { mode: InputMode; label: MessageKey }[] = [
  { mode: 'select', label: 'tool.select' },
  { mode: 'pan', label: 'tool.pan' },
  { mode: 'build', label: 'tool.build' },
  { mode: 'parcel', label: 'tool.parcel' },
]

const OVERLAYS: readonly { name: OverlayName; label: MessageKey }[] = [
  { name: 'logistics', label: 'overlay.logistics' },
  { name: 'resources', label: 'overlay.resources' },
  { name: 'regions', label: 'overlay.regions' },
  { name: 'influence', label: 'overlay.influence' },
  { name: 'congestion', label: 'overlay.congestion' },
]

const PANEL_BUTTONS: readonly { panel: GamePanelName; label: MessageKey }[] = [
  { panel: 'market', label: 'panel.market' },
  { panel: 'industry', label: 'panel.industry' },
  { panel: 'fleet', label: 'panel.fleet' },
  { panel: 'finance', label: 'panel.finance' },
  { panel: 'concessions', label: 'panel.concessions' },
]

function onTool(mode: InputMode): void {
  if (mode === 'build') {
    // Construir exige elegir tipo primero: abre el panel; el modo build lo
    // activa el propio panel al seleccionar el tipo (flujo del mandato §4).
    panels.open('build')
    if (panels.buildTypeId !== null) {
      mapui.setMode('build')
    }
    return
  }
  mapui.setMode(mode)
}
</script>

<template>
  <aside class="sidebar">
    <section class="sidebar__section">
      <h3 class="sidebar__title">{{ t('sidebar.tools') }}</h3>
      <button
        v-for="tool of TOOLS"
        :key="tool.mode"
        type="button"
        class="sidebar__item"
        :class="{ 'sidebar__item--active': mapui.mode === tool.mode }"
        :data-testid="`tool-${tool.mode}`"
        @click="onTool(tool.mode)"
      >
        {{ t(tool.label) }}
      </button>
    </section>

    <section class="sidebar__section">
      <h3 class="sidebar__title">{{ t('sidebar.overlays') }}</h3>
      <label v-for="overlay of OVERLAYS" :key="overlay.name" class="sidebar__toggle">
        <input
          type="checkbox"
          :checked="mapui.overlays[overlay.name] === true"
          @change="mapui.setOverlay(overlay.name, ($event.target as HTMLInputElement).checked)"
        />
        <span>{{ t(overlay.label) }}</span>
      </label>
    </section>

    <section v-if="jumpableRegions.length > 1" class="sidebar__section">
      <h3 class="sidebar__title">{{ t('sidebar.regions') }}</h3>
      <button
        v-for="region of jumpableRegions"
        :key="region.id"
        type="button"
        class="sidebar__item"
        :data-testid="`region-jump-${region.gridX}-${region.gridY}`"
        :title="t('sidebar.regions.jump', { name: region.name })"
        @click="onJumpToRegion(region)"
      >
        {{ region.name }}
      </button>
    </section>

    <section class="sidebar__section">
      <h3 class="sidebar__title">{{ t('sidebar.panels') }}</h3>
      <button
        v-for="entry of PANEL_BUTTONS"
        :key="entry.panel"
        type="button"
        class="sidebar__item"
        :class="{ 'sidebar__item--active': panels.activePanel === entry.panel }"
        :data-testid="`panel-${entry.panel}`"
        @click="panels.toggle(entry.panel)"
      >
        {{ t(entry.label) }}
        <span
          v-if="entry.panel === 'concessions' && concessionAlerts.count.value > 0"
          class="sidebar__badge"
          :class="`sidebar__badge--${concessionAlerts.severity.value}`"
          data-testid="sidebar-concessions-badge"
        >
          {{ concessionAlerts.count.value }}
        </span>
      </button>
    </section>
  </aside>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.sidebar {
  position: absolute;
  top: 3.25rem;
  bottom: s.$space-5;
  left: s.$space-4;
  z-index: s.$z-hud;
  display: flex;
  flex-direction: column;
  gap: s.$space-5;
  width: 11rem;
  padding: s.$space-4;
  overflow-y: auto;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.5rem;
}

.sidebar__section {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
}

.sidebar__title {
  color: var(--color-text-muted);
  font-size: s.$font-size-100;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.sidebar__item {
  padding: s.$space-2 s.$space-3;
  border: 1px solid transparent;
  border-radius: 0.375rem;
  background: transparent;
  color: var(--color-text);
  font-size: s.$font-size-300;
  text-align: left;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    background-color: var(--color-surface-hover);
  }
}

.sidebar__item--active {
  border-color: var(--color-accent);
  color: var(--color-text-strong);
  background-color: var(--color-surface-active);
}

.sidebar__badge {
  display: inline-block;
  min-width: 1.25rem;
  margin-left: s.$space-2;
  padding: 0 s.$space-1;
  border-radius: 999px;
  background-color: var(--color-warning);
  color: var(--color-surface);
  font-size: s.$font-size-100;
  text-align: center;
}

.sidebar__badge--danger {
  background-color: var(--color-danger);
}

.sidebar__toggle {
  display: flex;
  align-items: center;
  gap: s.$space-3;
  padding: s.$space-1 s.$space-3;
  color: var(--color-text);
  font-size: s.$font-size-300;
  cursor: pointer;
}
</style>
