<script setup lang="ts">
/**
 * BaseSpinner — indicador de actividad del UI kit (FAD §15, ADR-FE-006).
 *
 * Accesible por defecto: `role="status"` con etiqueta solo-lectores. En modo
 * `decorative` (p. ej. dentro de un BaseButton que ya anuncia `aria-busy`) se
 * oculta del árbol de accesibilidad para no duplicar anuncios.
 */

import { t } from '~shared/i18n'

interface Props {
  /** Oculto para lectores de pantalla (el contenedor ya anuncia la carga). */
  decorative?: boolean
  size?: 'sm' | 'md'
}

withDefaults(defineProps<Props>(), {
  decorative: false,
  size: 'md',
})
</script>

<template>
  <span
    :class="[$style.spinner, $style[size]]"
    :role="decorative ? undefined : 'status'"
    :aria-hidden="decorative ? 'true' : undefined"
  >
    <span v-if="!decorative" :class="$style.srOnly">{{ t('common.loading') }}</span>
  </span>
</template>

<style module lang="scss">
@use 'settings' as s;

.spinner {
  display: inline-block;
  flex: none;
  border-radius: 50%;
  border-style: solid;
  border-color: color-mix(in srgb, currentcolor 25%, transparent);
  border-top-color: currentcolor;
  animation: rotate 0.8s linear infinite;
}

.sm {
  width: 1em;
  height: 1em;
  border-width: 2px;
}

.md {
  width: 1.5rem;
  height: 1.5rem;
  border-width: 3px;
}

// Texto solo para lectores de pantalla (patrón visually-hidden).
.srOnly {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}

@keyframes rotate {
  to {
    transform: rotate(360deg);
  }
}
</style>
