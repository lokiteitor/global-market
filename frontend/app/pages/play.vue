<!--
  /play — mundo de juego. Client-only por routeRules ('/play': { ssr: false }):
  el estado es en vivo (WS) y Phaser no se monta en SSR (FAD §11.2). El bundle
  de Phaser carga perezosamente dentro de GameCanvasHost (import() dinámico).

  Composición del HUD (FAD §15): esta página es el HOST que ensambla las
  piezas de los tres subsistemas —
    · layout 'game' (grid top/sidebar/main/inspector/bottom);
    · HUD Vue (TopBar/SideBar/BottomBar/InspectorPanel + panel activo);
    · GameCanvasHost (único puente components/ ↔ game/).
  Además cablea el ciclo de vida de red de la sesión de juego:
    · al entrar: join corp:<account> + alerts:<account> (el transporte
      autoconecta y hace el hello con el token; FAD §12);
    · viewport del canvas → useRooms().joinViewport (interest management;
      un join de viewport REEMPLAZA el anterior, ws-protocol.md §2);
    · al salir: leave de todas las rooms + cierre ordenado del WS (§12.12);
  y enruta los intents del event bus tipado (FAD §19) hacia sus stores dueñas:
  'world:select' → ui.select, 'ui:openPanel' → ui.openPanel,
  'ui:notify' → notifications.push.
-->
<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, type Component } from 'vue'
import { appBus } from '~/lib/kernel/event-bus'
import type { ViewportBBox } from '~/lib/net/transport'
import { useApi } from '~/composables/useApi'
import { useConnection } from '~/composables/useConnection'
import { useRooms } from '~/composables/useRooms'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useUiStore, type PanelName } from '~/stores/ui.store'
import { useWorldStore } from '~/stores/world.store'
import GameCanvasHost from '~/components/game/GameCanvasHost.vue'
import ToastHost from '~/components/base/ToastHost.vue'
import TopBar from '~/components/hud/TopBar.vue'
import SideBar from '~/components/hud/SideBar.vue'
import BottomBar from '~/components/hud/BottomBar.vue'
import InspectorPanel from '~/components/hud/InspectorPanel.vue'
import BuildPanel from '~/components/panels/BuildPanel.vue'
import CitiesPanel from '~/components/panels/CitiesPanel.vue'
import FinancePanel from '~/components/panels/FinancePanel.vue'
import FleetPanel from '~/components/panels/FleetPanel.vue'
import MarketPanel from '~/components/panels/MarketPanel.vue'
import ProductionPanel from '~/components/panels/ProductionPanel.vue'

// layout: false + <NuxtLayout name="game"> para poder rellenar los slots
// nombrados del layout desde la página. El guard 'auth' exige sesión.
definePageMeta({ layout: false, middleware: 'auth' })

const ui = useUiStore()
const notifications = useNotificationsStore()
// Capturados en setup (client-only por routeRules); el $transport existe
// porque el plugin 02.network.client ya corrió.
const rooms = useRooms()
const connection = useConnection()
const api = useApi()
const world = useWorldStore()

// ─── Panel de gestión activo (SideBar conmuta ui.store.activePanel) ─────────
// 'fleet' y 'logistics' comparten FleetPanel (flota + rutas) en v1.
const PANEL_COMPONENTS: Partial<Record<PanelName, Component>> = {
  market: MarketPanel,
  concessions: BuildPanel,
  industry: ProductionPanel,
  fleet: FleetPanel,
  logistics: FleetPanel,
  finance: FinancePanel,
  city: CitiesPanel
}
const activePanelComponent = computed<Component | null>(() =>
  ui.activePanel !== null ? (PANEL_COMPONENTS[ui.activePanel] ?? null) : null
)

/** Interest management: bbox del canvas (lon/lat) → room viewport:<bbox>. */
function onJoinViewport(bbox: ViewportBBox): void {
  rooms.joinViewport(bbox)
}

/**
 * Bootstrap del MAPA base al entrar a /play: regiones (fondo del mundo) y
 * yacimientos son catálogo ESTÁTICO que NO llega por WS — se pulsa por REST
 * una vez por sesión (world.store). Sin esto el lienzo queda vacío al entrar
 * (las regiones solo se cargaban al abrir Construcción/Mercado, y los
 * yacimientos no se cargaban nunca). El encuadre inicial de cámara sobre el
 * mundo lo hace GameCanvasHost cuando el catálogo de regiones está cargado;
 * las ciudades llegan por la room viewport: al asentarse la cámara.
 */
async function bootstrapWorldMap(): Promise<void> {
  const tasks: Array<Promise<void>> = []
  if (!world.loaded.regions) {
    tasks.push(
      api.listRegions().then((r) => {
        if (r.ok) world.setRegions(r.value.data)
      })
    )
  }
  if (!world.loaded.deposits) {
    tasks.push(
      api.listResourceDeposits().then((r) => {
        if (r.ok) world.setDeposits(r.value.data)
      })
    )
  }
  await Promise.all(tasks)
}

const PANEL_NAMES: readonly PanelName[] = ['market', 'industry', 'fleet', 'logistics', 'city', 'concessions', 'finance', 'notifications', 'settings']

const unsubscribes: Array<() => void> = []

onMounted(() => {
  // Arranque de red de la sesión de juego: rooms propias. El join autoconecta
  // el transporte si está idle/closed (hello con el token de sesión); el
  // viewport llega después desde GameCanvasHost cuando la cámara se asienta.
  rooms.joinCorp()
  rooms.joinAlerts()

  // Mapa base (regiones + yacimientos) por REST: no llega por WS. Sin bloquear
  // el resto del arranque; al resolverse encuadra la cámara sobre el mundo.
  void bootstrapWorldMap()

  // Intents del event bus → stores dueñas (game/ nunca escribe stores, O2).
  unsubscribes.push(appBus.on('world:select', ({ kind, id }) => ui.select(kind, id)))
  unsubscribes.push(
    appBus.on('ui:openPanel', ({ panel }) => {
      if ((PANEL_NAMES as readonly string[]).includes(panel)) ui.openPanel(panel as PanelName)
    })
  )
  unsubscribes.push(appBus.on('ui:notify', ({ level, text }) => notifications.push({ level, text })))
})

onBeforeUnmount(() => {
  for (const unsubscribe of unsubscribes) unsubscribe()
  unsubscribes.length = 0
  // Salida de /play: abandona las áreas de interés y cierra el WS de forma
  // ordenada (sin reconexión automática, FAD §12.12).
  rooms.leaveAll()
  connection.disconnect()
})

async function onLoggedOut(): Promise<void> {
  await navigateTo('/login')
}
</script>

<template>
  <NuxtLayout name="game">
    <template #top>
      <div id="top-bar" class="play__hud-slot">
        <TopBar @logged-out="onLoggedOut" />
      </div>
    </template>

    <template #sidebar>
      <div id="side-panel" class="play__hud-slot">
        <SideBar />
      </div>
    </template>

    <template #inspector>
      <div id="inspector" class="play__hud-slot">
        <InspectorPanel />
      </div>
    </template>

    <template #bottom>
      <div id="bottom-bar" class="play__hud-slot">
        <BottomBar />
      </div>
    </template>

    <ClientOnly>
      <!-- Host del lienzo Phaser: único punto components/ ↔ game/ (bridge). -->
      <GameCanvasHost :join-viewport="onJoinViewport" />

      <!-- Panel de gestión activo, superpuesto al canvas (FAD §15.3/§15.4). -->
      <section v-if="activePanelComponent !== null" class="play__panel" aria-label="Panel de gestión">
        <component :is="activePanelComponent" />
      </section>

      <!-- Toasts efímeros (notifications.store: alerts WS + ui:notify). -->
      <ToastHost />

      <template #fallback>
        <div class="play__loading">
          <p class="play__placeholder">Cargando cliente…</p>
        </div>
      </template>
    </ClientOnly>
  </NuxtLayout>
</template>

<style lang="scss" scoped>
.play__hud-slot {
  // Ids ESTABLES del HUD (#top-bar, #side-panel, #inspector, #bottom-bar):
  // referenciados por tooling/tests; no renombrar.
  min-height: 1rem;
}

.play__panel {
  // Panel acoplado sobre el canvas (v1: posición fija; el WindowManager con
  // paneles flotantes/redimensionables queda para una fase posterior, §15.5).
  position: absolute;
  top: 0.75rem;
  left: 0.75rem;
  bottom: 0.75rem;
  width: min(42rem, calc(100% - 1.5rem));
  overflow-y: auto;
  background-color: var(--ii-bg-raised);
  border: 1px solid var(--ii-border);
  border-radius: 6px;
  padding: 0.75rem;
  box-shadow: 0 8px 24px rgb(0 0 0 / 45%);
}

.play__loading {
  position: absolute;
  inset: 0;
  display: grid;
  place-items: center;
}

.play__placeholder {
  color: var(--ii-text-faint);
  border: 1px dashed var(--ii-border);
  border-radius: 6px;
  padding: 2rem 3rem;
}
</style>
