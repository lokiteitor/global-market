/**
 * shared/result — Result<T, E> discriminado (kernel framework-agnostic, FAD §9.2).
 *
 * Tipo de retorno estándar para operaciones falibles del kernel y de los
 * mappers de la capa de red: hace el error parte del contrato de tipos (P9/P10)
 * en lugar de depender de excepciones no declaradas.
 */

export interface Ok<T> {
  readonly ok: true
  readonly value: T
}

export interface Err<E> {
  readonly ok: false
  readonly error: E
}

export type Result<T, E> = Ok<T> | Err<E>

/** Construye un resultado de éxito. */
export function ok<T>(value: T): Ok<T> {
  return { ok: true, value }
}

/** Construye un resultado de error. */
export function err<E>(error: E): Err<E> {
  return { ok: false, error }
}

/** Guarda de tipo: el resultado es éxito. */
export function isOk<T, E>(result: Result<T, E>): result is Ok<T> {
  return result.ok
}

/** Guarda de tipo: el resultado es error. */
export function isErr<T, E>(result: Result<T, E>): result is Err<E> {
  return !result.ok
}

/** Transforma el valor de éxito; propaga el error sin tocarlo. */
export function map<T, U, E>(result: Result<T, E>, fn: (value: T) => U): Result<U, E> {
  return result.ok ? ok(fn(result.value)) : result
}

/** Devuelve el valor de éxito o `fallback` si es error. */
export function unwrapOr<T, E>(result: Result<T, E>, fallback: T): T {
  return result.ok ? result.value : fallback
}
