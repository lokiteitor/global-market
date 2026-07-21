/**
 * game/map/chunk-logic — lógica PURA del streaming de chunks (FAD §16.3/§16.5/§16.8).
 *
 * Separada del objeto Phaser (chunks.ts) para poder testearse sin GPU ni
 * escena: diffing de visibilidad y política de retención LRU son funciones y
 * estructuras deterministas sobre claves de chunk (`chunkKey` de
 * shared/geometry).
 */

import type { ChunkCoord } from '~shared/geometry/grid'
import { chunkKey } from '~shared/geometry/grid'

/** Resultado del diffing: qué chunks entran y cuáles salen del conjunto visible. */
export interface ChunkDiff {
  /** Chunks que deben materializarse/mostrarse (no estaban visibles). */
  readonly shown: readonly ChunkCoord[]
  /** Claves de chunks que dejan de ser visibles (pasan a cached). */
  readonly hidden: readonly string[]
}

/**
 * Diff entre el conjunto visible actual (claves) y el próximo (coordenadas).
 * Determinista: `shown` conserva el orden de `next`; `hidden` el orden de
 * iteración de `prev`.
 */
export function diffChunks(prev: ReadonlySet<string>, next: readonly ChunkCoord[]): ChunkDiff {
  const nextKeys = new Set<string>()
  const shown: ChunkCoord[] = []
  for (const coord of next) {
    const key = chunkKey(coord.cx, coord.cy)
    nextKeys.add(key)
    if (!prev.has(key)) {
      shown.push(coord)
    }
  }
  const hidden: string[] = []
  for (const key of prev) {
    if (!nextKeys.has(key)) {
      hidden.push(key)
    }
  }
  return { shown, hidden }
}

/**
 * Rastreador LRU de chunks materializados (visibles + cached) con capacidad
 * máxima. Los chunks VISIBLES nunca se desalojan (el culling garantiza que no
 * exceden la capacidad en la práctica); al superar la capacidad se desalojan
 * los cached menos recientemente usados.
 *
 * Implementación: `Map` con orden de inserción como orden de recencia
 * (re-insertar = tocar). Puro, sin Phaser.
 */
export class ChunkLru {
  private readonly order = new Map<string, true>()

  constructor(readonly capacity: number) {
    if (!Number.isInteger(capacity) || capacity < 1) {
      throw new RangeError(`ChunkLru: capacidad inválida (${String(capacity)})`)
    }
  }

  /** Marca el chunk como usado ahora (lo crea si no estaba). */
  touch(key: string): void {
    this.order.delete(key)
    this.order.set(key, true)
  }

  /** Elimina el chunk del rastreo (chunk destruido). */
  delete(key: string): void {
    this.order.delete(key)
  }

  has(key: string): boolean {
    return this.order.has(key)
  }

  get size(): number {
    return this.order.size
  }

  /**
   * Claves a desalojar para volver a la capacidad, más antiguas primero,
   * saltándose las visibles (protegidas). Solo PLANIFICA: el llamante destruye
   * los objetos y llama a `delete` por cada una.
   */
  planEviction(visible: ReadonlySet<string>): string[] {
    let excess = this.order.size - this.capacity
    if (excess <= 0) {
      return []
    }
    const evict: string[] = []
    for (const key of this.order.keys()) {
      if (excess <= 0) {
        break
      }
      if (visible.has(key)) {
        continue
      }
      evict.push(key)
      excess -= 1
    }
    return evict
  }
}
