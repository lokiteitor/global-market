<script setup lang="ts">
/**
 * GameDialog — diálogo modal mínimo del HUD (FAD §15.6, v1).
 *
 * Un solo modal a la vez (lo garantiza el llamador); overlay que bloquea el
 * fondo, cierre por Esc y por botón. Sin focus-trap completo en v1
 * (documentado como deuda de accesibilidad consciente).
 */

import { onBeforeUnmount, onMounted } from 'vue'
import { t } from '~shared/i18n'
import BasePanel from '~/components/base/BasePanel.vue'

interface Props {
  title: string
}

defineProps<Props>()

const emit = defineEmits<{ close: [] }>()

function onKeydown(event: KeyboardEvent): void {
  if (event.key === 'Escape') {
    emit('close')
  }
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="dialog" role="dialog" aria-modal="true">
    <div class="dialog__overlay" @click="emit('close')" />
    <div class="dialog__panel">
      <BasePanel :title="title">
        <template #actions>
          <button
            class="dialog__close"
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
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.dialog {
  position: fixed;
  inset: 0;
  z-index: s.$z-modal;
  display: flex;
  align-items: center;
  justify-content: center;
}

.dialog__overlay {
  position: absolute;
  inset: 0;
  background-color: rgb(0 0 0 / 55%);
}

.dialog__panel {
  position: relative;
  width: min(34rem, calc(100vw - #{s.$space-7}));
  max-height: calc(100dvh - #{s.$space-8});
  overflow-y: auto;
}

.dialog__close {
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
