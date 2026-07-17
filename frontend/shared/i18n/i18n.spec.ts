import { describe, expect, it } from 'vitest'

// Import vía alias a propósito: ejercita la resolución `~shared` en
// vitest.config.ts y tsconfig.kernel.json (misma tríada que nuxt.config.ts).
import { isMessageKey, t, tErrorCode } from '~shared/i18n'

/** Códigos estables documentados por el contrato (docs/api/openapi.yaml, schema Error). */
const CONTRACT_ERROR_CODES = [
  'INSUFFICIENT_COLLATERAL',
  'INSUFFICIENT_FUNDS',
  'PUBLICATION_EXHAUSTED',
  'CANCEL_COOLDOWN_ACTIVE',
  'BELOW_MIN_LOT',
  'STOCK_ALREADY_RESERVED',
  'PLACEMENT_INVALID',
  'NOT_RESOURCE_OWNER',
  'VEHICLE_SEALED',
  'NO_ROUTE_FOUND',
  'MAINTENANCE_WINDOW',
  'RATE_LIMITED',
  'VALIDATION_ERROR',
  'NOT_FOUND',
  'UNAUTHORIZED',
  'INTERNAL',
] as const

describe('shared/i18n — t', () => {
  it('resuelve una clave simple', () => {
    expect(t('app.title')).toBe('Imperio Industrial')
  })

  it('interpola parámetros {nombre}', () => {
    expect(t('lobby.welcome', { corporationName: 'Aceros del Norte' })).toBe(
      'Corporación Aceros del Norte',
    )
  })

  it('interpola parámetros numéricos (conteos de UI, nunca importes)', () => {
    expect(t('maintenance.retryIn', { minutes: 15 })).toBe('Reapertura estimada en 15 min')
  })

  it('deja visible un placeholder sin parámetro (detectable en revisión)', () => {
    expect(t('lobby.welcome')).toBe('Corporación {corporationName}')
    expect(t('lobby.welcome', {})).toBe('Corporación {corporationName}')
  })

  it('ignora parámetros sobrantes', () => {
    expect(t('common.retry', { unused: 'x' })).toBe('Reintentar')
  })
})

describe('shared/i18n — tErrorCode (mapeo error.code → UX, FAD §13.7)', () => {
  it('traduce un código catalogado', () => {
    expect(tErrorCode('INSUFFICIENT_COLLATERAL')).toBe(
      'La garantía disponible no cubre la operación',
    )
  })

  it('cae en error.UNKNOWN mostrando el código estable ante códigos nuevos', () => {
    expect(tErrorCode('SOME_FUTURE_CODE')).toBe('Error inesperado del servidor (SOME_FUTURE_CODE)')
  })

  it.each(CONTRACT_ERROR_CODES)('el catálogo cubre el código %s del contrato', (code) => {
    expect(isMessageKey(`error.${code}`)).toBe(true)
    expect(tErrorCode(code)).not.toContain('{')
    expect(tErrorCode(code)).not.toBe(tErrorCode('__UNKNOWN__'))
  })
})

describe('shared/i18n — isMessageKey', () => {
  it('acepta claves existentes', () => {
    expect(isMessageKey('login.title')).toBe(true)
  })

  it('rechaza claves inexistentes y claves del prototipo', () => {
    expect(isMessageKey('no.existe')).toBe(false)
    expect(isMessageKey('toString')).toBe(false)
    expect(isMessageKey('__proto__')).toBe(false)
  })
})
