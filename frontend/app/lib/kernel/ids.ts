/**
 * kernel/ids.ts — identificadores branded (ADR-IMPL-01).
 *
 * El backend usa UUIDv7 sin prefijo por tipo; el namespacing que aportaba ULID
 * se compensa en el cliente con branded types: un Id<'vehicle'> no es asignable
 * donde se espera un Id<'building'> aunque ambos sean strings uuid.
 */

declare const idBrand: unique symbol

/** UUID (v7 en el backend) etiquetado con el tipo de entidad que identifica. */
export type Id<T extends string> = string & { readonly [idBrand]: T }

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function isUuid(value: string): boolean {
  return UUID_RE.test(value)
}

/** Afirmación validada (lanza si no es uuid): frontera de infraestructura. */
export function idOf<T extends string>(value: string): Id<T> {
  if (!isUuid(value)) throw new TypeError(`Id inválido (se esperaba uuid): "${value}"`)
  return value as Id<T>
}
