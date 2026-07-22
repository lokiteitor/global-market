import { describe, expect, it } from 'vitest'

import { crossTicksPx, dashSegmentsPx } from './link-geometry'

describe('game/entities/link-geometry — dashSegmentsPx', () => {
  it('trocea un tramo recto en dashes exactos (10 on / 8 off)', () => {
    // Longitud 100 con período 18: dashes en [0,10), [18,28), [36,46), [54,64),
    // [72,82), [90,100) — el último completo justo al borde.
    const dashes = dashSegmentsPx([[0, 0], [100, 0]], 10, 8)
    expect(dashes).toHaveLength(6)
    expect(dashes[0]).toEqual([0, 0, 10, 0])
    expect(dashes[1]).toEqual([18, 0, 28, 0])
    expect(dashes[5]).toEqual([90, 0, 100, 0])
  })

  it('recorta el dash final si el tramo termina en mitad de un dash', () => {
    const dashes = dashSegmentsPx([[0, 0], [23, 0]], 10, 8)
    // [0,10) completo y [18,23) recortado.
    expect(dashes).toHaveLength(2)
    expect(dashes[1]).toEqual([18, 0, 23, 0])
  })

  it('el patrón continúa a través de los vértices (fase compartida)', () => {
    // Dos tramos colineales de 9 px: el primer dash de 10 px cruza el vértice
    // y se emite fusionado (colineal); el patrón no se reinicia en el vértice
    // (si se reiniciara habría un dash [9,19) truncado en 18).
    const dashes = dashSegmentsPx([[0, 0], [9, 0], [18, 0]], 10, 8)
    expect(dashes).toEqual([[0, 0, 10, 0]])
  })

  it('en un vértice en ángulo el dash se parte en dos mitades no colineales', () => {
    const dashes = dashSegmentsPx([[0, 0], [5, 0], [5, 20]], 10, 8)
    // Mitades del primer dash: [0,5) horizontal y [0,5) vertical; luego [13,20).
    expect(dashes[0]).toEqual([0, 0, 5, 0])
    expect(dashes[1]).toEqual([5, 0, 5, 5])
    expect(dashes[2]).toEqual([5, 13, 5, 20])
  })

  it('gap 0 degenera en la línea continua; dash <= 0 no emite nada', () => {
    expect(dashSegmentsPx([[0, 0], [30, 0]], 10, 0)).toEqual([[0, 0, 30, 0]])
    expect(dashSegmentsPx([[0, 0], [30, 0]], 0, 8)).toEqual([])
  })

  it('paths degenerados (vacío, un punto, puntos repetidos) no emiten nada', () => {
    expect(dashSegmentsPx([], 10, 8)).toEqual([])
    expect(dashSegmentsPx([[3, 3]], 10, 8)).toEqual([])
    expect(dashSegmentsPx([[3, 3], [3, 3]], 10, 8)).toEqual([])
  })
})

describe('game/entities/link-geometry — crossTicksPx', () => {
  it('travesaños perpendiculares equiespaciados en un tramo horizontal', () => {
    // Longitud 100, spacing 24: primer tick a 12, luego 36, 60, 84.
    const ticks = crossTicksPx([[0, 0], [100, 0]], 24, 6)
    expect(ticks).toHaveLength(4)
    // Tramo hacia +X ⇒ travesaño vertical (perpendicular).
    expect(ticks[0]).toEqual([12, -6, 12, 6])
    expect(ticks[3]).toEqual([84, -6, 84, 6])
  })

  it('en un tramo diagonal el travesaño es perpendicular a la dirección local', () => {
    const ticks = crossTicksPx([[0, 0], [100, 100]], 60, 6)
    const first = ticks[0]
    expect(first).toBeDefined()
    if (!first) {
      return
    }
    const [x1, y1, x2, y2] = first
    // Vector del travesaño ⊥ (1,1)/√2: producto escalar ≈ 0.
    const dot = (x2 - x1) * Math.SQRT1_2 + (y2 - y1) * Math.SQRT1_2
    expect(dot).toBeCloseTo(0, 9)
    // Longitud total 2 × halfLen.
    expect(Math.hypot(x2 - x1, y2 - y1)).toBeCloseTo(12, 9)
  })

  it('la separación se arrastra a través de los vértices', () => {
    // Dos tramos de 20: spacing 24 ⇒ ticks a 12 (tramo 1) y a 36 (tramo 2, en 16 local).
    const ticks = crossTicksPx([[0, 0], [20, 0], [40, 0]], 24, 6)
    expect(ticks).toHaveLength(2)
    expect(ticks[1]?.[0]).toBeCloseTo(36, 9)
  })

  it('spacing o halfLen no positivos no emiten nada', () => {
    expect(crossTicksPx([[0, 0], [100, 0]], 0, 6)).toEqual([])
    expect(crossTicksPx([[0, 0], [100, 0]], 24, 0)).toEqual([])
  })
})
