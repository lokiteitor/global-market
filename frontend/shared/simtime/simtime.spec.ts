import { describe, expect, it } from 'vitest'

import {
  DAYS_PER_YEAR,
  RATIO,
  SIM_SECONDS_PER_DAY,
  SIM_SECONDS_PER_HOUR,
  SIM_SECONDS_PER_YEAR,
  deriveNow,
  formatSimTime,
  simDurationToWallMs,
  simTime,
  simToWallMs,
  wallDurationToSimSeconds,
} from './index'

/** Patrón de meta.sim_time del contrato (docs/api/openapi.yaml). */
const CONTRACT_PATTERN = /^[0-9]{1,4}-[0-9]{3}-[0-9]{2}:[0-9]{2}$/

describe('shared/simtime — constantes del dominio', () => {
  it('ratio 24× y año de 360 días (GDD/contrato)', () => {
    expect(RATIO).toBe(24)
    expect(DAYS_PER_YEAR).toBe(360)
    expect(SIM_SECONDS_PER_YEAR).toBe(31_104_000)
  })
})

describe('shared/simtime — simTime (validación)', () => {
  it('acepta 0 (génesis) y enteros grandes', () => {
    expect(simTime(0)).toBe(0)
    expect(simTime(31_104_000)).toBe(31_104_000)
  })

  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, 2 ** 53])(
    'lanza RangeError ante %s',
    (value) => {
      expect(() => simTime(value)).toThrow(RangeError)
    },
  )
})

describe('shared/simtime — formatSimTime (formato del contrato AAA-DDD-HH:MM)', () => {
  it('génesis → 001-001-00:00 (año y día 1-based)', () => {
    expect(formatSimTime(simTime(0))).toBe('001-001-00:00')
  })

  it('trunca los segundos dentro del minuto', () => {
    expect(formatSimTime(simTime(59))).toBe('001-001-00:00')
    expect(formatSimTime(simTime(60))).toBe('001-001-00:01')
  })

  it('horas y minutos de juego', () => {
    expect(formatSimTime(simTime(SIM_SECONDS_PER_HOUR))).toBe('001-001-01:00')
    expect(formatSimTime(simTime(12 * SIM_SECONDS_PER_HOUR + 30 * 60))).toBe('001-001-12:30')
  })

  it('borde de cambio de día', () => {
    expect(formatSimTime(simTime(SIM_SECONDS_PER_DAY - 1))).toBe('001-001-23:59')
    expect(formatSimTime(simTime(SIM_SECONDS_PER_DAY))).toBe('001-002-00:00')
  })

  it('último día del año y borde de cambio de año', () => {
    expect(formatSimTime(simTime(SIM_SECONDS_PER_YEAR - 1))).toBe('001-360-23:59')
    expect(formatSimTime(simTime(SIM_SECONDS_PER_YEAR))).toBe('002-001-00:00')
  })

  it('reproduce el ejemplo del contrato: "360-045-12:30"', () => {
    const seconds =
      359 * SIM_SECONDS_PER_YEAR + 44 * SIM_SECONDS_PER_DAY + 12 * SIM_SECONDS_PER_HOUR + 30 * 60
    expect(formatSimTime(simTime(seconds))).toBe('360-045-12:30')
  })

  it('años de 4 dígitos siguen cumpliendo el patrón del contrato', () => {
    const seconds = 999 * SIM_SECONDS_PER_YEAR // año 1000, día 001
    expect(formatSimTime(simTime(seconds))).toBe('1000-001-00:00')
  })

  it.each([0, 1, 86_399, 86_400, 31_103_999, 31_104_000, 999_999_999])(
    'la salida para %s s cumple el patrón del contrato',
    (seconds) => {
      expect(formatSimTime(simTime(seconds))).toMatch(CONTRACT_PATTERN)
    },
  )
})

describe('shared/simtime — deriveNow (ancla + ratio 24×)', () => {
  const anchorSim = simTime(1_000)
  const anchorWallMs = 5_000_000

  it('sin tiempo transcurrido devuelve el ancla', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs, false)).toBe(1_000)
  })

  it('1 segundo real → +24 segundos de juego', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs + 1_000, false)).toBe(1_024)
  })

  it('1,5 segundos reales → +36 segundos de juego', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs + 1_500, false)).toBe(1_036)
  })

  it('trunca a segundos enteros (999 ms → +23 s, no +23,976)', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs + 999, false)).toBe(1_023)
  })

  it('frozen=true congela el sim-time (ventana de mantenimiento, C9)', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs + 60_000, true)).toBe(1_000)
  })

  it('nunca retrocede: transcurso negativo se satura en el ancla', () => {
    expect(deriveNow(anchorSim, anchorWallMs, anchorWallMs - 10_000, false)).toBe(1_000)
  })

  it('desde el génesis exacto', () => {
    expect(deriveNow(simTime(0), 0, 1_000, false)).toBe(24)
  })

  it('cruza el borde de día correctamente', () => {
    const anchor = simTime(SIM_SECONDS_PER_DAY - RATIO) // a 24 s sim del día 002
    const now = deriveNow(anchor, 0, 1_000, false)
    expect(now).toBe(SIM_SECONDS_PER_DAY)
    expect(formatSimTime(now)).toBe('001-002-00:00')
  })

  it('cruza el borde de año correctamente', () => {
    const anchor = simTime(SIM_SECONDS_PER_YEAR - RATIO)
    const now = deriveNow(anchor, 0, 1_000, false)
    expect(now).toBe(SIM_SECONDS_PER_YEAR)
    expect(formatSimTime(now)).toBe('002-001-00:00')
  })
})

describe('shared/simtime — simToWallMs y duraciones', () => {
  const anchorSim = simTime(1_000)
  const anchorWallMs = 5_000_000

  it('el ancla se traduce a su propio wall-clock', () => {
    expect(simToWallMs(anchorSim, anchorSim, anchorWallMs)).toBe(anchorWallMs)
  })

  it('+24 s de juego → +1000 ms reales', () => {
    expect(simToWallMs(simTime(1_024), anchorSim, anchorWallMs)).toBe(anchorWallMs + 1_000)
  })

  it('un sim-segundo son 1000/24 ms reales (fraccionario permitido)', () => {
    expect(simToWallMs(simTime(1_001), anchorSim, anchorWallMs)).toBeCloseTo(
      anchorWallMs + 1_000 / 24,
      9,
    )
  })

  it('un instante sim anterior al ancla cae en el pasado wall-clock', () => {
    expect(simToWallMs(simTime(976), anchorSim, anchorWallMs)).toBe(anchorWallMs - 1_000)
  })

  it('es inversa de deriveNow en múltiplos exactos del ratio', () => {
    const nowWallMs = anchorWallMs + 30_000
    const derived = deriveNow(anchorSim, anchorWallMs, nowWallMs, false)
    expect(simToWallMs(derived, anchorSim, anchorWallMs)).toBe(nowWallMs)
  })

  it('simDurationToWallMs aplica el ratio 24×', () => {
    expect(simDurationToWallMs(24)).toBe(1_000)
    expect(simDurationToWallMs(SIM_SECONDS_PER_DAY)).toBe(3_600_000) // 1 día de juego = 1 h real
    expect(simDurationToWallMs(12)).toBe(500)
    expect(simDurationToWallMs(0)).toBe(0)
  })

  it('wallDurationToSimSeconds trunca a entero', () => {
    expect(wallDurationToSimSeconds(1_000)).toBe(24)
    expect(wallDurationToSimSeconds(999)).toBe(23)
    expect(wallDurationToSimSeconds(0)).toBe(0)
    expect(wallDurationToSimSeconds(3_600_000)).toBe(SIM_SECONDS_PER_DAY)
  })
})
