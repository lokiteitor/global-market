/**
 * game/entities/labels — etiquetas ancladas al mundo (FAD §11.4/§15.7, capa labels).
 *
 * POCAS por diseño (mandato): solo nombres de CIUDAD, con pool de
 * `Phaser.GameObjects.Text` y CULLING POR ZOOM (labelsVisibleAtZoom: por
 * debajo del umbral las etiquetas se ocultan sin liberar el pool — alternar
 * zoom no recrea objetos). El texto es DATO del servidor (nombre de la
 * ciudad), no literal de UI: no pasa por shared/i18n.
 */

import type Phaser from 'phaser'

import { PX_PER_M, mToPx } from '~shared/geometry/grid'

import type { EntitySink } from '../bridge/bridge'
import type { VmDiff } from '../bridge/diff'
import type { CityVM } from '../bridge/vm'
import { cityRadiusM, labelsVisibleAtZoom } from '../bridge/vm'
import { KeyedSet } from './keyed-set'
import type { RenderParent } from './parent'

/** Estilo del texto: paleta de tokens ($color-gray-100 sobre $color-gray-950). */
const LABEL_STYLE: Phaser.Types.GameObjects.Text.TextStyle = {
  fontFamily: 'system-ui, sans-serif',
  fontSize: '13px',
  color: '#e7ecf2',
  stroke: '#0d1117',
  strokeThickness: 3,
}

/** Separación vertical bajo el borde del círculo de la ciudad (px de render). */
const LABEL_GAP_PX = 4

export class CityLabelsRenderer implements EntitySink<CityVM> {
  private readonly set: KeyedSet<CityVM, Phaser.GameObjects.Text>
  private zoomVisible = true

  constructor(scene: Phaser.Scene, parent: RenderParent) {
    this.set = new KeyedSet<CityVM, Phaser.GameObjects.Text>({
      create: () => {
        const text = scene.add.text(0, 0, '', LABEL_STYLE)
        text.setOrigin(0.5, 0)
        parent.add(text)
        return text
      },
      onAcquire: (text) => text.setActive(true),
      onRelease: (text) => text.setActive(false).setVisible(false),
      destroy: (text) => {
        text.destroy()
      },
      update: (text, vm) => {
        const p = mToPx(vm.xM, vm.yM)
        text.setText(vm.name)
        text.setPosition(p.xPx, p.yPx + cityRadiusM(vm.level) * PX_PER_M + LABEL_GAP_PX)
        text.setVisible(this.zoomVisible)
      },
    })
  }

  apply(diff: VmDiff<CityVM>): void {
    this.set.apply(diff)
  }

  /** Culling por zoom (mandato: etiquetas solo a zoom ≥ 0.6). */
  setZoom(zoom: number): void {
    const visible = labelsVisibleAtZoom(zoom)
    if (visible === this.zoomVisible) {
      return
    }
    this.zoomVisible = visible
    this.set.forEach((text) => text.setVisible(visible))
  }

  count(): number {
    return this.set.count()
  }

  destroy(): void {
    this.set.drain()
  }
}
