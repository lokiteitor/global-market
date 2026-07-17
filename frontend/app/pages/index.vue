<script setup lang="ts">
/**
 * Portal mínimo (FAD Incremento 0): nombre del juego, claim de una línea del
 * GDD y acceso a /login. Página estática: sin stores ni red.
 */

import { t } from '~shared/i18n'
</script>

<template>
  <div class="portal o-stack">
    <h1 class="portal__title">{{ t('app.title') }}</h1>
    <p class="portal__tagline">{{ t('app.tagline') }}</p>
    <NuxtLink class="portal__cta" to="/login">{{ t('portal.enter') }}</NuxtLink>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.portal {
  margin: auto;
  max-width: 36rem;
  text-align: center;
}

.portal__title {
  font-size: s.$font-size-800;
}

.portal__tagline {
  color: var(--color-text-muted);
  font-size: s.$font-size-500;
}

// CTA de navegación: enlace con aspecto de acción principal. Es un <a> (no un
// BaseButton) porque navega, no ejecuta un comando.
.portal__cta {
  align-self: center;
  display: inline-block;
  padding: s.$space-3 s.$space-7;
  background-color: var(--color-accent);
  border-radius: 4px;
  color: var(--color-accent-contrast);
  font-weight: s.$font-weight-medium;

  @include t.focus-ring;

  &:hover {
    background-color: var(--color-accent-hover);
    text-decoration: none;
  }
}
</style>
