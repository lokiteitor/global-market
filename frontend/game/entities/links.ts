/**
 * game/entities/links — renderer de enlaces logísticos (FAD §11.9/§16.7, capa links).
 *
 * Polilínea `Graphics` por enlace (pool). El COLOR expresa la congestión
 * (mandato: verde < 1.2, ámbar < 2, rojo ≥ 2; neutro con el overlay apagado);
 * el PATRÓN del trazo expresa el MODO — identidad permanente del enlace, sin
 * overlay propio: road continuo, rail con travesaños (vía férrea), sea
 * discontinuo. El overlay de congestión alterna el color SIN re-derivar VMs:
 * retraza los Graphics activos.
 */

import type Phaser from 'phaser'

import { mToPx } from '~shared/geometry/grid'
import type { LinkMode } from '~domain/logistics'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { CongestionTier, LinkVM } from '../bridge/vm'
import { ObjectPool } from '../pools'
import type { RenderParent } from './parent'
import type { PointPx2 } from './link-geometry'
import { crossTicksPx, dashSegmentsPx } from './link-geometry'

/** Colores por tier (tokens Sass): success-400 / accent-500 / danger-500. */
const TIER_COLORS: Readonly<Record<CongestionTier, number>> = {
  fluid: 0x57b877,
  busy: 0xd29224,
  jammed: 0xc4504a,
}

/** $color-gray-600 — trazo neutro con el overlay de congestión apagado. */
const NEUTRAL_COLOR = 0x45536b

const LINK_ALPHA = 0.9

/** Grosor del trazo por modo (px de render). */
const WIDTH_BY_MODE: Readonly<Record<LinkMode, number>> = { road: 3, rail: 2, sea: 3 }

/** Rail: travesaños perpendiculares cada 24 px, de 12 px de largo. */
const RAIL_TICK_SPACING_PX = 24
const RAIL_TICK_HALF_PX = 6

/** Sea: trazo discontinuo 10/8 px, más tenue (rutas sobre agua). */
const SEA_DASH_PX = 10
const SEA_GAP_PX = 8
const SEA_ALPHA = 0.75

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
    if (vm.points.length < 2) {
      return
    }
    const color = this.congestionColoring ? TIER_COLORS[vm.congestionTier] : NEUTRAL_COLOR
    const width = WIDTH_BY_MODE[vm.mode]
    const pointsPx: PointPx2[] = vm.points.map(([xM, yM]) => {
      const p = mToPx(xM, yM)
      return [p.xPx, p.yPx]
    })

    switch (vm.mode) {
      case 'sea': {
        gfx.lineStyle(width, color, SEA_ALPHA)
        this.strokeSegments(gfx, dashSegmentsPx(pointsPx, SEA_DASH_PX, SEA_GAP_PX))
        return
      }
      case 'rail': {
        gfx.lineStyle(width, color, LINK_ALPHA)
        this.strokePolyline(gfx, pointsPx)
        this.strokeSegments(gfx, crossTicksPx(pointsPx, RAIL_TICK_SPACING_PX, RAIL_TICK_HALF_PX))
        return
      }
      case 'road': {
        gfx.lineStyle(width, color, LINK_ALPHA)
        this.strokePolyline(gfx, pointsPx)
      }
    }
  }

  private strokePolyline(gfx: Phaser.GameObjects.Graphics, pointsPx: readonly PointPx2[]): void {
    const first = pointsPx[0]
    if (!first) {
      return
    }
    gfx.beginPath()
    gfx.moveTo(first[0], first[1])
    for (let i = 1; i < pointsPx.length; i += 1) {
      const p = pointsPx[i]
      if (p) {
        gfx.lineTo(p[0], p[1])
      }
    }
    gfx.strokePath()
  }

  private strokeSegments(
    gfx: Phaser.GameObjects.Graphics,
    segments: readonly (readonly [number, number, number, number])[],
  ): void {
    gfx.beginPath()
    for (const [x1, y1, x2, y2] of segments) {
      gfx.moveTo(x1, y1)
      gfx.lineTo(x2, y2)
    }
    gfx.strokePath()
  }
}
