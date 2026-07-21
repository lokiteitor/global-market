import { describe, expect, it } from 'vitest'

import { ObjectPool } from './pools'

interface FakeSprite {
  id: number
  visible: boolean
}

const makePool = (): {
  pool: ObjectPool<FakeSprite>
  destroyed: FakeSprite[]
} => {
  let nextId = 0
  const destroyed: FakeSprite[] = []
  const pool = new ObjectPool<FakeSprite>({
    create: () => ({ id: (nextId += 1), visible: true }),
    onAcquire: (s) => {
      s.visible = true
    },
    onRelease: (s) => {
      s.visible = false
    },
    destroy: (s) => {
      destroyed.push(s)
    },
  })
  return { pool, destroyed }
}

describe('game/pools — ObjectPool', () => {
  it('acquire crea bajo demanda y contabiliza', () => {
    const { pool } = makePool()
    const a = pool.acquire()
    const b = pool.acquire()
    expect(a.id).not.toBe(b.id)
    expect(pool.counters()).toEqual({ created: 2, inUse: 2, free: 0 })
  })

  it('release devuelve al pool y acquire reutiliza (sin crear de nuevo)', () => {
    const { pool } = makePool()
    const a = pool.acquire()
    pool.release(a)
    expect(pool.counters()).toEqual({ created: 1, inUse: 0, free: 1 })
    const b = pool.acquire()
    expect(b).toBe(a)
    expect(pool.counters()).toEqual({ created: 1, inUse: 1, free: 0 })
  })

  it('aplica onAcquire/onRelease (mostrar/ocultar el sprite)', () => {
    const { pool } = makePool()
    const a = pool.acquire()
    expect(a.visible).toBe(true)
    pool.release(a)
    expect(a.visible).toBe(false)
    pool.acquire()
    expect(a.visible).toBe(true)
  })

  it('prewarm crea objetos ya libres y aparcados', () => {
    const { pool } = makePool()
    pool.prewarm(3)
    expect(pool.counters()).toEqual({ created: 3, inUse: 0, free: 3 })
    const a = pool.acquire()
    expect(a.id).toBeLessThanOrEqual(3) // reutiliza, no crea el 4º
    expect(pool.counters()).toEqual({ created: 3, inUse: 1, free: 2 })
  })

  it('la doble liberación falla alto', () => {
    const { pool } = makePool()
    const a = pool.acquire()
    pool.release(a)
    expect(() => {
      pool.release(a)
    }).toThrow(/doble release/)
  })

  it('liberar un objeto ajeno falla alto', () => {
    const { pool } = makePool()
    expect(() => {
      pool.release({ id: 999, visible: true })
    }).toThrow(/no adquirido/)
  })

  it('drain libera lo adquirido, destruye todo y resetea contadores', () => {
    const { pool, destroyed } = makePool()
    pool.prewarm(1)
    const a = pool.acquire()
    const b = pool.acquire()
    pool.drain()
    expect(destroyed).toHaveLength(2)
    expect(destroyed).toContain(a)
    expect(destroyed).toContain(b)
    expect(a.visible).toBe(false)
    expect(pool.counters()).toEqual({ created: 0, inUse: 0, free: 0 })
  })
})
