import { describe, expect, it } from 'vitest'
import { rasterizeLinks, supercoverLine } from '~/game/road-raster'
import { createProjection } from '~/lib/kernel/projection'

describe('game/road-raster', () => {
  describe('supercoverLine', () => {
    it('línea horizontal: una celda por columna', () => {
      const cells = supercoverLine({ u: 0.5, v: 0.5 }, { u: 3.5, v: 0.5 })
      expect(cells).toEqual([
        { u: 0, v: 0 },
        { u: 1, v: 0 },
        { u: 2, v: 0 },
        { u: 3, v: 0 }
      ])
    })

    it('línea vertical descendente en v', () => {
      const cells = supercoverLine({ u: 0.5, v: 2.5 }, { u: 0.5, v: 0.5 })
      expect(cells).toEqual([
        { u: 0, v: 2 },
        { u: 0, v: 1 },
        { u: 0, v: 0 }
      ])
    })

    it('diagonal exacta: emite la celda intermedia (4-conexo, sin saltos diagonales)', () => {
      const cells = supercoverLine({ u: 0, v: 0 }, { u: 2, v: 2 })
      // Cruza las esquinas (1,1) y (2,2) exactamente.
      expect(cells).toEqual([
        { u: 0, v: 0 },
        { u: 1, v: 0 },
        { u: 1, v: 1 },
        { u: 2, v: 1 },
        { u: 2, v: 2 }
      ])
    })

    it('toda la secuencia es 4-conexa para pendientes arbitrarias', () => {
      const cells = supercoverLine({ u: 0.2, v: 0.7 }, { u: 7.9, v: 3.1 })
      for (let i = 1; i < cells.length; i++) {
        const manhattan =
          Math.abs((cells[i]?.u ?? 0) - (cells[i - 1]?.u ?? 0)) + Math.abs((cells[i]?.v ?? 0) - (cells[i - 1]?.v ?? 0))
        expect(manhattan).toBe(1)
      }
    })

    it('punto degenerado: una sola celda', () => {
      expect(supercoverLine({ u: 1.5, v: 1.5 }, { u: 1.5, v: 1.5 })).toEqual([{ u: 1, v: 1 }])
    })
  })

  describe('rasterizeLinks', () => {
    // Proyección de 1 tile/grado para razonar en celdas = grados.
    const proj = createProjection({ tilesPerDegree: 1 })
    const toTile = proj.worldToTile.bind(proj)

    it('link recto E-W: extremos con frame de terminación y celdas interiores road.ew', () => {
      // lat -0.5 → v=0.5 constante; lon 0.5..3.5 → u 0.5..3.5.
      const out = rasterizeLinks([{ id: 'l1', coords: [[0.5, -0.5], [3.5, -0.5]] }], toTile)
      const cells = out.get('l1')
      expect(cells).toBeDefined()
      expect(cells).toEqual([
        { u: 0, v: 0, frame: 'road.e' },
        { u: 1, v: 0, frame: 'road.ew' },
        { u: 2, v: 0, frame: 'road.ew' },
        { u: 3, v: 0, frame: 'road.w' }
      ])
    })

    it('dos links que se cruzan comparten celda con frame road.nsew', () => {
      const out = rasterizeLinks(
        [
          { id: 'horizontal', coords: [[0.5, -2.5], [4.5, -2.5]] }, // v=2.5, u 0.5..4.5
          { id: 'vertical', coords: [[2.5, -0.5], [2.5, -4.5]] } // u=2.5, v 0.5..4.5
        ],
        toTile
      )
      const h = out.get('horizontal')
      const v = out.get('vertical')
      const crossH = h?.find((c) => c.u === 2 && c.v === 2)
      const crossV = v?.find((c) => c.u === 2 && c.v === 2)
      expect(crossH?.frame).toBe('road.nsew')
      expect(crossV?.frame).toBe('road.nsew')
      // Las celdas no compartidas siguen siendo rectas.
      expect(h?.find((c) => c.u === 1 && c.v === 2)?.frame).toBe('road.ew')
      expect(v?.find((c) => c.u === 2 && c.v === 1)?.frame).toBe('road.ns')
    })

    it('link en L produce el frame de curva en el vértice', () => {
      // Este 2 celdas y luego sur 2 celdas: el vértice conecta W (de dónde viene) y S (a dónde va).
      const out = rasterizeLinks([{ id: 'l', coords: [[0.5, -0.5], [2.5, -0.5], [2.5, -2.5]] }], toTile)
      const cells = out.get('l')
      const corner = cells?.find((c) => c.u === 2 && c.v === 0)
      expect(corner?.frame).toBe('road.sw')
    })

    it('un link que termina sobre otro produce una T', () => {
      const out = rasterizeLinks(
        [
          { id: 'principal', coords: [[0.5, -1.5], [4.5, -1.5]] }, // v=1, u 0..4
          { id: 'ramal', coords: [[2.5, -3.5], [2.5, -1.5]] } // sube por u=2 hasta v=1
        ],
        toTile
      )
      const junction = out.get('principal')?.find((c) => c.u === 2 && c.v === 1)
      // Conexiones: E y W del principal + S del ramal → road.sew.
      expect(junction?.frame).toBe('road.sew')
    })

    it('LineString multi-tramo no duplica la celda del vértice', () => {
      const out = rasterizeLinks([{ id: 'l', coords: [[0.5, -0.5], [1.5, -0.5], [2.5, -0.5]] }], toTile)
      const cells = out.get('l') ?? []
      const keys = cells.map((c) => `${c.u},${c.v}`)
      expect(new Set(keys).size).toBe(keys.length)
    })
  })
})
