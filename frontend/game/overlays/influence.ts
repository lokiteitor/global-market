/**
 * game/overlays/influence — overlay de influencia de ciudades (FAD §11.8.3/§11.9).
 *
 * Círculo translúcido con `influence_radius_m` de cada ciudad visible
 * (textura tx-influence escalada; el alpha va horneado en la textura). Vive en
 * un Container propio dentro de la capa overlays (toggle por visibilidad).
 */

import type Phaser from 'phaser'

import { PX_PER_M, mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { CityVM } from '../bridge/vm'
import { KeyedSet } from '../entities/keyed-set'
import type { RenderParent } from '../entities/parent'
import { EXTRA_TEXTURES } from '../entities/textures-extra'

export class InfluenceOverlay implements EntitySink<CityVM> {
  private readonly set: KeyedSet<CityVM, Phaser.GameObjects.Sprite>

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.set = new KeyedSet<CityVM, Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, EXTRA_TEXTURES.influence)
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
        const diameterPx = Math.max(2, vm.influenceRadiusM * 2 * PX_PER_M)
        sprite.setPosition(p.xPx, p.yPx)
        sprite.setDisplaySize(diameterPx, diameterPx)
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
