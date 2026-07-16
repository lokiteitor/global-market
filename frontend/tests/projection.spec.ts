import { describe, expect, it } from 'vitest'
import { createProjection, DEFAULT_PROJECTION } from '~/lib/kernel/projection'

describe('kernel/projection (top-down v1)', () => {
  it('la escala por defecto es 900 px/grado con origen (0,0)', () => {
    expect(DEFAULT_PROJECTION.pxPerDegree).toBe(900)
    const p = createProjection()
    expect(p.worldToScreen(1, 0)).toEqual({ x: 900, y: 0 })
  })

  it('proyecta con norte arriba: lat crece hacia -y de pantalla', () => {
    const p = createProjection()
    expect(p.worldToScreen(0, 1)).toEqual({ x: 0, y: -900 })
    expect(p.worldToScreen(0, -1)).toEqual({ x: 0, y: 900 })
  })

  it('respeta origen y escala configurables', () => {
    const p = createProjection({ pxPerDegree: 100, originLon: -3.5, originLat: 40 })
    expect(p.worldToScreen(-3.5, 40)).toEqual({ x: 0, y: 0 })
    expect(p.worldToScreen(-2.5, 41)).toEqual({ x: 100, y: -100 })
  })

  it('ida y vuelta: screenToWorld(worldToScreen(p)) ≈ p', () => {
    const p = createProjection({ pxPerDegree: 900, originLon: -10, originLat: 35 })
    const cases: [number, number][] = [
      [-3.7038, 40.4168],
      [0, 0],
      [179.999, -89.5],
      [-10, 35]
    ]
    for (const [lon, lat] of cases) {
      const screen = p.worldToScreen(lon, lat)
      const world = p.screenToWorld(screen.x, screen.y)
      expect(world.lon).toBeCloseTo(lon, 9)
      expect(world.lat).toBeCloseTo(lat, 9)
    }
  })

  it('ida y vuelta inversa: worldToScreen(screenToWorld(p)) ≈ p', () => {
    const p = createProjection()
    const world = p.screenToWorld(1234.5, -678.9)
    const screen = p.worldToScreen(world.lon, world.lat)
    expect(screen.x).toBeCloseTo(1234.5, 9)
    expect(screen.y).toBeCloseTo(-678.9, 9)
  })
})
