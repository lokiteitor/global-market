/**
 * charts/ohlc-layout — geometría PURA del gráfico de velas (FAD §15.8).
 *
 * Calcula escalas, ticks y rectángulos SIN tocar canvas: testeable en vitest.
 * Los IMPORTES nunca pasan por `number` para decidir nada (C11): min/max y
 * ticks se calculan con la aritmética BigInt de shared/money (compare/
 * subtract/add/format) y las etiquetas son exactas; solo la POSICIÓN en
 * píxeles usa `toApproxNumber` (la única escotilla, presentación pura — un
 * error de ulp en un pixel es invisible e inofensivo).
 *
 * Reparto vertical: 75% precios, 25% volumen (separados por un gap).
 */

import { ZERO, add, compare, format, subtract, toApproxNumber } from '~shared/money'
import type { Money } from '~shared/money'
import { formatSimTime } from '~shared/simtime'
import type { OhlcCandle } from '~domain/market'

export interface PlotRect {
  readonly x: number
  readonly y: number
  readonly w: number
  readonly h: number
}

export interface CandleGeom {
  /** Centro X de la vela (px). */
  readonly cx: number
  /** Mitad del ancho del cuerpo (px). */
  readonly halfW: number
  readonly wickTopY: number
  readonly wickBottomY: number
  readonly bodyTopY: number
  readonly bodyBottomY: number
  /** Alcista (close ≥ open): cuerpo HUECO además del color (forma, FAD §15.8). */
  readonly up: boolean
}

export interface VolumeBarGeom {
  readonly x: number
  readonly y: number
  readonly w: number
  readonly h: number
  readonly up: boolean
}

export interface AxisTick {
  /** Posición (Y para precios, X para tiempo) en px. */
  readonly at: number
  /** Etiqueta EXACTA (format de shared/money o sim-time). */
  readonly label: string
}

export interface OhlcLayout {
  readonly pricePlot: PlotRect
  readonly volumePlot: PlotRect
  readonly candles: readonly CandleGeom[]
  readonly volumeBars: readonly VolumeBarGeom[]
  readonly priceTicks: readonly AxisTick[]
  readonly timeTicks: readonly AxisTick[]
}

/** Margen izquierdo para etiquetas de precio; inferior para las de tiempo. */
const LEFT_AXIS_PX = 64
const BOTTOM_AXIS_PX = 18
const VOLUME_SHARE = 0.25
const PLOT_GAP_PX = 8
const PRICE_TICKS = 5
const TIME_TICKS = 4

function maxMoney(a: Money, b: Money): Money {
  return compare(a, b) >= 0 ? a : b
}

function minMoney(a: Money, b: Money): Money {
  return compare(a, b) <= 0 ? a : b
}

/** Divide un importe por un entero (floor, BigInt) — solo para ticks. */
function divMoney(amount: Money, divisor: number): Money {
  return (BigInt(amount) / BigInt(divisor)).toString(10) as Money
}

/**
 * Layout completo para un canvas `width × height` (px CSS). `null` si no hay
 * velas o el área es degenerada.
 */
export function computeOhlcLayout(
  candles: readonly OhlcCandle[],
  width: number,
  height: number,
): OhlcLayout | null {
  const first = candles[0]
  if (first === undefined || width <= LEFT_AXIS_PX || height <= BOTTOM_AXIS_PX) {
    return null
  }

  // ── Rango de precios (BigInt exacto) ──────────────────────────────────────
  let minPrice = first.lowPrice
  let maxPrice = first.highPrice
  let maxVolume = first.volume
  for (const candle of candles) {
    minPrice = minMoney(minPrice, candle.lowPrice)
    maxPrice = maxMoney(maxPrice, candle.highPrice)
    if (BigInt(candle.volume) > BigInt(maxVolume)) {
      maxVolume = candle.volume
    }
  }
  // Rango cero (precios planos): un colchón de ±1 para que la línea sea visible.
  if (compare(minPrice, maxPrice) === 0) {
    maxPrice = add(maxPrice, '1' as Money)
    minPrice = compare(minPrice, ZERO) === 0 ? minPrice : subtract(minPrice, '1' as Money)
  }
  const priceRange = subtract(maxPrice, minPrice)
  const priceRangeApprox = toApproxNumber(priceRange)

  const plotW = width - LEFT_AXIS_PX
  const chartH = height - BOTTOM_AXIS_PX
  const volumeH = Math.floor(chartH * VOLUME_SHARE)
  const priceH = chartH - volumeH - PLOT_GAP_PX
  if (plotW <= 0 || priceH <= 0 || volumeH <= 0) {
    return null
  }
  const pricePlot: PlotRect = { x: LEFT_AXIS_PX, y: 0, w: plotW, h: priceH }
  const volumePlot: PlotRect = { x: LEFT_AXIS_PX, y: priceH + PLOT_GAP_PX, w: plotW, h: volumeH }

  /** Y del plot de precios para un importe (aprox SOLO en posición). */
  const priceY = (value: Money): number => {
    const offset = toApproxNumber(subtract(value, minPrice))
    return pricePlot.y + pricePlot.h - (offset / priceRangeApprox) * pricePlot.h
  }

  // ── Velas ─────────────────────────────────────────────────────────────────
  const slot = plotW / candles.length
  const halfW = Math.max(1, Math.min(8, Math.floor(slot * 0.35)))
  const geoms: CandleGeom[] = []
  const volumeBars: VolumeBarGeom[] = []
  const maxVolumeApprox = toApproxNumber(maxVolume)
  candles.forEach((candle, i) => {
    const cx = pricePlot.x + slot * (i + 0.5)
    const up = compare(candle.closePrice, candle.openPrice) >= 0
    const bodyHigh = maxMoney(candle.openPrice, candle.closePrice)
    const bodyLow = minMoney(candle.openPrice, candle.closePrice)
    geoms.push({
      cx,
      halfW,
      wickTopY: priceY(candle.highPrice),
      wickBottomY: priceY(candle.lowPrice),
      bodyTopY: priceY(bodyHigh),
      bodyBottomY: priceY(bodyLow),
      up,
    })
    const volumeApprox = maxVolumeApprox === 0 ? 0 : toApproxNumber(candle.volume) / maxVolumeApprox
    const barH = volumeApprox * volumePlot.h
    volumeBars.push({
      x: cx - halfW,
      y: volumePlot.y + volumePlot.h - barH,
      w: halfW * 2,
      h: barH,
      up,
    })
  })

  // ── Ticks de precio (etiquetas EXACTAS: min + k×floor(rango/(n−1))) ───────
  const priceTicks: AxisTick[] = []
  const step = divMoney(priceRange, PRICE_TICKS - 1)
  let tickValue = minPrice
  for (let k = 0; k < PRICE_TICKS; k += 1) {
    // El último tick es el máximo exacto (el floor acumulado se corrige).
    const value = k === PRICE_TICKS - 1 ? maxPrice : tickValue
    priceTicks.push({ at: priceY(value), label: format(value) })
    tickValue = add(tickValue, step)
  }

  // ── Ticks de tiempo (sim-time del inicio de bucket) ───────────────────────
  const timeTicks: AxisTick[] = []
  const tickCount = Math.min(TIME_TICKS, candles.length)
  for (let k = 0; k < tickCount; k += 1) {
    const index =
      tickCount === 1 ? 0 : Math.round((k * (candles.length - 1)) / (tickCount - 1))
    const candle = candles[index]
    if (candle !== undefined) {
      timeTicks.push({
        at: pricePlot.x + slot * (index + 0.5),
        label: formatSimTime(candle.bucketStartSim),
      })
    }
  }

  return { pricePlot, volumePlot, candles: geoms, volumeBars, priceTicks, timeTicks }
}
