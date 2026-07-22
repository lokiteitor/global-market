<script setup lang="ts">
/**
 * OhlcChart — gráfico de velas en Canvas 2D PROPIO (FAD §15.8/§11.3, sin
 * librerías de gráficos y sin competir por el contexto GL del mundo).
 *
 * Canvas 2D (no SVG): una serie de cientos de velas + volumen se repinta como
 * raster único, sin cientos de nodos DOM que reconciliar. La geometría es pura
 * (ohlc-layout.ts, BigInt para todo lo monetario); aquí solo el pintado.
 * Dirección del precio distinguible por COLOR y por FORMA (mandato de
 * accesibilidad de los tokens $color-market-up/down): vela alcista hueca,
 * bajista rellena. Colores leídos de las custom properties del tema.
 */

import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { t } from '~shared/i18n'
import { format } from '~shared/money'
import type { OhlcCandle } from '~domain/market'
import { computeOhlcLayout } from './ohlc-layout'

interface Props {
  candles: readonly OhlcCandle[]
}

const props = defineProps<Props>()

const host = ref<HTMLDivElement | null>(null)
const canvas = ref<HTMLCanvasElement | null>(null)

const HEIGHT_PX = 280

const lastClose = computed(() => {
  const last = props.candles[props.candles.length - 1]
  return last === undefined ? null : format(last.closePrice)
})

const ariaLabel = computed(() =>
  t('market.prices.chartLabel', {
    count: props.candles.length,
    last: lastClose.value ?? '—',
  }),
)

function themeColor(name: string, fallback: string): string {
  const el = host.value
  if (el === null) {
    return fallback
  }
  const value = getComputedStyle(el).getPropertyValue(name).trim()
  return value === '' ? fallback : value
}

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

function draw(): void {
  const el = canvas.value
  const container = host.value
  if (el === null || container === null) {
    return
  }
  const width = Math.max(0, Math.floor(container.clientWidth))
  const dpr = window.devicePixelRatio || 1
  el.width = Math.max(1, width * dpr)
  el.height = HEIGHT_PX * dpr
  el.style.width = `${String(width)}px`
  el.style.height = `${String(HEIGHT_PX)}px`

  const ctx = el.getContext('2d')
  if (ctx === null) {
    return
  }
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
  ctx.clearRect(0, 0, width, HEIGHT_PX)

  const layout = computeOhlcLayout(props.candles, width, HEIGHT_PX)
  if (layout === null) {
    return
  }

  const upColor = themeColor('--color-market-up', '#4caf7d')
  const downColor = themeColor('--color-market-down', '#d4645c')
  const gridColor = themeColor('--color-border', '#3a4457')
  const textColor = themeColor('--color-text-muted', '#8896ab')

  // Rejilla + etiquetas de precio (exactas, formateadas con shared/money).
  ctx.strokeStyle = gridColor
  ctx.fillStyle = textColor
  ctx.font = '10px ui-monospace, monospace'
  ctx.textBaseline = 'middle'
  ctx.lineWidth = 1
  for (const tick of layout.priceTicks) {
    ctx.globalAlpha = 0.5
    ctx.beginPath()
    ctx.moveTo(layout.pricePlot.x, tick.at)
    ctx.lineTo(layout.pricePlot.x + layout.pricePlot.w, tick.at)
    ctx.stroke()
    ctx.globalAlpha = 1
    ctx.fillText(tick.label, 4, tick.at)
  }
  ctx.textAlign = 'center'
  ctx.textBaseline = 'top'
  for (const tick of layout.timeTicks) {
    ctx.fillText(tick.label, tick.at, layout.volumePlot.y + layout.volumePlot.h + 4)
  }
  ctx.textAlign = 'start'

  // Velas: mecha siempre trazada; cuerpo hueco (up) o relleno (down).
  for (const candle of layout.candles) {
    const color = candle.up ? upColor : downColor
    ctx.strokeStyle = color
    ctx.fillStyle = color
    ctx.beginPath()
    ctx.moveTo(candle.cx, candle.wickTopY)
    ctx.lineTo(candle.cx, candle.wickBottomY)
    ctx.stroke()
    const bodyH = Math.max(1, candle.bodyBottomY - candle.bodyTopY)
    if (candle.up) {
      ctx.strokeRect(candle.cx - candle.halfW, candle.bodyTopY, candle.halfW * 2, bodyH)
    } else {
      ctx.fillRect(candle.cx - candle.halfW, candle.bodyTopY, candle.halfW * 2, bodyH)
    }
  }

  // Volumen: barras tenues con el mismo criterio de dirección.
  ctx.globalAlpha = 0.45
  for (const bar of layout.volumeBars) {
    ctx.fillStyle = bar.up ? upColor : downColor
    ctx.fillRect(bar.x, bar.y, bar.w, bar.h)
  }
  ctx.globalAlpha = 1
}

let resizeObserver: ResizeObserver | null = null

onMounted(() => {
  scheduleDraw()
  if (typeof ResizeObserver !== 'undefined' && host.value !== null) {
    resizeObserver = new ResizeObserver(scheduleDraw)
    resizeObserver.observe(host.value)
  }
})

onBeforeUnmount(() => {
  resizeObserver?.disconnect()
  if (raf !== 0) {
    cancelAnimationFrame(raf)
  }
})

watch(() => props.candles, scheduleDraw)
</script>

<template>
  <div ref="host" class="ohlc">
    <p v-if="props.candles.length === 0" class="ohlc__empty">{{ t('market.prices.empty') }}</p>
    <canvas
      v-else
      ref="canvas"
      class="ohlc__canvas"
      role="img"
      :aria-label="ariaLabel"
      data-testid="ohlc-chart"
    />
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.ohlc {
  width: 100%;
}

.ohlc__canvas {
  display: block;
}

.ohlc__empty {
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
