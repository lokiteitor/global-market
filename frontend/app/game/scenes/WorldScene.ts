/**
 * game/scenes/WorldScene.ts — única escena de mundo (FAD §11, §16–§18).
 *
 * Render ISOMÉTRICO 2:1 estilo Simutrans (FE-6) con sprites del atlas pak128
 * (cargado por PreloadScene): terreno tileado por región (Blitter), carreteras
 * rasterizadas celda a celda, edificios/ciudades/depósitos como sprites con
 * anclaje en el pie del rombo y vehículos de 8 direcciones. Sin chunks ni
 * streaming (SIMPLIFICACIÓN v1 aceptada: el mundo demo cabe entero; FAD §16
 * completo llega después).
 *
 * La escena implementa el puerto `WorldRenderer`: recibe view-models planos
 * del bridge (upsert/remove con POOLING de Game Objects: se reutilizan y se
 * liberan a un pool, nunca se destruyen por frame) y emite intents espaciales
 * por el event bus tipado ('world:select'). No importa stores ni Vue (O2).
 *
 * Depth sorting iso: los objetos "de pie" (edificio/depósito/ciudad/vehículo)
 * usan depth = ISO_BASE + y del anclaje (pie del rombo): lo que está más al
 * sur en pantalla tapa a lo que tiene detrás.
 *
 * Interpolación (P5): cada frame, la posición de un vehículo en tránsito es
 * interpolateOnPath(path, progress) con progress = clamp(base +
 * (simNow - entered)/duration, 0, 1); simNow llega inyectado (deps.simNow),
 * único origen temporal — la escena jamás llama a Date.now() para dominio.
 * El ángulo px del tramo se mapea al frame direccional (8 direcciones pak128)
 * — los sprites iso no se rotan.
 */
import Phaser from 'phaser'
import { dirFromAngle } from '../iso-dirs'
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
import { PAK_ATLAS_KEY, PAK_META_KEY } from './PreloadScene'

export const WORLD_SCENE_KEY = 'world'

const ZOOM_MIN = 0.25
const ZOOM_MAX = 4
const CLICK_SLOP_PX = 5
const KEY_PAN_SPEED = 600 // px de pantalla por segundo

// Capas planas (fondo) — los objetos "de pie" van en la banda iso.
const DEPTH = { terrain: 0, regionLabel: 1, link: 10, node: 20 } as const
/** Base de la banda iso: depth = ISO_BASE + y del pie (px de mundo). */
const ISO_BASE = 1000

/** Ajustes de calibración vertical del arte pak128 (una constante por capa). */
const GROUND_OFFSET_Y = 0

/** Anclaje por defecto: centro del rombo (64,96) de una celda 128×128. */
const FALLBACK_ANCHOR = { anchorX: 0.5, anchorY: 0.75 }

interface FrameAnchor {
  anchorX: number
  anchorY: number
}

interface RegionEntry {
  blitter: Phaser.GameObjects.Blitter
  label: Phaser.GameObjects.Text
  vm: RegionVM
}
interface LinkEntry {
  imgs: Phaser.GameObjects.Image[]
  vm: LinkVM
}
interface NodeEntry {
  arc: Phaser.GameObjects.Arc
  vm: NodeVM
}
interface DepositEntry {
  img: Phaser.GameObjects.Image
  vm: DepositVM
}
interface CityEntry {
  img: Phaser.GameObjects.Image
  label: Phaser.GameObjects.Text
  vm: CityVM
}
interface BuildingEntry {
  img: Phaser.GameObjects.Image
  /** Rombo de 1 tile bajo el sprite: marca de lo propio (C13). */
  marker: Phaser.GameObjects.Graphics
  vm: BuildingVM
}
interface VehicleEntry {
  img: Phaser.GameObjects.Image
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
  /** Pool compartido de tiles de carretera (N images por link). */
  private readonly roadImagePool: Phaser.GameObjects.Image[] = []

  /** Anclajes por frame del manifiesto meta.json (pie del rombo). */
  private anchors: Record<string, FrameAnchor> = {}

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
    const meta = this.cache.json.get(PAK_META_KEY) as { frames?: Record<string, FrameAnchor> } | undefined
    this.anchors = meta?.frames ?? {}
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
    // En iso un rect lon/lat proyecta a un ROMBO: el bbox px que lo abarca
    // sale de las 4 esquinas, no de dos. Centra la cámara y ajusta el zoom
    // para abarcar todo el mundo con un margen, dentro de los límites.
    const px = this.projectBboxCorners(bbox)
    const worldW = px.maxX - px.minX
    const worldH = px.maxY - px.minY
    if (worldW === 0 || worldH === 0) return
    const cam = this.cameras.main
    const margin = 1.12 // ~12 % de aire alrededor del mundo
    const zoom = Phaser.Math.Clamp(Math.min(cam.width / (worldW * margin), cam.height / (worldH * margin)), ZOOM_MIN, ZOOM_MAX)
    cam.setZoom(zoom)
    cam.centerOn((px.minX + px.maxX) / 2, (px.minY + px.maxY) / 2)
  }

  /** Viewport actual en coordenadas de MUNDO (lon/lat) para interest management. */
  getViewportBbox(): WorldBbox {
    // El rect px de la cámara mapea a un rombo lon/lat: el bbox correcto sale
    // de proyectar las 4 esquinas (la iso es afín → min/max exactos ahí).
    const view = this.cameras.main.worldView
    const corners = [
      this.deps.projection.screenToWorld(view.x, view.y),
      this.deps.projection.screenToWorld(view.right, view.y),
      this.deps.projection.screenToWorld(view.x, view.bottom),
      this.deps.projection.screenToWorld(view.right, view.bottom)
    ]
    return {
      minLon: Math.min(...corners.map((c) => c.lon)),
      maxLon: Math.max(...corners.map((c) => c.lon)),
      minLat: Math.min(...corners.map((c) => c.lat)),
      maxLat: Math.max(...corners.map((c) => c.lat))
    }
  }

  // ─── Upserts con pooling ───────────────────────────────────────────────────

  private applyAnchor(img: Phaser.GameObjects.Image, frame: string): void {
    const a = this.anchors[frame] ?? FALLBACK_ANCHOR
    img.setOrigin(a.anchorX, a.anchorY)
  }

  private upsertRegion(vm: RegionVM): void {
    let entry = this.regions.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.region.pop() as RegionEntry | undefined) ?? {
        blitter: this.add.blitter(0, 0, PAK_ATLAS_KEY).setDepth(DEPTH.terrain),
        label: this.add
          .text(0, 0, '', { fontFamily: 'monospace', fontSize: '13px', color: this.palette.label })
          .setOrigin(0.5, 0)
          .setDepth(DEPTH.regionLabel)
          .setAlpha(0.6),
        vm
      }
      this.regions.set(vm.id, entry)
    }
    entry.vm = vm
    const blitter = entry.blitter
    blitter.clear()
    blitter.setVisible(true).setActive(true)
    // Terreno tileado: un bob (rombo 128×64) por celda. Orden v-exterior /
    // u-interior para que el solape natural de rombos pinte de norte a sur.
    for (let v = vm.v0; v < vm.v1; v++) {
      for (let u = vm.u0; u < vm.u1; u++) {
        const c = this.deps.projection.tileToScreen(u + 0.5, v + 0.5)
        blitter.create(c.x - 64, c.y - 32 + GROUND_OFFSET_Y, vm.groundFrame)
      }
    }
    entry.label.setVisible(true).setActive(true).setPosition(vm.labelX, vm.labelY - 10).setText(vm.name)
    this.growWorldBounds(vm)
  }

  private upsertLink(vm: LinkVM): void {
    let entry = this.links.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.link.pop() as LinkEntry | undefined) ?? { imgs: [], vm }
      this.links.set(vm.id, entry)
    }
    entry.vm = vm
    // Ajusta el nº de images al nº de celdas reutilizando el pool compartido.
    while (entry.imgs.length > vm.cells.length) {
      const img = entry.imgs.pop() as Phaser.GameObjects.Image
      img.setVisible(false).setActive(false)
      this.roadImagePool.push(img)
    }
    while (entry.imgs.length < vm.cells.length) {
      const img = this.roadImagePool.pop() ?? this.add.image(0, 0, PAK_ATLAS_KEY).setDepth(DEPTH.link)
      entry.imgs.push(img)
    }
    for (let i = 0; i < vm.cells.length; i++) {
      const cell = vm.cells[i] as LinkVM['cells'][number]
      const img = entry.imgs[i] as Phaser.GameObjects.Image
      const c = this.deps.projection.tileToScreen(cell.u + 0.5, cell.v + 0.5)
      img.setVisible(true).setActive(true).setFrame(cell.frame)
      this.applyAnchor(img, cell.frame)
      img.setPosition(c.x, c.y).setTint(vm.tint)
    }
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
      entry = (this.pools.deposit.pop() as DepositEntry | undefined) ?? {
        img: this.add.image(0, 0, PAK_ATLAS_KEY),
        vm
      }
      this.deposits.set(vm.id, entry)
    }
    entry.vm = vm
    entry.img.setVisible(true).setActive(true).setFrame(vm.frame)
    this.applyAnchor(entry.img, vm.frame)
    entry.img.setPosition(vm.x, vm.y).setDepth(ISO_BASE + vm.y)
  }

  private upsertCity(vm: CityVM): void {
    let entry = this.cities.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.city.pop() as CityEntry | undefined) ?? {
        img: this.add.image(0, 0, PAK_ATLAS_KEY),
        label: this.add
          .text(0, 0, '', { fontFamily: 'monospace', fontSize: '12px', color: this.palette.label })
          .setOrigin(0.5, 0),
        vm
      }
      this.cities.set(vm.id, entry)
    }
    entry.vm = vm
    entry.img.setVisible(true).setActive(true).setFrame(vm.frame)
    this.applyAnchor(entry.img, vm.frame)
    entry.img.setPosition(vm.x, vm.y).setDepth(ISO_BASE + vm.y)
    entry.label
      .setVisible(true)
      .setActive(true)
      .setPosition(vm.x, vm.y + 36)
      .setDepth(ISO_BASE + vm.y + 0.5)
      .setText(vm.label)
  }

  private upsertBuilding(vm: BuildingVM): void {
    let entry = this.buildings.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.building.pop() as BuildingEntry | undefined) ?? {
        img: this.add.image(0, 0, PAK_ATLAS_KEY),
        marker: this.add.graphics(),
        vm
      }
      this.buildings.set(vm.id, entry)
    }
    entry.vm = vm
    entry.img.setVisible(true).setActive(true).setFrame(vm.frame)
    this.applyAnchor(entry.img, vm.frame)
    entry.img.setPosition(vm.x, vm.y).setDepth(ISO_BASE + vm.y).setTint(vm.statusTint)
    // Rombo de 1 tile bajo el pie: marca de lo propio (C13: comandable).
    const marker = entry.marker
    marker.clear()
    marker.setVisible(vm.owned).setActive(vm.owned)
    if (vm.owned) {
      marker.setPosition(vm.x, vm.y).setDepth(ISO_BASE + vm.y - 0.5)
      marker.lineStyle(2, this.palette.ownedOutline, 0.9)
      marker.strokePoints(
        [
          { x: 0, y: -32 },
          { x: 64, y: 0 },
          { x: 0, y: 32 },
          { x: -64, y: 0 }
        ],
        true
      )
    }
  }

  private upsertVehicle(vm: VehicleVM): void {
    let entry = this.vehicles.get(vm.id)
    if (entry === undefined) {
      entry = (this.pools.vehicle.pop() as VehicleEntry | undefined) ?? {
        img: this.add.image(0, 0, PAK_ATLAS_KEY),
        vm
      }
      this.vehicles.set(vm.id, entry)
    }
    entry.vm = vm
    entry.img.setVisible(true).setActive(true).setTint(vm.owned ? this.palette.vehicleOwned : this.palette.vehicle)
    if (vm.motion.kind === 'fixed') {
      this.setVehicleFrame(entry, 'se') // aparcado: vista frontal por defecto
      entry.img.setPosition(vm.motion.x, vm.motion.y).setDepth(ISO_BASE + vm.motion.y)
    } else {
      this.placeVehicle(entry)
    }
  }

  private setVehicleFrame(entry: VehicleEntry, dir: string): void {
    const frame = `${entry.vm.frameBase}.${dir}`
    entry.img.setFrame(frame)
    this.applyAnchor(entry.img, frame)
  }

  private releaseEntry(kind: EntityKind, entry: unknown): void {
    const hide = (go: { setVisible(v: boolean): unknown; setActive(a: boolean): unknown }): void => {
      go.setVisible(false)
      go.setActive(false)
    }
    switch (kind) {
      case 'region': {
        const e = entry as RegionEntry
        e.blitter.clear()
        hide(e.blitter)
        hide(e.label)
        break
      }
      case 'link': {
        const e = entry as LinkEntry
        for (const img of e.imgs) {
          hide(img)
          this.roadImagePool.push(img)
        }
        e.imgs = []
        break
      }
      case 'node':
        hide((entry as NodeEntry).arc)
        break
      case 'deposit':
        hide((entry as DepositEntry).img)
        break
      case 'city': {
        const e = entry as CityEntry
        hide(e.img)
        hide(e.label)
        break
      }
      case 'building': {
        const e = entry as BuildingEntry
        e.marker.clear()
        hide(e.marker)
        hide(e.img)
        break
      }
      case 'vehicle':
        hide((entry as VehicleEntry).img)
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
    // Sprites iso: nada de setRotation — el ángulo px elige el frame de 8 dir.
    this.setVehicleFrame(entry, dirFromAngle(sample.angle))
    entry.img.setPosition(sample.x, sample.y).setDepth(ISO_BASE + sample.y)
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

  /** Bbox px de las 4 esquinas de un bbox lon/lat (la iso rota el rect). */
  private projectBboxCorners(bbox: WorldBbox): { minX: number; minY: number; maxX: number; maxY: number } {
    const pts = [
      this.deps.projection.worldToScreen(bbox.minLon, bbox.maxLat),
      this.deps.projection.worldToScreen(bbox.maxLon, bbox.maxLat),
      this.deps.projection.worldToScreen(bbox.minLon, bbox.minLat),
      this.deps.projection.worldToScreen(bbox.maxLon, bbox.minLat)
    ]
    return {
      minX: Math.min(...pts.map((p) => p.x)),
      maxX: Math.max(...pts.map((p) => p.x)),
      minY: Math.min(...pts.map((p) => p.y)),
      maxY: Math.max(...pts.map((p) => p.y))
    }
  }

  private growWorldBounds(vm: RegionVM): void {
    // Las 4 esquinas del rango de tiles de la región, proyectadas a px.
    const proj = this.deps.projection
    const pts = [proj.tileToScreen(vm.u0, vm.v0), proj.tileToScreen(vm.u1, vm.v0), proj.tileToScreen(vm.u0, vm.v1), proj.tileToScreen(vm.u1, vm.v1)]
    const minX = Math.min(...pts.map((p) => p.x))
    const maxX = Math.max(...pts.map((p) => p.x))
    const minY = Math.min(...pts.map((p) => p.y))
    const maxY = Math.max(...pts.map((p) => p.y))
    const rect = new Phaser.Geom.Rectangle(minX, minY, maxX - minX, maxY - minY)
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
      if (Phaser.Math.Distance.Between(x, y, entry.img.x, entry.img.y) <= 20) return { kind: 'vehicle', id }
    }
    for (const [id, entry] of this.buildings) {
      // Zona de clic ≈ rombo del tile base (elipse 2:1 del anclaje).
      if (Math.abs(x - entry.vm.x) / 2 + Math.abs(y - entry.vm.y) <= 40) return { kind: 'building', id }
    }
    for (const [id, entry] of this.cities) {
      if (Phaser.Math.Distance.Between(x, y, entry.vm.x, entry.vm.y) <= entry.vm.radius + 4) return { kind: 'city', id }
    }
    for (const [id, entry] of this.deposits) {
      if (Math.abs(x - entry.vm.x) / 2 + Math.abs(y - entry.vm.y) <= 56) return { kind: 'deposit', id }
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
        return e === undefined ? null : { x: e.img.x, y: e.img.y, r: 24 }
      }
      case 'building': {
        const e = this.buildings.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: 72 }
      }
      case 'city': {
        const e = this.cities.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: e.vm.radius + 8 }
      }
      case 'deposit': {
        const e = this.deposits.get(id)
        return e === undefined ? null : { x: e.vm.x, y: e.vm.y, r: 96 }
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
