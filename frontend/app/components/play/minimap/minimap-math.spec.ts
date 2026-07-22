import { describe, expect, it } from 'vitest'

import type { WorldBoundsM } from '~shared/geometry/grid'
import { makeTransform, miniToWorld, rectToMini, worldToMini } from './minimap-math'

/** Mundo 3×3 centrado en Askadia: [-50 000, 100 000)². */
const GRID3: WorldBoundsM = { minXM: -50_000, minYM: -50_000, maxXM: 100_000, maxYM: 100_000 }

describe('minimap-math — makeTransform', () => {
  it('aspect-fit en canvas cuadrado: el mundo cuadrado lo llena', () => {
    const t = makeTransform(GRID3, 220, 220)
    expect(t).not.toBeNull()
    if (!t) return
    // 150 000 m → 220 px.
    expect(t.scale).toBeCloseTo(220 / 150_000, 12)
    expect(worldToMini(t, -50_000, -50_000)).toEqual({ x: 0, y: 0 })
    expect(worldToMini(t, 100_000, 100_000)).toEqual({ x: 220, y: 220 })
  })

  it('canvas no cuadrado: centra el eje sobrante', () => {
    const t = makeTransform(GRID3, 220, 160)
    expect(t).not.toBeNull()
    if (!t) return
    expect(t.scale).toBeCloseTo(160 / 150_000, 12)
    // El mundo ocupa 160 px de ancho, centrado: margen (220−160)/2 = 30.
    expect(worldToMini(t, -50_000, -50_000).x).toBeCloseTo(30, 9)
    expect(worldToMini(t, 100_000, -50_000).x).toBeCloseTo(190, 9)
    expect(worldToMini(t, -50_000, -50_000).y).toBeCloseTo(0, 9)
  })

  it('bounds o canvas degenerados devuelven null', () => {
    expect(makeTransform({ minXM: 0, minYM: 0, maxXM: 0, maxYM: 100 }, 220, 220)).toBeNull()
    expect(makeTransform(GRID3, 0, 220)).toBeNull()
  })
})

describe('minimap-math — ida y vuelta', () => {
  it('miniToWorld ∘ worldToMini ≈ identidad (con negativos)', () => {
    const t = makeTransform(GRID3, 220, 160)
    if (!t) throw new Error('transform null')
    for (const [xM, yM] of [
      [-50_000, -50_000],
      [0, 0],
      [25_000, -10_000],
      [99_999, 99_999],
    ] as const) {
      const p = worldToMini(t, xM, yM)
      const back = miniToWorld(t, p.x, p.y)
      expect(back.xM).toBeCloseTo(xM, 6)
      expect(back.yM).toBeCloseTo(yM, 6)
    }
  })
})

describe('minimap-math — rectToMini', () => {
  it('proyecta el rect de viewport (Askadia entera dentro del mundo 3×3)', () => {
    const t = makeTransform(GRID3, 300, 300)
    if (!t) throw new Error('transform null')
    const rect = rectToMini(t, { xM: 0, yM: 0, widthM: 50_000, heightM: 50_000 })
    // Askadia es el tercio central: [100, 200) px.
    expect(rect.x).toBeCloseTo(100, 9)
    expect(rect.y).toBeCloseTo(100, 9)
    expect(rect.w).toBeCloseTo(100, 9)
    expect(rect.h).toBeCloseTo(100, 9)
  })
})
