<!--
  /lobby — antesala del mundo: sim-time actual, resumen de la corporación
  (saldo, edificios, vehículos vía REST) y acceso a /play. Los datos son un
  reflejo puntual del servidor (P1); el estado vivo llega en /play por WS.
-->
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { formatMoney, ZERO_MONEY, type Money } from '~/lib/kernel/money'
import { useApi } from '~/composables/useApi'
import { useSession } from '~/composables/useSession'
import { useSimClock } from '~/composables/useSimClock'
import { useSessionStore } from '~/stores/session.store'
import BaseBadge from '~/components/base/BaseBadge.vue'

// Guard de sesión: sin token → /login (client-only; ver middleware/auth.ts).
definePageMeta({ middleware: 'auth' })

const api = useApi()
const session = useSessionStore()
const { restore } = useSession()
const clock = useSimClock()

const cash = ref<Money>(ZERO_MONEY)
const buildingCount = ref<number | null>(null)
const vehicleCount = ref<number | null>(null)
const loaded = ref(false)

const cashFormatted = computed(() => formatMoney(cash.value))

onMounted(async () => {
  // SIMPLIFICACIÓN v1: restauración de sesión desde sessionStorage (dev).
  restore()
  if (!session.isAuthenticated) return

  const [accounts, buildings, vehicles] = await Promise.all([
    api.listLedgerAccounts({ kind: 'cash' }),
    api.listBuildings(),
    api.listVehicles()
  ])
  if (accounts.ok) {
    const cashAccount = accounts.value.data.find((a) => a.kind === 'cash' && a.product_id === undefined)
    cash.value = cashAccount?.balance ?? ZERO_MONEY
  }
  if (buildings.ok) buildingCount.value = buildings.value.data.length
  if (vehicles.ok) vehicleCount.value = vehicles.value.data.length
  loaded.value = true
})
</script>

<template>
  <section class="o-stack">
    <h1>Lobby</h1>

    <div class="o-cluster">
      <div class="o-panel o-stack o-stack--tight lobby__card" aria-label="Mundo">
        <h2 class="o-panel__title">Mundo — Imperio Industrial</h2>
        <p>
          Sim-time actual: <span class="e-num">{{ clock.nowSimFormatted.value }}</span>
          <BaseBadge :status="clock.health.value" class="lobby__badge">
            {{ clock.health.value === 'frozen' ? 'mantenimiento' : clock.health.value === 'live' ? 'en marcha' : 'sin señal' }}
          </BaseBadge>
        </p>
        <p class="lobby__faint">Un día de juego por hora real (ratio 24×); los plazos corren aunque no estés.</p>
      </div>

      <div class="o-panel o-stack o-stack--tight lobby__card" aria-label="Corporación">
        <h2 class="o-panel__title">Corporación</h2>
        <template v-if="session.isAuthenticated">
          <p><strong>{{ session.accountName }}</strong></p>
          <dl class="lobby__facts">
            <div><dt>Caja</dt><dd class="e-num">{{ cashFormatted }}</dd></div>
            <div><dt>Edificios</dt><dd class="e-num">{{ buildingCount ?? '…' }}</dd></div>
            <div><dt>Vehículos</dt><dd class="e-num">{{ vehicleCount ?? '…' }}</dd></div>
          </dl>
        </template>
        <template v-else>
          <p>Sin sesión activa.</p>
          <p><NuxtLink to="/login">Iniciar sesión →</NuxtLink></p>
        </template>
      </div>
    </div>

    <p v-if="session.isAuthenticated">
      <NuxtLink to="/play" class="lobby__cta">Entrar al mundo →</NuxtLink>
    </p>
  </section>
</template>

<style lang="scss" scoped>
.lobby__card {
  flex: 1 1 16rem;
}

.lobby__badge {
  margin-left: 0.5rem;
}

.lobby__faint {
  color: var(--ii-text-faint);
  font-size: 0.875rem;
}

.lobby__facts {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;

  > div {
    display: flex;
    justify-content: space-between;
  }

  dt {
    color: var(--ii-text-muted);
  }
}

.lobby__cta {
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
</style>
