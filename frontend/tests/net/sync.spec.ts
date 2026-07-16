/**
 * tests/net/sync.spec.ts — pipeline de sincronización sobre las stores REALES
 * (Pinia): snapshot reemplaza subárbol por room, patches upsert/remove
 * idempotentes con dedup por seq, y messages → notifications + efectos.
 * El transporte es un doble del puerto NetworkTransport (sin WS real).
 */
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import type { Building, City, Contract, LedgerAccount, Publication, Shipment, Vehicle } from '~/lib/api/types'
import { createSyncPipeline, type SyncDeps } from '~/lib/net/sync'
import type { MessageFrame, NetworkTransport, PatchFrame, SnapshotFrame } from '~/lib/net/transport'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCitiesStore } from '~/stores/cities.store'
import { useFinanceStore } from '~/stores/finance.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useShipmentsStore } from '~/stores/shipments.store'

// ─── Doble del transporte (implementa el puerto) ─────────────────────────────

function createFakeTransport() {
  const snapshotHandlers = new Set<(f: SnapshotFrame) => void>()
  const patchHandlers = new Set<(f: PatchFrame) => void>()
  const messageHandlers = new Set<(f: MessageFrame) => void>()

  const transport: NetworkTransport = {
    connect() {},
    close() {},
    join() {},
    leave() {},
    rooms: () => [],
    connectionState: () => 'open',
    onSnapshot(h) {
      snapshotHandlers.add(h)
      return () => snapshotHandlers.delete(h)
    },
    onPatch(h) {
      patchHandlers.add(h)
      return () => patchHandlers.delete(h)
    },
    onMessage(h) {
      messageHandlers.add(h)
      return () => messageHandlers.delete(h)
    },
    onStateChange() {
      return () => {}
    }
  }

  return {
    transport,
    emitSnapshot: (f: SnapshotFrame) => snapshotHandlers.forEach((h) => h(f)),
    emitPatch: (f: PatchFrame) => patchHandlers.forEach((h) => h(f)),
    emitMessage: (f: MessageFrame) => messageHandlers.forEach((h) => h(f))
  }
}

// ─── Entidades sintéticas mínimas (solo los campos que usan las stores) ──────

const building = (id: string, owner = 'acc-1'): Building =>
  ({ id, owner_account_id: owner, region_id: 'reg-1', status: 'operational' }) as unknown as Building

const vehicle = (id: string, status = 'idle'): Vehicle =>
  ({ id, owner_account_id: 'acc-1', status, position: {} }) as unknown as Vehicle

const shipment = (id: string): Shipment => ({ id, owner_account_id: 'acc-1', status: 'in_transit' }) as unknown as Shipment

const publication = (id: string, status = 'open'): Publication => ({ id, status, kind: 'sell' }) as unknown as Publication

const contract = (id: string, status = 'active'): Contract => ({ id, status }) as unknown as Contract

const ledgerAccount = (id: string, balance = '100'): LedgerAccount => ({ id, kind: 'cash', balance }) as unknown as LedgerAccount

const city = (id: string): City => ({ id, region_id: 'reg-1', name: `City ${id}`, level: 1 }) as unknown as City

// ─── Arnés ───────────────────────────────────────────────────────────────────

const CORP = 'corp:acc-1'
const VIEWPORT = 'viewport:0,0,1,1'
const ALERTS = 'alerts:acc-1'

function setup() {
  setActivePinia(createPinia())
  const buildings = useBuildingsStore()
  const fleet = useFleetStore()
  const shipments = useShipmentsStore()
  const market = useMarketStore()
  const finance = useFinanceStore()
  const cities = useCitiesStore()
  const notifications = useNotificationsStore()

  const simSyncs: Array<{ simSeconds: number; frozen: boolean | undefined }> = []
  const resolvedAcceptances: Array<Record<string, unknown>> = []

  const deps: SyncDeps = {
    corp: { buildings, fleet, shipments, market, finance },
    viewport: { cities, buildings, fleet },
    notifications,
    simClock: { sync: (simSeconds, frozen) => simSyncs.push({ simSeconds, frozen }) },
    effects: { onAcceptanceResolved: (data) => resolvedAcceptances.push(data) }
  }

  const fake = createFakeTransport()
  const pipeline = createSyncPipeline(fake.transport, deps)

  return { buildings, fleet, shipments, market, finance, cities, notifications, simSyncs, resolvedAcceptances, fake, pipeline }
}

function corpSnapshot(seq = 0, simSeconds = 1000): SnapshotFrame {
  return {
    room: CORP,
    seq,
    simSeconds,
    data: {
      buildings: [building('b1'), building('b2')],
      vehicles: [vehicle('v1')],
      shipments: [shipment('s1')],
      publications: [publication('p1')],
      contracts: [contract('c1')],
      ledger_accounts: [ledgerAccount('l1')]
    }
  }
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('sync — snapshots', () => {
  it('un snapshot corp: puebla las stores dueñas y sincroniza el SimClock', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())

    expect(Object.keys(t.buildings.byId).sort()).toEqual(['b1', 'b2'])
    expect(t.fleet.byId['v1']).toBeDefined()
    expect(t.shipments.byId['s1']).toBeDefined()
    expect(t.market.myPublications.byId['p1']).toBeDefined()
    expect(t.market.contractsById['c1']).toBeDefined()
    expect(t.finance.accountsById['l1']).toBeDefined()
    expect(t.simSyncs).toEqual([{ simSeconds: 1000, frozen: undefined }])
  })

  it('re-aplicar el mismo snapshot es idempotente (P6)', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())
    const before = JSON.parse(JSON.stringify(t.buildings.collection))
    t.fake.emitSnapshot(corpSnapshot())
    expect(JSON.parse(JSON.stringify(t.buildings.collection))).toEqual(before)
  })

  it('un snapshot nuevo de la room REEMPLAZA su subárbol (resync tras reconexión)', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())
    // Tras reconectar, el mundo cambió: b2 ya no existe, aparece b3.
    t.fake.emitSnapshot({
      room: CORP,
      seq: 0,
      simSeconds: 2000,
      data: { buildings: [building('b1'), building('b3')] }
    })
    expect(Object.keys(t.buildings.byId).sort()).toEqual(['b1', 'b3'])
  })

  it('un snapshot viewport: puebla ciudades/edificios/vehículos sin tocar lo corp', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())
    t.fake.emitSnapshot({
      room: VIEWPORT,
      seq: 0,
      simSeconds: 1500,
      data: { cities: [city('city1')], buildings: [building('b9', 'acc-2')], vehicles: [vehicle('v9')] }
    })

    expect(t.cities.byId['city1']).toBeDefined()
    expect(t.buildings.byId['b9']).toBeDefined()
    expect(t.buildings.byId['b1']).toBeDefined() // lo corp sigue
    // El ledger no recibe nada del viewport.
    expect(Object.keys(t.finance.accountsById)).toEqual(['l1'])
  })
})

describe('sync — patches', () => {
  it('upsert añade/actualiza y remove borra; ambos idempotentes', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())

    t.fake.emitPatch({
      room: CORP,
      seq: 1,
      simSeconds: 1100,
      ops: [
        { op: 'upsert', entity: 'vehicle', id: 'v2', data: vehicle('v2', 'in_transit') },
        { op: 'remove', entity: 'building', id: 'b2' },
        { op: 'upsert', entity: 'ledger_account', id: 'l1', data: ledgerAccount('l1', '250') }
      ]
    })

    expect(t.fleet.byId['v2']?.status).toBe('in_transit')
    expect(t.buildings.byId['b2']).toBeUndefined()
    expect(t.finance.accountsById['l1']?.balance).toBe('250')

    // remove de algo inexistente: tolerante, no lanza ni corrompe.
    t.fake.emitPatch({
      room: CORP,
      seq: 2,
      simSeconds: 1200,
      ops: [{ op: 'remove', entity: 'building', id: 'no-existe' }]
    })
    expect(Object.keys(t.buildings.byId).sort()).toEqual(['b1'])
  })

  it('un patch re-entregado (seq ya visto) es un no-op', () => {
    const t = setup()
    t.fake.emitSnapshot(corpSnapshot())

    const patch: PatchFrame = {
      room: CORP,
      seq: 1,
      simSeconds: 1100,
      ops: [{ op: 'upsert', entity: 'ledger_account', id: 'l1', data: ledgerAccount('l1', '250') }]
    }
    t.fake.emitPatch(patch)

    // Re-entrega del mismo seq con datos VIEJOS: debe descartarse.
    t.fake.emitPatch({
      room: CORP,
      seq: 1,
      simSeconds: 1100,
      ops: [{ op: 'upsert', entity: 'ledger_account', id: 'l1', data: ledgerAccount('l1', '100') }]
    })
    expect(t.finance.accountsById['l1']?.balance).toBe('250')

    // El snapshot resetea la base de seq de la room (nueva conexión lógica).
    t.fake.emitSnapshot(corpSnapshot(0, 3000))
    t.fake.emitPatch(patch) // seq 1 vuelve a ser válido tras el resync
    expect(t.finance.accountsById['l1']?.balance).toBe('250')
  })

  it('los patches de viewport actualizan ciudades', () => {
    const t = setup()
    t.fake.emitSnapshot({ room: VIEWPORT, seq: 0, simSeconds: 100, data: { cities: [city('city1')] } })
    t.fake.emitPatch({
      room: VIEWPORT,
      seq: 1,
      simSeconds: 200,
      ops: [
        { op: 'upsert', entity: 'city', id: 'city2', data: city('city2') },
        { op: 'remove', entity: 'city', id: 'city1' }
      ]
    })
    expect(Object.keys(t.cities.byId)).toEqual(['city2'])
  })
})

describe('sync — messages', () => {
  it('acceptance.resolved notifica y dispara el efecto de refresco de contratos', () => {
    const t = setup()
    t.fake.emitMessage({
      room: ALERTS,
      event: 'acceptance.resolved',
      simSeconds: 5000,
      data: { acceptance_id: 'a1', status: 'served', contract_id: 'c9' }
    })

    expect(t.resolvedAcceptances).toEqual([{ acceptance_id: 'a1', status: 'served', contract_id: 'c9' }])
    const last = t.notifications.items[0]
    expect(last?.event).toBe('acceptance.resolved')
    expect(last?.level).toBe('success')
    expect(last?.simSeconds).toBe(5000)
  })

  it('sim.frozen / sim.resumed gobiernan el SimClock y notifican', () => {
    const t = setup()
    t.fake.emitMessage({ room: ALERTS, event: 'sim.frozen', simSeconds: 6000, data: {} })
    t.fake.emitMessage({ room: ALERTS, event: 'sim.resumed', simSeconds: 6000, data: {} })

    expect(t.simSyncs).toEqual([
      { simSeconds: 6000, frozen: true },
      { simSeconds: 6000, frozen: false }
    ])
    expect(t.notifications.items).toHaveLength(2)
  })

  it('dispose() desconecta el pipeline del transporte', () => {
    const t = setup()
    t.pipeline.dispose()
    t.fake.emitSnapshot(corpSnapshot())
    expect(Object.keys(t.buildings.byId)).toEqual([])
  })
})
