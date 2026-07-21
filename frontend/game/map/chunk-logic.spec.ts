import { describe, expect, it } from 'vitest'

import { ChunkLru, diffChunks } from './chunk-logic'

describe('game/map/chunk-logic — diffChunks', () => {
  it('desde vacío: todo lo próximo se muestra, nada se oculta', () => {
    const diff = diffChunks(new Set(), [
      { cx: 0, cy: 0 },
      { cx: 1, cy: 0 },
    ])
    expect(diff.shown).toEqual([
      { cx: 0, cy: 0 },
      { cx: 1, cy: 0 },
    ])
    expect(diff.hidden).toEqual([])
  })

  it('sin cambios: diff vacío', () => {
    const diff = diffChunks(new Set(['0:0', '1:0']), [
      { cx: 0, cy: 0 },
      { cx: 1, cy: 0 },
    ])
    expect(diff.shown).toEqual([])
    expect(diff.hidden).toEqual([])
  })

  it('pan de un chunk: entra la columna nueva, sale la vieja', () => {
    const diff = diffChunks(new Set(['0:0', '1:0']), [
      { cx: 1, cy: 0 },
      { cx: 2, cy: 0 },
    ])
    expect(diff.shown).toEqual([{ cx: 2, cy: 0 }])
    expect(diff.hidden).toEqual(['0:0'])
  })

  it('próximo vacío (viewport fuera de mundo): todo se oculta', () => {
    const diff = diffChunks(new Set(['3:3', '4:3']), [])
    expect(diff.shown).toEqual([])
    expect(diff.hidden).toEqual(['3:3', '4:3'])
  })

  it('shown conserva el orden del próximo (determinista para materialización)', () => {
    const diff = diffChunks(new Set(), [
      { cx: 5, cy: 1 },
      { cx: 0, cy: 2 },
      { cx: 3, cy: 0 },
    ])
    expect(diff.shown.map((c) => `${String(c.cx)}:${String(c.cy)}`)).toEqual(['5:1', '0:2', '3:0'])
  })
})

describe('game/map/chunk-logic — ChunkLru', () => {
  it('rechaza capacidades inválidas', () => {
    expect(() => new ChunkLru(0)).toThrow(RangeError)
    expect(() => new ChunkLru(-1)).toThrow(RangeError)
    expect(() => new ChunkLru(1.5)).toThrow(RangeError)
  })

  it('bajo capacidad no planifica desalojos', () => {
    const lru = new ChunkLru(3)
    lru.touch('a')
    lru.touch('b')
    expect(lru.planEviction(new Set())).toEqual([])
    expect(lru.size).toBe(2)
  })

  it('al exceder capacidad desaloja el menos recientemente usado', () => {
    const lru = new ChunkLru(2)
    lru.touch('a')
    lru.touch('b')
    lru.touch('c')
    expect(lru.planEviction(new Set())).toEqual(['a'])
  })

  it('touch reordena la recencia (re-tocar salva del desalojo)', () => {
    const lru = new ChunkLru(2)
    lru.touch('a')
    lru.touch('b')
    lru.touch('a') // 'a' vuelve a ser el más reciente
    lru.touch('c')
    expect(lru.planEviction(new Set())).toEqual(['b'])
  })

  it('los chunks visibles están protegidos aunque sean los más antiguos', () => {
    const lru = new ChunkLru(2)
    lru.touch('a')
    lru.touch('b')
    lru.touch('c')
    expect(lru.planEviction(new Set(['a']))).toEqual(['b'])
  })

  it('si todos los excedentes son visibles, no desaloja nada (capacidad blanda)', () => {
    const lru = new ChunkLru(1)
    lru.touch('a')
    lru.touch('b')
    expect(lru.planEviction(new Set(['a', 'b']))).toEqual([])
  })

  it('planifica varios desalojos, más antiguos primero', () => {
    const lru = new ChunkLru(2)
    for (const k of ['a', 'b', 'c', 'd', 'e']) {
      lru.touch(k)
    }
    expect(lru.planEviction(new Set())).toEqual(['a', 'b', 'c'])
  })

  it('delete elimina del rastreo (chunk destruido)', () => {
    const lru = new ChunkLru(2)
    lru.touch('a')
    lru.touch('b')
    lru.touch('c')
    for (const k of lru.planEviction(new Set())) {
      lru.delete(k)
    }
    expect(lru.size).toBe(2)
    expect(lru.has('a')).toBe(false)
    expect(lru.has('b')).toBe(true)
  })
})
