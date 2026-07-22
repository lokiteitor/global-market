import { describe, expect, it } from 'vitest'

import { asEntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import { simTime } from '~shared/simtime'
import type { WorldPolygonM } from './geo'
import type { Region } from './world'
import { polygonBoundsM, regionsBoundsM } from './world'

function rectPolygon(minX: number, minY: number, maxX: number, maxY: number): WorldPolygonM {
  return [
    [
      [minX, minY],
      [maxX, minY],
      [maxX, maxY],
      [minX, maxY],
      [minX, minY],
    ],
  ]
}

function region(n: number, gridX: number, gridY: number, boundsM: WorldPolygonM | null): Region {
  return {
    id: asEntityId<'Region'>(`00000000-0000-7000-8000-${String(n).padStart(12, '0')}`),
    name: `Región ${String(n)}`,
    gridX,
    gridY,
    boundsM,
    biome: 'plains',
    taxRateBp: 500,
    customsRateBp: 200,
    canonBase: '1000' as Money,
    openedAtSim: simTime(0),
  }
}

describe('domain/world — polygonBoundsM', () => {
  it('bounding box de un rectángulo, incluidos vértices negativos', () => {
    expect(polygonBoundsM(rectPolygon(-50_000, -50_000, 0, 0))).toEqual({
      minXM: -50_000,
      minYM: -50_000,
      maxXM: 0,
      maxYM: 0,
    })
  })

  it('polígono sin vértices devuelve null', () => {
    expect(polygonBoundsM([])).toBeNull()
    expect(polygonBoundsM([[]])).toBeNull()
  })
})

describe('domain/world — regionsBoundsM', () => {
  it('unión de las 9 regiones del grid 3×3 centrado en Askadia', () => {
    const regions: Region[] = []
    let n = 1
    for (let gy = -1; gy <= 1; gy += 1) {
      for (let gx = -1; gx <= 1; gx += 1) {
        regions.push(
          region(n, gx, gy, rectPolygon(gx * 50_000, gy * 50_000, (gx + 1) * 50_000, (gy + 1) * 50_000)),
        )
        n += 1
      }
    }
    expect(regionsBoundsM(regions)).toEqual({
      minXM: -50_000,
      minYM: -50_000,
      maxXM: 100_000,
      maxYM: 100_000,
    })
  })

  it('ignora regiones sin bounds; null si ninguna los tiene', () => {
    const withBounds = region(1, 0, 0, rectPolygon(0, 0, 50_000, 50_000))
    const without = region(2, 1, 0, null)
    expect(regionsBoundsM([withBounds, without])).toEqual({
      minXM: 0,
      minYM: 0,
      maxXM: 50_000,
      maxYM: 50_000,
    })
    expect(regionsBoundsM([without])).toBeNull()
    expect(regionsBoundsM([])).toBeNull()
  })
})
