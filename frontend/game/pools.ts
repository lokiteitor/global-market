/**
 * game/pools — pooling genérico de objetos de render (FAD §11.8.1/§16.5, P8).
 *
 * Las entidades visibles se reconcilian creando/liberando desde pools, nunca
 * con new/destroy por frame. El pool es GENÉRICO y puro (testeable sin GPU);
 * `createSpritePool` lo especializa para sprites de Phaser por clave de
 * textura (Phaser solo como tipos: los objetos los crea la `scene` inyectada).
 */

import type Phaser from 'phaser'

export interface PoolCounters {
  /** Objetos construidos en total (vivos, en uso o libres). */
  readonly created: number
  /** Objetos actualmente adquiridos. */
  readonly inUse: number
  /** Objetos libres esperando en el pool. */
  readonly free: number
}

export interface ObjectPoolOptions<T> {
  /** Construye un objeto nuevo (solo cuando el pool está vacío). */
  readonly create: () => T
  /** Prepara un objeto al adquirirlo (mostrar/activar). */
  readonly onAcquire?: (item: T) => void
  /** Aparca un objeto al liberarlo (ocultar/desactivar). */
  readonly onRelease?: (item: T) => void
  /** Destruye un objeto real (al vaciar el pool en shutdown). */
  readonly destroy?: (item: T) => void
}

/**
 * Pool genérico acquire/release con prewarm y contadores. La doble liberación
 * y la liberación de objetos ajenos son bugs de reconciliación: fallan alto
 * (Error) en lugar de corromper los contadores en silencio.
 */
export class ObjectPool<T> {
  private readonly freeItems: T[] = []
  private readonly inUseItems = new Set<T>()
  private createdCount = 0

  constructor(private readonly options: ObjectPoolOptions<T>) {}

  /** Crea `count` objetos por adelantado (evita hipos de GC en ráfagas). */
  prewarm(count: number): void {
    for (let i = 0; i < count; i += 1) {
      const item = this.options.create()
      this.createdCount += 1
      this.options.onRelease?.(item)
      this.freeItems.push(item)
    }
  }

  acquire(): T {
    let item = this.freeItems.pop()
    if (item === undefined) {
      item = this.options.create()
      this.createdCount += 1
    }
    this.inUseItems.add(item)
    this.options.onAcquire?.(item)
    return item
  }

  release(item: T): void {
    if (!this.inUseItems.delete(item)) {
      throw new Error('ObjectPool.release: objeto no adquirido de este pool (o doble release)')
    }
    this.options.onRelease?.(item)
    this.freeItems.push(item)
  }

  /** Libera todo lo adquirido y destruye todos los objetos (shutdown de escena). */
  drain(): void {
    for (const item of this.inUseItems) {
      this.options.onRelease?.(item)
      this.freeItems.push(item)
    }
    this.inUseItems.clear()
    for (const item of this.freeItems) {
      this.options.destroy?.(item)
    }
    this.freeItems.length = 0
    this.createdCount = 0
  }

  counters(): PoolCounters {
    return {
      created: this.createdCount,
      inUse: this.inUseItems.size,
      free: this.freeItems.length,
    }
  }
}

/**
 * Pool de sprites por clave de textura, añadidos a una capa fija (el orden de
 * capas ES el z-order, ADR-019). Los sprites liberados quedan invisibles e
 * inactivos, listos para reutilizarse.
 */
export function createSpritePool(
  scene: Phaser.Scene,
  textureKey: string,
  layer: Phaser.GameObjects.Layer,
): ObjectPool<Phaser.GameObjects.Sprite> {
  return new ObjectPool<Phaser.GameObjects.Sprite>({
    create: () => {
      const sprite = scene.add.sprite(0, 0, textureKey)
      layer.add(sprite)
      return sprite
    },
    onAcquire: (sprite) => {
      sprite.setActive(true).setVisible(true)
    },
    onRelease: (sprite) => {
      sprite.setActive(false).setVisible(false)
    },
    destroy: (sprite) => {
      sprite.destroy()
    },
  })
}
