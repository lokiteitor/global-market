<script setup lang="ts">
/**
 * Layout del portal/shell (FAD §15, Incremento 0): cabecera con el nombre del
 * juego, contenido y pie sobrio. El layout de juego (canvas + HUD, §15.3)
 * llegará en incrementos posteriores como layout aparte.
 */

import { t } from '~shared/i18n'
</script>

<template>
  <div class="layout-default">
    <header class="layout-default__header">
      <NuxtLink class="layout-default__brand" to="/">{{ t('app.title') }}</NuxtLink>
    </header>
    <main class="layout-default__main">
      <slot />
    </main>
    <footer class="layout-default__footer">{{ t('app.footer') }}</footer>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.layout-default {
  min-height: 100dvh;
  display: flex;
  flex-direction: column;
}

.layout-default__header {
  display: flex;
  align-items: center;
  padding: s.$space-4 s.$space-6;
  background-color: var(--color-bg-raised);
  border-bottom: 1px solid var(--color-border);
}

.layout-default__brand {
  color: var(--color-text-strong);
  font-size: s.$font-size-500;
  font-weight: s.$font-weight-semibold;

  @include t.focus-ring;

  &:hover {
    text-decoration: none;
    color: var(--color-accent);
  }
}

.layout-default__main {
  flex: 1;
  display: flex;
  flex-direction: column;
  width: 100%;
  max-width: 80rem;
  margin-inline: auto;
  padding: s.$space-6;
}

.layout-default__footer {
  padding: s.$space-4 s.$space-6;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
