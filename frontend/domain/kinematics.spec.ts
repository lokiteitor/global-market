import { describe, expect, it } from 'vitest'

import { simTime } from '~shared/simtime'
import type { SegmentTraversalObservation } from './kinematics'
import {
  extrapolateProgressPct,
  pointAlongPath,
  progressPctToFraction,
  segmentTravelSimSeconds,
} from './kinematics'

/** Observación base: 30 km a 60 km/h fluidos = 0,5 h = 1800 s de sim-time. */
function obs(over: Partial<SegmentTraversalObservation> = {}): SegmentTraversalObservation {
  return {
    progressPct0: 0,
    simTimeObserved: simTime(10_000),
    lengthM: 30_000,
    baseSpeedKmh: 60,
    congestionEma: 1,
    ...over,
  }
}

describe('domain/kinematics — segmentTravelSimSeconds', () => {
  it('aplica la fórmula del contrato: length/1000 / (speed/congestion) * 3600', () => {
    expect(segmentTravelSimSeconds(30_000, 60, 1)).toBe(1_800)
    // Congestión 2 → mitad de velocidad efectiva → doble de tiempo.
    expect(segmentTravelSimSeconds(30_000, 60, 2)).toBe(3_600)
    expect(segmentTravelSimSeconds(1_000, 100, 1)).toBeCloseTo(36, 10)
  })

  it('congestión < 1 (más fluido que la base) acelera el tramo', () => {
    expect(segmentTravelSimSeconds(30_000, 60, 0.5)).toBe(900)
  })

  it('longitud no positiva → 0 (segmento ya recorrido)', () => {
    expect(segmentTravelSimSeconds(0, 60, 1)).toBe(0)
    expect(segmentTravelSimSeconds(-5, 60, 1)).toBe(0)
  })

  it('velocidad o congestión no positivas / no finitas → Infinity (no avanza)', () => {
    expect(segmentTravelSimSeconds(30_000, 0, 1)).toBe(Number.POSITIVE_INFINITY)
    expect(segmentTravelSimSeconds(30_000, -10, 1)).toBe(Number.POSITIVE_INFINITY)
    expect(segmentTravelSimSeconds(30_000, 60, 0)).toBe(Number.POSITIVE_INFINITY)
    expect(segmentTravelSimSeconds(30_000, Number.NaN, 1)).toBe(Number.POSITIVE_INFINITY)
    expect(segmentTravelSimSeconds(30_000, 60, Number.NaN)).toBe(Number.POSITIVE_INFINITY)
  })
})

describe('domain/kinematics — extrapolateProgressPct', () => {
  it('extrapola linealmente desde la observación', () => {
    // 900 sim-s de 1800 totales = 50 %.
    expect(extrapolateProgressPct(obs(), simTime(10_900))).toBeCloseTo(50, 10)
    // Desde un progreso previo del 40 %: 40 + 25 = 65.
    expect(extrapolateProgressPct(obs({ progressPct0: 40 }), simTime(10_450))).toBeCloseTo(65, 10)
  })

  it('congestión alta ralentiza la extrapolación', () => {
    // Congestión 2 → 3600 s totales: 900 s = 25 %.
    expect(extrapolateProgressPct(obs({ congestionEma: 2 }), simTime(10_900))).toBeCloseTo(25, 10)
    // Congestión 10 → 18000 s totales: 900 s = 5 %.
    expect(extrapolateProgressPct(obs({ congestionEma: 10 }), simTime(10_900))).toBeCloseTo(5, 10)
  })

  it('acota a 100 (jamás se pasa del final: el hito de llegada es del servidor)', () => {
    expect(extrapolateProgressPct(obs(), simTime(100_000))).toBe(100)
    expect(extrapolateProgressPct(obs({ progressPct0: 99.9 }), simTime(10_900))).toBe(100)
  })

  it('simNow anterior a la observación: el progreso NO retrocede', () => {
    expect(extrapolateProgressPct(obs({ progressPct0: 30 }), simTime(9_000))).toBe(30)
  })

  it('mismo instante de la observación devuelve el progreso observado', () => {
    expect(extrapolateProgressPct(obs({ progressPct0: 42 }), simTime(10_000))).toBe(42)
  })

  it('segmento de longitud 0 → 100 (ya recorrido)', () => {
    expect(extrapolateProgressPct(obs({ lengthM: 0 }), simTime(10_000))).toBe(100)
  })

  it('velocidad/congestión inválidas → no avanza (progreso observado, acotado)', () => {
    expect(
      extrapolateProgressPct(obs({ baseSpeedKmh: 0, progressPct0: 33 }), simTime(99_999)),
    ).toBe(33)
    expect(
      extrapolateProgressPct(obs({ congestionEma: 0, progressPct0: 120 }), simTime(99_999)),
    ).toBe(100)
  })

  it('progressPct0 fuera de rango se acota antes de extrapolar', () => {
    expect(extrapolateProgressPct(obs({ progressPct0: -50 }), simTime(10_000))).toBe(0)
    expect(extrapolateProgressPct(obs({ progressPct0: 150 }), simTime(10_000))).toBe(100)
    expect(extrapolateProgressPct(obs({ progressPct0: Number.NaN }), simTime(10_000))).toBe(0)
  })
})

describe('domain/kinematics — pointAlongPath', () => {
  it('camino de 2 puntos: extremos y punto medio', () => {
    const path = [
      [0, 0],
      [100, 0],
    ] as const
    expect(pointAlongPath(path, 0)).toEqual([0, 0])
    expect(pointAlongPath(path, 0.5)).toEqual([50, 0])
    expect(pointAlongPath(path, 1)).toEqual([100, 0])
  })

  it('interpola por LONGITUD acumulada, no por índice de vértice', () => {
    // Tramos de 10, 10 y 20 m (total 40): la mitad NO es el segundo vértice.
    const path = [
      [0, 0],
      [10, 0],
      [10, 10],
      [10, 30],
    ] as const
    expect(pointAlongPath(path, 0.25)).toEqual([10, 0])
    expect(pointAlongPath(path, 0.5)).toEqual([10, 10])
    expect(pointAlongPath(path, 0.75)).toEqual([10, 20])
    expect(pointAlongPath(path, 1)).toEqual([10, 30])
  })

  it('camino diagonal: interpolación en ambas coordenadas', () => {
    const path = [
      [0, 0],
      [30, 40],
    ] as const // longitud 50
    expect(pointAlongPath(path, 0.5)).toEqual([15, 20])
  })

  it('la fracción se acota a [0, 1]', () => {
    const path = [
      [0, 0],
      [100, 0],
    ] as const
    expect(pointAlongPath(path, -0.5)).toEqual([0, 0])
    expect(pointAlongPath(path, 1.5)).toEqual([100, 0])
  })

  it('camino de un solo punto devuelve ese punto para cualquier fracción', () => {
    expect(pointAlongPath([[7, 9]], 0)).toEqual([7, 9])
    expect(pointAlongPath([[7, 9]], 0.5)).toEqual([7, 9])
    expect(pointAlongPath([[7, 9]], 1)).toEqual([7, 9])
  })

  it('camino degenerado (longitud total 0) devuelve el primer punto', () => {
    const path = [
      [5, 5],
      [5, 5],
      [5, 5],
    ] as const
    expect(pointAlongPath(path, 0.7)).toEqual([5, 5])
  })

  it('tolera vértices repetidos intermedios (tramos de longitud 0)', () => {
    const path = [
      [0, 0],
      [10, 0],
      [10, 0],
      [20, 0],
    ] as const
    expect(pointAlongPath(path, 0.5)).toEqual([10, 0])
    expect(pointAlongPath(path, 0.75)).toEqual([15, 0])
  })

  it('camino vacío o fracción no finita → RangeError (bug del llamador)', () => {
    expect(() => pointAlongPath([], 0.5)).toThrow(RangeError)
    expect(() =>
      pointAlongPath(
        [
          [0, 0],
          [1, 1],
        ],
        Number.NaN,
      ),
    ).toThrow(RangeError)
    expect(() =>
      pointAlongPath(
        [
          [0, 0],
          [1, 1],
        ],
        Number.POSITIVE_INFINITY,
      ),
    ).toThrow(RangeError)
  })

  it('devuelve tuplas nuevas (no alias de los vértices del camino)', () => {
    const path = [
      [0, 0],
      [100, 0],
    ] as const
    const point = pointAlongPath(path, 0)
    expect(point).toEqual([0, 0])
    expect(point).not.toBe(path[0])
  })
})

describe('domain/kinematics — progressPctToFraction', () => {
  it('convierte porcentaje acotado a fracción [0, 1]', () => {
    expect(progressPctToFraction(0)).toBe(0)
    expect(progressPctToFraction(50)).toBe(0.5)
    expect(progressPctToFraction(100)).toBe(1)
    expect(progressPctToFraction(-10)).toBe(0)
    expect(progressPctToFraction(250)).toBe(1)
    expect(progressPctToFraction(Number.NaN)).toBe(0)
  })
})
