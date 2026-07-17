<script setup lang="ts">
/**
 * Layout de autenticación (FAD §15, Incremento 0): tarjeta centrada sobre el
 * fondo del shell, con la marca del juego encima como vuelta al portal.
 */

import { t } from '~shared/i18n'
</script>

<template>
  <div class="layout-auth">
    <div class="layout-auth__column o-stack">
      <NuxtLink class="layout-auth__brand" to="/">{{ t('app.title') }}</NuxtLink>
      <main class="layout-auth__card o-panel">
        <slot />
      </main>
    </div>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.layout-auth {
  min-height: 100dvh;
  display: grid;
  place-items: center;
  padding: s.$space-6;
}

.layout-auth__column {
  width: 100%;
  max-width: 26rem;
}

.layout-auth__brand {
  align-self: center;
  color: var(--color-text-strong);
  font-size: s.$font-size-600;
  font-weight: s.$font-weight-semibold;

  @include t.focus-ring;

  &:hover {
    text-decoration: none;
    color: var(--color-accent);
  }
}

.layout-auth__card {
  --panel-padding: #{s.$space-6};
}
</style>
