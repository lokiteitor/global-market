<!-- Layout del portal (index, lobby, settings): shell SSR ligero (FAD §10.6). -->
<template>
  <div class="l-default">
    <header class="l-default__header">
      <div class="o-container o-cluster o-cluster--between">
        <NuxtLink to="/" class="l-default__brand">Imperio Industrial</NuxtLink>
        <nav class="o-cluster">
          <NuxtLink to="/lobby">Lobby</NuxtLink>
          <NuxtLink to="/settings">Ajustes</NuxtLink>
          <NuxtLink to="/login">Entrar</NuxtLink>
        </nav>
      </div>
    </header>

    <main class="l-default__main o-container">
      <slot />
    </main>

    <footer class="l-default__footer">
      <div class="o-container">
        <p>Imperio Industrial — cliente web (dev)</p>
      </div>
    </footer>
  </div>
</template>

<style lang="scss" scoped>
.l-default {
  min-height: 100vh;
  display: flex;
  flex-direction: column;

  &__header {
    border-bottom: 1px solid var(--ii-border);
    background-color: var(--ii-bg-raised);
    padding-block: 0.75rem;
  }

  &__brand {
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--ii-accent);
  }

  &__main {
    flex: 1;
    padding-block: 2rem;
  }

  &__footer {
    border-top: 1px solid var(--ii-border-subtle);
    padding-block: 0.75rem;
    color: var(--ii-text-faint);
    font-size: 0.875rem;
  }
}
</style>
