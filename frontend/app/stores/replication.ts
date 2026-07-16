/**
 * stores/replication.ts — soporte común de estado replicado (FAD §12/§20).
 *
 * Colección normalizada byId alimentada por varias rooms WS a la vez (p. ej.
 * un vehículo puede llegar por `corp:` y por `viewport:`). Semántica:
 *   - snapshot  → REEMPLAZA el subárbol aportado por esa room (resync por
 *     construcción tras una reconexión, ws-protocol.md §6);
 *   - upsert    → entidad completa, aplicación idempotente;
 *   - remove    → tolerante (borrar lo inexistente no es error).
 * Ninguna función decide reglas de negocio: solo espeja lo recibido (P1).
 */

export interface ReplicatedCollection<T extends { id: string }> {
  byId: Record<string, T>
  /** ids aportados por cada origen (room o 'patch') — base del reemplazo por subárbol. */
  idsBySource: Record<string, string[]>
}

/** Origen sintético de las entidades llegadas por patch fuera de un snapshot. */
export const PATCH_SOURCE = 'patch'

export function emptyCollection<T extends { id: string }>(): ReplicatedCollection<T> {
  return { byId: {}, idsBySource: {} }
}

/** Snapshot de una room: reemplaza el subárbol de ese origen. Idempotente. */
export function applySnapshotTo<T extends { id: string }>(
  col: ReplicatedCollection<T>,
  source: string,
  items: readonly T[]
): void {
  const nextIds = items.map((i) => i.id)
  const nextSet = new Set(nextIds)
  const prevIds = col.idsBySource[source] ?? []

  for (const id of prevIds) {
    if (nextSet.has(id)) continue
    const stillReferenced = Object.entries(col.idsBySource).some(([s, ids]) => s !== source && ids.includes(id))
    if (!stillReferenced) delete col.byId[id]
  }

  for (const item of items) col.byId[item.id] = item
  col.idsBySource[source] = nextIds
}

/** Upsert por patch (entidad completa). Idempotente. */
export function upsertTo<T extends { id: string }>(col: ReplicatedCollection<T>, item: T): void {
  col.byId[item.id] = item
  const known = Object.values(col.idsBySource).some((ids) => ids.includes(item.id))
  if (!known) {
    ;(col.idsBySource[PATCH_SOURCE] ??= []).push(item.id)
  }
}

/** Remove por patch. Tolerante e idempotente. */
export function removeFrom<T extends { id: string }>(col: ReplicatedCollection<T>, id: string): void {
  delete col.byId[id]
  for (const source of Object.keys(col.idsBySource)) {
    const ids = col.idsBySource[source]
    if (ids !== undefined) col.idsBySource[source] = ids.filter((x) => x !== id)
  }
}
