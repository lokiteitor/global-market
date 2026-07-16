<!--
  components/game/GameCanvasHost.vue — host del lienzo Phaser (FAD §11).

  ÚNICO componente Vue autorizado a tocar game/ (O2): monta el juego en
  onMounted con import() DINÁMICO (Phaser y el bundle de game/ jamás se
  evalúan en SSR), inyecta las deps (simNow del SimClock, event bus tipado,
  stores en lectura) y lo destruye con destroy(true) al desmontar.

  Interest management: cuando el viewport cambia, emite el bbox (lon/lat)
  hacia useRooms().joinViewport con debounce de 400 ms. La capa de red aún no
  es propiedad de esta fase: se acepta un callback opcional por prop o por
  provide('rooms') — quien integre la red lo cablea sin tocar este flujo.
-->
<script setup lang="ts">
import { inject, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { appBus } from '~/lib/kernel/event-bus'
import { createProjection } from '~/lib/kernel/projection'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCitiesStore } from '~/stores/cities.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useSessionStore } from '~/stores/session.store'
import { useSimStore } from '~/stores/sim.store'
import { useWorldStore } from '~/stores/world.store'
// Solo TIPOS de game/ (se borran al compilar): los valores llegan por import().
import type { CreatedGame } from '~/game/boot'
import type { WorldStateBridge } from '~/game/bridge'
import type { WorldBbox } from '~/game/types'

const props = defineProps<{
  /** Callback de interest management (prioritario sobre provide('rooms')). */
  joinViewport?: (bbox: WorldBbox) => void
}>()

/** Puerto de red inyectable (la capa de red lo proveerá como useRooms()). */
const rooms = inject<{ joinViewport: (bbox: WorldBbox) => void } | null>('rooms', null)

const host = ref<HTMLElement | null>(null)

const simStore = useSimStore()
const sessionStore = useSessionStore()
const worldStore = useWorldStore()
const citiesStore = useCitiesStore()
const buildingsStore = useBuildingsStore()
const fleetStore = useFleetStore()

let created: CreatedGame | null = null
let bridge: WorldStateBridge | null = null
let joinTimer: ReturnType<typeof setTimeout> | null = null
let stopFrameWatch: (() => void) | null = null
let destroyed = false

function emitViewport(bbox: WorldBbox): void {
  const join = props.joinViewport ?? rooms?.joinViewport
  if (join === undefined) return
  // Debounce 400 ms: un pan continuo produce UN solo join al asentarse.
  if (joinTimer !== null) clearTimeout(joinTimer)
  joinTimer = setTimeout(() => {
    joinTimer = null
    join(bbox)
  }, 400)
}

onMounted(async () => {
  if (host.value === null) return

  // Carga perezosa client-only de game/ (Phaser fuera del bundle SSR).
  const [{ createGame }, { createWorldBridge }] = await Promise.all([import('~/game/boot'), import('~/game/bridge')])
  if (destroyed || host.value === null) return

  created = await createGame(host.value, {
    // SimClock vía sim.store: ÚNICO origen del "ahora" de simulación (P5).
    simNow: () => simStore.now(),
    eventBus: appBus,
    // Proyección top-down v1 del kernel (900 px/grado).
    projection: createProjection(),
    onViewportChange: (bbox) => {
      bridge?.setViewport(bbox)
      emitViewport(bbox)
    }
  })

  if (destroyed) {
    created.game.destroy(true)
    created = null
    return
  }

  bridge = createWorldBridge({
    renderer: created.renderer,
    projection: createProjection(),
    stores: { world: worldStore, cities: citiesStore, buildings: buildingsStore, fleet: fleetStore },
    eventBus: appBus,
    ownAccountId: () => sessionStore.accountId
    // SIMPLIFICACIÓN v1: sin provider de red logística (nodos/enlaces) hasta
    // que exista su store; el bridge lo acepta como `getNetwork` opcional.
  })

  const initial = created.renderer.getViewportBbox()
  bridge.setViewport(initial)
  bridge.flush()
  emitViewport(initial)

  // Encuadre inicial de la cámara sobre el mundo: en cuanto el catálogo de
  // regiones esté cargado (lo pulsa play.vue por REST), ajusta zoom+centro
  // para abarcarlo. Determinista —el renderer ya existe— y se auto-desengancha
  // tras encuadrar una vez; evita la carrera del event bus con el boot de Phaser.
  stopFrameWatch = watch(
    () => worldStore.worldBbox,
    (bbox) => {
      if (bbox === null || created === null || destroyed) return
      created.renderer.frameWorld(bbox)
      stopFrameWatch?.()
      stopFrameWatch = null
    },
    { immediate: true }
  )
})

onBeforeUnmount(() => {
  destroyed = true
  stopFrameWatch?.()
  stopFrameWatch = null
  if (joinTimer !== null) clearTimeout(joinTimer)
  bridge?.dispose()
  bridge = null
  // destroy(true) desmonta el canvas y libera el contexto WebGL.
  created?.game.destroy(true)
  created = null
})
</script>

<template>
  <div ref="host" class="game-canvas-host" data-testid="game-canvas-host" />
</template>

<style lang="scss" scoped>
.game-canvas-host {
  position: absolute;
  inset: 0;
  overflow: hidden;

  // Phaser Scale.RESIZE sigue el tamaño de este contenedor.
  :deep(canvas) {
    display: block;
  }
}
</style>
