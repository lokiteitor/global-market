import { describe, expect, it } from 'vitest'

import {
  API_ERROR_CODES,
  AppError,
  appErrorFromHttp,
  appErrorFromProtocol,
  appErrorFromTransport,
  errorCodeFromStatus,
  isApiErrorCode,
} from '~network/rest/errors'

const noHeaders = () => null

function headers(map: Record<string, string>) {
  return (name: string) => map[name.toLowerCase()] ?? null
}

describe('network/rest/errors — mapeo del envelope de error (FAD §13.7)', () => {
  it('mapea {error:{code,message,details}} a AppError tipado', () => {
    const error = appErrorFromHttp(
      422,
      {
        error: {
          code: 'INSUFFICIENT_COLLATERAL',
          message: 'La garantía disponible no cubre la publicación solicitada',
          details: { required: '1000', available: '740' },
        },
      },
      noHeaders,
    )

    expect(error).toBeInstanceOf(AppError)
    expect(error.kind).toBe('http')
    expect(error.code).toBe('INSUFFICIENT_COLLATERAL')
    expect(error.status).toBe(422)
    expect(error.message).toBe('La garantía disponible no cubre la publicación solicitada')
    // Los importes de details siguen siendo strings de punto fijo (C11).
    expect(error.details).toEqual({ required: '1000', available: '740' })
    expect(error.rawCode).toBe('INSUFFICIENT_COLLATERAL')
    expect(error.isMaintenance).toBe(false)
  })

  it('un código no catalogado cae al código derivado del status y conserva rawCode', () => {
    const error = appErrorFromHttp(
      409,
      { error: { code: 'SOME_FUTURE_CODE', message: 'x' } },
      noHeaders,
    )
    expect(error.code).toBe('VALIDATION_ERROR')
    expect(error.rawCode).toBe('SOME_FUTURE_CODE')
  })

  it('sin envelope (HTML de un proxy, cuerpo vacío) infiere el código por status', () => {
    expect(appErrorFromHttp(401, undefined, noHeaders).code).toBe('UNAUTHORIZED')
    expect(appErrorFromHttp(403, 'forbidden', noHeaders).code).toBe('NOT_RESOURCE_OWNER')
    expect(appErrorFromHttp(404, {}, noHeaders).code).toBe('NOT_FOUND')
    expect(appErrorFromHttp(429, null, noHeaders).code).toBe('RATE_LIMITED')
    expect(appErrorFromHttp(500, undefined, noHeaders).code).toBe('INTERNAL')
    expect(appErrorFromHttp(502, undefined, noHeaders).code).toBe('INTERNAL')
  })

  it('lee retryAfterSeconds de la cabecera Retry-After (429/503)', () => {
    const error = appErrorFromHttp(
      429,
      { error: { code: 'RATE_LIMITED', message: 'despacio' } },
      headers({ 'retry-after': '30' }),
    )
    expect(error.retryAfterSeconds).toBe(30)
  })

  it('usa details.retry_after_seconds como respaldo si no hay cabecera', () => {
    const error = appErrorFromHttp(
      503,
      {
        error: {
          code: 'MAINTENANCE_WINDOW',
          message: 'mantenimiento',
          details: { retry_after_seconds: 900 },
        },
      },
      noHeaders,
    )
    expect(error.retryAfterSeconds).toBe(900)
  })

  it('ignora una cabecera Retry-After no numérica', () => {
    const error = appErrorFromHttp(429, undefined, headers({ 'retry-after': 'Wed, 21 Oct' }))
    expect(error.retryAfterSeconds).toBeNull()
  })

  it('503 se distingue como mantenimiento (isMaintenance), no como error genérico', () => {
    const withEnvelope = appErrorFromHttp(
      503,
      { error: { code: 'MAINTENANCE_WINDOW', message: 'el mundo está pausado' } },
      headers({ 'retry-after': '900' }),
    )
    expect(withEnvelope.isMaintenance).toBe(true)
    expect(withEnvelope.retryAfterSeconds).toBe(900)

    // Incluso un 503 sin envelope (Caddy caído a mitad de ventana) se trata como mantenimiento.
    const bare = appErrorFromHttp(503, undefined, noHeaders)
    expect(bare.code).toBe('MAINTENANCE_WINDOW')
    expect(bare.isMaintenance).toBe(true)
  })
})

describe('network/rest/errors — fallos de transporte y de protocolo', () => {
  it('el fallo sin respuesta HTTP es kind network con status 0 y causa preservada', () => {
    const cause = new TypeError('fetch failed')
    const error = appErrorFromTransport(cause)
    expect(error.kind).toBe('network')
    expect(error.status).toBe(0)
    expect(error.code).toBe('INTERNAL')
    expect(error.cause).toBe(cause)
  })

  it('el payload fuera de contrato es kind protocol', () => {
    const error = appErrorFromProtocol('falta el campo "data" del envelope', 200)
    expect(error.kind).toBe('protocol')
    expect(error.status).toBe(200)
    expect(error.message).toContain('falta el campo "data"')
  })
})

describe('network/rest/errors — taxonomía', () => {
  it('la unión cubre exactamente los 18 códigos documentados del contrato', () => {
    expect(API_ERROR_CODES).toHaveLength(18)
    expect(isApiErrorCode('INSUFFICIENT_COLLATERAL')).toBe(true)
    expect(isApiErrorCode('NO_ROUTE_FOUND')).toBe(true)
    expect(isApiErrorCode('VEHICLE_NOT_IDLE')).toBe(true)
    expect(isApiErrorCode('SLOT_HELD')).toBe(true)
    expect(isApiErrorCode('SOME_FUTURE_CODE')).toBe(false)
  })

  it('errorCodeFromStatus cubre los status documentados del contrato', () => {
    expect(errorCodeFromStatus(400)).toBe('VALIDATION_ERROR')
    expect(errorCodeFromStatus(422)).toBe('VALIDATION_ERROR')
    expect(errorCodeFromStatus(401)).toBe('UNAUTHORIZED')
    expect(errorCodeFromStatus(403)).toBe('NOT_RESOURCE_OWNER')
    expect(errorCodeFromStatus(404)).toBe('NOT_FOUND')
    expect(errorCodeFromStatus(409)).toBe('VALIDATION_ERROR')
    expect(errorCodeFromStatus(429)).toBe('RATE_LIMITED')
    expect(errorCodeFromStatus(503)).toBe('MAINTENANCE_WINDOW')
    expect(errorCodeFromStatus(500)).toBe('INTERNAL')
  })
})
