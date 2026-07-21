/**
 * game/entities/keyed-set — reconciliación VM → objeto de render vía pool (P8).
 *
 * Pieza común de todos los renderers de entidades: mantiene un objeto de
 * render vivo por id de VM, adquiriéndolo del `ObjectPool` en las altas,
 * actualizándolo en cada upsert y devolviéndolo al pool en las bajas. Nunca
 * new/destroy por frame (FAD §11.8.1/§16.5).
 */

import { ObjectPool } from '../pools'
import type { ObjectPoolOptions } from '../pools'
import type { VmDiff } from '../bridge/diff'

export interface KeyedSetOptions<
  VM extends { readonly id: string },
  T,
> extends ObjectPoolOptions<T> {
  /** Aplica el VM al objeto de render (posición, textura, tinte…). */
  readonly update: (item: T, vm: VM) => void
}

export class KeyedSet<VM extends { readonly id: string }, T> {
  private readonly pool: ObjectPool<T>
  private readonly active = new Map<string, T>()

  constructor(private readonly options: KeyedSetOptions<VM, T>) {
    this.pool = new ObjectPool<T>(options)
  }

  apply(diff: VmDiff<VM>): void {
    for (const id of diff.removes) {
      const item = this.active.get(id)
      if (item !== undefined) {
        this.active.delete(id)
        this.pool.release(item)
      }
    }
    for (const vm of diff.upserts) {
      let item = this.active.get(vm.id)
      if (item === undefined) {
        item = this.pool.acquire()
        this.active.set(vm.id, item)
      }
      this.options.update(item, vm)
    }
  }

  get(id: string): T | null {
    return this.active.get(id) ?? null
  }

  /** Recorre los objetos activos (re-estilados globales, p. ej. overlays). */
  forEach(callback: (item: T, id: string) => void): void {
    for (const [id, item] of this.active) {
      callback(item, id)
    }
  }

  count(): number {
    return this.active.size
  }

  /** Libera todo y destruye los objetos del pool (shutdown). */
  drain(): void {
    this.active.clear()
    this.pool.drain()
  }
}
