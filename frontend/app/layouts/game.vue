<script setup lang="ts">
/**
 * Layout de juego (FAD §15.3): lienzo a viewport completo sin el chrome del
 * portal. El HUD (top bar, sidebar, inspector, paneles) lo compone la propia
 * página /play superpuesto al canvas por z-index.
 */
</script>

<template>
  <div class="layout-game">
    <slot />
  </div>
</template>

<style scoped lang="scss">
.layout-game {
  position: relative;
  height: 100dvh;
  overflow: hidden;
  background-color: var(--color-bg);
}
</style>
