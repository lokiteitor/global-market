<!-- Layout de autenticación: tarjeta centrada, sin navegación. -->
<template>
  <div class="l-auth">
    <div class="l-auth__card o-panel o-stack">
      <NuxtLink to="/" class="l-auth__brand">Imperio Industrial</NuxtLink>
      <slot />
    </div>
  </div>
</template>

<style lang="scss" scoped>
.l-auth {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 1rem;

  &__card {
    width: 100%;
    max-width: 24rem;
  }

  &__brand {
    font-weight: 700;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--ii-accent);
    text-align: center;
  }
}
</style>
