/**
 * game/entities/points — renderer genérico de entidades PUNTUALES (sprite fijo).
 *
 * Yacimientos y nodos logísticos: un sprite de textura fija centrado en su
 * punto del mundo. Reconciliación por diffs contra pool (KeyedSet).
 */

import type Phaser from 'phaser'

import { mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import { KeyedSet } from './keyed-set'
import type { RenderParent } from './parent'

interface PointVM {
  readonly id: string
  readonly xM: number
  readonly yM: number
}

export class PointSpritesRenderer<VM extends PointVM> implements EntitySink<VM> {
  private readonly set: KeyedSet<VM, Phaser.GameObjects.Sprite>

  constructor(scene: Phaser.Scene, parent: RenderParent, textureKey: string) {
    this.set = new KeyedSet<VM, Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, textureKey)
        parent.add(sprite)
        return sprite
      },
      onAcquire: (sprite) => sprite.setActive(true).setVisible(true),
      onRelease: (sprite) => sprite.setActive(false).setVisible(false),
      destroy: (sprite) => {
        sprite.destroy()
      },
      update: (sprite, vm) => {
        const p = mToPx(vm.xM, vm.yM)
        sprite.setPosition(p.xPx, p.yPx)
      },
    })
  }

  apply(diff: VmDiff<VM>): void {
    this.set.apply(diff)
  }

  count(): number {
    return this.set.count()
  }

  destroy(): void {
    this.set.drain()
  }
}
