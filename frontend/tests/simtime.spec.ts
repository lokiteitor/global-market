import { describe, expect, it } from 'vitest'
import {
  formatSimDuration,
  formatSimTime,
  plazoRestante,
  SIM_RATIO,
  simTime,
  simToWallMs,
  wallMsToSim
} from '~/lib/kernel/simtime'

describe('kernel/simtime', () => {
  it('el ratio es 24× (ADR-IMPL-06)', () => {
    expect(SIM_RATIO).toBe(24)
  })

  it('simTime valida entero >= 0', () => {
    expect(simTime(0)).toBe(0)
    expect(() => simTime(-1)).toThrow(RangeError)
    expect(() => simTime(1.5)).toThrow(RangeError)
  })

  describe('formatSimTime — AÑO-DDD-HH:MM (año = días/360 + 1, día 001..360)', () => {
    it('génesis', () => {
      expect(formatSimTime(simTime(0))).toBe('1-001-00:00')
    })

    it('último minuto del día 1', () => {
      expect(formatSimTime(simTime(86_399))).toBe('1-001-23:59')
    })

    it('inicio del día 2', () => {
      expect(formatSimTime(simTime(86_400))).toBe('1-002-00:00')
    })

    it('último día del año 1', () => {
      expect(formatSimTime(simTime(359 * 86_400))).toBe('1-360-00:00')
    })

    it('cambio de año: 360 días = año 2, día 001 (ejemplo de openapi.yaml)', () => {
      expect(formatSimTime(simTime(31_104_000))).toBe('2-001-00:00')
    })

    it('año 360, día 45, 12:30 (ejemplo de Meta.sim_time)', () => {
      const seconds = (359 * 360 + 44) * 86_400 + 12 * 3600 + 30 * 60
      expect(formatSimTime(simTime(seconds))).toBe('360-045-12:30')
    })
  })

  it('simToWallMs / wallMsToSim son inversas a ratio 24×', () => {
    expect(simToWallMs(24)).toBe(1000) // 24 s sim = 1 s pared
    expect(simToWallMs(86_400)).toBe(3_600_000) // 1 día sim = 1 h pared
    expect(wallMsToSim(1000)).toBe(24)
    expect(wallMsToSim(simToWallMs(123_456))).toBeCloseTo(123_456, 9)
  })

  it('plazoRestante devuelve el resto en sim-seconds con suelo 0', () => {
    expect(plazoRestante(simTime(1000), simTime(400))).toBe(600)
    expect(plazoRestante(simTime(400), simTime(1000))).toBe(0)
    expect(plazoRestante(simTime(400), simTime(400))).toBe(0)
  })

  it('formatSimDuration', () => {
    expect(formatSimDuration(0)).toBe('00:00:00')
    expect(formatSimDuration(3_725)).toBe('01:02:05')
    expect(formatSimDuration(2 * 86_400 + 4 * 3600 + 30 * 60)).toBe('2d 04:30')
  })
})
