/**
 * game/scenes/OverlayScene.ts — escena paralela de selección/hover (FAD §11.5).
 *
 * Dibuja los anillos de highlight POR ENCIMA del mundo sin tocar los Game
 * Objects de WorldScene (P4: la selección es estado de presentación). La
 * cámara se sincroniza cada frame con la de WorldScene para que el highlight
 * siga pan/zoom. El estado de selección de DOMINIO vive en ui.store (lo
 * escribe la app al recibir el intent 'world:select'); aquí solo hay eco
 * visual local.
 */
import Phaser from 'phaser'
import { DEFAULT_PALETTE } from '../palette'
import type { WorldPalette } from '../types'

export const OVERLAY_SCENE_KEY = 'overlay'

export interface HighlightTarget {
  kind: string
  id: string
}

interface WorldLike extends Phaser.Scene {
  getEntityHighlight(kind: string, id: string): { x: number; y: number; r: number } | null
}

export class OverlayScene extends Phaser.Scene {
  private gfx: Phaser.GameObjects.Graphics | null = null
  private selection: HighlightTarget | null = null
  private hover: HighlightTarget | null = null
  private palette: WorldPalette = DEFAULT_PALETTE

  constructor() {
    super(OVERLAY_SCENE_KEY)
  }

  create(): void {
    this.gfx = this.add.graphics()
  }

  setPalette(palette: WorldPalette): void {
    this.palette = palette
  }

  setSelection(target: HighlightTarget | null): void {
    this.selection = target
  }

  setHover(target: HighlightTarget | null): void {
    this.hover = target
  }

  override update(): void {
    const gfx = this.gfx
    if (gfx === null) return
    const world = this.scene.get('world') as WorldLike | null
    if (world === null) return

    // Cámara espejo de la del mundo: mismo scroll y zoom.
    const wc = world.cameras.main
    const cam = this.cameras.main
    cam.setZoom(wc.zoom)
    cam.setScroll(wc.scrollX, wc.scrollY)

    gfx.clear()

    if (this.hover !== null && (this.selection === null || this.hover.id !== this.selection.id)) {
      const pos = world.getEntityHighlight(this.hover.kind, this.hover.id)
      if (pos !== null) {
        gfx.lineStyle(1.5 / wc.zoom, this.palette.hover, 0.8)
        gfx.strokeCircle(pos.x, pos.y, pos.r)
      }
    }

    if (this.selection !== null) {
      const pos = world.getEntityHighlight(this.selection.kind, this.selection.id)
      if (pos !== null) {
        gfx.lineStyle(2 / wc.zoom, this.palette.selection, 1)
        gfx.strokeCircle(pos.x, pos.y, pos.r)
        gfx.lineStyle(1 / wc.zoom, this.palette.selection, 0.35)
        gfx.strokeCircle(pos.x, pos.y, pos.r + 4 / wc.zoom)
      }
    }
  }
}
