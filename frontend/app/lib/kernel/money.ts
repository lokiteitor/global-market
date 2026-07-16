/**
 * kernel/money.ts — dinero y stock en punto fijo (C11 / P10 del FAD).
 *
 * El servidor serializa importes y cantidades como STRINGS de enteros de punto
 * fijo (`MoneyAmount`, `StockQty` en specs/openapi.yaml). En el cliente:
 *   - NUNCA se usa parseFloat/Number sobre estos valores;
 *   - toda aritmética pasa por BigInt a través de estos helpers;
 *   - los tipos branded Money/Quantity impiden mezclar importes y cantidades.
 */
import type { Result } from './result'
import { err, ok } from './result'

declare const moneyBrand: unique symbol
declare const quantityBrand: unique symbol

/** Importe monetario en unidades menores (string de entero, puede ser negativo en el ledger). */
export type Money = string & { readonly [moneyBrand]: 'Money' }

/** Cantidad de stock en la unidad mínima del producto (string de entero no negativo). */
export type Quantity = string & { readonly [quantityBrand]: 'Quantity' }

const SIGNED_INT_RE = /^-?[0-9]+$/
const UNSIGNED_INT_RE = /^[0-9]+$/

export const ZERO_MONEY = '0' as Money
export const ZERO_QUANTITY = '0' as Quantity

/** Parseo validado de un importe (acepta signo; normaliza ceros a la izquierda). */
export function parseMoney(raw: string): Result<Money, string> {
  if (!SIGNED_INT_RE.test(raw)) return err(`Money inválido: "${raw}"`)
  return ok(BigInt(raw).toString() as Money)
}

/** Parseo validado de una cantidad (entero no negativo). */
export function parseQuantity(raw: string): Result<Quantity, string> {
  if (!UNSIGNED_INT_RE.test(raw)) return err(`Quantity inválida: "${raw}"`)
  return ok(BigInt(raw).toString() as Quantity)
}

/** Afirmación (lanza): para valores que YA vienen validados del contrato REST/WS. */
export function moneyOf(raw: string): Money {
  const r = parseMoney(raw)
  if (!r.ok) throw new TypeError(r.error)
  return r.value
}

/** Afirmación (lanza): para cantidades que YA vienen validadas del contrato. */
export function quantityOf(raw: string): Quantity {
  const r = parseQuantity(raw)
  if (!r.ok) throw new TypeError(r.error)
  return r.value
}

export function addMoney(a: Money, b: Money): Money {
  return (BigInt(a) + BigInt(b)).toString() as Money
}

export function subMoney(a: Money, b: Money): Money {
  return (BigInt(a) - BigInt(b)).toString() as Money
}

/** Comparación: -1 si a < b, 0 si iguales, 1 si a > b. */
export function cmpMoney(a: Money, b: Money): -1 | 0 | 1 {
  const x = BigInt(a)
  const y = BigInt(b)
  return x < y ? -1 : x > y ? 1 : 0
}

/** Importe total = precio unitario × cantidad (p. ej. valor de una aceptación K de N). */
export function mulByQty(unitPrice: Money, qty: Quantity): Money {
  return (BigInt(unitPrice) * BigInt(qty)).toString() as Money
}

export function negMoney(a: Money): Money {
  return (-BigInt(a)).toString() as Money
}

export function isNegative(a: Money): boolean {
  return BigInt(a) < 0n
}

/**
 * Formatea un importe con separador de miles (por defecto '.', convención es-ES).
 * Money es un entero en unidades menores: no se inventan decimales aquí.
 */
export function formatMoney(amount: Money, thousandsSep = '.'): string {
  const negative = amount.startsWith('-')
  const digits = negative ? amount.slice(1) : amount
  const grouped = digits.replace(/\B(?=(\d{3})+(?!\d))/g, thousandsSep)
  return negative ? `-${grouped}` : grouped
}

/** Formatea una cantidad de stock con separador de miles. */
export function formatQuantity(qty: Quantity, thousandsSep = '.'): string {
  return qty.replace(/\B(?=(\d{3})+(?!\d))/g, thousandsSep)
}
