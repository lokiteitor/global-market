import { describe, expect, it } from 'vitest'

import { candle, mon, qty, st } from '~/stores/testing/fixtures'
import { computeOhlcLayout } from './ohlc-layout'

const WIDTH = 464 // 64 de eje + 400 de plot
const HEIGHT = 318 // 300 de gráfica + 18 de eje temporal

describe('charts/ohlc-layout — computeOhlcLayout', () => {
  it('sin velas o área degenerada devuelve null', () => {
    expect(computeOhlcLayout([], WIDTH, HEIGHT)).toBeNull()
    expect(computeOhlcLayout([candle()], 10, HEIGHT)).toBeNull()
  })

  it('escala de precios EXACTA en los extremos (BigInt, sin pasar por float)', () => {
    const serie = [
      candle({ bucketStartSim: st(0), openPrice: mon('100'), highPrice: mon('200'), lowPrice: mon('100'), closePrice: mon('150') }),
      candle({ bucketStartSim: st(3_600), openPrice: mon('150'), highPrice: mon('300'), lowPrice: mon('120'), closePrice: mon('130') }),
    ]
    const layout = computeOhlcLayout(serie, WIDTH, HEIGHT)
    expect(layout).not.toBeNull()
    if (!layout) return

    // Ticks: min exacto abajo, max exacto arriba, etiquetas formateadas.
    expect(layout.priceTicks[0]?.label).toBe('100')
    expect(layout.priceTicks[layout.priceTicks.length - 1]?.label).toBe('300')
    expect(layout.priceTicks[0]?.at).toBeCloseTo(layout.pricePlot.y + layout.pricePlot.h, 6)
    expect(layout.priceTicks[layout.priceTicks.length - 1]?.at).toBeCloseTo(layout.pricePlot.y, 6)

    // La mecha superior de la vela 2 toca el techo del plot (high = max).
    expect(layout.candles[1]?.wickTopY).toBeCloseTo(layout.pricePlot.y, 6)
    // La mecha inferior de la vela 1 toca el suelo (low = min).
    expect(layout.candles[0]?.wickBottomY).toBeCloseTo(layout.pricePlot.y + layout.pricePlot.h, 6)
  })

  it('dirección por forma: up (close ≥ open) marcado en vela y barra de volumen', () => {
    const serie = [
      candle({ bucketStartSim: st(0), openPrice: mon('100'), closePrice: mon('150'), volume: qty('10') }),
      candle({ bucketStartSim: st(3_600), openPrice: mon('150'), closePrice: mon('120'), volume: qty('20') }),
    ]
    const layout = computeOhlcLayout(serie, WIDTH, HEIGHT)
    if (!layout) throw new Error('layout null')
    expect(layout.candles[0]?.up).toBe(true)
    expect(layout.candles[1]?.up).toBe(false)
    expect(layout.volumeBars[0]?.up).toBe(true)
    // La barra de mayor volumen llena el plot de volumen.
    expect(layout.volumeBars[1]?.h).toBeCloseTo(layout.volumePlot.h, 6)
    expect(layout.volumeBars[0]?.h).toBeCloseTo(layout.volumePlot.h / 2, 6)
  })

  it('serie degenerada: una sola vela con precios planos no divide por cero', () => {
    const flat = candle({
      openPrice: mon('100'),
      highPrice: mon('100'),
      lowPrice: mon('100'),
      closePrice: mon('100'),
    })
    const layout = computeOhlcLayout([flat], WIDTH, HEIGHT)
    expect(layout).not.toBeNull()
    if (!layout) return
    expect(Number.isFinite(layout.candles[0]?.wickTopY ?? Number.NaN)).toBe(true)
    expect(layout.timeTicks).toHaveLength(1)
  })

  it('los ticks temporales usan el sim-time del bucket (formato AAA-DDD-HH:MM)', () => {
    const serie = [candle({ bucketStartSim: st(86_400) })]
    const layout = computeOhlcLayout(serie, WIDTH, HEIGHT)
    if (!layout) throw new Error('layout null')
    expect(layout.timeTicks[0]?.label).toMatch(/^\d{3}-\d{3}-\d{2}:\d{2}$/)
  })
})
