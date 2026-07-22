/**
 * game/input/controller — modos de interacción, picking y intents (FAD §11.10).
 *
 * Convierte input espacial de Phaser en SELECCIÓN e INTENTS tipados (única
 * salida del render, FAD §11.1). Modos: select (picking + doble clic en
 * vehículo = follow de cámara), pan (cámara), build (ghost + intent) y parcel
 * (rubber-band + intent). El ghost solo valida FORMA (dentro del mundo): la
 * validación real de emplazamiento es del servidor (thin client, FAD §5.2).
 *
 * Objetos propios (marcador de selección, ghost, rubber-band): instancias
 * únicas en la capa effects — no hay N, no necesitan pool.
 */

import type Phaser from 'phaser'

import { PX_PER_M, WORLD_M_PER_TILE, isInsideWorldM, mToPx, mToTile } from '~shared/geometry/grid'

import type { VisibleVms, WorldStateBridge } from '../bridge/bridge'
import { CITY_TEXTURE_PX, cityScale } from '../bridge/vm'
import type { CameraController } from '../camera'
import { TypedEmitter } from '../events'
import { EXTRA_TEXTURES } from '../entities/textures-extra'
import type { WorldScene, WorldSelectEvent } from '../scenes/world-scene'
import { ENTITY_BASE_PX, NODE_PX, TEXTURES, VEHICLE_W_PX } from '../textures'
import { hitTest } from './hit-test'
import type { InputMode, SelectionRef, WorldLiveEvents } from './types'

/** Tolerancia de picking en px de PANTALLA (se traduce a metros según zoom). */
const PICK_TOLERANCE_PX = 6
/** Ventana de doble clic (wall-clock ms; interacción de UI, no dominio). */
const DOUBLE_CLICK_MS = 350
/** Desplazamiento mínimo (px pantalla) para que un arrastre de parcela cuente. */
const MIN_PARCEL_DRAG_PX = 4
/** Margen del marcador de selección alrededor de la entidad (px de render). */
const MARKER_MARGIN_PX = 8
/** $color-accent-400 — trazo del rubber-band de parcela. */
const RUBBER_COLOR = 0xe3ab45

export class InputController {
  readonly events = new TypedEmitter<WorldLiveEvents>()

  private currentMode: InputMode = 'select'
  private currentSelection: SelectionRef | null = null
  private followId: string | null = null
  private lastClick: { id: string; atMs: number } | null = null
  private parcelAnchor: { xM: number; yM: number; screenX: number; screenY: number } | null = null

  private readonly marker: Phaser.GameObjects.Sprite
  private readonly ghost: Phaser.GameObjects.Sprite
  private readonly rubber: Phaser.GameObjects.Graphics
  private readonly offSelect: () => void
  private readonly detachInput: () => void

  constructor(
    private readonly scene: WorldScene,
    private readonly camera: CameraController,
    private readonly bridge: WorldStateBridge,
  ) {
    const effects = scene.layer('effects')
    this.marker = scene.add.sprite(0, 0, TEXTURES.selection).setVisible(false)
    this.ghost = scene.add.sprite(0, 0, TEXTURES.ghost).setVisible(false)
    this.rubber = scene.add.graphics()
    effects.add(this.marker)
    effects.add(this.ghost)
    effects.add(this.rubber)

    this.offSelect = scene.worldEvents.on('select', (event) => {
      this.onWorldClick(event)
    })

    const onMove = (pointer: Phaser.Input.Pointer): void => {
      this.onPointerMove(pointer)
    }
    const onDown = (pointer: Phaser.Input.Pointer): void => {
      this.onPointerDown(pointer)
    }
    const onUp = (pointer: Phaser.Input.Pointer): void => {
      this.onPointerUp(pointer)
    }
    scene.input.on('pointermove', onMove)
    scene.input.on('pointerdown', onDown)
    scene.input.on('pointerup', onUp)
    this.detachInput = () => {
      scene.input.off('pointermove', onMove)
      scene.input.off('pointerdown', onDown)
      scene.input.off('pointerup', onUp)
    }

    this.applyMode()
  }

  mode(): InputMode {
    return this.currentMode
  }

  setMode(mode: InputMode): void {
    if (mode === this.currentMode) {
      return
    }
    this.currentMode = mode
    this.applyMode()
    this.events.emit('mode', mode)
  }

  selection(): SelectionRef | null {
    return this.currentSelection
  }

  /** Selección programática (listas/inspector de la UI). `null` deselecciona. */
  select(selection: SelectionRef | null): void {
    if (
      selection?.id === this.currentSelection?.id &&
      selection?.type === this.currentSelection?.type
    ) {
      return
    }
    this.currentSelection = selection
    this.refreshMarker()
    this.events.emit('selection', selection)
  }

  followedVehicleId(): string | null {
    return this.followId
  }

  /** Sigue un vehículo con la cámara (`null` cancela). El pan manual lo cancela. */
  setFollow(vehicleId: string | null): void {
    if (vehicleId === this.followId) {
      return
    }
    this.followId = vehicleId
    this.events.emit('follow', vehicleId)
  }

  /** Tick por frame: follow de cámara + marcador pegado a entidades móviles. */
  tick(): void {
    if (this.followId !== null) {
      if (this.camera.isDragging()) {
        // Pan manual ⇒ el jugador retoma el control: cancela el seguimiento.
        this.setFollow(null)
      } else {
        const vm = this.bridge.visible().vehicles.get(this.followId)
        if (vm) {
          this.camera.centerOnM(vm.xM, vm.yM)
        } else {
          this.setFollow(null)
        }
      }
    }
    this.refreshMarker()
  }

  destroy(): void {
    this.offSelect()
    this.detachInput()
    this.events.removeAll()
    this.marker.destroy()
    this.ghost.destroy()
    this.rubber.destroy()
  }

  // ── Input handlers ─────────────────────────────────────────────────────────

  private onWorldClick(event: WorldSelectEvent): void {
    if (this.currentMode === 'select') {
      this.pick(event)
      return
    }
    if (this.currentMode === 'build') {
      this.emitBuildIntent(event.xM, event.yM)
    }
  }

  private pick(event: WorldSelectEvent): void {
    const toleranceM = PICK_TOLERANCE_PX / (PX_PER_M * this.camera.zoom())
    const hit = hitTest(this.bridge.visible(), event.xM, event.yM, toleranceM)

    // Doble clic sobre el MISMO vehículo ⇒ follow de cámara (mandato).
    const nowMs = Date.now()
    if (
      hit?.type === 'vehicle' &&
      this.lastClick !== null &&
      this.lastClick.id === hit.id &&
      nowMs - this.lastClick.atMs <= DOUBLE_CLICK_MS
    ) {
      this.setFollow(hit.id)
    }
    this.lastClick = hit ? { id: hit.id, atMs: nowMs } : null

    this.select(hit)
  }

  private emitBuildIntent(xM: number, yM: number): void {
    const tile = mToTile(xM, yM)
    if (!this.tileInsideWorld(tile.tx, tile.ty)) {
      return
    }
    // Ancla a la rejilla ortogonal (ADR-019): esquina del tile en metros.
    this.events.emit('intent', {
      type: 'build',
      xM: tile.tx * WORLD_M_PER_TILE,
      yM: tile.ty * WORLD_M_PER_TILE,
    })
  }

  private onPointerMove(pointer: Phaser.Input.Pointer): void {
    if (this.currentMode === 'build') {
      this.updateGhost(pointer)
      return
    }
    if (this.currentMode === 'parcel' && this.parcelAnchor !== null) {
      this.drawRubber(pointer)
    }
  }

  private onPointerDown(pointer: Phaser.Input.Pointer): void {
    if (this.currentMode === 'parcel' && pointer.leftButtonDown()) {
      const { xM, yM } = this.camera.screenToM(pointer.x, pointer.y)
      this.parcelAnchor = { xM, yM, screenX: pointer.x, screenY: pointer.y }
    }
  }

  private onPointerUp(pointer: Phaser.Input.Pointer): void {
    const anchor = this.parcelAnchor
    if (this.currentMode !== 'parcel' || anchor === null) {
      return
    }
    this.parcelAnchor = null
    this.rubber.clear()
    if (
      Math.abs(pointer.x - anchor.screenX) < MIN_PARCEL_DRAG_PX &&
      Math.abs(pointer.y - anchor.screenY) < MIN_PARCEL_DRAG_PX
    ) {
      return // clic sin arrastre: no hay parcela
    }
    const { xM, yM } = this.camera.screenToM(pointer.x, pointer.y)
    this.events.emit('intent', {
      type: 'parcel',
      rectM: {
        xM: Math.min(anchor.xM, xM),
        yM: Math.min(anchor.yM, yM),
        widthM: Math.abs(xM - anchor.xM),
        heightM: Math.abs(yM - anchor.yM),
      },
    })
  }

  // ── Presentación (ghost, rubber, marcador) ─────────────────────────────────

  private applyMode(): void {
    // El botón izquierdo solo panea en modo pan; el central panea siempre.
    this.camera.interactionMode = this.currentMode === 'pan' ? 'pan' : 'select'
    this.ghost.setVisible(false) // reaparece al mover el puntero en modo build
    this.parcelAnchor = null
    this.rubber.clear()
  }

  /** ¿El tile cae dentro del mundo vigente? (bounds dinámicos de la cámara). */
  private tileInsideWorld(tx: number, ty: number): boolean {
    return isInsideWorldM(tx * WORLD_M_PER_TILE, ty * WORLD_M_PER_TILE, this.camera.worldBoundsM())
  }

  private updateGhost(pointer: Phaser.Input.Pointer): void {
    const { xM, yM } = this.camera.screenToM(pointer.x, pointer.y)
    const tile = mToTile(xM, yM)
    const valid = this.tileInsideWorld(tile.tx, tile.ty)
    const center = mToPx((tile.tx + 0.5) * WORLD_M_PER_TILE, (tile.ty + 0.5) * WORLD_M_PER_TILE)
    this.ghost.setTexture(valid ? TEXTURES.ghost : EXTRA_TEXTURES.ghostInvalid)
    this.ghost.setPosition(center.xPx, center.yPx)
    this.ghost.setVisible(true)
  }

  private drawRubber(pointer: Phaser.Input.Pointer): void {
    const anchor = this.parcelAnchor
    if (anchor === null) {
      return
    }
    const { xM, yM } = this.camera.screenToM(pointer.x, pointer.y)
    const origin = mToPx(Math.min(anchor.xM, xM), Math.min(anchor.yM, yM))
    const x0 = origin.xPx
    const y0 = origin.yPx
    const w = Math.abs(xM - anchor.xM) * PX_PER_M
    const h = Math.abs(yM - anchor.yM) * PX_PER_M
    this.rubber.clear()
    this.rubber.lineStyle(2, RUBBER_COLOR, 1)
    this.rubber.strokeRect(x0, y0, w, h)
  }

  private refreshMarker(): void {
    const selection = this.currentSelection
    if (selection === null) {
      this.marker.setVisible(false)
      return
    }
    const target = markerTarget(this.bridge.visible(), selection)
    if (target === null) {
      // La entidad salió del viewport (culling) o del estado: marcador oculto;
      // la selección persiste y el marcador reaparece cuando vuelva a ser visible.
      this.marker.setVisible(false)
      return
    }
    this.marker.setPosition(target.xPx, target.yPx)
    this.marker.setDisplaySize(target.wPx + MARKER_MARGIN_PX, target.hPx + MARKER_MARGIN_PX)
    this.marker.setVisible(true)
  }
}


interface MarkerTarget {
  readonly xPx: number
  readonly yPx: number
  readonly wPx: number
  readonly hPx: number
}

/** Centro y tamaño (px de render) de la entidad seleccionada, o `null` si no es visible. */
function markerTarget(vms: VisibleVms, selection: SelectionRef): MarkerTarget | null {
  switch (selection.type) {
    case 'vehicle': {
      const vm = vms.vehicles.get(selection.id)
      if (!vm) {
        return null
      }
      const p = mToPx(vm.xM, vm.yM)
      return { xPx: p.xPx, yPx: p.yPx, wPx: VEHICLE_W_PX + 4, hPx: VEHICLE_W_PX + 4 }
    }
    case 'building': {
      const vm = vms.buildings.get(selection.id)
      if (!vm) {
        return null
      }
      const p = mToPx(vm.xM + vm.wM / 2, vm.yM + vm.hM / 2)
      return { xPx: p.xPx, yPx: p.yPx, wPx: vm.wM * PX_PER_M, hPx: vm.hM * PX_PER_M }
    }
    case 'city': {
      const vm = vms.cities.get(selection.id)
      if (!vm) {
        return null
      }
      const p = mToPx(vm.xM, vm.yM)
      const d = CITY_TEXTURE_PX * cityScale(vm.level)
      return { xPx: p.xPx, yPx: p.yPx, wPx: d, hPx: d }
    }
    case 'deposit': {
      const vm = vms.deposits.get(selection.id)
      if (!vm) {
        return null
      }
      const p = mToPx(vm.xM, vm.yM)
      return { xPx: p.xPx, yPx: p.yPx, wPx: ENTITY_BASE_PX, hPx: ENTITY_BASE_PX }
    }
    case 'node': {
      const vm = vms.nodes.get(selection.id)
      if (!vm) {
        return null
      }
      const p = mToPx(vm.xM, vm.yM)
      return { xPx: p.xPx, yPx: p.yPx, wPx: NODE_PX + 6, hPx: NODE_PX + 6 }
    }
  }
}
