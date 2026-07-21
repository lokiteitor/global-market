import { describe, expect, it } from 'vitest'

import {
  CHUNK_PX,
  CHUNK_TILES,
  PX_PER_M,
  TILE_PX,
  WORLD_CHUNKS,
  WORLD_M_PER_TILE,
  WORLD_SIZE_M,
  WORLD_SIZE_PX,
  WORLD_SIZE_TILES,
  chunkBounds,
  chunkBoundsClamped,
  chunkKey,
  isInsideWorldM,
  mToPx,
  mToTile,
  parseChunkKey,
  pxToM,
  tileToChunk,
  visibleChunks,
} from './grid'

describe('shared/geometry — constantes (ADR-019)', () => {
  it('fija la escala del mandato: 250 m/tile, 32 px/tile, 0.128 px/m', () => {
    expect(WORLD_M_PER_TILE).toBe(250)
    expect(TILE_PX).toBe(32)
    expect(PX_PER_M).toBeCloseTo(0.128, 12)
  })

  it('deriva el mundo Askadia: 50 km = 200 tiles = 6400 px', () => {
    expect(WORLD_SIZE_M).toBe(50_000)
    expect(WORLD_SIZE_TILES).toBe(200)
    expect(WORLD_SIZE_PX).toBe(6_400)
  })

  it('deriva chunks de 32 tiles = 1024 px y una rejilla de 7×7 (última fila parcial)', () => {
    expect(CHUNK_TILES).toBe(32)
    expect(CHUNK_PX).toBe(1_024)
    expect(WORLD_CHUNKS).toBe(7)
  })
})

describe('shared/geometry — mToPx / pxToM', () => {
  it('convierte el origen y el extremo del mundo', () => {
    expect(mToPx(0, 0)).toEqual({ xPx: 0, yPx: 0 })
    expect(mToPx(WORLD_SIZE_M, WORLD_SIZE_M)).toEqual({ xPx: 6_400, yPx: 6_400 })
  })

  it('convierte un punto interior con fracciones (sin redondear)', () => {
    const p = mToPx(125, 375)
    expect(p.xPx).toBeCloseTo(16, 12)
    expect(p.yPx).toBeCloseTo(48, 12)
    const q = mToPx(100, 1)
    expect(q.xPx).toBeCloseTo(12.8, 12)
    expect(q.yPx).toBeCloseTo(0.128, 12)
  })

  it('acepta negativos (fuera de mundo): la proyección es lineal, no recorta', () => {
    expect(mToPx(-250, -500)).toEqual({ xPx: -32, yPx: -64 })
  })

  it('pxToM es la inversa exacta de mToPx (ida y vuelta)', () => {
    for (const [xM, yM] of [
      [0, 0],
      [250, 250],
      [123.456, 49_999.9],
      [-1_000, 70_000],
    ] as const) {
      const px = mToPx(xM, yM)
      const m = pxToM(px.xPx, px.yPx)
      expect(m.xM).toBeCloseTo(xM, 9)
      expect(m.yM).toBeCloseTo(yM, 9)
    }
  })
})

describe('shared/geometry — mToTile', () => {
  it('el origen cae en el tile (0,0) y el interior de un tile no cambia de celda', () => {
    expect(mToTile(0, 0)).toEqual({ tx: 0, ty: 0 })
    expect(mToTile(249.999, 249.999)).toEqual({ tx: 0, ty: 0 })
  })

  it('el borde exacto de tile pertenece al tile siguiente (floor, intervalo semiabierto)', () => {
    expect(mToTile(250, 0)).toEqual({ tx: 1, ty: 0 })
    expect(mToTile(500, 250)).toEqual({ tx: 2, ty: 1 })
  })

  it('el extremo del mundo (50 000 m) cae YA FUERA de la rejilla válida [0,200)', () => {
    expect(mToTile(WORLD_SIZE_M, WORLD_SIZE_M)).toEqual({ tx: 200, ty: 200 })
    expect(mToTile(WORLD_SIZE_M - 0.001, WORLD_SIZE_M - 0.001)).toEqual({ tx: 199, ty: 199 })
  })

  it('negativos fuera de mundo: floor hacia -∞ (no trunca hacia 0)', () => {
    expect(mToTile(-0.001, -1)).toEqual({ tx: -1, ty: -1 })
    expect(mToTile(-250, -251)).toEqual({ tx: -1, ty: -2 })
  })
})

describe('shared/geometry — tileToChunk', () => {
  it('mapea tiles interiores y el borde exacto de chunk (semiabierto)', () => {
    expect(tileToChunk(0, 0)).toEqual({ cx: 0, cy: 0 })
    expect(tileToChunk(31, 31)).toEqual({ cx: 0, cy: 0 })
    expect(tileToChunk(32, 31)).toEqual({ cx: 1, cy: 0 })
    expect(tileToChunk(199, 199)).toEqual({ cx: 6, cy: 6 })
  })

  it('tiles negativos van a chunks negativos (floor hacia -∞)', () => {
    expect(tileToChunk(-1, -32)).toEqual({ cx: -1, cy: -1 })
    expect(tileToChunk(-33, 0)).toEqual({ cx: -2, cy: 0 })
  })
})

describe('shared/geometry — isInsideWorldM', () => {
  it('bordes superior/izquierdo inclusivos, inferior/derecho exclusivos', () => {
    expect(isInsideWorldM(0, 0)).toBe(true)
    expect(isInsideWorldM(49_999.999, 49_999.999)).toBe(true)
    expect(isInsideWorldM(50_000, 0)).toBe(false)
    expect(isInsideWorldM(0, 50_000)).toBe(false)
    expect(isInsideWorldM(-0.001, 0)).toBe(false)
  })
})

describe('shared/geometry — chunkKey / parseChunkKey', () => {
  it('clave estable e inversa, incluidos negativos', () => {
    expect(chunkKey(3, 5)).toBe('3:5')
    expect(parseChunkKey('3:5')).toEqual({ cx: 3, cy: 5 })
    expect(parseChunkKey('-1:-7')).toEqual({ cx: -1, cy: -7 })
  })

  it('rechaza claves malformadas', () => {
    for (const bad of ['', '3', '3:5:7', 'a:b', '3.5:1', ' 3:5']) {
      expect(() => parseChunkKey(bad)).toThrow(RangeError)
    }
  })
})

describe('shared/geometry — chunkBounds / chunkBoundsClamped', () => {
  it('bounds nominales: rejilla completa de 1024 px', () => {
    expect(chunkBounds(0, 0)).toEqual({ x: 0, y: 0, width: 1_024, height: 1_024 })
    expect(chunkBounds(2, 1)).toEqual({ x: 2_048, y: 1_024, width: 1_024, height: 1_024 })
  })

  it('el chunk 6 (borde) se recorta al mundo: 6400 − 6144 = 256 px', () => {
    expect(chunkBoundsClamped(6, 0)).toEqual({ x: 6_144, y: 0, width: 256, height: 1_024 })
    expect(chunkBoundsClamped(6, 6)).toEqual({ x: 6_144, y: 6_144, width: 256, height: 256 })
  })

  it('chunks interiores no se recortan', () => {
    expect(chunkBoundsClamped(3, 3)).toEqual(chunkBounds(3, 3))
  })

  it('chunks fuera del mundo devuelven null', () => {
    expect(chunkBoundsClamped(-1, 0)).toBeNull()
    expect(chunkBoundsClamped(7, 0)).toBeNull()
    expect(chunkBoundsClamped(0, 99)).toBeNull()
  })
})

describe('shared/geometry — visibleChunks', () => {
  it('viewport dentro de un solo chunk', () => {
    expect(visibleChunks({ x: 10, y: 10, width: 100, height: 100 })).toEqual([{ cx: 0, cy: 0 }])
  })

  it('viewport que cruza una frontera de chunk incluye ambos lados', () => {
    expect(visibleChunks({ x: 1_000, y: 10, width: 48, height: 100 })).toEqual([
      { cx: 0, cy: 0 },
      { cx: 1, cy: 0 },
    ])
  })

  it('borde derecho EXACTAMENTE sobre la frontera no incluye el chunk tangente', () => {
    // [0, 1024) toca solo el chunk 0: el píxel 1024 ya es del chunk 1.
    expect(visibleChunks({ x: 0, y: 0, width: 1_024, height: 1_024 })).toEqual([{ cx: 0, cy: 0 }])
  })

  it('borde izquierdo exactamente sobre la frontera empieza en ese chunk', () => {
    expect(visibleChunks({ x: 1_024, y: 0, width: 10, height: 10 })).toEqual([{ cx: 1, cy: 0 }])
  })

  it('rect de área cero no interseca nada', () => {
    expect(visibleChunks({ x: 500, y: 500, width: 0, height: 100 })).toEqual([])
    expect(visibleChunks({ x: 500, y: 500, width: 100, height: 0 })).toEqual([])
  })

  it('margen 1 añade el anillo completo alrededor', () => {
    const ring = visibleChunks({ x: 2_100, y: 2_100, width: 100, height: 100 }, 1)
    expect(ring).toEqual([
      { cx: 1, cy: 1 },
      { cx: 2, cy: 1 },
      { cx: 3, cy: 1 },
      { cx: 1, cy: 2 },
      { cx: 2, cy: 2 },
      { cx: 3, cy: 2 },
      { cx: 1, cy: 3 },
      { cx: 2, cy: 3 },
      { cx: 3, cy: 3 },
    ])
  })

  it('el margen se recorta al mundo (esquina superior izquierda)', () => {
    expect(visibleChunks({ x: 10, y: 10, width: 10, height: 10 }, 1)).toEqual([
      { cx: 0, cy: 0 },
      { cx: 1, cy: 0 },
      { cx: 0, cy: 1 },
      { cx: 1, cy: 1 },
    ])
  })

  it('el margen se recorta al mundo (esquina inferior derecha, chunk 6 parcial)', () => {
    expect(visibleChunks({ x: 6_300, y: 6_300, width: 50, height: 50 }, 1)).toEqual([
      { cx: 5, cy: 5 },
      { cx: 6, cy: 5 },
      { cx: 5, cy: 6 },
      { cx: 6, cy: 6 },
    ])
  })

  it('viewport totalmente fuera del mundo (negativo o más allá) devuelve vacío', () => {
    expect(visibleChunks({ x: -5_000, y: 0, width: 1_000, height: 1_000 })).toEqual([])
    expect(visibleChunks({ x: 0, y: 7_500, width: 1_000, height: 1_000 })).toEqual([])
    expect(visibleChunks({ x: 9_000, y: 9_000, width: 100, height: 100 }, 1)).toEqual([])
  })

  it('viewport parcialmente fuera se recorta a los chunks válidos', () => {
    expect(visibleChunks({ x: -600, y: -600, width: 1_000, height: 1_000 })).toEqual([
      { cx: 0, cy: 0 },
    ])
  })

  it('un viewport que cubre el mundo entero devuelve los 49 chunks en orden fila-columna', () => {
    const all = visibleChunks({ x: 0, y: 0, width: 6_400, height: 6_400 })
    expect(all).toHaveLength(49)
    expect(all[0]).toEqual({ cx: 0, cy: 0 })
    expect(all[6]).toEqual({ cx: 6, cy: 0 })
    expect(all[48]).toEqual({ cx: 6, cy: 6 })
  })

  it('un margen grande satura en el mundo completo, nunca fuera', () => {
    const all = visibleChunks({ x: 3_000, y: 3_000, width: 10, height: 10 }, 99)
    expect(all).toHaveLength(WORLD_CHUNKS * WORLD_CHUNKS)
  })
})
