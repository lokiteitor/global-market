<!--
  Layout del mundo de juego (/play): grid HUD (FAD §15).
  Regiones: top bar / sidebar / main (canvas) / inspector / bottom bar.
  Solo estructura y clases: los contenidos reales llegan por slots nombrados
  en fases posteriores; el slot por defecto es el host del canvas.
-->
<template>
  <div class="l-game">
    <header class="l-game__top">
      <slot name="top">
        <span class="l-game__placeholder">top bar</span>
      </slot>
    </header>

    <aside class="l-game__sidebar">
      <slot name="sidebar">
        <span class="l-game__placeholder">sidebar</span>
      </slot>
    </aside>

    <main class="l-game__main">
      <slot />
    </main>

    <aside class="l-game__inspector">
      <slot name="inspector">
        <span class="l-game__placeholder">inspector</span>
      </slot>
    </aside>

    <footer class="l-game__bottom">
      <slot name="bottom">
        <span class="l-game__placeholder">bottom bar</span>
      </slot>
    </footer>
  </div>
</template>

<style lang="scss" scoped>
.l-game {
  height: 100vh;
  display: grid;
  grid-template-areas:
    'top top top'
    'sidebar main inspector'
    'bottom bottom bottom';
  grid-template-rows: auto 1fr auto;
  grid-template-columns: 16rem 1fr 20rem;
  overflow: hidden;

  &__top {
    grid-area: top;
    background-color: var(--ii-bg-raised);
    border-bottom: 1px solid var(--ii-border);
    padding: 0.5rem 1rem;
  }

  &__sidebar {
    grid-area: sidebar;
    background-color: var(--ii-bg-raised);
    border-right: 1px solid var(--ii-border);
    padding: 0.75rem;
    overflow-y: auto;
  }

  &__main {
    grid-area: main;
    position: relative;
    background-color: var(--ii-bg-deep);
    overflow: hidden;
  }

  &__inspector {
    grid-area: inspector;
    background-color: var(--ii-bg-raised);
    border-left: 1px solid var(--ii-border);
    padding: 0.75rem;
    overflow-y: auto;
  }

  &__bottom {
    grid-area: bottom;
    background-color: var(--ii-bg-raised);
    border-top: 1px solid var(--ii-border);
    padding: 0.5rem 1rem;
  }

  &__placeholder {
    color: var(--ii-text-faint);
    font-size: 0.75rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
  }
}
</style>
