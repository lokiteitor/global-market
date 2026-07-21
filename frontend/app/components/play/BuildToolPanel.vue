<script setup lang="ts">
/**
 * BuildToolPanel — selector de tipo de edificio del flujo CONSTRUIR
 * (mandato §4). Elegir un tipo activa el modo `build` del mapa (ghost de
 * emplazamiento); el clic en el mapa emite el intent que abre la
 * confirmación. El coste mostrado es el del catálogo (el servidor valida).
 */

import { t } from '~shared/i18n'
import { format } from '~shared/money'
import type { BuildingType } from '~domain/world'
import FloatingPanel from '~/components/play/FloatingPanel.vue'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useWorldStore } from '~/stores/world.store'

const world = useWorldStore()
const mapui = useMapUiStore()
const panels = usePanelsStore()

function onSelect(type: BuildingType): void {
  panels.setBuildType(type.id)
  mapui.setMode('build')
}

function onClose(): void {
  panels.close()
  if (mapui.mode === 'build' && panels.pendingBuild === null) {
    mapui.setMode('select')
    panels.setBuildType(null)
  }
}
</script>

<template>
  <FloatingPanel :title="t('panel.build')" width="30rem" @close="onClose">
    <div class="o-stack">
      <p class="build__hint">
        {{ panels.buildTypeId === null ? t('build.selectType') : t('build.clickHint') }}
      </p>

      <ul class="build__list">
        <li v-for="type of world.buildingTypeList" :key="type.id">
          <button
            type="button"
            class="build__item"
            :class="{ 'build__item--active': panels.buildTypeId === type.id }"
            @click="onSelect(type)"
          >
            <span class="build__name">{{ type.name }}</span>
            <span class="build__meta u-numeric">
              {{ t('build.cost', { cost: format(type.buildCost) }) }} ·
              {{ t('build.cells', { cells: type.footprintCells }) }}
            </span>
          </button>
        </li>
      </ul>
    </div>
  </FloatingPanel>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.build__hint {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.build__list {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  list-style: none;
}

.build__item {
  display: flex;
  flex-direction: column;
  gap: s.$space-1;
  width: 100%;
  padding: s.$space-3;
  border: 1px solid var(--color-border);
  border-radius: 0.5rem;
  background-color: var(--color-surface);
  color: var(--color-text);
  text-align: left;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    background-color: var(--color-surface-hover);
  }
}

.build__item--active {
  border-color: var(--color-accent);
  background-color: var(--color-surface-active);
}

.build__name {
  color: var(--color-text-strong);
  font-weight: s.$font-weight-medium;
}

.build__meta {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
