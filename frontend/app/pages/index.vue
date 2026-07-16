<!-- Portal público: hero mínimo del juego + enlaces (prerender opcional). -->
<template>
  <section class="hero o-stack o-stack--loose">
    <div class="o-stack">
      <h1 class="hero__title">Imperio Industrial</h1>
      <p class="hero__tagline">
        MMO de simulación económica sobre un mundo único y persistente: produce, comercia por contratos CCRI y
        construye la logística que mueve la economía.
      </p>
    </div>

    <div class="o-cluster">
      <NuxtLink to="/login" class="hero__cta">Entrar</NuxtLink>
      <NuxtLink to="/lobby" class="hero__link">Lobby</NuxtLink>
      <NuxtLink to="/play" class="hero__link">Ir al mundo</NuxtLink>
    </div>

    <div class="o-cluster">
      <div class="o-panel o-stack o-stack--tight hero__card">
        <h2 class="o-panel__title">Servidor autoritativo</h2>
        <p>El mundo se simula en el servidor; el cliente observa y comanda.</p>
      </div>
      <div class="o-panel o-stack o-stack--tight hero__card">
        <h2 class="o-panel__title">Economía real</h2>
        <p>Ledger de doble entrada: cada unidad de dinero y stock es trazable.</p>
      </div>
      <div class="o-panel o-stack o-stack--tight hero__card">
        <h2 class="o-panel__title">Sim-time 24×</h2>
        <p>Un día de juego por hora real; los plazos corren aunque no estés.</p>
      </div>
    </div>
  </section>
</template>

<style lang="scss" scoped>
.hero {
  padding-block: 2rem;

  &__title {
    font-size: 2.5rem;
    color: var(--ii-accent);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }

  &__tagline {
    max-width: 42rem;
    color: var(--ii-text-muted);
    font-size: 1.125rem;
  }

  &__cta {
    display: inline-block;
    background-color: var(--ii-accent);
    color: var(--ii-bg-deep);
    font-weight: 600;
    padding: 0.5rem 1.5rem;
    border-radius: 6px;

    &:hover,
    &:focus-visible {
      background-color: var(--ii-accent-strong);
      color: var(--ii-bg-deep);
      text-decoration: none;
    }
  }

  &__link {
    padding: 0.5rem 0.75rem;
  }

  &__card {
    flex: 1 1 14rem;
  }
}
</style>
