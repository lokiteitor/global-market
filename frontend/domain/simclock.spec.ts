import { describe, expect, it } from 'vitest'

import { createSimClock } from '~domain/simclock'
import { simTime } from '~shared/simtime'

/** Reloj wall-clock falso, controlado por el test (inyección, no mock global). */
function fakeWall(startMs: number) {
  let nowMs = startMs
  return {
    now: () => nowMs,
    advance(ms: number) {
      nowMs += ms
    },
  }
}

describe('domain/simclock — anclaje y derivación (ADR-FE-007, FAD §12.7)', () => {
  it('sin meta del servidor no hay sim-time: now() es null', () => {
    const wall = fakeWall(1_000_000)
    const clock = createSimClock({ wallNow: wall.now })
    expect(clock.now()).toBeNull()
    expect(clock.snapshot()).toEqual({ anchorSimSeconds: null, anchorWallMs: null, frozen: false })
  })

  it('tras update() deriva con ratio 24× sobre el tiempo real transcurrido', () => {
    const wall = fakeWall(1_000_000)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(500_000) })

    expect(clock.now()).toBe(500_000)
    wall.advance(1_000) // 1 s real = 24 s sim
    expect(clock.now()).toBe(500_024)
    wall.advance(2_500) // 2,5 s reales = 60 s sim
    expect(clock.now()).toBe(500_084)
  })

  it('acepta un instante de recepción explícito (receivedAtWallMs)', () => {
    const wall = fakeWall(1_000_000)
    const clock = createSimClock({ wallNow: wall.now })
    // La respuesta se recibió hace 1 s: el ahora ya va 24 s sim por delante.
    clock.update({ simTimeSeconds: simTime(100), receivedAtWallMs: 999_000 })
    expect(clock.now()).toBe(124)
  })

  it('un re-anclaje hacia delante salta de inmediato (el servidor manda)', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(1_000) })
    expect(clock.now()).toBe(1_000)

    clock.update({ simTimeSeconds: simTime(9_999) })
    expect(clock.now()).toBe(9_999)
  })
})

describe('domain/simclock — monotonía del valor visible', () => {
  it('un re-anclaje "por detrás" no retrocede el reloj visible; lo alcanza suavemente', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(1_000) })

    wall.advance(10_000) // visible = 1000 + 240 = 1240
    expect(clock.now()).toBe(1_240)

    // Meta vieja/jitter: el servidor dice 1100 (140 s sim por detrás de lo mostrado).
    clock.update({ simTimeSeconds: simTime(1_100) })
    expect(clock.now()).toBe(1_240) // NO retrocede

    wall.advance(5_000) // derivado = 1100 + 120 = 1220 < 1240 → sigue reteniendo
    expect(clock.now()).toBe(1_240)

    wall.advance(1_000) // derivado = 1100 + 144 = 1244 > 1240 → ya lo alcanzó
    expect(clock.now()).toBe(1_244)
  })

  it('now() nunca decrece a lo largo de una secuencia de anclajes arbitraria', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    const seen: number[] = []
    const anchors = [5_000, 4_000, 6_000, 5_500, 5_999]
    for (const anchor of anchors) {
      clock.update({ simTimeSeconds: simTime(anchor) })
      for (let i = 0; i < 3; i += 1) {
        wall.advance(400)
        const value = clock.now()
        expect(value).not.toBeNull()
        seen.push(value as number)
      }
    }
    const sorted = [...seen].sort((a, b) => a - b)
    expect(seen).toEqual(sorted)
  })
})

describe('domain/simclock — estado frozen (ventana de mantenimiento, FAD §12.9)', () => {
  it('freeze() detiene el avance en el valor visible actual', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(2_000) })
    wall.advance(1_000)
    expect(clock.now()).toBe(2_024)

    clock.freeze()
    expect(clock.isFrozen()).toBe(true)
    wall.advance(60_000)
    expect(clock.now()).toBe(2_024) // congelado: nada avanza
  })

  it('update() tras el mantenimiento reanuda el reloj (autocurativo)', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(2_000) })
    clock.freeze()
    wall.advance(30_000)

    clock.update({ simTimeSeconds: simTime(2_100) })
    expect(clock.isFrozen()).toBe(false)
    expect(clock.now()).toBe(2_100)
    wall.advance(1_000)
    expect(clock.now()).toBe(2_124)
  })

  it('freeze() antes de cualquier anclaje deja el reloj congelado y sin valor', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    clock.freeze()
    expect(clock.isFrozen()).toBe(true)
    expect(clock.now()).toBeNull()
  })
})

describe('domain/simclock — traducción sim → wall (countdowns)', () => {
  it('traduce un deadline sim-time al wall-clock local con ratio 24×', () => {
    const wall = fakeWall(1_000_000)
    const clock = createSimClock({ wallNow: wall.now })
    clock.update({ simTimeSeconds: simTime(500_000) })

    // 240 s sim por delante = 10 s reales.
    expect(clock.simToWallMs(simTime(500_240))).toBe(1_010_000)
  })

  it('devuelve null sin anclar o en frozen (ningún plazo avanza en mantenimiento)', () => {
    const wall = fakeWall(0)
    const clock = createSimClock({ wallNow: wall.now })
    expect(clock.simToWallMs(simTime(100))).toBeNull()

    clock.update({ simTimeSeconds: simTime(50) })
    clock.freeze()
    expect(clock.simToWallMs(simTime(100))).toBeNull()
  })
})
