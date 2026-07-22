<script setup lang="ts">
/**
 * HudTopBar — barra superior del HUD (FAD §15.9, v1).
 *
 * Saldo cash del ledger (REFLEJADO, formateado con shared/money — jamás
 * aritmética propia), reloj del mundo (sim-time del SimClock + wall-clock),
 * indicador de conexión WS (estado del puerto NetworkTransport) y menú
 * (ajustes / cerrar sesión).
 */

import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import type { MessageKey } from '~shared/i18n'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import { formatSimTime } from '~shared/simtime'
import type { ConnectionState } from '~network/transport'
import BaseButton from '~/components/base/BaseButton.vue'
import { useConcessionAlerts } from '~/composables/useConcessionAlerts'
import { useSimFrozen } from '~/composables/useSimFrozen'
import { useSimNow } from '~/composables/useSimNow'
import { useWallClock } from '~/composables/useWallClock'
import { useFinanceStore } from '~/stores/finance.store'
import { usePanelsStore } from '~/stores/panels.store'
import { useSessionStore } from '~/stores/session.store'

interface Props {
  connection: ConnectionState
  /** Estado propio re-sincronizándose tras hueco/reconexión (marcado stale). */
  stale?: boolean
}

const props = withDefaults(defineProps<Props>(), { stale: false })

const finance = useFinanceStore()
const session = useSessionStore()
const router = useRouter()

const simNow = useSimNow()
const frozen = useSimFrozen()
const wallClock = useWallClock()

const cashText = computed(() => format(finance.cash))

const simTimeText = computed(() => {
  const now = simNow.value
  return now === null ? t('lobby.clock.unanchored') : formatSimTime(now)
})

const CONNECTION_LABEL: Record<ConnectionState, MessageKey> = {
  connecting: 'hud.connection.connecting',
  open: 'hud.connection.open',
  reconnecting: 'hud.connection.reconnecting',
  closed: 'hud.connection.closed',
}

const connectionLabel = computed(() => t(CONNECTION_LABEL[props.connection]))

// ── Aviso de concesiones (vencimiento próximo o cascada de embargo) ──────────
const panels = usePanelsStore()
const concessionAlerts = useConcessionAlerts()

const concessionAlertText = computed(() => {
  if (concessionAlerts.severity.value === 'danger') {
    return t('hud.concessions.danger', { count: concessionAlerts.atRisk.value.length })
  }
  return t('hud.concessions.warning', { count: concessionAlerts.expiringSoon.value.length })
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
  <header class="topbar">
    <div class="topbar__group">
      <span class="topbar__brand">{{ t('app.title') }}</span>
      <span class="topbar__corp">{{ session.account?.name ?? '' }}</span>
    </div>

    <div class="topbar__group">
      <span class="topbar__label">{{ t('hud.cash.label') }}</span>
      <span class="topbar__value u-numeric" data-testid="hud-cash">{{ cashText }}</span>
    </div>

    <div class="topbar__group">
      <span class="topbar__label">{{ t('lobby.simTime.label') }}</span>
      <span class="topbar__value u-numeric" data-testid="hud-sim-time">{{ simTimeText }}</span>
      <span class="topbar__wall u-numeric" data-testid="hud-wall-time">{{ wallClock ?? '—' }}</span>
      <span v-if="frozen" class="topbar__frozen">{{ t('lobby.worldStatus.maintenance') }}</span>
    </div>

    <button
      v-if="concessionAlerts.severity.value !== 'none'"
      type="button"
      class="topbar__alert"
      :class="`topbar__alert--${concessionAlerts.severity.value}`"
      data-testid="hud-concession-alert"
      @click="panels.open('concessions')"
    >
      {{ concessionAlertText }}
    </button>

    <div class="topbar__group">
      <span
        class="topbar__dot"
        :class="`topbar__dot--${props.connection}`"
        data-testid="hud-connection"
        :title="connectionLabel"
      />
      <span class="topbar__label">{{ connectionLabel }}</span>
      <span v-if="props.stale" class="topbar__stale">{{ t('common.stale') }}</span>
    </div>

    <div class="topbar__group topbar__group--menu">
      <NuxtLink class="topbar__link" to="/settings">{{ t('lobby.settings') }}</NuxtLink>
      <BaseButton variant="ghost" :loading="loggingOut" @click="onLogout">
        {{ t('login.logout') }}
      </BaseButton>
    </div>
  </header>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.topbar {
  position: absolute;
  inset-inline: 0;
  top: 0;
  z-index: s.$z-hud;
  display: flex;
  align-items: center;
  gap: s.$space-6;
  padding: s.$space-3 s.$space-5;
  background-color: var(--color-bg-raised);
  border-bottom: 1px solid var(--color-border);
  font-size: s.$font-size-300;
}

.topbar__group {
  display: flex;
  align-items: center;
  gap: s.$space-3;
  white-space: nowrap;
}

.topbar__group--menu {
  margin-left: auto;
}

.topbar__brand {
  color: var(--color-text-strong);
  font-weight: s.$font-weight-semibold;
}

.topbar__corp {
  color: var(--color-text-muted);
}

.topbar__label {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.topbar__value {
  color: var(--color-text-strong);
  font-weight: s.$font-weight-medium;
}

.topbar__wall {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}

.topbar__frozen {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.topbar__stale {
  color: var(--color-warning);
  font-size: s.$font-size-200;
}

.topbar__alert {
  padding: s.$space-1 s.$space-3;
  border: 1px solid var(--color-warning);
  border-radius: 999px;
  background: transparent;
  color: var(--color-warning);
  font-size: s.$font-size-200;
  cursor: pointer;

  @include t.focus-ring;
}

.topbar__alert--danger {
  border-color: var(--color-danger);
  color: var(--color-danger);
}

.topbar__dot {
  width: 0.6rem;
  height: 0.6rem;
  border-radius: 50%;
  background-color: var(--color-text-muted);

  &--open {
    background-color: var(--color-success);
  }

  &--connecting,
  &--reconnecting {
    background-color: var(--color-warning);
  }

  &--closed {
    background-color: var(--color-danger);
  }
}

.topbar__link {
  color: var(--color-text-muted);

  @include t.focus-ring;

  &:hover {
    color: var(--color-text);
  }
}
</style>
