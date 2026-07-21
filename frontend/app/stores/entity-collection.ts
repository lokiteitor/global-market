/**
 * app/stores/entity-collection — colección normalizada reutilizable (FAD §20.3/§20.4).
 *
 * Patrón único de estado replicado por entidad: `byId: Record<uuid, Entidad>`
 * inmutable (shallowRef + reemplazo de objeto: las entidades de dominio son
 * snapshots congelados del servidor, no proxies profundos) con la tríada de
 * escritura IDEMPOTENTE:
 *
 * - `applySnapshot(list)`: REEMPLAZA el subárbol completo (converge tras
 *   bootstrap/resync — no fusiona ciegamente, FAD §20.4).
 * - `applyOne(entity)`: upsert de una entidad (respuesta de comando o delta WS).
 * - `remove(id)`: baja; no-op si no existe.
 *
 * Variantes para pulls ACOTADOS del contrato (por región, por edificio…):
 * - `applyScopedSnapshot(scope, list)`: reemplaza SOLO el subárbol que cumple
 *   `scope` (p. ej. los lotes de UN edificio) y conserva el resto.
 * - `applyMany(list)`: upsert en lote sin eliminar nada (refresco suave).
 *
 * Reaplicar el mismo snapshot/entidad/baja deja el estado equivalente
 * (idempotencia P6 — el WS entrega at-least-once). Los índices derivados
 * (`indexBy`, `uniqueIndexBy`) son computed memoizados: se recalculan solo al
 * cambiar `byId` y no pueden desincronizarse del estado (P2).
 *
 * NO es una store: es el ladrillo con el que las stores por bounded context
 * componen sus colecciones. Sin lógica de negocio.
 */

import type { ComputedRef, ShallowRef } from 'vue'
import { computed, shallowRef } from 'vue'

export interface EntityCollection<Id extends string, T> {
  /** Estado normalizado: fuente única por entidad (solo lectura fuera de la tríada). */
  readonly byId: ShallowRef<Readonly<Record<Id, T>>>
  readonly ids: ComputedRef<readonly Id[]>
  readonly list: ComputedRef<readonly T[]>
  readonly count: ComputedRef<number>
  /** Extractor de id (lo usan los índices derivados). */
  readonly idOf: (entity: T) => Id
  get(id: Id | null | undefined): T | null
  has(id: Id): boolean
  applySnapshot(entities: readonly T[]): void
  applyScopedSnapshot(scope: (entity: T) => boolean, entities: readonly T[]): void
  applyMany(entities: readonly T[]): void
  applyOne(entity: T): void
  remove(id: Id): void
  clear(): void
}

export function createEntityCollection<Id extends string, T>(
  idOf: (entity: T) => Id,
): EntityCollection<Id, T> {
  const byId = shallowRef<Readonly<Record<Id, T>>>(emptyRecord<Id, T>())

  const ids = computed(() => Object.keys(byId.value) as Id[])
  const list = computed(() => Object.values(byId.value) as T[])
  const count = computed(() => ids.value.length)

  function get(id: Id | null | undefined): T | null {
    if (id === null || id === undefined) {
      return null
    }
    return byId.value[id] ?? null
  }

  function has(id: Id): boolean {
    return byId.value[id] !== undefined
  }

  function applySnapshot(entities: readonly T[]): void {
    const next = emptyRecord<Id, T>()
    for (const entity of entities) {
      next[idOf(entity)] = entity
    }
    byId.value = next
  }

  function applyScopedSnapshot(scope: (entity: T) => boolean, entities: readonly T[]): void {
    const next = emptyRecord<Id, T>()
    for (const entity of list.value) {
      if (!scope(entity)) {
        next[idOf(entity)] = entity
      }
    }
    for (const entity of entities) {
      if (scope(entity)) {
        next[idOf(entity)] = entity
      }
    }
    byId.value = next
  }

  function applyMany(entities: readonly T[]): void {
    if (entities.length === 0) {
      return
    }
    const next = { ...byId.value } as Record<Id, T>
    for (const entity of entities) {
      next[idOf(entity)] = entity
    }
    byId.value = next
  }

  function applyOne(entity: T): void {
    byId.value = { ...byId.value, [idOf(entity)]: entity }
  }

  function remove(id: Id): void {
    if (byId.value[id] === undefined) {
      return
    }
    const next = emptyRecord<Id, T>()
    for (const entity of list.value) {
      const entityId = idOf(entity)
      if (entityId !== id) {
        next[entityId] = entity
      }
    }
    byId.value = next
  }

  function clear(): void {
    byId.value = emptyRecord<Id, T>()
  }

  return {
    byId,
    ids,
    list,
    count,
    idOf,
    get,
    has,
    applySnapshot,
    applyScopedSnapshot,
    applyMany,
    applyOne,
    remove,
    clear,
  }
}

/**
 * Índice multivaluado derivado: agrupa los ids por la clave que devuelva
 * `keyOf` (`null` = fuera del índice). Orden estable: el de inserción en `byId`.
 */
export function indexBy<Id extends string, T, K extends string>(
  collection: EntityCollection<Id, T>,
  keyOf: (entity: T) => K | null,
): ComputedRef<Readonly<Record<K, readonly Id[]>>> {
  return computed(() => {
    const index = emptyRecord<K, Id[]>()
    for (const entity of collection.list.value) {
      const key = keyOf(entity)
      if (key === null) {
        continue
      }
      const bucket = index[key] ?? (index[key] = [])
      bucket.push(collection.idOf(entity))
    }
    return index
  })
}

/**
 * Índice univaluado derivado (p. ej. `byCode` de catálogos). Si dos entidades
 * comparten clave, gana la última en orden de inserción (los catálogos del
 * contrato garantizan unicidad; esto solo define el desempate).
 */
export function uniqueIndexBy<Id extends string, T, K extends string>(
  collection: EntityCollection<Id, T>,
  keyOf: (entity: T) => K | null,
): ComputedRef<Readonly<Record<K, Id>>> {
  return computed(() => {
    const index = emptyRecord<K, Id>()
    for (const entity of collection.list.value) {
      const key = keyOf(entity)
      if (key === null) {
        continue
      }
      index[key] = collection.idOf(entity)
    }
    return index
  })
}

function emptyRecord<K extends string, V>(): Record<K, V> {
  return Object.create(null) as Record<K, V>
}
