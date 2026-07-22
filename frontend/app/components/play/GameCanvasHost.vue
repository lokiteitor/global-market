<script setup lang="ts">
/**
 * GameCanvasHost — host del motor Phaser en /play (FAD §11.2, O7).
 *
 * Carga PEREZOSA: `await import('~~/game')` en onMounted — Phaser jamás entra
 * en el bundle del portal (aquí solo hay imports type-only, erasados). Monta
 * el juego, implementa `GameDeps.biomeAtM` sobre los bounds de región del
 * estado replicado (el backend no expone terreno por tile: suelo plano por
 * bioma de región, listo para datos por tile futuros), crea el mundo vivo
 * (bridge sobre las stores vía `createWorldStateSource`) y lo ata a la UI con
 * `bindWorldLive`. Los intents espaciales (build/parcel) se re-emiten a la
 * página, que los convierte en diálogos de confirmación → comandos REST.
 */

import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WorldBoundsM } from '~shared/geometry/grid'
import { t } from '~shared/i18n'
import { polygonContainsPointM } from '~domain/geo'
import type { Biome } from '~domain/world'
import { regionsBoundsM } from '~domain/world'
import type { WorldIntent } from '~~/game'
import BaseSpinner from '~/components/base/BaseSpinner.vue'
import { bindWorldLive, createWorldStateSource } from '~/composables/useWorldLive'
import { useWorldStore } from '~/stores/world.store'

const emit = defineEmits<{ intent: [intent: WorldIntent] }>()

const host = ref<HTMLDivElement | null>(null)
const booting = ref(true)
const failed = ref(false)

const world = useWorldStore()
// El puerto del bridge se crea en setup (contexto Pinia/Nuxt activo).
const sourceHandle = createWorldStateSource()

/** Bioma por bounds de región; con una única región, su bioma cubre el mundo. */
function biomeAtM(xM: number, yM: number): Biome | null {
  const regions = world.regionList
  for (const region of regions) {
    if (region.boundsM !== null && polygonContainsPointM(region.boundsM, xM, yM)) {
      return region.biome
    }
  }
  const only = regions.length === 1 ? regions[0] : undefined
  return only === undefined ? null : only.biome
}

function boundsEqual(a: WorldBoundsM, b: WorldBoundsM | null): boolean {
  return (
    b !== null &&
    a.minXM === b.minXM &&
    a.minYM === b.minYM &&
    a.maxXM === b.maxXM &&
    a.maxYM === b.maxYM
  )
}

let cleanup: (() => void) | null = null

onMounted(async () => {
  const parent = host.value
  if (parent === null) {
    return
  }
  try {
    const engine = await import('~~/game')
    const created = await engine.createGame(parent, { biomeAtM })
    const live = engine.createWorldLive({ worldApi: created.worldApi, source: sourceHandle.source })
    const unbind = bindWorldLive(live)
    const offIntent = live.on('intent', (intent) => {
      emit('intent', intent)
    })
    // Límites del mundo derivados del catálogo de regiones (FAD §17.6). El
    // juego monta en paralelo al bootstrap REST: el watcher inmediato cubre
    // tanto el catálogo ya presente como su llegada/refresco posterior.
    let appliedBounds: WorldBoundsM | null = null
    const stopBounds = watch(
      () => world.regionById,
      () => {
        const bounds = regionsBoundsM(world.regionList)
        if (bounds === null || boundsEqual(bounds, appliedBounds)) {
          return
        }
        appliedBounds = bounds
        created.worldApi.setWorldBoundsM(bounds)
      },
      { immediate: true },
    )
    cleanup = () => {
      stopBounds()
      offIntent()
      unbind()
      live.destroy()
      created.destroy()
    }
    booting.value = false
  } catch {
    failed.value = true
    booting.value = false
  }
})

onBeforeUnmount(() => {
  cleanup?.()
  cleanup = null
  sourceHandle.dispose()
})
</script>

<template>
  <div ref="host" class="canvas-host" data-testid="canvas-host">
    <div v-if="booting" class="canvas-host__veil">
      <BaseSpinner />
      <p>{{ t('play.canvas.loading') }}</p>
    </div>
    <div v-else-if="failed" class="canvas-host__veil">
      <p>{{ t('play.canvas.error') }}</p>
    </div>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.canvas-host {
  position: absolute;
  inset: 0;
  z-index: s.$z-canvas;
}

.canvas-host__veil {
  position: absolute;
  inset: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: s.$space-4;
  color: var(--color-text-muted);
}
</style>
