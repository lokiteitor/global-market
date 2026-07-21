/**
 * domain/quantity — tipo Quantity y aritmética de stock en punto fijo (FAD §20.6, C11).
 *
 * El contrato (schema StockQty) serializa el stock como entero sin signo en
 * string con patrón `^[0-9]+$` (unidad mínima del producto). El brand es
 * DISTINTO de Money a propósito: el compilador impide sumar dinero con stock
 * (error de unidad). Misma disciplina que shared/money:
 *
 * - PROHIBIDO pasar una cantidad por `number`/float (`parseFloat`, `Number(...)`);
 *   la aritmética interna usa BigInt.
 * - Quantity nunca es negativa; `subtractQuantity` falla si el resultado lo sería.
 * - El formateo a texto es la única salida legible.
 */

import type { Result } from '~shared/result'
import { err, ok } from '~shared/result'

/** Cantidad de stock en la unidad mínima del producto: branded string de punto fijo. */
export type Quantity = string & { readonly __brand: 'Quantity' }

/** Patrón del contrato (StockQty): dígitos ASCII, sin signo ni decimales. */
const QUANTITY_PATTERN = /^[0-9]+$/

/** Errores de parseo/aritmética de Quantity. */
export type QuantityError =
  | { readonly kind: 'invalid-format'; readonly input: string }
  | { readonly kind: 'negative-result'; readonly minuend: Quantity; readonly subtrahend: Quantity }

/** Guarda de tipo: `value` cumple el patrón de StockQty del contrato. */
export function isQuantity(value: string): value is Quantity {
  return QUANTITY_PATTERN.test(value)
}

/**
 * Valida y canonicaliza una cantidad recibida del servidor.
 * Canónico = sin ceros a la izquierda ("007" → "7"); "0" se conserva.
 */
export function parseQuantity(input: string): Result<Quantity, QuantityError> {
  if (!isQuantity(input)) {
    return err({ kind: 'invalid-format', input })
  }
  return ok(canonical(BigInt(input)))
}

/** Suma de cantidades (BigInt interno, sin pérdida de precisión). */
export function addQuantity(a: Quantity, b: Quantity): Quantity {
  return canonical(BigInt(a) + BigInt(b))
}

/**
 * Resta de cantidades. Lanza `RangeError` si el resultado sería negativo:
 * el dominio no admite stock negativo (patrón del contrato).
 */
export function subtractQuantity(minuend: Quantity, subtrahend: Quantity): Quantity {
  const result = BigInt(minuend) - BigInt(subtrahend)
  if (result < 0n) {
    throw new RangeError(
      `Quantity.subtract: resultado negativo (${minuend} - ${subtrahend}); Quantity no admite negativos`,
    )
  }
  return canonical(result)
}

/** Comparación total: -1 si a < b, 0 si iguales, 1 si a > b. */
export function compareQuantity(a: Quantity, b: Quantity): -1 | 0 | 1 {
  const left = BigInt(a)
  const right = BigInt(b)
  if (left < right) return -1
  if (left > right) return 1
  return 0
}

/**
 * Formatea para UI en es-ES: separador de miles "." y sin decimales.
 * Implementado por agrupación de dígitos — la cantidad jamás pasa por `number`.
 */
export function formatQuantity(quantity: Quantity): string {
  const digits = canonical(BigInt(quantity)) as string
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

/** Cero de stock. */
export const ZERO_QUANTITY: Quantity = '0' as Quantity

function canonical(value: bigint): Quantity {
  return value.toString(10) as Quantity
}
