<!--
  TopBar — barra superior del HUD (/play).
  Lee: session.store (corporación), finance.store (saldo cash), sim.store vía
  useSimClock (sim-time dual vivo + salud live/stale/frozen). El logout se
  emite hacia el host (que navega); la sesión se cierra vía useSession.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useFinanceStore } from '~/stores/finance.store'
import { useSessionStore } from '~/stores/session.store'
import { useSimClock } from '~/composables/useSimClock'
import { useSession } from '~/composables/useSession'
import BaseBadge from '~/components/base/BaseBadge.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import MoneyText from '~/components/base/MoneyText.vue'

const emit = defineEmits<{ 'logged-out': [] }>()

const session = useSessionStore()
const finance = useFinanceStore()
const clock = useSimClock()
const { logout } = useSession()

const healthLabel = computed(() => {
  switch (clock.health.value) {
    case 'live':
      return 'en vivo'
    case 'frozen':
      return 'mantenimiento'
    default:
      return 'sin señal'
  }
})

async function onLogout(): Promise<void> {
  await logout()
  emit('logged-out')
}
</script>

<template>
  <div class="hud-top">
    <div class="hud-top__group">
      <span class="hud-top__brand">Imperio Industrial</span>
      <span class="hud-top__corp">{{ session.accountName ?? '—' }}</span>
    </div>

    <div class="hud-top__group" aria-label="Tesorería">
      <span class="hud-top__label">Caja</span>
      <MoneyText :amount="finance.saldoCash" />
    </div>

    <div class="hud-top__group" aria-label="Tiempo de juego">
      <span class="hud-top__label">Sim</span>
      <span class="e-num">{{ clock.nowSimFormatted.value }}</span>
      <span class="hud-top__label">Pared</span>
      <span class="e-num">{{ clock.nowWallFormatted.value }}</span>
      <BaseBadge :status="clock.health.value">{{ healthLabel }}</BaseBadge>
    </div>

    <div class="hud-top__group">
      <BaseButton variant="subtle" size="sm" @click="onLogout">Salir</BaseButton>
    </div>
  </div>
</template>

<style lang="scss" scoped>
.hud-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  flex-wrap: wrap;

  &__group {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    min-width: 0;
  }

  &__brand {
    font-weight: 700;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-accent);
    font-size: 0.8125rem;
  }

  &__corp {
    font-weight: 600;
    font-size: 0.875rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  &__label {
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-text-faint);
  }
}
</style>
