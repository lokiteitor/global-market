/**
 * game/entities/cities — renderer de ciudades (FAD §11.8.3).
 *
 * Círculo (textura tx-city) escalado por NIVEL. El nombre va en la capa de
 * etiquetas (labels.ts, con culling por zoom); el radio de influencia en el
 * overlay de influencia (game/overlays/influence.ts).
 */

import type Phaser from 'phaser'

import { mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { CityVM } from '../bridge/vm'
import { cityScale } from '../bridge/vm'
import { TEXTURES } from '../textures'
import { KeyedSet } from './keyed-set'
import type { RenderParent } from './parent'

export class CitiesRenderer implements EntitySink<CityVM> {
  private readonly set: KeyedSet<CityVM, Phaser.GameObjects.Sprite>

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.set = new KeyedSet<CityVM, Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, TEXTURES.city)
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
        sprite.setScale(cityScale(vm.level))
      },
    })
  }

  apply(diff: VmDiff<CityVM>): void {
    this.set.apply(diff)
  }

  count(): number {
    return this.set.count()
  }

  destroy(): void {
    this.set.drain()
  }
}
