import { describe, expect, it } from 'vitest'

import { diffVms, vmEquals } from './diff'

interface FakeVM extends Record<string, unknown> {
  readonly id: string
  readonly xM: number
  readonly points?: readonly (readonly [number, number])[]
}

const toMap = (vms: readonly FakeVM[]): ReadonlyMap<string, FakeVM> =>
  new Map(vms.map((vm) => [vm.id, vm]))

describe('game/bridge/diff — vmEquals', () => {
  it('compara primitivas por valor', () => {
    expect(vmEquals({ id: 'a', xM: 1 }, { id: 'a', xM: 1 })).toBe(true)
    expect(vmEquals({ id: 'a', xM: 1 }, { id: 'a', xM: 2 })).toBe(false)
  })

  it('compara arrays de puntos por valor (y por referencia como atajo)', () => {
    const points: (readonly [number, number])[] = [
      [0, 0],
      [10, 5],
    ]
    expect(vmEquals({ id: 'a', xM: 0, points }, { id: 'a', xM: 0, points })).toBe(true)
    expect(
      vmEquals(
        { id: 'a', xM: 0, points },
        {
          id: 'a',
          xM: 0,
          points: [
            [0, 0],
            [10, 5],
          ],
        },
      ),
    ).toBe(true)
    expect(
      vmEquals(
        { id: 'a', xM: 0, points },
        {
          id: 'a',
          xM: 0,
          points: [
            [0, 0],
            [10, 6],
          ],
        },
      ),
    ).toBe(false)
    expect(vmEquals({ id: 'a', xM: 0, points }, { id: 'a', xM: 0, points: [[0, 0]] })).toBe(false)
  })
})

describe('game/bridge/diff — diffVms (dado prev+next ⇒ upserts/removes)', () => {
  it('todo nuevo ⇒ todo upsert, nada remove', () => {
    const next = toMap([
      { id: 'a', xM: 1 },
      { id: 'b', xM: 2 },
    ])
    const diff = diffVms(new Map(), next)
    expect(diff.upserts.map((v) => v.id)).toEqual(['a', 'b'])
    expect(diff.removes).toEqual([])
  })

  it('sin cambios ⇒ diff vacío (no toca sprites)', () => {
    const prev = toMap([{ id: 'a', xM: 1 }])
    const next = toMap([{ id: 'a', xM: 1 }])
    const diff = diffVms(prev, next)
    expect(diff.upserts).toEqual([])
    expect(diff.removes).toEqual([])
  })

  it('cambio de campo ⇒ upsert solo del cambiado; desaparición ⇒ remove', () => {
    const prev = toMap([
      { id: 'a', xM: 1 },
      { id: 'b', xM: 2 },
      { id: 'c', xM: 3 },
    ])
    const next = toMap([
      { id: 'a', xM: 1 },
      { id: 'b', xM: 99 },
    ])
    const diff = diffVms(prev, next)
    expect(diff.upserts.map((v) => v.id)).toEqual(['b'])
    expect(diff.removes).toEqual(['c'])
  })

  it('un cambio SOLO en un campo string nuevo (p. ej. mode) también es upsert', () => {
    const prev = toMap([{ id: 'l1', xM: 1, mode: 'road' }])
    const next = toMap([{ id: 'l1', xM: 1, mode: 'rail' }])
    expect(diffVms(prev, next).upserts.map((v) => v.id)).toEqual(['l1'])
  })
})
