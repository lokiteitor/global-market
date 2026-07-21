import { describe, expect, it } from 'vitest'

import { TypedEmitter } from './events'

type Events = {
  select: { xM: number; yM: number }
}

describe('game/events — TypedEmitter', () => {
  it('entrega el payload a todos los suscriptores', () => {
    const emitter = new TypedEmitter<Events>()
    const seen: number[] = []
    emitter.on('select', (e) => seen.push(e.xM))
    emitter.on('select', (e) => seen.push(e.xM * 2))
    emitter.emit('select', { xM: 3, yM: 0 })
    expect(seen).toEqual([3, 6])
  })

  it('la función de baja desuscribe solo ese handler', () => {
    const emitter = new TypedEmitter<Events>()
    const seen: string[] = []
    const off = emitter.on('select', () => seen.push('a'))
    emitter.on('select', () => seen.push('b'))
    off()
    emitter.emit('select', { xM: 0, yM: 0 })
    expect(seen).toEqual(['b'])
  })

  it('emitir sin suscriptores no falla; removeAll limpia todo', () => {
    const emitter = new TypedEmitter<Events>()
    expect(() => {
      emitter.emit('select', { xM: 1, yM: 1 })
    }).not.toThrow()
    let count = 0
    emitter.on('select', () => {
      count += 1
    })
    emitter.removeAll()
    emitter.emit('select', { xM: 1, yM: 1 })
    expect(count).toBe(0)
  })
})
