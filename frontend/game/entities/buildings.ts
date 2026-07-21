/**
 * game/entities/buildings — renderer de edificios (FAD §11.8.2, ADR-019).
 *
 * Rectángulo (sprite escalado al bbox del footprint) con TEXTURA por
 * tipo/estado: mina con textura propia cuando opera; el resto, cuadrado
 * tintado por estado del contrato (game/textures.ts). Los edificios PROPIOS
 * llevan un borde ámbar (sprite de anillo, pool aparte) — distinción
 * observable vs comandable a golpe de vista (FAD §5.3).
 */

import type Phaser from 'phaser'

import { PX_PER_M, mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { BuildingVM } from '../bridge/vm'
import { ObjectPool } from '../pools'
import { TEXTURES, buildingTextureKey } from '../textures'
import { KeyedSet } from './keyed-set'
import type { RenderParent } from './parent'
import { EXTRA_TEXTURES } from './textures-extra'

/** Margen del borde de propiedad alrededor del bbox (px de render). */
const OWN_BORDER_MARGIN_PX = 4

/** Textura por tipo/estado: la mina operativa tiene arte propio; el resto, por estado. */
export function buildingTexture(vm: BuildingVM): string {
  if (vm.typeCode === 'MINE' && vm.status === 'operational') {
    return TEXTURES.mine
  }
  return buildingTextureKey(vm.status)
}

export class BuildingsRenderer implements EntitySink<BuildingVM> {
  private readonly sprites: KeyedSet<BuildingVM, Phaser.GameObjects.Sprite>
  private readonly borderPool: ObjectPool<Phaser.GameObjects.Sprite>
  private readonly borders = new Map<string, Phaser.GameObjects.Sprite>()

  constructor(scene: Phaser.Scene, spriteParent: RenderParent, borderParent: RenderParent) {
    this.sprites = new KeyedSet<BuildingVM, Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, buildingTextureKey('operational'))
        spriteParent.add(sprite)
        return sprite
      },
      onAcquire: (sprite) => sprite.setActive(true).setVisible(true),
      onRelease: (sprite) => sprite.setActive(false).setVisible(false),
      destroy: (sprite) => {
        sprite.destroy()
      },
      update: (sprite, vm) => {
        const center = mToPx(vm.xM + vm.wM / 2, vm.yM + vm.hM / 2)
        sprite.setTexture(buildingTexture(vm))
        sprite.setPosition(center.xPx, center.yPx)
        sprite.setDisplaySize(vm.wM * PX_PER_M, vm.hM * PX_PER_M)
      },
    })

    this.borderPool = new ObjectPool<Phaser.GameObjects.Sprite>({
      create: () => {
        const sprite = scene.add.sprite(0, 0, EXTRA_TEXTURES.ownBorder)
        borderParent.add(sprite)
        return sprite
      },
      onAcquire: (sprite) => sprite.setActive(true).setVisible(true),
      onRelease: (sprite) => sprite.setActive(false).setVisible(false),
      destroy: (sprite) => {
        sprite.destroy()
      },
    })
  }

  apply(diff: VmDiff<BuildingVM>): void {
    this.sprites.apply(diff)
    for (const id of diff.removes) {
      this.releaseBorder(id)
    }
    for (const vm of diff.upserts) {
      if (vm.own) {
        let border = this.borders.get(vm.id)
        if (border === undefined) {
          border = this.borderPool.acquire()
          this.borders.set(vm.id, border)
        }
        const center = mToPx(vm.xM + vm.wM / 2, vm.yM + vm.hM / 2)
        border.setPosition(center.xPx, center.yPx)
        border.setDisplaySize(
          vm.wM * PX_PER_M + OWN_BORDER_MARGIN_PX,
          vm.hM * PX_PER_M + OWN_BORDER_MARGIN_PX,
        )
      } else {
        this.releaseBorder(vm.id)
      }
    }
  }

  count(): number {
    return this.sprites.count()
  }

  destroy(): void {
    this.borders.clear()
    this.borderPool.drain()
    this.sprites.drain()
  }

  private releaseBorder(id: string): void {
    const border = this.borders.get(id)
    if (border !== undefined) {
      this.borders.delete(id)
      this.borderPool.release(border)
    }
  }
}
