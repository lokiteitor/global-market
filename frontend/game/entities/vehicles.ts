/**
 * game/entities/vehicles — renderer de vehículos (FAD §11.7/§11.8.1).
 *
 * Rectángulo orientable (textura tx-vehicle, morro a +X) rotado al VECTOR DEL
 * TRAMO y tintado por estado (broken rojo, sealed apagado). La posición llega
 * YA extrapolada en el VM (el bridge la deriva cada frame con
 * domain/kinematics + SimClock); aquí solo se aplica al sprite.
 */

import type Phaser from 'phaser'

import { mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { VehicleVM } from '../bridge/vm'
import { vehicleTint } from '../bridge/vm'
import { TEXTURES } from '../textures'
import { KeyedSet } from './keyed-set'
import type { RenderParent } from './parent'

export class VehiclesRenderer implements EntitySink<VehicleVM> {
  private readonly set: KeyedSet<VehicleVM, Phaser.GameObjects.Sprite>

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.set = new KeyedSet<VehicleVM, Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, TEXTURES.vehicle)
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
        sprite.setRotation(vm.angleRad)
        const tint = vehicleTint(vm.status)
        if (tint === null) {
          sprite.clearTint()
        } else {
          sprite.setTint(tint)
        }
      },
    })
  }

  apply(diff: VmDiff<VehicleVM>): void {
    this.set.apply(diff)
  }

  count(): number {
    return this.set.count()
  }

  destroy(): void {
    this.set.drain()
  }
}
