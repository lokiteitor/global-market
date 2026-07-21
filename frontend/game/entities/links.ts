/**
 * game/entities/links — renderer de enlaces logísticos (FAD §11.9, capa links).
 *
 * Polilínea `Graphics` por enlace (pool), coloreada por tier de congestión
 * (mandato: verde < 1.2, ámbar < 2, rojo ≥ 2). El overlay de congestión
 * alterna entre color por tier y color neutro SIN re-derivar VMs: retraza los
 * Graphics activos (tinte de capa, no re-render del mundo).
 */

import type Phaser from 'phaser'

import { mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { CongestionTier, LinkVM } from '../bridge/vm'
import { ObjectPool } from '../pools'
import type { RenderParent } from './parent'

/** Colores por tier (tokens Sass): success-400 / accent-500 / danger-500. */
const TIER_COLORS: Readonly<Record<CongestionTier, number>> = {
  fluid: 0x57b877,
  busy: 0xd29224,
  jammed: 0xc4504a,
}

/** $color-gray-600 — trazo neutro con el overlay de congestión apagado. */
const NEUTRAL_COLOR = 0x45536b

const LINK_WIDTH_PX = 3
const LINK_ALPHA = 0.9

export class LinksRenderer implements EntitySink<LinkVM> {
  private readonly pool: ObjectPool<Phaser.GameObjects.Graphics>
  private readonly active = new Map<string, { gfx: Phaser.GameObjects.Graphics; vm: LinkVM }>()
  private congestionColoring = true

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.pool = new ObjectPool<Phaser.GameObjects.Graphics>({
      create: () => {
        const gfx = scene.add.graphics()
        parent.add(gfx)
        return gfx
      },
      onAcquire: (gfx) => gfx.setActive(true).setVisible(true),
      onRelease: (gfx) => {
        gfx.clear()
        gfx.setActive(false).setVisible(false)
      },
      destroy: (gfx) => {
        gfx.destroy()
      },
    })
  }

  apply(diff: VmDiff<LinkVM>): void {
    for (const id of diff.removes) {
      const entry = this.active.get(id)
      if (entry !== undefined) {
        this.active.delete(id)
        this.pool.release(entry.gfx)
      }
    }
    for (const vm of diff.upserts) {
      let entry = this.active.get(vm.id)
      if (entry === undefined) {
        entry = { gfx: this.pool.acquire(), vm }
        this.active.set(vm.id, entry)
      } else {
        entry.vm = vm
      }
      this.draw(entry.gfx, vm)
    }
  }

  /** Overlay de congestión: color por tier (on) o neutro (off). Retraza lo activo. */
  setCongestionColoring(on: boolean): void {
    if (this.congestionColoring === on) {
      return
    }
    this.congestionColoring = on
    for (const { gfx, vm } of this.active.values()) {
      this.draw(gfx, vm)
    }
  }

  count(): number {
    return this.active.size
  }

  destroy(): void {
    this.active.clear()
    this.pool.drain()
  }

  private draw(gfx: Phaser.GameObjects.Graphics, vm: LinkVM): void {
    gfx.clear()
    const first = vm.points[0]
    if (!first || vm.points.length < 2) {
      return
    }
    const color = this.congestionColoring ? TIER_COLORS[vm.congestionTier] : NEUTRAL_COLOR
    gfx.lineStyle(LINK_WIDTH_PX, color, LINK_ALPHA)
    gfx.beginPath()
    const start = mToPx(first[0], first[1])
    gfx.moveTo(start.xPx, start.yPx)
    for (let i = 1; i < vm.points.length; i += 1) {
      const point = vm.points[i]
      if (!point) {
        continue
      }
      const p = mToPx(point[0], point[1])
      gfx.lineTo(p.xPx, p.yPx)
    }
    gfx.strokePath()
  }
}
