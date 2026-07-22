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
 * Multiplica un importe por una cantidad de punto fijo del contrato (StockQty
 * u otra magnitud `^[0-9]+$`), p. ej. tarifa unitaria × cantidad de carga para
 * previsualizar el escrow de un flete. `units` es un string validado por patrón
 * (no `Quantity`: shared/ no puede importar domain/). Lanza `RangeError` si
 * `units` no cumple el patrón.
 */
export function multiplyByUnits(amount: Money, units: string): Money {
  if (!MONEY_PATTERN.test(units)) {
    throw new RangeError(
      `Money.multiplyByUnits: unidades inválidas (${units}); se requiere ^[0-9]+$`,
    )
  }
  return canonical(BigInt(amount) * BigInt(units))
}

/**
 * Pro-rata entera: floor(amount × numerator / denominator), la misma división
 * entera del ledger del servidor (p. ej. valor declarado prorrateado a la
 * cantidad aceptada de un flete). `numerator`/`denominator` son strings de
 * punto fijo `^[0-9]+$`. Lanza `RangeError` con operandos inválidos o
 * denominador cero.
 */
export function prorate(amount: Money, numerator: string, denominator: string): Money {
  if (!MONEY_PATTERN.test(numerator) || !MONEY_PATTERN.test(denominator)) {
    throw new RangeError(
      `Money.prorate: operandos inválidos (${numerator} / ${denominator}); se requiere ^[0-9]+$`,
    )
  }
  const den = BigInt(denominator)
  if (den === 0n) {
    throw new RangeError('Money.prorate: denominador cero')
  }
  return canonical((BigInt(amount) * BigInt(numerator)) / den)
}

/**
 * Aplica puntos básicos con redondeo a suelo: floor(amount × bp / 10000) —
 * la fórmula de las garantías del servidor (p. ej. garantía del transportista
 * sobre el valor declarado). Lanza `RangeError` si `bp` no es un entero seguro
 * en [0, 10000] (rango del schema BasisPoints).
 */
export function applyBasisPoints(amount: Money, bp: number): Money {
  if (!Number.isSafeInteger(bp) || bp < 0 || bp > 10_000) {
    throw new RangeError(
      `Money.applyBasisPoints: bp inválido (${String(bp)}); se requiere entero en [0, 10000]`,
    )
  }
  return canonical((BigInt(amount) * BigInt(bp)) / 10_000n)
}

/**
 * Conversión APROXIMADA a `number`, exclusivamente para geometría de
 * presentación (escalar importes a píxeles en gráficos propios, FAD §15.8).
 * Pierde precisión por encima de 2^53: PROHIBIDA para aritmética de valor,
 * comparación o cualquier dato que vuelva al servidor — para eso están
 * `add`/`subtract`/`compare`. Acepta cualquier magnitud `^[0-9]+$` (Money o
 * cantidades); lanza `RangeError` con entradas fuera de patrón.
 */
export function toApproxNumber(value: string): number {
  if (!MONEY_PATTERN.test(value)) {
    throw new RangeError(
      `Money.toApproxNumber: magnitud inválida (${value}); se requiere ^[0-9]+$`,
    )
  }
  return Number(value)
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
