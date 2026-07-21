/**
 * game/overlays/regions — overlay de regiones (FAD §11.9, capa overlays).
 *
 * Bounds de cada región como borde rectangular + nombre en la esquina. Vive en
 * un Container propio dentro de la capa overlays: alternarlo es visibilidad
 * (`setVisible`), no re-render del mundo. El nombre es dato del servidor, no
 * literal de UI (no pasa por i18n).
 */

import type Phaser from 'phaser'

import { PX_PER_M, mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { RegionVM } from '../bridge/vm'
import { KeyedSet } from '../entities/keyed-set'
import type { RenderParent } from '../entities/parent'

/** $color-gray-200 — borde y nombre de región (discreto sobre el terreno). */
const REGION_COLOR = 0xcdd6e2
const REGION_COLOR_CSS = '#cdd6e2'
const BORDER_WIDTH_PX = 2
const BORDER_ALPHA = 0.45

const NAME_STYLE: Phaser.Types.GameObjects.Text.TextStyle = {
  fontFamily: 'system-ui, sans-serif',
  fontSize: '14px',
  color: REGION_COLOR_CSS,
  stroke: '#0d1117',
  strokeThickness: 3,
}

/** Margen del nombre respecto a la esquina superior izquierda (px de render). */
const NAME_PADDING_PX = 6

export class RegionsOverlay implements EntitySink<RegionVM> {
  private readonly borders: KeyedSet<RegionVM, Phaser.GameObjects.Graphics>
  private readonly names: KeyedSet<RegionVM, Phaser.GameObjects.Text>

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.borders = new KeyedSet<RegionVM, Phaser.GameObjects.Graphics>({
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
      update: (gfx, vm) => {
        gfx.clear()
        gfx.lineStyle(BORDER_WIDTH_PX, REGION_COLOR, BORDER_ALPHA)
        const origin = mToPx(vm.xM, vm.yM)
        gfx.strokeRect(origin.xPx, origin.yPx, vm.wM * PX_PER_M, vm.hM * PX_PER_M)
      },
    })

    this.names = new KeyedSet<RegionVM, Phaser.GameObjects.Text>({
      create: () => {
        const text = scene.add.text(0, 0, '', NAME_STYLE)
        parent.add(text)
        return text
      },
      onAcquire: (text) => text.setActive(true).setVisible(true),
      onRelease: (text) => text.setActive(false).setVisible(false),
      destroy: (text) => {
        text.destroy()
      },
      update: (text, vm) => {
        const origin = mToPx(vm.xM, vm.yM)
        text.setText(vm.name)
        text.setPosition(origin.xPx + NAME_PADDING_PX, origin.yPx + NAME_PADDING_PX)
      },
    })
  }

  apply(diff: VmDiff<RegionVM>): void {
    this.borders.apply(diff)
    this.names.apply(diff)
  }

  count(): number {
    return this.borders.count()
  }

  destroy(): void {
    this.borders.drain()
    this.names.drain()
  }
}
