/**
 * kernel/result.ts — Result<T, E>: retorno explícito de éxito/error sin
 * excepciones para las fronteras internas del cliente (P10 — explícito
 * sobre implícito).
 */

export type Result<T, E> = { readonly ok: true; readonly value: T } | { readonly ok: false; readonly error: E }

export function ok<T>(value: T): { ok: true; value: T } {
  return { ok: true, value }
}

export function err<E>(error: E): { ok: false; error: E } {
  return { ok: false, error }
}

/** Desenvuelve un Result o lanza; para código que ya validó la entrada. */
export function unwrap<T, E>(result: Result<T, E>): T {
  if (result.ok) return result.value
  throw new Error(`unwrap() sobre un Result de error: ${String(result.error)}`)
}
