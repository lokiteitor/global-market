/**
 * network/mappers/page.mapper — paginación por cursor del contrato (FAD §12.8).
 *
 * Toda lista paginada responde `{data: T[], meta}` con `meta.next_cursor`
 * opaco (ausente si no hay más resultados). Este helper único convierte la
 * envoltura en una `Page<T>` para que los módulos `*.api` no repitan el
 * unwrap. El cursor es OPACO: el cliente lo devuelve tal cual en
 * `query.cursor` de la petición siguiente, sin interpretarlo.
 */

import type { RequestSpec, RestClient } from '../rest'

/** Página de una lista por cursor: items + cursor siguiente (`null` = última página). */
export interface Page<TItem> {
  readonly items: readonly TItem[]
  /** Cursor opaco para pedir la página siguiente; `null` si no hay más resultados. */
  readonly nextCursor: string | null
}

/** Ejecuta una petición de lista y proyecta el envelope a `Page` (items + `meta.next_cursor`). */
export async function requestPage<TItem>(
  rest: RestClient,
  spec: RequestSpec,
): Promise<Page<TItem>> {
  const { data, meta } = await rest.request<readonly TItem[]>(spec)
  return { items: data, nextCursor: meta.nextCursor }
}
