/**
 * game/bridge.ts — WorldStateBridge (FAD §11.6, O2).
 *
 * ÚNICO punto de game/ que conoce las stores Pinia: se suscribe (lectura),
 * deriva view-models PLANOS con culling por viewport + margen y entrega
 * DIFFS upsert/remove al puerto `WorldRenderer` (la escena hace pooling:
 * reutiliza Game Objects, nunca destruye por frame). Dirección de datos:
 *   stores → bridge → renderer (estado);  escena → eventBus (intents).
 *
 * Coalescing: por muchos patches que lleguen, máximo UNA recomputación por
 * frame (scheduler inyectable; requestAnimationFrame por defecto).
 *
 * No importa Phaser: es puro TS y se testea en node con stores reales.
 */
import type { NetworkLink, NetworkNode, Region, Vehicle } from '~/lib/api/types'
import type { AppEvents, EventBus } from '~/lib/kernel/event-bus'
import type { WorldProjection } from '~/lib/kernel/projection'
import type { useBuildingsStore } from '~/stores/buildings.store'
import type { useCitiesStore } from '~/stores/cities.store'
import type { useFleetStore } from '~/stores/fleet.store'
import type { useWorldStore } from '~/stores/world.store'
import type { PathPoint } from './kinematics'
import { DEFAULT_PALETTE } from './palette'
import type {
  AnyVM,
  BuildingVM,
  CityVM,
  DepositVM,
  EntityKind,
  LinkVM,
  NodeVM,
  RegionVM,
  VehicleMotion,
  VehicleVM,
  WorldBbox,
  WorldPalette,
  WorldRenderer
} from './types'

// ─── Dependencias ────────────────────────────────────────────────────────────

export interface WorldBridgeStores {
  world: ReturnType<typeof useWorldStore>
  cities: ReturnType<typeof useCitiesStore>
  buildings: ReturnType<typeof useBuildingsStore>
  fleet: ReturnType<typeof useFleetStore>
}

export interface WorldNetwork {
  nodes: NetworkNode[]
  links: NetworkLink[]
}

export interface WorldBridgeDeps {
  renderer: WorldRenderer
  projection: WorldProjection
  stores: WorldBridgeStores
  eventBus: EventBus<AppEvents>
  palette?: WorldPalette
  /** Cuenta propia para marcar lo comandable (C13); null si anónimo. */
  ownAccountId?: () => string | null
  /**
   * SIMPLIFICACIÓN v1 (aceptada): aún no existe store de red logística
   * (nodos/enlaces); el host los inyecta como provider opcional. Cuando la
   * capa de red aporte su store, este provider se cablea a ella y el bridge
   * no cambia.
   */
  getNetwork?: () => WorldNetwork
  /** Scheduler de coalescing (rAF por defecto; síncrono/manual en tests). */
  schedule?: (cb: () => void) => void
  /** Margen de culling como fracción del viewport por lado (0.25 = 25 %). */
  cullMarginFactor?: number
}

export interface WorldStateBridge {
  /** Viewport actual en mundo (lon/lat); null = sin culling (todo visible). */
  setViewport(bbox: WorldBbox | null): void
  /** Fuerza recomputación en el siguiente frame (p. ej. cambió la red inyectada). */
  refresh(): void
  /** Recomputación síncrona inmediata (primer frame y tests). */
  flush(): void
  dispose(): void
}

// ─── Helpers geométricos ─────────────────────────────────────────────────────

function expandBbox(bbox: WorldBbox, factor: number): WorldBbox {
  const dLon = (bbox.maxLon - bbox.minLon) * factor
  const dLat = (bbox.maxLat - bbox.minLat) * factor
  return {
    minLon: bbox.minLon - dLon,
    minLat: bbox.minLat - dLat,
    maxLon: bbox.maxLon + dLon,
    maxLat: bbox.maxLat + dLat
  }
}

function containsPoint(bbox: WorldBbox | null, lon: number, lat: number): boolean {
  if (bbox === null) return true
  return lon >= bbox.minLon && lon <= bbox.maxLon && lat >= bbox.minLat && lat <= bbox.maxLat
}

function intersects(bbox: WorldBbox | null, other: WorldBbox): boolean {
  if (bbox === null) return true
  return other.maxLon >= bbox.minLon && other.minLon <= bbox.maxLon && other.maxLat >= bbox.minLat && other.minLat <= bbox.maxLat
}

function coordsBbox(coords: readonly [number, number][]): WorldBbox {
  let minLon = Infinity
  let minLat = Infinity
  let maxLon = -Infinity
  let maxLat = -Infinity
  for (const [lon, lat] of coords) {
    if (lon < minLon) minLon = lon
    if (lon > maxLon) maxLon = lon
    if (lat < minLat) minLat = lat
    if (lat > maxLat) maxLat = lat
  }
  return { minLon, minLat, maxLon, maxLat }
}

function polygonCentroid(ring: readonly [number, number][]): [number, number] {
  if (ring.length === 0) return [0, 0]
  let lon = 0
  let lat = 0
  // Anillo GeoJSON cerrado: el último punto repite el primero — se omite.
  const n = ring.length > 1 ? ring.length - 1 : ring.length
  for (let i = 0; i < n; i++) {
    const p = ring[i] as [number, number]
    lon += p[0]
    lat += p[1]
  }
  return [lon / n, lat / n]
}

const defaultSchedule: (cb: () => void) => void =
  typeof requestAnimationFrame === 'function' ? (cb) => requestAnimationFrame(() => cb()) : (cb) => setTimeout(cb, 16)

// ─── Bridge ──────────────────────────────────────────────────────────────────

const KINDS: readonly EntityKind[] = ['region', 'city', 'deposit', 'node', 'link', 'building', 'vehicle']

export function createWorldBridge(deps: WorldBridgeDeps): WorldStateBridge {
  const palette = deps.palette ?? DEFAULT_PALETTE
  const schedule = deps.schedule ?? defaultSchedule
  const marginFactor = deps.cullMarginFactor ?? 0.25
  const project = deps.projection.worldToScreen.bind(deps.projection)

  let culled: WorldBbox | null = null
  /** Época: viewport/red/config cambió → re-emitir todo lo visible. */
  let epoch = 0

  /** Último origen (referencia del DTO) emitido por entidad — base del diff. */
  const prevSrc = new Map<EntityKind, Map<string, unknown>>(KINDS.map((k) => [k, new Map()]))
  const prevEpoch = new Map<EntityKind, Map<string, number>>(KINDS.map((k) => [k, new Map()]))

  let dirty = false
  let scheduled = false
  let disposed = false

  // ── Derivación de VMs ──

  function projectPath(coords: readonly [number, number][]): PathPoint[] {
    return coords.map(([lon, lat]) => project(lon, lat))
  }

  function deriveRegions(out: Map<string, { vm: RegionVM; src: unknown }>): void {
    for (const region of deps.stores.world.regionList as Region[]) {
      const ring = region.bounds?.coordinates[0]
      if (ring === undefined || ring.length === 0) continue // sin geometría no hay render v1
      const bb = coordsBbox(ring)
      if (!intersects(culled, bb)) continue
      const topLeft = project(bb.minLon, bb.maxLat)
      const bottomRight = project(bb.maxLon, bb.minLat)
      out.set(region.id, {
        src: region,
        vm: {
          id: region.id,
          x: topLeft.x,
          y: topLeft.y,
          width: bottomRight.x - topLeft.x,
          height: bottomRight.y - topLeft.y,
          fillColor: palette.regionFillByBiome[region.biome] ?? palette.regionStroke,
          strokeColor: palette.regionStroke,
          name: region.name
        }
      })
    }
  }

  function deriveCities(out: Map<string, { vm: CityVM; src: unknown }>): void {
    for (const city of deps.stores.cities.list) {
      const [lon, lat] = city.location.coordinates
      if (!containsPoint(culled, lon, lat)) continue
      const { x, y } = project(lon, lat)
      out.set(city.id, {
        src: city,
        vm: {
          id: city.id,
          x,
          y,
          radius: 6 + city.level * 2, // radio por nivel (presentación)
          color: palette.city,
          label: `${city.name} · N${city.level}`
        }
      })
    }
  }

  function deriveDeposits(out: Map<string, { vm: DepositVM; src: unknown }>): void {
    for (const deposit of Object.values(deps.stores.world.deposits)) {
      const [lon, lat] = deposit.location.coordinates
      if (!containsPoint(culled, lon, lat)) continue
      const { x, y } = project(lon, lat)
      out.set(deposit.id, {
        src: deposit,
        vm: { id: deposit.id, x, y, size: 7, color: palette.deposit }
      })
    }
  }

  function deriveNodes(network: WorldNetwork, out: Map<string, { vm: NodeVM; src: unknown }>): void {
    for (const node of network.nodes) {
      const [lon, lat] = node.location.coordinates
      if (!containsPoint(culled, lon, lat)) continue
      const { x, y } = project(lon, lat)
      out.set(node.id, { src: node, vm: { id: node.id, x, y, size: 3, color: palette.node } })
    }
  }

  function linkCongestion(link: NetworkLink): number {
    if (link.segments.length === 0) return 1
    let sum = 0
    for (const seg of link.segments) sum += seg.congestion_ema
    return sum / link.segments.length
  }

  function congestionColor(ema: number): number {
    const [ok, mid, bad] = palette.linkCongestion
    if (ema < 1.5) return ok
    if (ema < 2.5) return mid
    return bad
  }

  function linkPathCoords(link: NetworkLink, network: WorldNetwork): [number, number][] | null {
    if (link.path !== undefined && link.path.coordinates.length >= 2) return link.path.coordinates
    const from = network.nodes.find((n) => n.id === link.from_node_id)
    const to = network.nodes.find((n) => n.id === link.to_node_id)
    if (from === undefined || to === undefined) return null
    return [from.location.coordinates, to.location.coordinates]
  }

  function deriveLinks(network: WorldNetwork, out: Map<string, { vm: LinkVM; src: unknown }>): void {
    for (const link of network.links) {
      const coords = linkPathCoords(link, network)
      if (coords === null) continue
      if (!intersects(culled, coordsBbox(coords))) continue
      const ema = linkCongestion(link)
      out.set(link.id, {
        src: link,
        vm: {
          id: link.id,
          points: projectPath(coords),
          width: Math.min(6, 1.5 + (ema - 1) * 2), // grosor por congestión EMA
          color: congestionColor(ema)
        }
      })
    }
  }

  function deriveBuildings(out: Map<string, { vm: BuildingVM; src: unknown }>): void {
    const own = deps.ownAccountId?.() ?? null
    for (const building of deps.stores.buildings.list) {
      const ring = building.footprint.coordinates[0]
      if (ring === undefined || ring.length === 0) continue
      const [lon, lat] = polygonCentroid(ring)
      if (!containsPoint(culled, lon, lat)) continue
      const { x, y } = project(lon, lat)
      out.set(building.id, {
        src: building,
        vm: {
          id: building.id,
          x,
          y,
          size: 12,
          color: palette.buildingByStatus[building.status] ?? palette.buildingDefault,
          owned: own !== null && building.owner_account_id === own
        }
      })
    }
  }

  /**
   * Cinemática del vehículo (advance_fn + position del fleet.store).
   *
   * SIMPLIFICACIÓN v1 (aceptada, documentada):
   * - `segment_entered_sim` ≈ `updated_at_sim` del vehículo: los únicos
   *   escritores del vehículo en tránsito son los hitos
   *   (vehicle.segment_entered / departed / arrived, ws-protocol §5), así que
   *   la última escritura ES la entrada al tramo actual.
   * - Los LinkSegment del DTO v1.1.0 no llevan geometría propia: se usa el
   *   `path` del LINK como LineString del tramo y `duration_sim_seconds` se
   *   deriva analíticamente de length_m, base_speed_kmh y congestion_ema.
   * El servidor sigue siendo la única verdad del hito (P1): esto solo pinta.
   */
  function vehicleMotion(vehicle: Vehicle, network: WorldNetwork): VehicleMotion | null {
    const pos = vehicle.position

    if (pos.at_node_id !== undefined) {
      const node = network.nodes.find((n) => n.id === pos.at_node_id)
      const coords = node?.location.coordinates ?? pos.location?.coordinates
      if (coords === undefined) return null
      const { x, y } = project(coords[0], coords[1])
      return { kind: 'fixed', x, y }
    }

    if (pos.on_segment_id !== undefined) {
      const link = network.links.find((l) => l.segments.some((s) => s.id === pos.on_segment_id))
      const segment = link?.segments.find((s) => s.id === pos.on_segment_id)
      if (link !== undefined && segment !== undefined) {
        const coords = linkPathCoords(link, network)
        if (coords !== null) {
          const speedKmh = Math.max(1, link.base_speed_kmh)
          const durationSim = (segment.length_m / 1000 / speedKmh) * 3600 * Math.max(1, segment.congestion_ema)
          return {
            kind: 'path',
            points: projectPath(coords),
            enteredSim: vehicle.updated_at_sim ?? 0,
            durationSim,
            baseProgress: (pos.segment_progress_pct ?? 0) / 100
          }
        }
      }
    }

    // Fallback: el gateway ya deriva `location` para render.
    if (pos.location !== undefined) {
      const [lon, lat] = pos.location.coordinates
      const { x, y } = project(lon, lat)
      return { kind: 'fixed', x, y }
    }
    return null
  }

  function motionBboxVisible(vehicle: Vehicle, motion: VehicleMotion): boolean {
    if (culled === null) return true
    if (motion.kind === 'fixed') {
      // Culling en coords de mundo: usa la posición cruda del DTO si existe.
      const loc = vehicle.position.location?.coordinates
      if (loc !== undefined) return containsPoint(culled, loc[0], loc[1])
      // Sin lon/lat cruda (at_node): compara en px proyectando el viewport.
      return screenVisible(motion.x, motion.y)
    }
    // Path: bbox del LineString en px contra el viewport proyectado.
    let minX = Infinity
    let minY = Infinity
    let maxX = -Infinity
    let maxY = -Infinity
    for (const p of motion.points) {
      if (p.x < minX) minX = p.x
      if (p.x > maxX) maxX = p.x
      if (p.y < minY) minY = p.y
      if (p.y > maxY) maxY = p.y
    }
    const tl = project(culled.minLon, culled.maxLat)
    const br = project(culled.maxLon, culled.minLat)
    return maxX >= tl.x && minX <= br.x && maxY >= tl.y && minY <= br.y
  }

  function screenVisible(x: number, y: number): boolean {
    if (culled === null) return true
    const tl = project(culled.minLon, culled.maxLat)
    const br = project(culled.maxLon, culled.minLat)
    return x >= tl.x && x <= br.x && y >= tl.y && y <= br.y
  }

  function deriveVehicles(network: WorldNetwork, out: Map<string, { vm: VehicleVM; src: unknown }>): void {
    const own = deps.ownAccountId?.() ?? null
    for (const vehicle of deps.stores.fleet.list) {
      const motion = vehicleMotion(vehicle, network)
      if (motion === null) continue
      if (!motionBboxVisible(vehicle, motion)) continue
      const owned = own !== null && vehicle.owner_account_id === own
      out.set(vehicle.id, {
        src: vehicle,
        vm: {
          id: vehicle.id,
          color: owned ? palette.vehicleOwned : palette.vehicle,
          owned,
          motion
        }
      })
    }
  }

  // ── Diff + entrega al renderer ──

  function reconcile(kind: EntityKind, next: Map<string, { vm: AnyVM; src: unknown }>): void {
    const prev = prevSrc.get(kind) as Map<string, unknown>
    const prevEp = prevEpoch.get(kind) as Map<string, number>

    for (const [id, { vm, src }] of next) {
      // Upsert solo si la entidad es nueva, su DTO cambió (los patches
      // reemplazan el objeto entero) o cambió la época (viewport/red).
      if (!prev.has(id) || prev.get(id) !== src || prevEp.get(id) !== epoch) {
        deps.renderer.upsert(kind, vm as never)
      }
      prevEp.set(id, epoch)
    }
    for (const id of prev.keys()) {
      if (!next.has(id)) {
        deps.renderer.remove(kind, id)
        prevEp.delete(id)
      }
    }

    prev.clear()
    for (const [id, { src }] of next) prev.set(id, src)
  }

  function recompute(): void {
    if (disposed) return
    const network = deps.getNetwork?.() ?? { nodes: [], links: [] }

    const regions = new Map<string, { vm: RegionVM; src: unknown }>()
    const cities = new Map<string, { vm: CityVM; src: unknown }>()
    const depositsOut = new Map<string, { vm: DepositVM; src: unknown }>()
    const nodes = new Map<string, { vm: NodeVM; src: unknown }>()
    const links = new Map<string, { vm: LinkVM; src: unknown }>()
    const buildings = new Map<string, { vm: BuildingVM; src: unknown }>()
    const vehicles = new Map<string, { vm: VehicleVM; src: unknown }>()

    deriveRegions(regions)
    deriveCities(cities)
    deriveDeposits(depositsOut)
    deriveNodes(network, nodes)
    deriveLinks(network, links)
    deriveBuildings(buildings)
    deriveVehicles(network, vehicles)

    // Orden de capas de fondo a frente.
    reconcile('region', regions)
    reconcile('link', links)
    reconcile('node', nodes)
    reconcile('deposit', depositsOut)
    reconcile('city', cities)
    reconcile('building', buildings)
    reconcile('vehicle', vehicles)

    dirty = false
  }

  function markDirty(): void {
    if (disposed) return
    dirty = true
    if (scheduled) return
    scheduled = true
    schedule(() => {
      scheduled = false
      if (dirty) recompute()
    })
  }

  // ── Suscripciones (lectura de stores; escritura jamás — P1/O2) ──
  const unsubscribers: Array<() => void> = [
    // flush 'sync': cada acción apply* marca dirty al instante y el scheduler
    // coalesce a UNA recomputación por frame (determinista también en tests).
    deps.stores.world.$subscribe(() => markDirty(), { flush: 'sync', detached: true }),
    deps.stores.cities.$subscribe(() => markDirty(), { flush: 'sync', detached: true }),
    deps.stores.buildings.$subscribe(() => markDirty(), { flush: 'sync', detached: true }),
    deps.stores.fleet.$subscribe(() => markDirty(), { flush: 'sync', detached: true }),
    // Intent de cámara desde la UI: el bridge lo reenvía a la escena.
    deps.eventBus.on('camera:flyTo', ({ lon, lat }) => deps.renderer.flyTo(lon, lat))
  ]

  return {
    setViewport(bbox) {
      culled = bbox === null ? null : expandBbox(bbox, marginFactor)
      epoch++
      markDirty()
    },
    refresh() {
      epoch++
      markDirty()
    },
    flush() {
      recompute()
    },
    dispose() {
      disposed = true
      for (const unsub of unsubscribers) unsub()
    }
  }
}
