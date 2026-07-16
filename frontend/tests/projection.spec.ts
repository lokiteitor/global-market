import { describe, expect, it } from 'vitest'
import { createProjection, DEFAULT_PROJECTION } from '~/lib/kernel/projection'

describe('kernel/projection (isométrica 2:1, FE-6)', () => {
  it('la config por defecto es 32 tiles/grado con rombo 128×64 y origen (0,0)', () => {
    expect(DEFAULT_PROJECTION.tilesPerDegree).toBe(32)
    expect(DEFAULT_PROJECTION.tileWidth).toBe(128)
    expect(DEFAULT_PROJECTION.tileHeight).toBe(64)
    expect(DEFAULT_PROJECTION.originLon).toBe(0)
    expect(DEFAULT_PROJECTION.originLat).toBe(0)
  })

  it('worldToTile: u crece al este, v crece al sur', () => {
    const p = createProjection()
    expect(p.worldToTile(1, 0)).toEqual({ u: 32, v: 0 })
    expect(p.worldToTile(0, -1)).toEqual({ u: 0, v: 32 })
    expect(p.worldToTile(0, 1)).toEqual({ u: 0, v: -32 })
  })

  it('tileToScreen: iso 2:1 — un tile al este avanza (+64,+32), uno al sur (-64,+32)', () => {
    const p = createProjection()
    expect(p.tileToScreen(0, 0)).toEqual({ x: 0, y: 0 })
    expect(p.tileToScreen(1, 0)).toEqual({ x: 64, y: 32 })
    expect(p.tileToScreen(0, 1)).toEqual({ x: -64, y: 32 })
    expect(p.tileToScreen(1, 1)).toEqual({ x: 0, y: 64 })
  })

  it('convención Simutrans: el norte queda arriba-derecha, el este abajo-derecha', () => {
    const p = createProjection()
    const norte = p.worldToScreen(0, 1)
    expect(norte.x).toBeGreaterThan(0)
    expect(norte.y).toBeLessThan(0)
    const este = p.worldToScreen(1, 0)
    expect(este.x).toBeGreaterThan(0)
    expect(este.y).toBeGreaterThan(0)
  })

  it('worldToScreen compone worldToTile y tileToScreen', () => {
    const p = createProjection()
    // 1° este = 32 tiles → x=32·64=2048, y=32·32=1024
    expect(p.worldToScreen(1, 0)).toEqual({ x: 2048, y: 1024 })
    // 1° norte = v=-32 → x=2048, y=-1024
    expect(p.worldToScreen(0, 1)).toEqual({ x: 2048, y: -1024 })
  })

  it('respeta origen y escala configurables', () => {
    const p = createProjection({ tilesPerDegree: 1, originLon: -3.5, originLat: 40 })
    expect(p.worldToScreen(-3.5, 40)).toEqual({ x: 0, y: 0 })
    // 1° este y 1° norte desde el origen: u=1, v=-1 → x=128, y=0
    expect(p.worldToScreen(-2.5, 41)).toEqual({ x: 128, y: 0 })
  })

  it('ida y vuelta: screenToWorld(worldToScreen(p)) ≈ p', () => {
    const p = createProjection({ originLon: -10, originLat: 35 })
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
