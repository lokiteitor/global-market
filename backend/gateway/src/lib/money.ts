// Dinero y stock: BIGINT en la base, strings decimales en JSON — nunca floats.
// Toda aritmética de valor se hace con BigInt.
import { badRequest } from './errors.js';

const INT_RE = /^[0-9]+$/;

/** Parsea un importe/cantidad no negativo serializado como string de dígitos. */
export function parseAmount(value: unknown, field: string): bigint {
  if (typeof value !== 'string' || !INT_RE.test(value)) {
    throw badRequest(`El campo '${field}' debe ser un entero serializado como string de dígitos`);
  }
  return BigInt(value);
}

/** Como parseAmount pero exige > 0. */
export function parsePositiveAmount(value: unknown, field: string): bigint {
  const v = parseAmount(value, field);
  if (v <= 0n) throw badRequest(`El campo '${field}' debe ser mayor que cero`);
  return v;
}

/** Garantía monetaria del 10% fijo (decisión #27), redondeada hacia arriba. */
export function guaranteeTenPct(value: bigint): bigint {
  return (value * 10n + 99n) / 100n;
}

/** Comparación numérica de strings de punto fijo (para filtros min/max). */
export function compareAmounts(a: string, b: string): number {
  const x = BigInt(a);
  const y = BigInt(b);
  return x < y ? -1 : x > y ? 1 : 0;
}
