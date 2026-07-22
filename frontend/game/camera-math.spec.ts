import { describe, expect, it } from 'vitest'

import type { WorldBoundsPx } from '~shared/geometry/grid'
import { DEFAULT_WORLD_BOUNDS_M, boundsMToPx } from '~shared/geometry/grid'
import {
  ZOOM_FLOOR_ABS,
  ZOOM_MAX,
  ZOOM_MIN,
  clampCenter,
  clampZoom,
  decayVelocity,
  fitZoom,
  panBy,
  viewRect,
  wheelZoomFactor,
  zoomAtCursor,
  zoomRange,
} from './camera-math'

const VIEWPORT = { width: 800, height: 600 }

/** Fallback Askadia: [0, 6400)² px. */
const ASKADIA_PX = boundsMToPx(DEFAULT_WORLD_BOUNDS_M)

/** Mundo 3×3 centrado en Askadia: [-6400, 12800)² px. */
const GRID3_PX: WorldBoundsPx = { minXPx: -6_400, minYPx: -6_400, maxXPx: 12_800, maxYPx: 12_800 }

describe('game/camera-math — clampZoom', () => {
  it('respeta los límites del mandato 0.15..3', () => {
    expect(ZOOM_MIN).toBe(0.15)
    expect(ZOOM_MAX).toBe(3)
    expect(clampZoom(0.01)).toBe(0.15)
    expect(clampZoom(99)).toBe(3)
    expect(clampZoom(1)).toBe(1)
    expect(clampZoom(0.15)).toBe(0.15)
    expect(clampZoom(3)).toBe(3)
  })
})

describe('game/camera-math — wheelZoomFactor', () => {
  it('rueda hacia abajo (deltaY > 0) aleja; hacia arriba acerca', () => {
    expect(wheelZoomFactor(100)).toBeLessThan(1)
    expect(wheelZoomFactor(-100)).toBeGreaterThan(1)
    expect(wheelZoomFactor(0)).toBe(1)
  })

  it('es simétrico: subir y bajar lo mismo se cancela', () => {
    expect(wheelZoomFactor(120) * wheelZoomFactor(-120)).toBeCloseTo(1, 12)
  })
})

describe('game/camera-math — zoomAtCursor', () => {
  const worldUnder = (
    state: { centerX: number; centerY: number; zoom: number },
    cx: number,
    cy: number,
  ): { x: number; y: number } => ({
    x: state.centerX + (cx - VIEWPORT.width / 2) / state.zoom,
    y: state.centerY + (cy - VIEWPORT.height / 2) / state.zoom,
  })

  it('el punto de mundo bajo el cursor permanece fijo tras el zoom', () => {
    const state = { centerX: 3_200, centerY: 3_200, zoom: 1 }
    const cursor = { x: 700, y: 100 }
    const before = worldUnder(state, cursor.x, cursor.y)
    const next = zoomAtCursor(state, cursor.x, cursor.y, VIEWPORT, 2)
    const after = worldUnder(next, cursor.x, cursor.y)
    expect(next.zoom).toBe(2)
    expect(after.x).toBeCloseTo(before.x, 9)
    expect(after.y).toBeCloseTo(before.y, 9)
  })

  it('zoom en el centro del viewport no mueve el centro', () => {
    const state = { centerX: 1_000, centerY: 2_000, zoom: 0.5 }
    const next = zoomAtCursor(state, VIEWPORT.width / 2, VIEWPORT.height / 2, VIEWPORT, 1.5)
    expect(next.centerX).toBeCloseTo(1_000, 9)
    expect(next.centerY).toBeCloseTo(2_000, 9)
  })

  it('clampea el zoom pedido a los límites (y el punto sigue fijo con el zoom efectivo)', () => {
    const state = { centerX: 3_200, centerY: 3_200, zoom: 2.5 }
    const cursor = { x: 100, y: 500 }
    const before = worldUnder(state, cursor.x, cursor.y)
    const next = zoomAtCursor(state, cursor.x, cursor.y, VIEWPORT, 10)
    expect(next.zoom).toBe(3)
    const after = worldUnder(next, cursor.x, cursor.y)
    expect(after.x).toBeCloseTo(before.x, 9)
    expect(after.y).toBeCloseTo(before.y, 9)
  })

  it('alejar también mantiene el punto bajo el cursor', () => {
    const state = { centerX: 3_200, centerY: 3_200, zoom: 2 }
    // Cursor en la esquina superior izquierda (0, 0).
    const before = worldUnder(state, 0, 0)
    const next = zoomAtCursor(state, 0, 0, VIEWPORT, 1)
    const after = worldUnder(next, 0, 0)
    expect(after.x).toBeCloseTo(before.x, 9)
    expect(after.y).toBeCloseTo(before.y, 9)
  })
})

describe('game/camera-math — panBy', () => {
  it('arrastrar a la derecha mueve el centro a la izquierda, escalado por zoom', () => {
    const state = { centerX: 1_000, centerY: 1_000, zoom: 2 }
    const next = panBy(state, 100, -50)
    expect(next.centerX).toBe(950)
    expect(next.centerY).toBe(1_025)
    expect(next.zoom).toBe(2)
  })

  it('a zoom 0.5 el mismo arrastre recorre el doble de mundo', () => {
    const next = panBy({ centerX: 0, centerY: 0, zoom: 0.5 }, 100, 0)
    expect(next.centerX).toBe(-200)
  })
})

describe('game/camera-math — clampCenter', () => {
  it('un centro interior no se toca', () => {
    const state = { centerX: 3_200, centerY: 3_200, zoom: 1 }
    expect(clampCenter(state, VIEWPORT, ASKADIA_PX)).toEqual(state)
  })

  it('clampea contra el borde izquierdo/superior', () => {
    const state = { centerX: 10, centerY: 10, zoom: 1 }
    const next = clampCenter(state, VIEWPORT, ASKADIA_PX)
    expect(next.centerX).toBe(400) // width/2 / zoom
    expect(next.centerY).toBe(300)
  })

  it('clampea contra el borde derecho/inferior (mundo 6400 px)', () => {
    const state = { centerX: 6_390, centerY: 6_390, zoom: 1 }
    const next = clampCenter(state, VIEWPORT, ASKADIA_PX)
    expect(next.centerX).toBe(6_000)
    expect(next.centerY).toBe(6_100)
  })

  it('el zoom amplía el margen de clampeo (viewport de mundo más grande)', () => {
    const state = { centerX: 0, centerY: 0, zoom: 0.5 }
    const next = clampCenter(state, VIEWPORT, ASKADIA_PX)
    expect(next.centerX).toBe(800)
    expect(next.centerY).toBe(600)
  })

  it('si el viewport es más grande que el mundo, centra el mundo', () => {
    // A zoom mínimo 0.15, 800/0.15 ≈ 5333 < 6400 (no dispara); forzamos con zoom menor.
    const state = { centerX: 0, centerY: 9_999, zoom: 0.1 }
    const next = clampCenter(state, VIEWPORT, ASKADIA_PX)
    expect(next.centerX).toBe(3_200)
    // height: 600/0.1 = 6000 < 6400 ⇒ ese eje sí clampea normal
    expect(next.centerY).toBe(6_400 - 3_000)
  })

  it('mundo 3×3: la cámara panea a las regiones negativas sin recortarse a Askadia', () => {
    const state = { centerX: -6_000, centerY: -6_000, zoom: 1 }
    const next = clampCenter(state, VIEWPORT, GRID3_PX)
    expect(next.centerX).toBe(-6_000)
    expect(next.centerY).toBe(-6_000)
  })

  it('mundo 3×3: clampea contra el borde oeste/norte negativo', () => {
    const state = { centerX: -9_999, centerY: -9_999, zoom: 1 }
    const next = clampCenter(state, VIEWPORT, GRID3_PX)
    expect(next.centerX).toBe(-6_400 + 400)
    expect(next.centerY).toBe(-6_400 + 300)
  })

  it('mundo 3×3: viewport mayor que el mundo centra en el centro del mundo', () => {
    const state = { centerX: 0, centerY: 0, zoom: 0.02 }
    const next = clampCenter(state, VIEWPORT, GRID3_PX)
    expect(next.centerX).toBe(3_200) // (−6400 + 12800) / 2
    expect(next.centerY).toBe(3_200)
  })
})

describe('game/camera-math — fitZoom / zoomRange', () => {
  it('fitZoom encaja el mundo entero en el viewport (aspect-fit)', () => {
    // Askadia 6400 px: min(800/6400, 600/6400) = 0.09375.
    expect(fitZoom(ASKADIA_PX, VIEWPORT)).toBeCloseTo(0.09375, 9)
    // Mundo 3×3 (19 200 px): un tercio.
    expect(fitZoom(GRID3_PX, VIEWPORT)).toBeCloseTo(0.03125, 9)
  })

  it('zoomRange: el mínimo permite ver el mundo entero, con techo en ZOOM_MIN', () => {
    const askadia = zoomRange(ASKADIA_PX, VIEWPORT)
    expect(askadia.min).toBeCloseTo(0.09375 * 0.95, 9)
    expect(askadia.max).toBe(ZOOM_MAX)

    const grid3 = zoomRange(GRID3_PX, VIEWPORT)
    expect(grid3.min).toBeCloseTo(0.03125 * 0.95, 9)
    expect(grid3.min).toBeLessThan(ZOOM_MIN)
  })

  it('zoomRange nunca baja del suelo absoluto (mundo enorme)', () => {
    const huge: WorldBoundsPx = { minXPx: 0, minYPx: 0, maxXPx: 10_000_000, maxYPx: 10_000_000 }
    expect(zoomRange(huge, VIEWPORT).min).toBe(ZOOM_FLOOR_ABS)
  })

  it('con un mundo pequeño el mínimo se queda en ZOOM_MIN (no acerca de más)', () => {
    const tiny: WorldBoundsPx = { minXPx: 0, minYPx: 0, maxXPx: 100, maxYPx: 100 }
    expect(zoomRange(tiny, VIEWPORT).min).toBe(ZOOM_MIN)
  })
})

describe('game/camera-math — viewRect', () => {
  it('a zoom 1 el rect es el viewport centrado', () => {
    expect(viewRect({ centerX: 1_000, centerY: 900, zoom: 1 }, VIEWPORT)).toEqual({
      x: 600,
      y: 600,
      width: 800,
      height: 600,
    })
  })

  it('a zoom 2 se ve la mitad de mundo; a 0.5, el doble', () => {
    expect(viewRect({ centerX: 0, centerY: 0, zoom: 2 }, VIEWPORT)).toEqual({
      x: -200,
      y: -150,
      width: 400,
      height: 300,
    })
    expect(viewRect({ centerX: 0, centerY: 0, zoom: 0.5 }, VIEWPORT).width).toBe(1_600)
  })
})

describe('game/camera-math — decayVelocity', () => {
  it('decae exponencialmente y conserva el signo', () => {
    const v1 = decayVelocity(1, 200) // un tau ⇒ ×e⁻¹
    expect(v1).toBeCloseTo(Math.exp(-1), 9)
    expect(decayVelocity(-1, 200)).toBeCloseTo(-Math.exp(-1), 9)
  })

  it('se satura a 0 por debajo del umbral (la inercia termina)', () => {
    expect(decayVelocity(0.02, 1_000)).toBe(0)
    expect(decayVelocity(0, 16)).toBe(0)
  })

  it('dt = 0 no cambia la velocidad (por encima del umbral)', () => {
    expect(decayVelocity(5, 0)).toBe(5)
  })
})
