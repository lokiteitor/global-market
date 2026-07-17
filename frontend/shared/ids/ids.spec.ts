import { describe, expect, it } from 'vitest'

import { asEntityId, isUuid, parseEntityId, uuidv7, uuidv7TimestampMs } from './index'

const UUID_V7_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('shared/ids — isUuid', () => {
  it.each([
    '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012', // ejemplo del contrato (UUIDv7)
    '123e4567-e89b-12d3-a456-426614174000',
    '00000000-0000-0000-0000-000000000000',
    '01981C5E-7D2A-7F3B-9E41-A2C4D6E8F012', // mayúsculas aceptadas
  ])('acepta "%s"', (value) => {
    expect(isUuid(value)).toBe(true)
  })

  it.each([
    '',
    '01981c5e7d2a7f3b9e41a2c4d6e8f012', // sin guiones
    '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f01', // corto
    '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f0123', // largo
    'acc_01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012', // prefijado (derogado por ADR-018)
    '01981c5g-7d2a-7f3b-9e41-a2c4d6e8f012', // 'g' no es hex
    '01981c5e-7d2a-7f3b-9e41_a2c4d6e8f012',
    ' 01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012',
  ])('rechaza "%s"', (value) => {
    expect(isUuid(value)).toBe(false)
  })
})

describe('shared/ids — parseEntityId / asEntityId', () => {
  it('valida y normaliza a minúsculas (forma canónica)', () => {
    const result = parseEntityId<'Building'>('01981C5E-7D2A-7F3B-9E41-A2C4D6E8F012')
    expect(result.ok && result.value).toBe('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012')
  })

  it('devuelve error tipado ante entrada inválida', () => {
    const result = parseEntityId<'Vehicle'>('no-es-un-uuid')
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.error).toEqual({ kind: 'invalid-uuid', input: 'no-es-un-uuid' })
  })

  it('asEntityId devuelve el id validado', () => {
    expect(asEntityId<'Contract'>('01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012')).toBe(
      '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012',
    )
  })

  it('asEntityId lanza RangeError ante entrada inválida', () => {
    expect(() => asEntityId<'Contract'>('x')).toThrow(RangeError)
  })
})

describe('shared/ids — uuidv7 (RFC 9562)', () => {
  it('produce el formato canónico con versión 7 y variante 10xx', () => {
    for (let i = 0; i < 200; i++) {
      const id = uuidv7()
      expect(id).toMatch(UUID_V7_PATTERN)
      expect(isUuid(id)).toBe(true)
      expect(id.charAt(14)).toBe('7') // nibble de versión
      expect('89ab').toContain(id.charAt(19)) // variante RFC
    }
  })

  it('codifica el timestamp inyectado en los 48 bits altos', () => {
    expect(uuidv7(0x0123456789ab).startsWith('01234567-89ab-7')).toBe(true)
    expect(uuidv7TimestampMs(uuidv7(0x0123456789ab))).toBe(0x0123456789ab)
  })

  it('timestamp 0 (epoch) es representable', () => {
    const id = uuidv7(0)
    expect(id.startsWith('00000000-0000-7')).toBe(true)
    expect(uuidv7TimestampMs(id)).toBe(0)
  })

  it('el timestamp máximo de 48 bits es representable', () => {
    const max = 2 ** 48 - 1
    expect(uuidv7TimestampMs(uuidv7(max))).toBe(max)
  })

  it('por defecto usa Date.now()', () => {
    const before = Date.now()
    const ts = uuidv7TimestampMs(uuidv7())
    const after = Date.now()
    expect(ts).toBeGreaterThanOrEqual(before)
    expect(ts).toBeLessThanOrEqual(after)
  })

  it('monotonía temporal razonable: timestamps crecientes ordenan lexicográficamente', () => {
    const base = 1_750_000_000_000
    const ids = Array.from({ length: 100 }, (_, i) => uuidv7(base + i))
    const sorted = [...ids].sort()
    expect(sorted).toEqual(ids)
  })

  it('ids del mismo milisegundo comparten prefijo de timestamp y son únicos', () => {
    const ts = 1_750_000_000_000
    const ids = new Set<string>()
    let prefix: string | undefined
    for (let i = 0; i < 1_000; i++) {
      const id = uuidv7(ts)
      prefix ??= id.slice(0, 13)
      expect(id.slice(0, 13)).toBe(prefix)
      ids.add(id)
    }
    expect(ids.size).toBe(1_000)
  })

  it.each([-1, 1.5, Number.NaN, 2 ** 48])('lanza RangeError ante timestamp inválido (%s)', (ts) => {
    expect(() => uuidv7(ts)).toThrow(RangeError)
  })

  it('uuidv7TimestampMs lanza RangeError ante un no-UUID', () => {
    expect(() => uuidv7TimestampMs('no-uuid')).toThrow(RangeError)
  })
})
