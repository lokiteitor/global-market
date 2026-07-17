/**
 * shared/money — tipo Money y aritmética de punto fijo (kernel, FAD §20.6, C11).
 *
 * El contrato (docs/api/openapi.yaml, schema MoneyAmount) serializa el dinero
 * como entero de punto fijo en string con patrón `^[0-9]+$` (unidades menores;
 * la unidad menor ES la unidad del juego, sin decimales). Invariantes:
 *
 * - PROHIBIDO pasar un importe por `number`/float (`parseFloat`, `Number(...)`):
 *   toda la aritmética interna usa BigInt (regla de linter en eslint.config.mjs).
 * - Money nunca es negativo (el patrón del contrato no admite signo);
 *   `subtract` falla si el resultado sería negativo.
 * - El formateo a texto (es-ES, separador de miles) es la única salida legible.
 */

import type { Result } from '../result'
import { err, ok } from '../result'

/** Importe monetario en unidades menores: branded string de punto fijo. */
export type Money = string & { readonly __brand: 'Money' }

/** Patrón del contrato (MoneyAmount): dígitos ASCII, sin signo ni decimales. */
const MONEY_PATTERN = /^[0-9]+$/

/** Errores de parseo/aritmética de Money. */
export type MoneyError =
  | { readonly kind: 'invalid-format'; readonly input: string }
  | { readonly kind: 'negative-result'; readonly minuend: Money; readonly subtrahend: Money }

/** Guarda de tipo: `value` cumple el patrón de MoneyAmount del contrato. */
export function isMoney(value: string): value is Money {
  return MONEY_PATTERN.test(value)
}

/**
 * Valida y canonicaliza un importe recibido del servidor.
 * Canónico = sin ceros a la izquierda ("007" → "7"); "0" se conserva.
 */
export function parseMoney(input: string): Result<Money, MoneyError> {
  if (!isMoney(input)) {
    return err({ kind: 'invalid-format', input })
  }
  return ok(canonical(BigInt(input)))
}

/** Suma de importes (BigInt interno, sin pérdida de precisión). */
export function add(a: Money, b: Money): Money {
  return canonical(BigInt(a) + BigInt(b))
}

/**
 * Resta de importes. Lanza `RangeError` si el resultado sería negativo:
 * el dominio no admite importes negativos (patrón del contrato).
 */
export function subtract(minuend: Money, subtrahend: Money): Money {
  const result = BigInt(minuend) - BigInt(subtrahend)
  if (result < 0n) {
    throw new RangeError(
      `Money.subtract: resultado negativo (${minuend} - ${subtrahend}); Money no admite negativos`,
    )
  }
  return canonical(result)
}

/** Comparación total: -1 si a < b, 0 si iguales, 1 si a > b. */
export function compare(a: Money, b: Money): -1 | 0 | 1 {
  const left = BigInt(a)
  const right = BigInt(b)
  if (left < right) return -1
  if (left > right) return 1
  return 0
}

/**
 * Multiplica un importe por un entero no negativo (p. ej. precio unitario ×
 * cantidad de lotes). Lanza `RangeError` si `factor` no es un entero seguro ≥ 0.
 */
export function multiplyByInt(amount: Money, factor: number): Money {
  if (!Number.isSafeInteger(factor) || factor < 0) {
    throw new RangeError(
      `Money.multiplyByInt: factor inválido (${String(factor)}); se requiere entero seguro >= 0`,
    )
  }
  return canonical(BigInt(amount) * BigInt(factor))
}

/**
 * Formatea para UI en es-ES: separador de miles "." y sin decimales
 * (la unidad menor es la unidad del juego). Implementado por agrupación de
 * dígitos — el importe jamás pasa por `number`.
 */
export function format(amount: Money): string {
  const digits = canonical(BigInt(amount)) as string
  let grouped = ''
  for (let i = 0; i < digits.length; i++) {
    const fromEnd = digits.length - i
    if (i > 0 && fromEnd % 3 === 0) {
      grouped += '.'
    }
    grouped += digits.charAt(i)
  }
  return grouped
}

/** Cero monetario. */
export const ZERO: Money = '0' as Money

function canonical(value: bigint): Money {
  return value.toString(10) as Money
}
