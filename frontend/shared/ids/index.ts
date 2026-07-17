/**
 * shared/ids — identificadores de entidad y UUIDv7 propio (kernel, FAD §20.6, ADR-018).
 *
 * El contrato usa UUIDv7 planos sin prefijo (`type: string, format: uuid`) con
 * schemas nominales por entidad (AccountId, ContractId, VehicleId, …). El tipado
 * por entidad vive en el brand de `EntityId<T>`, no en el formato del string:
 * el compilador impide pasar un BuildingId donde se espera un VehicleId.
 *
 * `uuidv7()` genera claves de idempotencia y otros IDs de cliente (FAD §12.8)
 * sin dependencias: timestamp de 48 bits + versión 7 + variante RFC 9562 +
 * 74 bits aleatorios de `crypto.getRandomValues`.
 */

import type { Result } from '../result'
import { err, ok } from '../result'

/** UUID plano tipado por entidad vía brand (los brands nacen de los schemas nominales del contrato). */
export type EntityId<Brand extends string> = string & { readonly __brand: `id:${Brand}` }

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

/** Máximo timestamp representable en 48 bits (ms desde epoch Unix). */
const MAX_UUIDV7_TIMESTAMP_MS = 2 ** 48 - 1

/** Error de validación de identificador. */
export interface IdError {
  readonly kind: 'invalid-uuid'
  readonly input: string
}

/** ¿Es un UUID canónico (8-4-4-4-12 hexadecimal)? Acepta mayúsculas y minúsculas. */
export function isUuid(value: string): boolean {
  return UUID_PATTERN.test(value)
}

/**
 * Valida un id recibido de la red y lo tipa por entidad (uso en mappers, FAD §9.5).
 * Normaliza a minúsculas (forma canónica RFC 9562).
 */
export function parseEntityId<Brand extends string>(
  input: string,
): Result<EntityId<Brand>, IdError> {
  if (!isUuid(input)) {
    return err({ kind: 'invalid-uuid', input })
  }
  return ok(input.toLowerCase() as EntityId<Brand>)
}

/**
 * Variante que lanza `RangeError`: para contextos donde un id inválido es un
 * bug de programación, no un dato de entrada (p. ej. fixtures, constantes).
 */
export function asEntityId<Brand extends string>(input: string): EntityId<Brand> {
  const result = parseEntityId<Brand>(input)
  if (!result.ok) {
    throw new RangeError(`asEntityId: "${input}" no es un UUID válido`)
  }
  return result.value
}

/**
 * Genera un UUIDv7 (RFC 9562): 48 bits de timestamp en ms + 4 bits de versión
 * (7) + 12 bits aleatorios + 2 bits de variante (10) + 62 bits aleatorios.
 *
 * `timestampMs` es inyectable para tests deterministas; por defecto `Date.now()`.
 * Lanza `RangeError` si el timestamp no cabe en 48 bits.
 */
export function uuidv7(timestampMs: number = Date.now()): string {
  if (
    !Number.isSafeInteger(timestampMs) ||
    timestampMs < 0 ||
    timestampMs > MAX_UUIDV7_TIMESTAMP_MS
  ) {
    throw new RangeError(
      `uuidv7: timestamp inválido (${String(timestampMs)}); se requiere entero en [0, 2^48)`,
    )
  }

  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)

  // 48 bits de timestamp (big-endian) en los bytes 0..5.
  bytes[0] = Math.floor(timestampMs / 2 ** 40) & 0xff
  bytes[1] = Math.floor(timestampMs / 2 ** 32) & 0xff
  bytes[2] = Math.floor(timestampMs / 2 ** 24) & 0xff
  bytes[3] = Math.floor(timestampMs / 2 ** 16) & 0xff
  bytes[4] = Math.floor(timestampMs / 2 ** 8) & 0xff
  bytes[5] = timestampMs & 0xff

  // Versión 7 en los 4 bits altos del byte 6; variante RFC (10) en el byte 8.
  bytes[6] = 0x70 | ((bytes[6] ?? 0) & 0x0f)
  bytes[8] = 0x80 | ((bytes[8] ?? 0) & 0x3f)

  let hex = ''
  for (const byte of bytes) {
    hex += byte.toString(16).padStart(2, '0')
  }
  return (
    `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-` +
    `${hex.slice(16, 20)}-${hex.slice(20, 32)}`
  )
}

/**
 * Extrae el timestamp (ms desde epoch) de un UUIDv7. Útil para diagnóstico y
 * tests de monotonía. Lanza `RangeError` si el valor no es un UUID.
 */
export function uuidv7TimestampMs(id: string): number {
  if (!isUuid(id)) {
    throw new RangeError(`uuidv7TimestampMs: "${id}" no es un UUID válido`)
  }
  const hex = id.replaceAll('-', '').slice(0, 12)
  return Number.parseInt(hex, 16)
}
