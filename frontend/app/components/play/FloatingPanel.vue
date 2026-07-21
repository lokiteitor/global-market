<script setup lang="ts">
/**
 * FloatingPanel — envoltorio común de los paneles flotantes del HUD (v1:
 * posición fija junto a la sidebar, uno visible a la vez; el window manager
 * completo del FAD §15.5 queda para incrementos futuros).
 */

import { t } from '~shared/i18n'
import BasePanel from '~/components/base/BasePanel.vue'

interface Props {
  title: string
  /** Ancho máximo del panel (CSS). */
  width?: string
}

withDefaults(defineProps<Props>(), { width: '46rem' })

const emit = defineEmits<{ close: [] }>()
</script>

<template>
  <div class="floating" :style="{ width: `min(${width}, calc(100vw - 14rem))` }">
    <BasePanel :title="title">
      <template #actions>
        <button
          class="floating__close"
          type="button"
          :aria-label="t('common.close')"
          @click="emit('close')"
        >
          ×
        </button>
      </template>
      <slot />
    </BasePanel>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.floating {
  position: absolute;
  top: 3.25rem;
  left: 12.5rem;
  z-index: s.$z-panel;
  max-height: calc(100dvh - 5rem);
  overflow-y: auto;
}

.floating__close {
  border: 0;
  background: transparent;
  color: var(--color-text-muted);
  font-size: s.$font-size-600;
  line-height: 1;
  cursor: pointer;

  @include t.focus-ring;

  &:hover {
    color: var(--color-text-strong);
  }
}
</style>
