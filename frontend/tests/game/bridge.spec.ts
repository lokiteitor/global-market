/**
 * Unit del WorldStateBridge con stores REALES de Pinia y renderer FAKE
 * inyectado: verifica culling por viewport + margen, diffs upsert/remove,
 * coalescing (máx 1 recomputación por frame) y pooling lógico (una entidad
 * que sale del viewport se retira, no se re-emite lo que no cambió).
 */
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import { createWorldBridge, type WorldNetwork, type WorldStateBridge } from '~/game/bridge'
import { DEFAULT_PALETTE } from '~/game/palette'
import type { AnyVM, EntityKind, VehicleVM, VMByKind, WorldRenderer } from '~/game/types'
import type { Building, City, NetworkLink, NetworkNode, Region, Vehicle } from '~/lib/api/types'
import { createEventBus, type AppEvents } from '~/lib/kernel/event-bus'
import { createProjection } from '~/lib/kernel/projection'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCitiesStore } from '~/stores/cities.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useWorldStore } from '~/stores/world.store'

// ─── Dobles de prueba ────────────────────────────────────────────────────────

interface Call {
  type: 'upsert' | 'remove'
  kind: EntityKind
  id: string
  vm?: AnyVM
}

class FakeRenderer implements WorldRenderer {
  calls: Call[] = []
  flyTos: Array<{ lon: number; lat: number }> = []

  upsert<K extends EntityKind>(kind: K, vm: VMByKind[K]): void {
    this.calls.push({ type: 'upsert', kind, id: vm.id, vm })
  }

  remove(kind: EntityKind, id: string): void {
    this.calls.push({ type: 'remove', kind, id })
  }

  flyTo(lon: number, lat: number): void {
    this.flyTos.push({ lon, lat })
  }

  reset(): void {
    this.calls = []
  }

  ofKind(kind: EntityKind, type: 'upsert' | 'remove'): Call[] {
    return this.calls.filter((c) => c.kind === kind && c.type === type)
  }
}

/** Scheduler manual: cuenta invocaciones y ejecuta bajo demanda (frame a frame). */
class ManualScheduler {
  pending: Array<() => void> = []
  scheduledCount = 0

  schedule = (cb: () => void): void => {
    this.scheduledCount++
    this.pending.push(cb)
  }

  runFrame(): void {
    const batch = this.pending
    this.pending = []
    for (const cb of batch) cb()
  }
}

// ─── Fixtures (formas DTO mínimas que el bridge lee) ─────────────────────────

function makeCity(id: string, lon: number, lat: number, level = 3): City {
  return {
    id,
    region_id: 'r1',
    account_id: 'acc-city',
    name: `City ${id}`,
    location: { type: 'Point', coordinates: [lon, lat] },
    level,
    population: 1000,
    supply_index: 1,
    influence_radius_m: 5000,
    base_salary: '100'
  } as unknown as City
}

function makeBuilding(id: string, lon: number, lat: number, status: string, owner: string): Building {
  const d = 0.01
  return {
    id,
    owner_account_id: owner,
    region_id: 'r1',
    concession_id: 'c1',
    building_type_id: 'bt1',
    footprint: {
      type: 'Polygon',
      coordinates: [
        [
          [lon - d, lat - d],
          [lon + d, lat - d],
          [lon + d, lat + d],
          [lon - d, lat + d],
          [lon - d, lat - d]
        ]
      ]
    },
    level: 1,
    status,
    condition_pct: 100,
    fuel_stock: '0'
  } as unknown as Building
}

function makeRegion(id: string, minLon: number, minLat: number, maxLon: number, maxLat: number, biome = 'plains'): Region {
  return {
    id,
    name: `Region ${id}`,
    grid_x: 0,
    grid_y: 0,
    biome,
    tax_rate_bp: 0,
    customs_rate_bp: 0,
    canon_base: '0',
    opened_at_sim: 0,
    bounds: {
      type: 'Polygon',
      coordinates: [
        [
          [minLon, minLat],
          [maxLon, minLat],
          [maxLon, maxLat],
          [minLon, maxLat],
          [minLon, minLat]
        ]
      ]
    }
  } as unknown as Region
}

const NODE_A: NetworkNode = {
  id: 'node-a',
  kind: 'warehouse',
  region_id: 'r1',
  location: { type: 'Point', coordinates: [1, 1] }
} as unknown as NetworkNode

const NODE_B: NetworkNode = {
  id: 'node-b',
  kind: 'city_gate',
  region_id: 'r1',
  location: { type: 'Point', coordinates: [2, 1] }
} as unknown as NetworkNode

// Link con path de 2 puntos y un segmento de 30 km a 60 km/h → 1800 s de sim.
const LINK_AB: NetworkLink = {
  id: 'link-ab',
  mode: 'road',
  from_node_id: 'node-a',
  to_node_id: 'node-b',
  path: { type: 'LineString', coordinates: [[1, 1], [2, 1]] },
  length_m: 30000,
  capacity_per_hour: 100,
  base_speed_kmh: 60,
  segments: [{ id: 'seg-1', region_id: 'r1', seq: 1, length_m: 30000, congestion_ema: 1, updated_at_sim: 0 }]
} as unknown as NetworkLink

const NETWORK: WorldNetwork = { nodes: [NODE_A, NODE_B], links: [LINK_AB] }

function makeVehicle(id: string, partial: Record<string, unknown>): Vehicle {
  return {
    id,
    vehicle_type_id: 'vt1',
    owner_account_id: 'me',
    status: 'in_transit',
    wear_pct: 0,
    fuel: '100',
    ...partial
  } as unknown as Vehicle
}

// ─── Arnés ───────────────────────────────────────────────────────────────────

const VIEWPORT = { minLon: 0, minLat: 0, maxLon: 4, maxLat: 4 }

interface Harness {
  renderer: FakeRenderer
  scheduler: ManualScheduler
  bridge: WorldStateBridge
  bus: ReturnType<typeof createEventBus<AppEvents>>
  stores: {
    world: ReturnType<typeof useWorldStore>
    cities: ReturnType<typeof useCitiesStore>
    buildings: ReturnType<typeof useBuildingsStore>
    fleet: ReturnType<typeof useFleetStore>
  }
}

function makeHarness(withNetwork = false): Harness {
  const renderer = new FakeRenderer()
  const scheduler = new ManualScheduler()
  const bus = createEventBus<AppEvents>()
  const stores = {
    world: useWorldStore(),
    cities: useCitiesStore(),
    buildings: useBuildingsStore(),
    fleet: useFleetStore()
  }
  const bridge = createWorldBridge({
    renderer,
    projection: createProjection(),
    stores,
    eventBus: bus,
    ownAccountId: () => 'me',
    schedule: scheduler.schedule,
    ...(withNetwork ? { getNetwork: () => NETWORK } : {})
  })
  bridge.setViewport(VIEWPORT)
  scheduler.runFrame()
  renderer.reset()
  scheduler.scheduledCount = 0
  return { renderer, scheduler, bridge, bus, stores }
}

beforeEach(() => {
  setActivePinia(createPinia())
})

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('game/bridge — culling por viewport + margen', () => {
  it('solo entrega las entidades dentro del viewport expandido', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', {
      cities: [makeCity('inside', 2, 2), makeCity('outside', 50, 50)]
    })
    h.bridge.flush()

    const upserts = h.renderer.ofKind('city', 'upsert')
    expect(upserts.map((c) => c.id)).toEqual(['inside'])
  })

  it('el margen del 25 % mantiene visibles entidades justo fuera del borde', () => {
    const h = makeHarness()
    // Viewport 0..4 con margen 25 % → 1..5 de margen: lon 4.5 entra, lon 6 no.
    h.stores.cities.applySnapshot('viewport:test', {
      cities: [makeCity('edge', 4.5, 2), makeCity('far', 6, 2)]
    })
    h.bridge.flush()

    expect(h.renderer.ofKind('city', 'upsert').map((c) => c.id)).toEqual(['edge'])
  })

  it('al mover el viewport se retira lo que sale y se incorpora lo que entra', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', {
      cities: [makeCity('west', 2, 2), makeCity('east', 50, 2)]
    })
    h.bridge.flush()
    h.renderer.reset()

    h.bridge.setViewport({ minLon: 48, minLat: 0, maxLon: 52, maxLat: 4 })
    h.scheduler.runFrame()

    expect(h.renderer.ofKind('city', 'remove').map((c) => c.id)).toEqual(['west'])
    expect(h.renderer.ofKind('city', 'upsert').map((c) => c.id)).toEqual(['east'])
  })
})

describe('game/bridge — diffs y pooling lógico', () => {
  it('no re-emite entidades sin cambios: solo el DTO que cambió', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', { cities: [makeCity('c1', 1, 1), makeCity('c2', 2, 2)] })
    h.bridge.flush()
    h.renderer.reset()

    // Patch upsert de UNA ciudad (entidad completa, idempotente).
    h.stores.cities.applyPatch([{ op: 'upsert', entity: 'city', id: 'c1', data: makeCity('c1', 1, 1, 5) }])
    h.scheduler.runFrame()

    const upserts = h.renderer.ofKind('city', 'upsert')
    expect(upserts.map((c) => c.id)).toEqual(['c1'])
    expect((upserts[0]?.vm as { label: string }).label).toContain('N5')
  })

  it('remove del patch → remove en el renderer (libera al pool, no destruye)', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', { cities: [makeCity('c1', 1, 1)] })
    h.bridge.flush()
    h.renderer.reset()

    h.stores.cities.applyPatch([{ op: 'remove', entity: 'city', id: 'c1' }])
    h.scheduler.runFrame()

    expect(h.renderer.ofKind('city', 'remove').map((c) => c.id)).toEqual(['c1'])
    expect(h.renderer.ofKind('city', 'upsert')).toHaveLength(0)
  })

  it('una entidad que sale y vuelve a entrar se re-upserta (reutilización, no duplicado)', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', { cities: [makeCity('c1', 2, 2)] })
    h.bridge.flush()

    h.bridge.setViewport({ minLon: 100, minLat: 100, maxLon: 104, maxLat: 104 })
    h.scheduler.runFrame()
    h.renderer.reset()

    h.bridge.setViewport(VIEWPORT)
    h.scheduler.runFrame()

    expect(h.renderer.ofKind('city', 'upsert').map((c) => c.id)).toEqual(['c1'])
  })
})

describe('game/bridge — coalescing (máx 1 recomputación por frame)', () => {
  it('varias mutaciones seguidas programan UNA sola recomputación', () => {
    const h = makeHarness()
    h.stores.cities.applySnapshot('viewport:test', { cities: [makeCity('c1', 1, 1)] })
    h.stores.buildings.applySnapshot('viewport:test', { buildings: [makeBuilding('b1', 2, 2, 'operational', 'me')] })
    h.stores.fleet.applySnapshot('viewport:test', { vehicles: [] })

    expect(h.scheduler.scheduledCount).toBe(1)
    h.scheduler.runFrame()
    expect(h.renderer.ofKind('city', 'upsert')).toHaveLength(1)
    expect(h.renderer.ofKind('building', 'upsert')).toHaveLength(1)
  })
})

describe('game/bridge — view-models de edificios (status y propiedad)', () => {
  it('colorea por status y marca owned solo lo de la cuenta propia (C13)', () => {
    const h = makeHarness()
    h.stores.buildings.applySnapshot('viewport:test', {
      buildings: [
        makeBuilding('mine', 1, 1, 'operational', 'me'),
        makeBuilding('theirs', 2, 2, 'seized', 'other')
      ]
    })
    h.bridge.flush()

    const byId = new Map(h.renderer.ofKind('building', 'upsert').map((c) => [c.id, c.vm as { color: number; owned: boolean }]))
    expect(byId.get('mine')).toMatchObject({ color: DEFAULT_PALETTE.buildingByStatus['operational'], owned: true })
    expect(byId.get('theirs')).toMatchObject({ color: DEFAULT_PALETTE.buildingByStatus['seized'], owned: false })
  })
})

describe('game/bridge — cinemática de vehículos (advance_fn + position)', () => {
  it('vehículo en segmento → motion path con entered/duración/base derivados', () => {
    const h = makeHarness(true)
    h.stores.fleet.applySnapshot('viewport:test', {
      vehicles: [
        makeVehicle('v1', {
          position: { on_segment_id: 'seg-1', segment_progress_pct: 40 },
          updated_at_sim: 5000
        })
      ]
    })
    h.bridge.flush()

    const [call] = h.renderer.ofKind('vehicle', 'upsert')
    const vm = call?.vm as VehicleVM
    expect(vm.owned).toBe(true)
    expect(vm.motion.kind).toBe('path')
    if (vm.motion.kind !== 'path') return
    expect(vm.motion.enteredSim).toBe(5000)
    expect(vm.motion.baseProgress).toBeCloseTo(0.4)
    // 30 km a 60 km/h con congestión 1 → 1800 s de sim.
    expect(vm.motion.durationSim).toBeCloseTo(1800)
    // LineString proyectado con la proyección del kernel (900 px/grado).
    expect(vm.motion.points).toEqual([
      { x: 900, y: -900 },
      { x: 1800, y: -900 }
    ])
  })

  it('vehículo at_node → motion fixed en la posición del nodo', () => {
    const h = makeHarness(true)
    h.stores.fleet.applySnapshot('viewport:test', {
      vehicles: [makeVehicle('v2', { status: 'idle', position: { at_node_id: 'node-b' } })]
    })
    h.bridge.flush()

    const [call] = h.renderer.ofKind('vehicle', 'upsert')
    const vm = call?.vm as VehicleVM
    expect(vm.motion).toEqual({ kind: 'fixed', x: 1800, y: -900 })
  })

  it('sin nodo conocido usa position.location derivada por el gateway', () => {
    const h = makeHarness()
    h.stores.fleet.applySnapshot('viewport:test', {
      vehicles: [makeVehicle('v3', { position: { location: { type: 'Point', coordinates: [1, 2] } } })]
    })
    h.bridge.flush()

    const [call] = h.renderer.ofKind('vehicle', 'upsert')
    expect((call?.vm as VehicleVM).motion).toEqual({ kind: 'fixed', x: 900, y: -1800 })
  })
})

describe('game/bridge — regiones y eventos de cámara', () => {
  it('proyecta el bbox del bounds y tinta por bioma', () => {
    const h = makeHarness()
    h.stores.world.setRegions([makeRegion('r1', 0, 0, 2, 2, 'desert')])
    h.bridge.flush()

    const [call] = h.renderer.ofKind('region', 'upsert')
    const vm = call?.vm as { x: number; y: number; width: number; height: number; fillColor: number }
    expect(vm).toMatchObject({ x: 0, y: -1800, width: 1800, height: 1800 })
    expect(vm.fillColor).toBe(DEFAULT_PALETTE.regionFillByBiome['desert'])
  })

  it("reenvía 'camera:flyTo' del bus al renderer", () => {
    const h = makeHarness()
    h.bus.emit('camera:flyTo', { lon: -3.7, lat: 40.4 })
    expect(h.renderer.flyTos).toEqual([{ lon: -3.7, lat: 40.4 }])
  })

  it('dispose() desengancha stores y bus', () => {
    const h = makeHarness()
    h.bridge.dispose()
    h.stores.cities.applySnapshot('viewport:test', { cities: [makeCity('c1', 1, 1)] })
    h.bus.emit('camera:flyTo', { lon: 1, lat: 1 })
    h.scheduler.runFrame()

    expect(h.renderer.calls).toHaveLength(0)
    expect(h.renderer.flyTos).toHaveLength(0)
  })
})
