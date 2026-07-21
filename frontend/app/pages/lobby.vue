<script setup lang="ts">
/**
 * Lobby / sala de operaciones (FAD Incremento 0, middleware auth).
 *
 * Muestra la corporación autenticada (refrescada de /auth/me), el reloj del
 * mundo en vivo (sim-time del SimClock vía useSimNow + formatSimTime, junto al
 * wall-clock local y el indicador de ritmo ×24, FAD §15.9) y el estado del
 * mundo: banner de mantenimiento cuando el cliente está `frozen` (FAD §12.9).
 */

import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { MessageKey } from '~shared/i18n'
import { t } from '~shared/i18n'
import { formatSimTime } from '~shared/simtime'
import type { AccountKind, AccountStatus } from '~domain/auth'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BasePanel from '~/components/base/BasePanel.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import { useAppError } from '~/composables/useAppError'
import { useSimFrozen } from '~/composables/useSimFrozen'
import { useSimNow } from '~/composables/useSimNow'
import { useWallClock } from '~/composables/useWallClock'
import { useSessionStore } from '~/stores/session.store'

definePageMeta({ middleware: 'auth' })

const session = useSessionStore()
const router = useRouter()
const { messageFor, isMaintenance } = useAppError()

const simNow = useSimNow()
const frozen = useSimFrozen()
const wallClock = useWallClock()

/** Los enums de dominio se traducen por diccionario tipado, nunca por concatenación. */
const KIND_LABEL: Record<AccountKind, MessageKey> = {
  human: 'account.kind.human',
  bot: 'account.kind.bot',
  city: 'account.kind.city',
  system: 'account.kind.system',
}
const STATUS_LABEL: Record<AccountStatus, MessageKey> = {
  active: 'account.status.active',
  suspended: 'account.status.suspended',
  retired: 'account.status.retired',
}

const account = computed(() => session.account)
const kindLabel = computed(() => {
  const current = account.value
  return current === null ? '' : t(KIND_LABEL[current.kind])
})
const statusLabel = computed(() => {
  const current = account.value
  return current === null ? '' : t(STATUS_LABEL[current.status])
})

/** Sim-time formateado por el ÚNICO formateador del kernel (P5/ADR-FE-007). */
const simTimeText = computed(() => {
  const now = simNow.value
  return now === null ? t('lobby.clock.unanchored') : formatSimTime(now)
})
const wallClockText = computed(() => wallClock.value ?? '—')

const fetchError = ref<unknown>(null)
const fetchBanner = computed<{ variant: 'warn' | 'error'; message: string } | null>(() => {
  const error = fetchError.value
  if (error === null) {
    return null
  }
  if (isMaintenance(error)) {
    return { variant: 'warn', message: t('error.MAINTENANCE_WINDOW') }
  }
  return { variant: 'error', message: messageFor(error) }
})

onMounted(async () => {
  try {
    // Refresca la corporación desde /auth/me (la del login puede ser vieja).
    await session.fetchMe()
  } catch (error) {
    fetchError.value = error
    if (!session.isAuthenticated) {
      // 401: la store ya purgó la sesión caducada — de vuelta al login.
      await router.replace('/login')
    }
  }
})

const loggingOut = ref(false)

async function onLogout(): Promise<void> {
  loggingOut.value = true
  try {
    await session.logout()
  } finally {
    loggingOut.value = false
    await router.push('/login')
  }
}
</script>

<template>
  <div class="lobby o-stack">
    <header class="lobby__header o-cluster">
      <h1 class="lobby__title">{{ t('lobby.title') }}</h1>
      <div class="lobby__actions o-cluster">
        <NuxtLink class="lobby__enter-link" to="/play">{{ t('lobby.enterWorld') }}</NuxtLink>
        <NuxtLink class="lobby__settings-link" to="/settings">{{ t('lobby.settings') }}</NuxtLink>
        <BaseButton variant="ghost" :loading="loggingOut" @click="onLogout">
          {{ t('login.logout') }}
        </BaseButton>
      </div>
    </header>

    <BaseBanner v-if="fetchBanner !== null" :variant="fetchBanner.variant">
      {{ fetchBanner.message }}
    </BaseBanner>

    <div class="lobby__grid">
      <BasePanel :title="t('lobby.account.title')">
        <template v-if="account !== null">
          <p class="lobby__welcome">
            {{ t('lobby.welcome', { corporationName: account.name }) }}
          </p>
          <dl class="lobby__facts">
            <div class="lobby__fact">
              <dt>{{ t('lobby.account.kind') }}</dt>
              <dd>{{ kindLabel }}</dd>
            </div>
            <div class="lobby__fact">
              <dt>{{ t('lobby.account.status') }}</dt>
              <dd>{{ statusLabel }}</dd>
            </div>
          </dl>
        </template>
        <BaseSpinner v-else />
      </BasePanel>

      <BasePanel :title="t('lobby.clock.title')">
        <dl class="lobby__facts">
          <div class="lobby__fact">
            <dt>{{ t('lobby.simTime.label') }}</dt>
            <dd class="u-numeric" data-testid="sim-time">{{ simTimeText }}</dd>
          </div>
          <div class="lobby__fact">
            <dt>{{ t('lobby.clock.wall') }}</dt>
            <dd class="u-numeric" data-testid="wall-time">{{ wallClockText }}</dd>
          </div>
        </dl>
        <p class="lobby__ratio">{{ t('lobby.clock.ratio') }}</p>
      </BasePanel>
    </div>

    <BasePanel :title="t('lobby.worldStatus.label')">
      <BaseBanner v-if="frozen" variant="warn" :title="t('maintenance.title')">
        {{ t('maintenance.body') }}
      </BaseBanner>
      <p v-else class="lobby__world-live">{{ t('lobby.worldStatus.live') }}</p>
    </BasePanel>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.lobby__header {
  justify-content: space-between;
}

.lobby__title {
  font-size: s.$font-size-700;
}

.lobby__settings-link {
  color: var(--color-text-muted);

  @include t.focus-ring;

  &:hover {
    color: var(--color-text);
  }
}

.lobby__enter-link {
  color: var(--color-accent);
  font-weight: s.$font-weight-medium;

  @include t.focus-ring;

  &:hover {
    color: var(--color-accent-hover);
  }
}

.lobby__grid {
  display: grid;
  gap: s.$space-5;

  @include t.media('md') {
    grid-template-columns: 1fr 1fr;
  }
}

.lobby__welcome {
  margin-bottom: s.$space-4;
  color: var(--color-text-strong);
  font-weight: s.$font-weight-medium;
}

.lobby__facts {
  display: flex;
  flex-direction: column;
  gap: s.$space-3;
}

.lobby__fact {
  display: flex;
  justify-content: space-between;
  gap: s.$space-4;

  dt {
    color: var(--color-text-muted);
  }

  dd {
    color: var(--color-text-strong);
  }
}

.lobby__ratio {
  margin-top: s.$space-4;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.lobby__world-live {
  color: var(--color-success);
  font-weight: s.$font-weight-medium;
}
</style>
