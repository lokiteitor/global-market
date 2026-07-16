import { describe, expect, it } from 'vitest'
import { interpolateOnPath, pathLength, progressAt } from '~/game/kinematics'

describe('game/kinematics — interpolación pura (P5)', () => {
  describe('pathLength', () => {
    it('suma tramos euclídeos', () => {
      expect(pathLength([{ x: 0, y: 0 }, { x: 10, y: 0 }, { x: 10, y: 10 }])).toBe(20)
    })

    it('camino vacío o de un punto mide 0', () => {
      expect(pathLength([])).toBe(0)
      expect(pathLength([{ x: 3, y: 4 }])).toBe(0)
    })
  })

  describe('progressAt — clamp((simNow - entered)/duration + base, 0, 1)', () => {
    const kin = { enteredSim: 1000, durationSim: 200, baseProgress: 0 }

    it('antes de entrar al tramo → 0 (clamp inferior)', () => {
      expect(progressAt(kin, 900)).toBe(0)
    })

    it('avance lineal dentro del tramo', () => {
      expect(progressAt(kin, 1000)).toBe(0)
      expect(progressAt(kin, 1100)).toBeCloseTo(0.5)
      expect(progressAt(kin, 1200)).toBe(1)
    })

    it('pasada la duración → 1 (clamp superior: la llegada la decide el servidor)', () => {
      expect(progressAt(kin, 99999)).toBe(1)
    })

    it('respeta el progreso base del hito (segment_progress_pct)', () => {
      expect(progressAt({ ...kin, baseProgress: 0.4 }, 1000)).toBeCloseTo(0.4)
      expect(progressAt({ ...kin, baseProgress: 0.4 }, 1100)).toBeCloseTo(0.9)
    })

    it('duración no positiva degrada a 1 (no divide por cero)', () => {
      expect(progressAt({ enteredSim: 0, durationSim: 0, baseProgress: 0 }, 10)).toBe(1)
    })
  })

  describe('interpolateOnPath — por longitud de arco sobre el LineString', () => {
    const path = [
      { x: 0, y: 0 },
      { x: 10, y: 0 },
      { x: 10, y: 10 }
    ]

    it('progress 0 → inicio; progress 1 → final', () => {
      expect(interpolateOnPath(path, 0)).toMatchObject({ x: 0, y: 0 })
      expect(interpolateOnPath(path, 1)).toMatchObject({ x: 10, y: 10 })
    })

    it('interpola dentro del primer tramo con su ángulo', () => {
      const s = interpolateOnPath(path, 0.25) // 5 de 20
      expect(s.x).toBeCloseTo(5)
      expect(s.y).toBeCloseTo(0)
      expect(s.angle).toBeCloseTo(0)
    })

    it('interpola en el segundo tramo con el ángulo del vector del segmento', () => {
      const s = interpolateOnPath(path, 0.75) // 15 de 20 → 5 dentro del 2º tramo
      expect(s.x).toBeCloseTo(10)
      expect(s.y).toBeCloseTo(5)
      expect(s.angle).toBeCloseTo(Math.PI / 2)
    })

    it('clampa progress fuera de [0,1]', () => {
      expect(interpolateOnPath(path, -3)).toMatchObject({ x: 0, y: 0 })
      expect(interpolateOnPath(path, 7)).toMatchObject({ x: 10, y: 10 })
    })

    it('degrada con caminos vacíos, de un punto o de longitud cero', () => {
      expect(interpolateOnPath([], 0.5)).toEqual({ x: 0, y: 0, angle: 0 })
      expect(interpolateOnPath([{ x: 2, y: 3 }], 0.5)).toMatchObject({ x: 2, y: 3 })
      const s = interpolateOnPath([{ x: 1, y: 1 }, { x: 1, y: 1 }], 0.5)
      expect(s.x).toBe(1)
      expect(s.y).toBe(1)
    })
  })
})
