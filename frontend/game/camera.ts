/**
 * game/camera — CameraController sobre la cámara de Phaser (FAD §17, ADR-019).
 *
 * Encapsula pan (arrastre con botón izquierdo en modo pan / botón medio
 * siempre), zoom a la rueda HACIA EL CURSOR con límites, bounds del mundo e
 * inercia ligera. La matemática es la de camera-math.ts (pura y testeada);
 * aquí solo el cableado de input y la aplicación al objeto Camera de Phaser.
 *
 * El estado propio `{centerX, centerY, zoom}` es la verdad; la cámara de
 * Phaser es un espejo (no se usa `setBounds` de Phaser: el clamping propio es
 * el que entiende el zoom-al-cursor). Las animaciones/inercia van en
 * wall-clock (RAF), no en sim-time: mover la cámara no es un evento de
 * dominio (FAD §17.5).
 */

import type Phaser from 'phaser'

import type { PointM, RectPx } from '~shared/geometry/grid'
import { PX_PER_M, WORLD_SIZE_PX, mToPx, pxToM } from '~shared/geometry/grid'

import type { CameraState } from './camera-math'
import {
  clampCenter,
  clampZoom,
  decayVelocity,
  panBy,
  viewRect,
  wheelZoomFactor,
  zoomAtCursor,
} from './camera-math'

/** Rectángulo del viewport en METROS de mundo (para bridge/consultas de dominio). */
export interface RectM {
  readonly xM: number
  readonly yM: number
  readonly widthM: number
  readonly heightM: number
}

/**
 * Modo de interacción del botón izquierdo: en `pan` arrastra la cámara; en
 * `select` queda libre para selección/rubber-band (fase UI). El botón medio
 * siempre panea.
 */
export type InteractionMode = 'pan' | 'select'

export interface CameraControllerOptions {
  readonly scene: Phaser.Scene
  /** Notificado tras cada cambio de vista (alimenta chunking/culling/bridge). */
  readonly onViewChanged?: (viewPx: RectPx) => void
  /** Inercia ligera al soltar el arrastre (respetar reduced-motion en la fase UI). */
  readonly inertia?: boolean
}

export class CameraController {
  private state: CameraState
  private dragging = false
  private lastPointer = { x: 0, y: 0 }
  private lastMoveAtMs = 0
  private velocity = { x: 0, y: 0 }
  private readonly camera: Phaser.Cameras.Scene2D.Camera
  private readonly detach: () => void

  constructor(private readonly options: CameraControllerOptions) {
    this.camera = options.scene.cameras.main
    this.state = clampCenter(
      { centerX: WORLD_SIZE_PX / 2, centerY: WORLD_SIZE_PX / 2, zoom: 1 },
      this.viewport(),
    )
    this.apply()

    const input = options.scene.input
    const onDown = (pointer: Phaser.Input.Pointer): void => {
      this.onPointerDown(pointer)
    }
    const onMove = (pointer: Phaser.Input.Pointer): void => {
      this.onPointerMove(pointer)
    }
    const onUp = (): void => {
      this.onPointerUp()
    }
    const onWheel = (
      pointer: Phaser.Input.Pointer,
      _over: unknown,
      _deltaX: number,
      deltaY: number,
    ): void => {
      this.onWheel(pointer, deltaY)
    }
    input.on('pointerdown', onDown)
    input.on('pointermove', onMove)
    input.on('pointerup', onUp)
    input.on('pointerupoutside', onUp)
    input.on('wheel', onWheel)
    this.detach = () => {
      input.off('pointerdown', onDown)
      input.off('pointermove', onMove)
      input.off('pointerup', onUp)
      input.off('pointerupoutside', onUp)
      input.off('wheel', onWheel)
    }
  }

  interactionMode: InteractionMode = 'pan'

  /** ¿Hay un arrastre de cámara en curso? (la UI lo usa para no "seleccionar" al soltar). */
  isDragging(): boolean {
    return this.dragging
  }

  /** Centra la cámara en un punto del mundo en METROS (clampeado a bounds). */
  centerOnM(xM: number, yM: number): void {
    const p = mToPx(xM, yM)
    this.setState({ centerX: p.xPx, centerY: p.yPx, zoom: this.state.zoom })
  }

  zoom(): number {
    return this.state.zoom
  }

  /** Zoom absoluto manteniendo el centro (botones +/− del HUD). */
  setZoom(zoom: number): void {
    this.setState({ ...this.state, zoom: clampZoom(zoom) })
  }

  /** Viewport actual en píxeles de render (para chunking/culling). */
  viewRectPx(): RectPx {
    return viewRect(this.state, this.viewport())
  }

  /** Viewport actual en metros de mundo (para el bridge y consultas de dominio). */
  viewRectM(): RectM {
    const px = this.viewRectPx()
    const origin = pxToM(px.x, px.y)
    return {
      xM: origin.xM,
      yM: origin.yM,
      widthM: px.width / PX_PER_M,
      heightM: px.height / PX_PER_M,
    }
  }

  /** Punto de pantalla (px de viewport) → metros de mundo (picking). */
  screenToM(screenX: number, screenY: number): PointM {
    const vp = this.viewport()
    const worldXPx = this.state.centerX + (screenX - vp.width / 2) / this.state.zoom
    const worldYPx = this.state.centerY + (screenY - vp.height / 2) / this.state.zoom
    return pxToM(worldXPx, worldYPx)
  }

  /** Tick del game-loop: aplica la inercia (wall-clock, FAD §17.5). */
  update(deltaMs: number): void {
    if (this.dragging || (this.velocity.x === 0 && this.velocity.y === 0)) {
      return
    }
    this.setState(panBy(this.state, this.velocity.x * deltaMs, this.velocity.y * deltaMs))
    this.velocity = {
      x: decayVelocity(this.velocity.x, deltaMs),
      y: decayVelocity(this.velocity.y, deltaMs),
    }
  }

  /** Re-clampea y renotifica (tras un resize del canvas). */
  refresh(): void {
    this.setState(this.state)
  }

  destroy(): void {
    this.detach()
  }

  private viewport(): { width: number; height: number } {
    return { width: this.camera.width, height: this.camera.height }
  }

  private setState(next: CameraState): void {
    this.state = clampCenter(next, this.viewport())
    this.apply()
    this.options.onViewChanged?.(this.viewRectPx())
  }

  private apply(): void {
    this.camera.setZoom(this.state.zoom)
    this.camera.centerOn(this.state.centerX, this.state.centerY)
  }

  private onPointerDown(pointer: Phaser.Input.Pointer): void {
    const panButton =
      pointer.middleButtonDown() || (this.interactionMode === 'pan' && pointer.leftButtonDown())
    if (!panButton) {
      return
    }
    this.dragging = true
    this.velocity = { x: 0, y: 0 }
    this.lastPointer = { x: pointer.x, y: pointer.y }
    this.lastMoveAtMs = performance.now()
  }

  private onPointerMove(pointer: Phaser.Input.Pointer): void {
    if (!this.dragging) {
      return
    }
    const dx = pointer.x - this.lastPointer.x
    const dy = pointer.y - this.lastPointer.y
    this.lastPointer = { x: pointer.x, y: pointer.y }
    const now = performance.now()
    const dt = now - this.lastMoveAtMs
    this.lastMoveAtMs = now
    if ((this.options.inertia ?? true) && dt > 0) {
      // Velocidad de pantalla (px/ms) para la inercia al soltar.
      this.velocity = { x: dx / dt, y: dy / dt }
    }
    this.setState(panBy(this.state, dx, dy))
  }

  private onPointerUp(): void {
    if (!this.dragging) {
      return
    }
    this.dragging = false
    if (!(this.options.inertia ?? true)) {
      this.velocity = { x: 0, y: 0 }
    }
  }

  private onWheel(pointer: Phaser.Input.Pointer, deltaY: number): void {
    this.velocity = { x: 0, y: 0 }
    const factor = wheelZoomFactor(deltaY)
    this.setState(
      zoomAtCursor(this.state, pointer.x, pointer.y, this.viewport(), this.state.zoom * factor),
    )
  }
}
