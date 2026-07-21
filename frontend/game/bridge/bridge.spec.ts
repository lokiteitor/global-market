import { describe, expect, it } from 'vitest'

import type { Vehicle } from '~domain/fleet'
import type { SimTime } from '~shared/simtime'

import { building, city, link, node, st, uid, vehicle, MY_ACCOUNT } from '../testing/fixtures'
import { WorldStateBridge } from './bridge'
import type { BridgeSinks, CameraPort } from './bridge'
import type { VmDiff } from './diff'
import type { SegmentContextInfo, WorldStateSource } from './source'
import type { WorldRectM } from './vm'

/** Sink de grabación: acumula los diffs aplicados. */
function recordingSink<VM>(): { apply(diff: VmDiff<VM>): void; calls: VmDiff<VM>[] } {
  const calls: VmDiff<VM>[] = []
  return {
    calls,
    apply: (diff) => {
      calls.push(diff)
    },
  }
}

interface Harness {
  bridge: WorldStateBridge
  sinks: {
    buildings: ReturnType<typeof recordingSink>
    cities: ReturnType<typeof recordingSink>
    deposits: ReturnType<typeof recordingSink>
    nodes: ReturnType<typeof recordingSink>
    links: ReturnType<typeof recordingSink>
    regions: ReturnType<typeof recordingSink>
    vehicles: ReturnType<typeof recordingSink>
  }
  world: {
    buildings: ReturnType<typeof building>[]
    vehicles: Vehicle[]
    simNow: SimTime
    view: WorldRectM
  }
  notify: () => void
}

function harness(): Harness {
  const world = {
    buildings: [building()],
    vehicles: [vehicle()],
    simNow: st(1_000),
    view: { xM: 0, yM: 0, widthM: 5_000, heightM: 5_000 },
  }
  const listeners = new Set<() => void>()
  const theLink = link()
  const context: SegmentContextInfo = { link: theLink, segment: theLink.segments[0]! }

  const source: WorldStateSource = {
    regions: () => [],
    cities: () => [city()],
    deposits: () => [],
    nodes: () => [node()],
    links: () => [theLink],
    buildings: () => world.buildings,
    vehicles: () => world.vehicles,
    concessions: () => [],
    buildingTypeCode: () => null,
    segmentContext: (id) => (id === context.segment.id ? context : null),
    ownAccountId: () => MY_ACCOUNT,
    simNow: () => world.simNow,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
  }

  const camera: CameraPort = {
    viewRectM: () => world.view,
    zoom: () => 1,
  }

  const sinks = {
    buildings: recordingSink(),
    cities: recordingSink(),
    deposits: recordingSink(),
    nodes: recordingSink(),
    links: recordingSink(),
    regions: recordingSink(),
    vehicles: recordingSink(),
  }

  const bridge = new WorldStateBridge({
    source,
    camera,
    sinks: sinks as unknown as BridgeSinks,
  })
  bridge.start()

  return {
    bridge,
    sinks,
    world,
    notify: () => {
      for (const listener of listeners) {
        listener()
      }
    },
  }
}

describe('game/bridge — WorldStateBridge (stores+viewport ⇒ upserts/removes)', () => {
  it('primer tick: upserts de todo lo visible', () => {
    const h = harness()
    h.bridge.tick()
    expect(h.sinks.buildings.calls).toHaveLength(1)
    expect(h.sinks.buildings.calls[0]?.upserts).toHaveLength(1)
    expect(h.sinks.cities.calls[0]?.upserts).toHaveLength(1)
    expect(h.sinks.nodes.calls[0]?.upserts).toHaveLength(1)
    expect(h.sinks.links.calls[0]?.upserts).toHaveLength(1)
    expect(h.sinks.vehicles.calls[0]?.upserts).toHaveLength(1)
    expect(h.bridge.stats()).toMatchObject({ buildings: 1, cities: 1, vehicles: 1 })
  })

  it('sin cambios ni movimiento de cámara: ticks posteriores no recomputan ni aplican nada', () => {
    const h = harness()
    h.bridge.tick()
    h.bridge.tick()
    h.bridge.tick()
    expect(h.bridge.stats().staticRecomputes).toBe(1)
    expect(h.sinks.buildings.calls).toHaveLength(1)
    // Vehículo quieto (at-node): sin diff de vehículos tampoco.
    expect(h.sinks.vehicles.calls).toHaveLength(1)
  })

  it('varios cambios de store entre frames coalescen en UNA recomputación en el siguiente tick', () => {
    const h = harness()
    h.bridge.tick()
    h.notify()
    h.notify()
    h.notify()
    expect(h.bridge.stats().staticRecomputes).toBe(1) // aún nada: no hubo tick
    h.bridge.tick()
    expect(h.bridge.stats().staticRecomputes).toBe(2)
  })

  it('mover el viewport fuera del área emite removes (culling)', () => {
    const h = harness()
    h.bridge.tick()
    h.world.view = { xM: 30_000, yM: 30_000, widthM: 5_000, heightM: 5_000 }
    h.bridge.tick()
    const last = h.sinks.buildings.calls.at(-1)
    expect(last?.removes).toEqual([building().id])
    expect(h.bridge.stats().buildings).toBe(0)
  })

  it('un cambio de store se refleja tras notify + tick (estado replicado ⇒ render)', () => {
    const h = harness()
    h.bridge.tick()
    h.world.buildings = [building({ status: 'damaged' })]
    h.notify()
    h.bridge.tick()
    const last = h.sinks.buildings.calls.at(-1)
    expect(last?.upserts).toHaveLength(1)
    expect((last?.upserts[0] as { status: string }).status).toBe('damaged')
  })

  it('los vehículos on-segment se recomputan CADA tick con el sim-now (posición analítica)', () => {
    const h = harness()
    h.world.vehicles = [
      vehicle({
        status: 'in_transit',
        position: {
          kind: 'on-segment',
          segmentId: uid<'Segment'>(110),
          progressPct: 0,
          locationM: null,
        },
        observedAtSim: st(1_000),
      }),
    ]
    h.bridge.tick()
    const first = h.sinks.vehicles.calls.at(-1)?.upserts[0] as { xM: number }
    expect(first.xM).toBeCloseTo(0)

    h.world.simNow = st(1_300) // 300 sim-s ⇒ 50% de 10 km
    h.bridge.tick()
    expect(h.sinks.vehicles.calls.length).toBeGreaterThan(1)
    const moved = h.sinks.vehicles.calls.at(-1)?.upserts[0] as { xM: number }
    expect(moved.xM).toBeCloseTo(5_000)
    // Y SIN recomputación de estáticos (no hubo dirty ni cambio de viewport).
    expect(h.bridge.stats().staticRecomputes).toBe(1)
  })

  it('destroy da de baja la suscripción a la fuente', () => {
    const h = harness()
    h.bridge.tick()
    h.bridge.destroy()
    h.notify()
    h.bridge.tick()
    expect(h.bridge.stats().staticRecomputes).toBe(1)
  })
})
