<script setup lang="ts">
/**
 * MinimapPanel — minimapa del mundo (FAD §15.11/§16.9, ADR-026).
 *
 * Canvas 2D propio en la capa Vue (desviación consciente del RenderTexture de
 * Phaser, formalizada en ADR-026): el contenido es el MODELO AGREGADO por
 * región que el propio FAD exige (coropleta de biomas + ciudades + edificios
 * propios + rect del viewport), y todos esos datos ya viven en las stores —
 * nada requiere el pipeline GL. Ver el minimapa no carga chunks.
 *
 * Entrada de cámara: `mapui.cameraViewM` (evento `camera` del motor, ~5 Hz).
 * Salida: clic/arrastre → `mapui.requestCenterOn` (comando de cámara).
 * Redibujo coalescido por rAF sobre los watchers de stores.
 */

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { WorldBoundsM } from '~shared/geometry/grid'
import { DEFAULT_WORLD_BOUNDS_M } from '~shared/geometry/grid'
import { t } from '~shared/i18n'
import type { Biome } from '~domain/world'
import { polygonBoundsM, regionsBoundsM } from '~domain/world'
import { isMine } from '~domain/ownership'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useMapUiStore } from '~/stores/mapui.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'
import type { MinimapTransform } from './minimap-math'
import { makeTransform, miniToWorld, rectToMini, worldToMini } from './minimap-math'

const world = useWorldStore()
const buildings = useBuildingsStore()
const session = useSessionStore()
const mapui = useMapUiStore()

/**
 * Paleta de biomas del minimapa: ESPEJO CONSCIENTE de BIOME_COLORS de
 * game/textures.ts (mismo precedente que la duplicación de BiomeName allí
 * documentada; comentario cruzado en ambos ficheros). Cambiar un color exige
 * cambiar los dos.
 */
const MINIMAP_BIOME_COLORS: Readonly<Record<Biome, string>> = {
  plains: '#3f5a3c',
  forest: '#2c4430',
  desert: '#7d6f45',
  mountain: '#5f6f88',
  ocean: '#1d3a54',
  coast: '#35597a',
}

const CANVAS_CSS_PX = 220
const CITY_DOT_PX = 3
const OWN_BUILDING_PX = 4

const canvas = ref<HTMLCanvasElement | null>(null)

const boundsM = computed<WorldBoundsM>(
  () => regionsBoundsM(world.regionList) ?? DEFAULT_WORLD_BOUNDS_M,
)

let raf = 0

function scheduleDraw(): void {
  if (raf !== 0) {
    return
  }
  raf = requestAnimationFrame(() => {
    raf = 0
    draw()
  })
}

function transform(): MinimapTransform | null {
  return makeTransform(boundsM.value, CANVAS_CSS_PX, CANVAS_CSS_PX)
}

function draw(): void {
  const el = canvas.value
  if (el === null) {
    return
  }
  const dpr = window.devicePixelRatio || 1
  if (el.width !== CANVAS_CSS_PX * dpr) {
    el.width = CANVAS_CSS_PX * dpr
    el.height = CANVAS_CSS_PX * dpr
  }
  const ctx = el.getContext('2d')
  const tf = transform()
  if (ctx === null || tf === null) {
    return
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, CANVAS_CSS_PX, CANVAS_CSS_PX)

  // Coropleta de regiones por bioma (bbox de sus bounds).
  for (const region of world.regionList) {
    if (region.boundsM === null) {
      continue
    }
    const box = polygonBoundsM(region.boundsM)
    if (box === null) {
      continue
    }
    const rect = rectToMini(tf, {
      xM: box.minXM,
      yM: box.minYM,
      widthM: box.maxXM - box.minXM,
      heightM: box.maxYM - box.minYM,
    })
    ctx.fillStyle = MINIMAP_BIOME_COLORS[region.biome]
    ctx.fillRect(rect.x, rect.y, rect.w, rect.h)
    ctx.strokeStyle = 'rgba(0, 0, 0, 0.35)'
    ctx.lineWidth = 1
    ctx.strokeRect(rect.x + 0.5, rect.y + 0.5, rect.w - 1, rect.h - 1)
  }

  // Ciudades: punto claro.
  ctx.fillStyle = '#e8ecf3'
  for (const city of world.cityList) {
    const p = worldToMini(tf, city.locationM[0], city.locationM[1])
    ctx.beginPath()
    ctx.arc(p.x, p.y, CITY_DOT_PX / 2, 0, Math.PI * 2)
    ctx.fill()
  }

  // Edificios PROPIOS destacados en ámbar (primer vértice del footprint).
  const myAccountId = session.account?.id ?? null
  ctx.fillStyle = '#d29224'
  for (const building of buildings.buildingList) {
    if (!isMine(building.ownerAccountId, myAccountId)) {
      continue
    }
    const anchor = building.footprintM[0]?.[0]
    if (anchor === undefined) {
      continue
    }
    const p = worldToMini(tf, anchor[0], anchor[1])
    ctx.fillRect(p.x - OWN_BUILDING_PX / 2, p.y - OWN_BUILDING_PX / 2, OWN_BUILDING_PX, OWN_BUILDING_PX)
  }

  // Rectángulo del viewport de cámara.
  const view = mapui.cameraViewM
  if (view !== null) {
    const rect = rectToMini(tf, view)
    ctx.strokeStyle = '#f0b13d'
    ctx.lineWidth = 1.5
    ctx.strokeRect(rect.x, rect.y, rect.w, rect.h)
  }
}

let dragging = false

function jumpTo(event: PointerEvent): void {
  const el = canvas.value
  const tf = transform()
  if (el === null || tf === null) {
    return
  }
  const box = el.getBoundingClientRect()
  const target = miniToWorld(tf, event.clientX - box.left, event.clientY - box.top)
  mapui.requestCenterOn(target.xM, target.yM)
}

function onPointerDown(event: PointerEvent): void {
  dragging = true
  // Feature-check: los entornos de test (happy-dom) no implementan capture.
  if (typeof canvas.value?.setPointerCapture === 'function') {
    canvas.value.setPointerCapture(event.pointerId)
  }
  jumpTo(event)
}

function onPointerMove(event: PointerEvent): void {
  if (dragging) {
    jumpTo(event)
  }
}

function onPointerUp(event: PointerEvent): void {
  dragging = false
  if (typeof canvas.value?.releasePointerCapture === 'function') {
    canvas.value.releasePointerCapture(event.pointerId)
  }
}

const stops = [
  watch(() => world.regionById, scheduleDraw),
  watch(() => world.cityById, scheduleDraw),
  watch(() => buildings.buildingById, scheduleDraw),
  watch(() => mapui.cameraViewM, scheduleDraw),
  watch(() => mapui.minimapVisible, scheduleDraw, { flush: 'post' }),
]

onMounted(scheduleDraw)

onBeforeUnmount(() => {
  for (const stop of stops) {
    stop()
  }
  if (raf !== 0) {
    cancelAnimationFrame(raf)
  }
})
</script>

<template>
  <aside class="minimap" data-testid="minimap-panel">
    <header class="minimap__bar">
      <h3 class="minimap__title">{{ t('minimap.title') }}</h3>
      <button
        type="button"
        class="minimap__toggle"
        data-testid="minimap-toggle"
        :aria-expanded="mapui.minimapVisible"
        :title="t(mapui.minimapVisible ? 'minimap.hide' : 'minimap.show')"
        @click="mapui.toggleMinimap()"
      >
        {{ mapui.minimapVisible ? '−' : '+' }}
      </button>
    </header>
    <canvas
      v-show="mapui.minimapVisible"
      ref="canvas"
      class="minimap__canvas"
      data-testid="minimap-canvas"
      :aria-label="t('minimap.title')"
      role="img"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
    />
  </aside>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.minimap {
  position: absolute;
  right: s.$space-4;
  bottom: s.$space-5;
  z-index: s.$z-hud;
  display: flex;
  flex-direction: column;
  background-color: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 0.5rem;
  overflow: hidden;
}

.minimap__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: s.$space-3;
  padding: s.$space-1 s.$space-3;
}

.minimap__title {
  color: var(--color-text-muted);
  font-size: s.$font-size-100;
  font-weight: s.$font-weight-semibold;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.minimap__toggle {
  padding: 0 s.$space-2;
  border: none;
  background: transparent;
  color: var(--color-text);
  font-size: s.$font-size-300;
  cursor: pointer;

  @include t.focus-ring;
}

.minimap__canvas {
  // Lado CSS fijo (CANVAS_CSS_PX del script; el buffer interno escala por DPR).
  display: block;
  width: 220px;
  height: 220px;
  cursor: crosshair;
  touch-action: none;
}
</style>
