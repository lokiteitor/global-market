<script setup lang="ts">
/**
 * /play — el CLIENTE JUGABLE (Incremento 5, FAD §15.3).
 *
 * Composición del juego completo: canvas Phaser (GameCanvasHost, carga
 * perezosa client-only) + HUD DOM superpuesto (top bar, sidebar, inspector,
 * paneles flotantes, diálogos de confirmación de intents espaciales).
 *
 * Sincronización (useGameSync): join a la room corp → bootstrap REST →
 * deltas WS con refetch dirigido → resync ante hueco/reconexión → refrescos
 * suaves periódicos. Todo el estado replicado entra por respuestas del
 * servidor (thin client: el cliente presenta, nunca decide).
 *
 * Client-only por <ClientOnly>: el sim-time vivo, el WS y Phaser son
 * presentación de cliente; el shell SSR solo entrega el esqueleto.
 */

import { computed, onBeforeUnmount, onMounted, watch } from 'vue'
import { t } from '~shared/i18n'
import type { WorldIntent } from '~~/game'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import BuildToolPanel from '~/components/play/BuildToolPanel.vue'
import ConcessionsPanel from '~/components/play/ConcessionsPanel.vue'
import ConfirmBuildDialog from '~/components/play/ConfirmBuildDialog.vue'
import ConfirmParcelDialog from '~/components/play/ConfirmParcelDialog.vue'
import FinancePanel from '~/components/play/FinancePanel.vue'
import FleetPanel from '~/components/play/FleetPanel.vue'
import GameCanvasHost from '~/components/play/GameCanvasHost.vue'
import HudSidebar from '~/components/play/HudSidebar.vue'
import HudTopBar from '~/components/play/HudTopBar.vue'
import IndustryPanel from '~/components/play/IndustryPanel.vue'
import InspectorPanel from '~/components/play/InspectorPanel.vue'
import MarketPanel from '~/components/play/MarketPanel.vue'
import { useAppError } from '~/composables/useAppError'
import { useGameSync } from '~/composables/useGameSync'
import { useSimFrozen } from '~/composables/useSimFrozen'
import { useMapUiStore } from '~/stores/mapui.store'
import { usePanelsStore } from '~/stores/panels.store'

definePageMeta({ layout: 'game', middleware: 'auth' })

const mapui = useMapUiStore()
const panels = usePanelsStore()
const frozen = useSimFrozen()
const { messageFor } = useAppError()

const sync = useGameSync()

/**
 * Hook de test E2E — SOLO en dev (`import.meta.dev`): señala a Playwright
 * (tests/e2e-browser/play.spec.ts) que el bootstrap REST+WS terminó y el
 * estado replicado está listo. En el build de producción `import.meta.dev`
 * es `false` estático y la asignación desaparece del bundle; el guard de
 * `import.meta.client` evita tocar `window` durante el SSR del shell.
 */
function exposeWorldReadyFlag(ready: boolean): void {
  if (!import.meta.dev || !import.meta.client) {
    return
  }
  ;(window as Window & { __II_WORLD_READY__?: boolean }).__II_WORLD_READY__ = ready
}

watch(sync.phase, (phase) => {
  exposeWorldReadyFlag(phase === 'ready')
})

onMounted(() => {
  void sync.start()
})

onBeforeUnmount(() => {
  sync.stop()
  mapui.reset()
  panels.reset()
  exposeWorldReadyFlag(false)
})

/** Intents espaciales del mundo vivo → diálogos de confirmación (comando REST). */
function onIntent(intent: WorldIntent): void {
  if (intent.type === 'build') {
    panels.setPendingBuild(intent)
    return
  }
  panels.setPendingParcel(intent)
}

const bootError = computed(() =>
  sync.phase.value === 'error' && sync.lastError.value !== null
    ? messageFor(sync.lastError.value)
    : null,
)

function onRetry(): void {
  void sync.start()
}
</script>

<template>
  <div class="play">
    <ClientOnly>
      <GameCanvasHost @intent="onIntent" />

      <HudTopBar :connection="sync.connection.value" :stale="sync.stale.value" />
      <HudSidebar />
      <InspectorPanel />

      <MarketPanel v-if="panels.activePanel === 'market'" />
      <IndustryPanel v-else-if="panels.activePanel === 'industry'" />
      <FleetPanel v-else-if="panels.activePanel === 'fleet'" />
      <FinancePanel v-else-if="panels.activePanel === 'finance'" />
      <ConcessionsPanel v-else-if="panels.activePanel === 'concessions'" />
      <BuildToolPanel v-else-if="panels.activePanel === 'build'" />

      <ConfirmBuildDialog
        v-if="panels.pendingBuild !== null"
        :intent="panels.pendingBuild"
        @close="panels.setPendingBuild(null)"
      />
      <ConfirmParcelDialog
        v-if="panels.pendingParcel !== null"
        :intent="panels.pendingParcel"
        @close="panels.setPendingParcel(null)"
      />

      <div v-if="sync.phase.value === 'bootstrapping'" class="play__boot">
        <BaseSpinner />
        <p>{{ t('play.bootstrapping') }}</p>
      </div>

      <div v-if="bootError !== null" class="play__error">
        <BaseBanner variant="error" :title="t('play.bootError.title')">
          {{ bootError }}
        </BaseBanner>
        <BaseButton @click="onRetry">{{ t('common.retry') }}</BaseButton>
      </div>

      <div v-if="frozen" class="play__frozen">
        <BaseBanner variant="warn" :title="t('maintenance.title')">
          {{ t('maintenance.body') }}
        </BaseBanner>
      </div>

      <template #fallback>
        <div class="play__boot">
          <BaseSpinner />
          <p>{{ t('common.loading') }}</p>
        </div>
      </template>
    </ClientOnly>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.play {
  position: absolute;
  inset: 0;
}

.play__boot {
  position: absolute;
  inset: 0;
  z-index: s.$z-overlay-system;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: s.$space-4;
  background-color: rgb(0 0 0 / 45%);
  color: var(--color-text);
  pointer-events: none;
}

.play__error {
  position: absolute;
  top: 25%;
  left: 50%;
  z-index: s.$z-overlay-system;
  display: flex;
  flex-direction: column;
  gap: s.$space-4;
  width: min(28rem, calc(100vw - 3rem));
  transform: translateX(-50%);
}

.play__frozen {
  position: absolute;
  bottom: s.$space-5;
  left: 50%;
  z-index: s.$z-overlay-system;
  width: min(30rem, calc(100vw - 3rem));
  transform: translateX(-50%);
}
</style>
