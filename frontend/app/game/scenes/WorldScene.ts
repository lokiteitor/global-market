/**
 * game/scenes/WorldScene.ts — única escena de mundo v1 (FAD §11, §16–§18).
 *
 * Render TOP-DOWN (SIMPLIFICACIÓN v1 aceptada: proyección lon/lat → px del
 * kernel; la isométrica llega en FE-6) con gráficos PROCEDURALES
 * (SIMPLIFICACIÓN v1 aceptada: Shapes/Graphics de Phaser, sin atlases ni
 * assets binarios).
 *
 * La escena implementa el puerto `WorldRenderer`: recibe view-models planos
 * del bridge (upsert/remove con POOLING de Game Objects: se reutilizan y se
 * liberan a un pool, nunca se destruyen por frame) y emite intents espaciales
 * por el event bus tipado ('world:select'). No importa stores ni Vue (O2).
 *
 * Interpolación (P5): cada frame, la posición de un vehículo en tránsito es
 * interpolateOnPath(path, progress) con progress = clamp(base +
 * (simNow - entered)/duration, 0, 1); simNow llega inyectado (deps.simNow),
 * único origen temporal — la escena jamás llama a Date.now() para dominio.
 */
import Phaser from 'phaser'
import { interpolateOnPath, progressAt } from '../kinematics'
import { DEFAULT_PALETTE } from '../palette'
import type {
  BuildingVM,
  CityVM,
  DepositVM,
  EntityKind,
  GameDeps,
  LinkVM,
  NodeVM,
  RegionVM,
  VehicleVM,
  VMByKind,
  WorldBbox,
  WorldPalette,
  WorldRenderer
} from '../types'
import { OVERLAY_SCENE_KEY, type OverlayScene } from './OverlayScene'

export const WORLD_SCENE_KEY = 'world'

const ZOOM_MIN = 0.25
const ZOOM_MAX = 4
const CLICK_SLOP_PX = 5
const KEY_PAN_SPEED = 600 // px de pantalla por segundo

// Profundidades por capa (de fondo a frente).
const DEPTH = { region: 0, regionLabel: 1, link: 10, node: 20, deposit: 30, city: 40, cityLabel: 41, building: 50, vehicle: 60 } as const

interface RegionEntry {
  gfx: Phaser.GameObjects.Graphics
  label: Phaser.GameObjects.Text
  vm: RegionVM
}
interface LinkEntry {
  gfx: Phaser.GameObjects.Graphics
  vm: LinkVM
}
interface NodeEntry {
  arc: Phaser.GameObjects.Arc
  vm: NodeVM
}
interface DepositEntry {
  poly: Phaser.GameObjects.Polygon
  vm: DepositVM
}
interface CityEntry {
  arc: Phaser.GameObjects.Arc
  label: Phaser.GameObjects.Text
  vm: CityVM
}
interface BuildingEntry {
  rect: Phaser.GameObjects.Rectangle
  vm: BuildingVM
}
interface VehicleEntry {
  tri: Phaser.GameObjects.Triangle
  vm: VehicleVM
}

interface DragState {
  startX: number
  startY: number
  scrollX: number
  scrollY: number
  panning: boolean
  moved: number
}

export class WorldScene extends Phaser.Scene implements WorldRenderer {
  private readonly deps: GameDeps
  private readonly palette: WorldPalette

  private readonly regions = new Map<string, RegionEntry>()
  private readonly links = new Map<string, LinkEntry>()
  private readonly nodes = new Map<string, NodeEntry>()
  private readonly deposits = new Map<string, DepositEntry>()
  private readonly cities = new Map<string, CityEntry>()
  private readonly buildings = new Map<string, BuildingEntry>()
  private readonly vehicles = new Map<string, VehicleEntry>()

  // Pools por tipo: remove() libera aquí; upsert() reutiliza (P8).
  private readonly pools: { [K in EntityKind]: unknown[] } = {
    region: [],
    link: [],
    node: [],
    deposit: [],
    city: [],
    building: [],
    vehicle: []
  }

  private drag: DragState | null = null
  private keys: Record<'up' | 'down' | 'left' | 'right' | 'w' | 'a' | 's' | 'd', Phaser.Input.Keyboard.Key> | null = null

  /**
   * Señal de "create() ya corrió" que boot.ts espera. NO se puede usar
   * `world.events`/`world.sys.events` desde fuera para esto: Phaser los crea
   * durante `sys.init()`, que ocurre en el arranque ASÍNCRONO del juego —
   * después de `new Phaser.Game()`— así que engancharlos justo tras construir
   * el juego lee `undefined` y revienta. La escena avisa ella misma.
   */
  onReady?: () => void

  /** Bbox (px) acumulado de las regiones: límites de cámara del mundo. */
  private worldPxBounds: Phaser.Geom.Rectangle | null = null

  private lastViewport = { scrollX: NaN, scrollY: NaN, zoom: NaN, width: NaN, height: NaN }
  private pointerDirty = false

  constructor(deps: GameDeps) {
    super(WORLD_SCENE_KEY)
    this.deps = deps
    this.palette = deps.palette ?? DEFAULT_PALETTE
  }

  // ─── Ciclo de vida ─────────────────────────────────────────────────────────

  create(): void {
    this.scene.launch(OVERLAY_SCENE_KEY)
    this.cameras.main.setZoom(1)
    this.setupInput()
    // El renderer ya está operativo: desbloquea el await de createGame().
    this.onReady?.()
  }

  override update(_time: number, delta: number): void {
    this.updateVehicles()
    this.updateKeyboardPan(delta)
    this.reportViewportIfChanged()
    this.updateHover()
  }

  // ─── Puerto WorldRenderer ──────────────────────────────────────────────────

  upsert<K extends EntityKind>(kind: K, vm: VMByKind[K]): void {
    switch (kind) {
      case 'region':
        this.upsertRegion(vm as RegionVM)
        break
      case 'link':
        this.upsertLink(vm as LinkVM)
        break
      case 'node':
        this.upsertNode(vm as NodeVM)
        break
      case 'deposit':
        this.upsertDeposit(vm as DepositVM)
        break
      case 'city':
        this.upsertCity(vm as CityVM)
        break
      case 'building':
        this.upsertBuilding(vm as BuildingVM)
        break
      case 'vehicle':
        this.upsertVehicle(vm as VehicleVM)
        break
    }
  }

  remove(kind: EntityKind, id: string): void {
    const maps: Record<EntityKind, Map<string, unknown>> = {
      region: this.regions,
      link: this.links,
      node: this.nodes,
      deposit: this.deposits,
      city: this.cities,
      building: this.buildings,
      vehicle: this.vehicles
    }
    const map = maps[kind]
    const entry = map.get(id)
    if (entry === undefined) return
    map.delete(id)
    this.releaseEntry(kind, entry)
  }

  flyTo(lon: number, lat: number): void {
    const { x, y } = this.deps.projection.worldToScreen(lon, lat)
    this.cameras.main.pan(x, y, 600, 'Sine.easeInOut')
  }

  frameWorld(bbox: WorldBbox): void {
    // Proyecta las esquinas del bbox de mundo a px (y crece hacia el sur, de
    // ahí que maxLat mapee al borde superior). Centra la cámara y ajusta el
    // zoom para abarcar todo el mundo con un margen, dentro de los límites.
    const tl = this.deps.projection.worldToScreen(bbox.minLon, bbox.maxLat)
    const br = this.deps.projection.worldToScreen(bbox.maxLon, bbox.minLat)
    const worldW = Math.abs(br.x - tl.x)
    const worldH = Math.abs(br.y - tl.y)
    if (worldW === 0 || worldH === 0) return
    const cam = this.cameras.main
    const margin = 1.12 // ~12 % de aire alrededor del mundo
    const zoom = Phaser.Math.Clamp(Math.min(cam.width / (worldW * margin), cam.height / (worldH * margin)), ZOOM_MIN, ZOOM_MAX)
    cam.setZoom(zoom)
    cam.centerOn((tl.x + br.x) / 2, (tl.y + br.y) / 2)
  }

  /** Viewport actual en coordenadas de MUNDO (lon/lat) para interest management. */
  getViewportBbox(): WorldBbox {
    const view = this.cameras.main.worldView
    const tl = this.deps.projection.screenToWorld(view.x, view.y)
    const br = this.deps.projection.screenToWorld(view.right, view.bottom)
    return { minLon: tl.lon, maxLon: br.lon, minLat: br.lat, maxLat: tl.lat }
  }

  // ─── Upserts con pooling ───────────────────────────────────────────────────

  private upsertRegion(vm: RegionVM): void {
    let entry = this.regions.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.region.pop() as RegionEntry | undefined) ?? {
        gfx: this.add.graphics().setDepth(DEPTH.region),
        label: this.add
          .text(0, 0, '', { fontFamily: 'monospace', fontSize: '13px', color: this.palette.label })
          .setDepth(DEPTH.regionLabel)
          .setAlpha(0.6),
        vm
      }
      this.regions.set(vm.id, entry)
    }
    entry.vm = vm
    const g = entry.gfx
    g.clear()
    g.setVisible(true).setActive(true)
    g.fillStyle(vm.fillColor, 0.18)
    g.fillRect(vm.x, vm.y, vm.width, vm.height)
    g.lineStyle(1.5, vm.strokeColor, 0.9)
    g.strokeRect(vm.x, vm.y, vm.width, vm.height)
    entry.label.setVisible(true).setActive(true).setPosition(vm.x + 8, vm.y + 6).setText(vm.name)
    this.growWorldBounds(vm)
  }

  private upsertLink(vm: LinkVM): void {
    let entry = this.links.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.link.pop() as LinkEntry | undefined) ?? { gfx: this.add.graphics().setDepth(DEPTH.link), vm }
      this.links.set(vm.id, entry)
    }
    entry.vm = vm
    const g = entry.gfx
    g.clear()
    g.setVisible(true).setActive(true)
    g.lineStyle(vm.width, vm.color, 0.75)
    g.strokePoints(vm.points, false)
  }

  private upsertNode(vm: NodeVM): void {
    let entry = this.nodes.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.node.pop() as NodeEntry | undefined) ?? { arc: this.add.circle(0, 0, 1).setDepth(DEPTH.node), vm }
      this.nodes.set(vm.id, entry)
    }
    entry.vm = vm
    entry.arc.setVisible(true).setActive(true).setPosition(vm.x, vm.y).setRadius(vm.size).setFillStyle(vm.color, 0.9)
  }

  private upsertDeposit(vm: DepositVM): void {
    let entry = this.deposits.get(vm.id)
    if (entry === undefined) {
      const s = vm.size
      // Rombo procedural (sin texturas): polígono de 4 vértices.
      entry = (this.pools.deposit.pop() as DepositEntry | undefined) ?? {
        poly: this.add.polygon(0, 0, [0, -s, s, 0, 0, s, -s, 0], vm.color, 0.95).setDepth(DEPTH.deposit),
        vm
      }
      this.deposits.set(vm.id, entry)
    }
    entry.vm = vm
    entry.poly.setVisible(true).setActive(true).setPosition(vm.x, vm.y).setFillStyle(vm.color, 0.95)
  }

  private upsertCity(vm: CityVM): void {
    let entry = this.cities.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.city.pop() as CityEntry | undefined) ?? {
        arc: this.add.circle(0, 0, 1).setDepth(DEPTH.city),
        label: this.add
          .text(0, 0, '', { fontFamily: 'monospace', fontSize: '12px', color: this.palette.label })
          .setOrigin(0.5, 0)
          .setDepth(DEPTH.cityLabel),
        vm
      }
      this.cities.set(vm.id, entry)
    }
    entry.vm = vm
    entry.arc
      .setVisible(true)
      .setActive(true)
      .setPosition(vm.x, vm.y)
      .setRadius(vm.radius)
      .setFillStyle(vm.color, 0.85)
      .setStrokeStyle(1.5, 0xffffff, 0.35)
    entry.label.setVisible(true).setActive(true).setPosition(vm.x, vm.y + vm.radius + 4).setText(vm.label)
  }

  private upsertBuilding(vm: BuildingVM): void {
    let entry = this.buildings.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.building.pop() as BuildingEntry | undefined) ?? {
        rect: this.add.rectangle(0, 0, 1, 1).setDepth(DEPTH.building),
        vm
      }
      this.buildings.set(vm.id, entry)
    }
    entry.vm = vm
    entry.rect
      .setVisible(true)
      .setActive(true)
      .setPosition(vm.x, vm.y)
      .setSize(vm.size, vm.size)
      .setFillStyle(vm.color, 1)
    // Borde destacado si es propio (C13: comandable), sutil si es ajeno.
    if (vm.owned) entry.rect.setStrokeStyle(2, this.palette.ownedOutline, 1)
    else entry.rect.setStrokeStyle(1, 0x000000, 0.35)
  }

  private upsertVehicle(vm: VehicleVM): void {
    let entry = this.vehicles.get(vm.id)
    if (entry === undefined) {
      // Triángulo procedural apuntando a +x; rotation = ángulo del tramo.
      entry = (this.pools.vehicle.pop() as VehicleEntry | undefined) ?? {
        tri: this.add.triangle(0, 0, -6, -4, 6, 0, -6, 4, vm.color, 1).setDepth(DEPTH.vehicle),
        vm
      }
      this.vehicles.set(vm.id, entry)
    }
    entry.vm = vm
    entry.tri.setVisible(true).setActive(true).setFillStyle(vm.color, 1)
    if (vm.motion.kind === 'fixed') {
      entry.tri.setPosition(vm.motion.x, vm.motion.y).setRotation(0)
    } else {
      this.placeVehicle(entry)
    }
  }

  private releaseEntry(kind: EntityKind, entry: unknown): void {
    const hide = (go: { setVisible(v: boolean): unknown; setActive(a: boolean): unknown }): void => {
      go.setVisible(false)
      go.setActive(false)
    }
    switch (kind) {
      case 'region': {
        const e = entry as RegionEntry
        e.gfx.clear()
        hide(e.gfx)
        hide(e.label)
        break
      }
      case 'link': {
        const e = entry as LinkEntry
        e.gfx.clear()
        hide(e.gfx)
        break
      }
      case 'node':
        hide((entry as NodeEntry).arc)
        break
      case 'deposit':
        hide((entry as DepositEntry).poly)
        break
      case 'city': {
        const e = entry as CityEntry
        hide(e.arc)
        hide(e.label)
        break
      }
      case 'building':
        hide((entry as BuildingEntry).rect)
        break
      case 'vehicle':
        hide((entry as VehicleEntry).tri)
        break
    }
    this.pools[kind].push(entry)
  }

  // ─── Interpolación por frame (P5) ─────────────────────────────────────────

  private placeVehicle(entry: VehicleEntry): void {
    const motion = entry.vm.motion
    if (motion.kind === 'fixed') return
    const progress = progressAt(
      { enteredSim: motion.enteredSim, durationSim: motion.durationSim, baseProgress: motion.baseProgress },
      this.deps.simNow()
    )
    const sample = interpolateOnPath(motion.points, progress)
    entry.tri.setPosition(sample.x, sample.y).setRotation(sample.angle)
  }

  private updateVehicles(): void {
    for (const entry of this.vehicles.values()) this.placeVehicle(entry)
  }

  // ─── Cámara e input ────────────────────────────────────────────────────────

  private setupInput(): void {
    const kb = this.input.keyboard
    if (kb !== null) {
      this.keys = kb.addKeys('up,down,left,right,w,a,s,d') as NonNullable<WorldScene['keys']>
    }

    this.input.on('wheel', (pointer: Phaser.Input.Pointer, _over: unknown, _dx: number, dy: number) => {
      this.zoomAt(pointer, dy > 0 ? 1 / 1.15 : 1.15)
    })

    this.input.on('pointerdown', (pointer: Phaser.Input.Pointer) => {
      const cam = this.cameras.main
      const world = cam.getWorldPoint(pointer.x, pointer.y)
      const overEntity = this.pick(world.x, world.y) !== null
      // Pan: botón medio siempre; izquierdo solo sobre vacío.
      const panning = pointer.middleButtonDown() || (pointer.leftButtonDown() && !overEntity)
      this.drag = { startX: pointer.x, startY: pointer.y, scrollX: cam.scrollX, scrollY: cam.scrollY, panning, moved: 0 }
    })

    this.input.on('pointermove', (pointer: Phaser.Input.Pointer) => {
      this.pointerDirty = true
      const drag = this.drag
      if (drag === null || !pointer.isDown) return
      const dx = pointer.x - drag.startX
      const dy = pointer.y - drag.startY
      drag.moved = Math.max(drag.moved, Math.hypot(dx, dy))
      if (drag.panning) {
        const cam = this.cameras.main
        cam.setScroll(drag.scrollX - dx / cam.zoom, drag.scrollY - dy / cam.zoom)
      }
    })

    const endDrag = (pointer: Phaser.Input.Pointer): void => {
      const drag = this.drag
      this.drag = null
      if (drag === null) return
      // Clic (sin arrastre) con botón izquierdo → picking.
      if (drag.moved <= CLICK_SLOP_PX && pointer.button === 0) {
        const world = this.cameras.main.getWorldPoint(pointer.x, pointer.y)
        const hit = this.pick(world.x, world.y)
        if (hit !== null) {
          // Intent espacial hacia la app: SOLO por el event bus tipado (O2).
          this.deps.eventBus.emit('world:select', hit)
          this.overlay()?.setSelection(hit)
        } else {
          this.overlay()?.setSelection(null)
        }
      }
    }
    this.input.on('pointerup', endDrag)
    this.input.on('pointerupoutside', endDrag)
  }

  private zoomAt(pointer: Phaser.Input.Pointer, factor: number): void {
    const cam = this.cameras.main
    const anchor = cam.getWorldPoint(pointer.x, pointer.y)
    const zoom = Phaser.Math.Clamp(cam.zoom * factor, ZOOM_MIN, ZOOM_MAX)
    cam.setZoom(zoom)
    // Zoom hacia el cursor: el punto del mundo bajo el puntero no se mueve.
    const midX = anchor.x - (pointer.x - cam.width / 2) / zoom
    const midY = anchor.y - (pointer.y - cam.height / 2) / zoom
    cam.centerOn(midX, midY)
  }

  private updateKeyboardPan(delta: number): void {
    const keys = this.keys
    if (keys === null) return
    const cam = this.cameras.main
    const step = (KEY_PAN_SPEED * delta) / 1000 / cam.zoom
    let dx = 0
    let dy = 0
    if (keys.left.isDown || keys.a.isDown) dx -= step
    if (keys.right.isDown || keys.d.isDown) dx += step
    if (keys.up.isDown || keys.w.isDown) dy -= step
    if (keys.down.isDown || keys.s.isDown) dy += step
    if (dx !== 0 || dy !== 0) cam.setScroll(cam.scrollX + dx, cam.scrollY + dy)
  }

  private growWorldBounds(vm: RegionVM): void {
    const rect = new Phaser.Geom.Rectangle(vm.x, vm.y, vm.width, vm.height)
    this.worldPxBounds = this.worldPxBounds === null ? rect : Phaser.Geom.Rectangle.Union(this.worldPxBounds, rect)
    const pad = 400
    const b = this.worldPxBounds
    // Límites de cámara = bbox del mundo (regiones) + margen.
    this.cameras.main.setBounds(b.x - pad, b.y - pad, b.width + pad * 2, b.height + pad * 2)
  }

  private reportViewportIfChanged(): void {
    const cam = this.cameras.main
    const last = this.lastViewport
    if (
      cam.scrollX === last.scrollX &&
      cam.scrollY === last.scrollY &&
      cam.zoom === last.zoom &&
      cam.width === last.width &&
      cam.height === last.height
    ) {
      return
    }
    this.lastViewport = { scrollX: cam.scrollX, scrollY: cam.scrollY, zoom: cam.zoom, width: cam.width, height: cam.height }
    this.deps.onViewportChange?.(this.getViewportBbox())
  }

  // ─── Picking (prioridad vehículo > edificio > ciudad > nodo) ──────────────

  pick(x: number, y: number): { kind: 'vehicle' | 'building' | 'city' | 'deposit' | 'node'; id: string } | null {
    for (const [id, entry] of this.vehicles) {
      if (Phaser.Math.Distance.Between(x, y, entry.tri.x, entry.tri.y) <= 12) return { kind: 'vehicle', id }
    }
    for (const [id, entry] of this.buildings) {
      const half = entry.vm.size / 2 + 4
      if (Math.abs(x - entry.vm.x) <= half && Math.abs(y - entry.vm.y) <= half) return { kind: 'building', id }
    }
    for (const [id, entry] of this.cities) {
      if (Phaser.Math.Distance.Between(x, y, entry.vm.x, entry.vm.y) <= entry.vm.radius + 4) return { kind: 'city', id }
    }
    for (const [id, entry] of this.deposits) {
      if (Phaser.Math.Distance.Between(x, y, entry.vm.x, entry.vm.y) <= entry.vm.size + 4) return { kind: 'deposit', id }
    }
    for (const [id, entry] of this.nodes) {
      if (Phaser.Math.Distance.Between(x, y, entry.vm.x, entry.vm.y) <= entry.vm.size + 5) return { kind: 'node', id }
    }
    return null
  }

  /** Posición/radio actual de una entidad para el highlight del overlay. */
  getEntityHighlight(kind: string, id: string): { x: number; y: number; r: number } | null {
    switch (kind) {
      case 'vehicle': {
        const e = this.vehicles.get(id)
        return e === undefined ? null : { x: e.tri.x, y: e.tri.y, r: 14 }
      }
      case 'building': {
        const e = this.buildings.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: e.vm.size / 2 + 8 }
      }
      case 'city': {
        const e = this.cities.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: e.vm.radius + 8 }
      }
      case 'deposit': {
        const e = this.deposits.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: e.vm.size + 8 }
      }
      case 'node': {
        const e = this.nodes.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: e.vm.size + 8 }
      }
      default:
        return null
    }
  }

  private updateHover(): void {
    if (!this.pointerDirty) return
    this.pointerDirty = false
    const pointer = this.input.activePointer
    if (pointer.isDown) return
    const world = this.cameras.main.getWorldPoint(pointer.x, pointer.y)
    this.overlay()?.setHover(this.pick(world.x, world.y))
  }

  private overlay(): OverlayScene | null {
    const scene = this.scene.get(OVERLAY_SCENE_KEY)
    return (scene as OverlayScene | null) ?? null
  }
}
