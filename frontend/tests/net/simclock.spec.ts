/**
 * tests/net/simclock.spec.ts — SimClock (P5/C8/ADR-FE-007): deriva 24×,
 * freeze de mantenimiento y no-retroceso (clamp monotónico).
 *
 * Se prueba contra la sim.store REAL (Pinia): el servicio solo añade la
 * monotonicidad y la vista reactiva; la derivación vive en store + kernel.
 */
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { createSimClock } from '~/lib/net/simclock'
import { useSimStore } from '~/stores/sim.store'

describe('SimClock', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
  })

  it('avanza a ratio 24× sobre el wall-clock transcurrido desde el último sync', () => {
    const clock = createSimClock(useSimStore())
    clock.sync(1_000, false, 0)

    expect(clock.now(0)).toBe(1_000)
    // 1 s de pared = 24 s de sim.
    expect(clock.now(1_000)).toBe(1_024)
    expect(clock.now(10_000)).toBe(1_240)
  })

  it('sin muestra inicial devuelve 0 y no revienta', () => {
    const clock = createSimClock(useSimStore())
    expect(clock.now(123_456)).toBe(0)
  })

  it('congelado (ventana de mantenimiento) no avanza; al reanudar sí', () => {
    const store = useSimStore()
    const clock = createSimClock(store)

    clock.sync(5_000, true, 0)
    expect(clock.isFrozen()).toBe(true)
    expect(clock.now(60_000)).toBe(5_000) // un minuto de pared después, quieto

    clock.sync(5_000, false, 60_000) // sim.resumed
    expect(clock.isFrozen()).toBe(false)
    expect(clock.now(61_000)).toBe(5_024)
  })

  it('sync sin frozen explícito conserva el estado frozen vigente', () => {
    const store = useSimStore()
    const clock = createSimClock(store)

    clock.sync(5_000, true, 0)
    clock.sync(5_000, undefined, 1_000) // p. ej. meta REST durante mantenimiento
    expect(store.frozen).toBe(true)
  })

  it('nunca retrocede: una muestra del servidor por detrás se corrige con clamp suave', () => {
    const clock = createSimClock(useSimStore())

    clock.sync(1_000, false, 0)
    expect(clock.now(10_000)).toBe(1_240) // ya mostrado

    // El servidor corrige hacia atrás (p. ej. muestra vieja tras reconexión).
    clock.sync(1_100, false, 10_000)
    // El reloj visible NO salta hacia atrás: se mantiene en 1240…
    expect(clock.now(10_000)).toBe(1_240)
    expect(clock.now(12_000)).toBe(1_240) // derivado 1148 < 1240 → clamp
    // …hasta que la deriva de la muestra nueva lo alcanza y lo supera.
    // derivado(t) = 1100 + 24 × (t − 10000)/1000 ⇒ supera 1240 en t ≈ 15833 ms
    expect(clock.now(16_000)).toBeCloseTo(1_244, 6)
  })

  it('tick() actualiza la vista reactiva en segundos enteros', () => {
    const clock = createSimClock(useSimStore())
    clock.sync(100, false, 0)

    clock.tick(0)
    expect(clock.viewNowSeconds.value).toBe(100)
    clock.tick(250)
    expect(clock.viewNowSeconds.value).toBe(106) // 100 + 24 × 0.25
  })
})
