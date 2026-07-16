/**
 * lib/net/sync.ts — pipeline de aplicación de frames del transporte a las
 * stores dueñas (FAD §12/§13, P1/P2/P6).
 *
 * Recibe snapshot/patch/message del puerto NetworkTransport y los enruta:
 *
 *   room `corp:`      → buildings, fleet (vehicles), shipments,
 *                       market (publications/contracts), finance (ledger_accounts)
 *   room `viewport:`  → cities, buildings, fleet (vehicles)
 *   room `alerts:` y messages → notifications + efectos declarados
 *     (acceptance.resolved → refresco de contratos; sim.frozen/resumed → SimClock)
 *
 * Garantías:
 *   - Idempotencia (P6): un snapshot reemplaza el subárbol de su room; los
 *     patches upsert/remove son idempotentes en las stores; un patch con seq
 *     ya visto (re-entrega) se descarta. No hay detección de huecos: tras una
 *     reconexión el re-join produce snapshot nuevo (resync por construcción).
 *   - Cero lógica económica: aquí solo se espeja lo recibido (P1).
 *
 * Las stores se inyectan por interfaz estrecha (P3): el pipeline no importa
 * Pinia y se prueba con stores reales o dobles en tests/net/sync.spec.ts.
 */
import type { PatchOp } from '../api/types'
import type { MessageFrame, NetworkTransport, PatchFrame, SnapshotFrame } from './transport'

// ─── Contratos mínimos con las stores dueñas ─────────────────────────────────

/** Store con estado replicado por rooms (acciones apply*, única vía de escritura). */
export interface ReplicaTarget {
  applySnapshot(room: string, data: Record<string, unknown>): void
  applyPatch(ops: readonly PatchOp[]): void
}

export type SyncNotificationLevel = 'info' | 'success' | 'warning' | 'error'

export interface NotificationsTarget {
  push(input: { level: SyncNotificationLevel; text: string; event?: string; simSeconds?: number }): unknown
}

export interface SimClockTarget {
  sync(simSeconds: number, frozen?: boolean, wallMs?: number): void
}

/** Efectos colaterales de messages; los cablea el plugin de red (sin REST aquí). */
export interface SyncEffects {
  /** `acceptance.resolved`: el aceptante refresca sus contratos por REST. */
  onAcceptanceResolved?(data: Record<string, unknown>): void
}

export interface SyncDeps {
  /** Dueños del estado de la room corp: (en este orden lógico). */
  corp: {
    buildings: ReplicaTarget
    fleet: ReplicaTarget
    shipments: ReplicaTarget
    market: ReplicaTarget
    finance: ReplicaTarget
  }
  /** Dueños del estado de las rooms viewport:. */
  viewport: {
    cities: ReplicaTarget
    buildings: ReplicaTarget
    fleet: ReplicaTarget
  }
  notifications: NotificationsTarget
  simClock: SimClockTarget
  effects?: SyncEffects
}

export interface SyncPipeline {
  /** Desconecta el pipeline del transporte (teardown/tests). */
  dispose(): void
  /** Aplicación directa de frames (además de la suscripción al transporte). */
  applySnapshot(frame: SnapshotFrame): void
  applyPatch(frame: PatchFrame): void
  applyMessage(frame: MessageFrame): void
}

// ─── Presentación de messages (catálogo ws-protocol.md §5) ───────────────────

const EVENT_LEVELS: Record<string, SyncNotificationLevel> = {
  'acceptance.resolved': 'success',
  'contract.confirmed': 'success',
  'contract.settled': 'success',
  'delivery.confirmed': 'info',
  'vehicle.broken': 'warning',
  'vehicle.repaired': 'info',
  'sim.frozen': 'warning',
  'sim.resumed': 'info',
  'city.level_changed': 'info',
  'city.demand_updated': 'info'
}

const EVENT_TEXTS: Record<string, string> = {
  'acceptance.resolved': 'Aceptación resuelta',
  'contract.confirmed': 'Contrato confirmado',
  'contract.settled': 'Contrato liquidado',
  'delivery.confirmed': 'Entrega confirmada',
  'vehicle.broken': 'Vehículo averiado',
  'vehicle.repaired': 'Vehículo reparado',
  'sim.frozen': 'Mundo pausado: ventana de mantenimiento (sim-time congelado)',
  'sim.resumed': 'Mundo reanudado: fin de la ventana de mantenimiento',
  'city.level_changed': 'Una ciudad ha cambiado de nivel',
  'city.demand_updated': 'Demanda de ciudad actualizada'
}

function describeMessage(frame: MessageFrame): { level: SyncNotificationLevel; text: string } {
  if (frame.event === 'acceptance.resolved') {
    // Solo presentación del resultado del sorteo (el sorteo es del servidor).
    const status = frame.data['status']
    if (status === 'served') return { level: 'success', text: 'Aceptación servida: contrato creado' }
    if (status === 'released') return { level: 'info', text: 'Aceptación no servida: garantía liberada' }
  }
  return {
    level: EVENT_LEVELS[frame.event] ?? 'info',
    text: EVENT_TEXTS[frame.event] ?? `Evento del mundo: ${frame.event}`
  }
}

// ─── Pipeline ────────────────────────────────────────────────────────────────

export function createSyncPipeline(transport: NetworkTransport, deps: SyncDeps): SyncPipeline {
  /** Último seq aplicado por room (base = seq del snapshot; re-entregas se descartan). */
  const lastSeqByRoom = new Map<string, number>()

  function targetsFor(room: string): ReplicaTarget[] {
    if (room.startsWith('corp:')) {
      const c = deps.corp
      return [c.buildings, c.fleet, c.shipments, c.market, c.finance]
    }
    if (room.startsWith('viewport:')) {
      const v = deps.viewport
      return [v.cities, v.buildings, v.fleet]
    }
    return [] // alerts: no lleva estado replicado (solo messages)
  }

  function applySnapshot(frame: SnapshotFrame): void {
    lastSeqByRoom.set(frame.room, frame.seq)
    for (const target of targetsFor(frame.room)) target.applySnapshot(frame.room, frame.data)
    // El snapshot trae sim-time autoritativo (no informa frozen: se conserva).
    deps.simClock.sync(frame.simSeconds)
  }

  function applyPatch(frame: PatchFrame): void {
    const last = lastSeqByRoom.get(frame.room)
    if (last !== undefined && frame.seq <= last) return // re-entrega: no-op (P6)
    lastSeqByRoom.set(frame.room, frame.seq)
    for (const target of targetsFor(frame.room)) target.applyPatch(frame.ops)
  }

  function applyMessage(frame: MessageFrame): void {
    // Efectos de sistema primero (el SimClock manda sobre la presentación).
    if (frame.event === 'sim.frozen') deps.simClock.sync(frame.simSeconds, true)
    else if (frame.event === 'sim.resumed') deps.simClock.sync(frame.simSeconds, false)
    else if (frame.event === 'acceptance.resolved') deps.effects?.onAcceptanceResolved?.(frame.data)

    const { level, text } = describeMessage(frame)
    deps.notifications.push({ level, text, event: frame.event, simSeconds: frame.simSeconds })
  }

  const unsubscribes = [transport.onSnapshot(applySnapshot), transport.onPatch(applyPatch), transport.onMessage(applyMessage)]

  return {
    dispose() {
      for (const unsubscribe of unsubscribes) unsubscribe()
      lastSeqByRoom.clear()
    },
    applySnapshot,
    applyPatch,
    applyMessage
  }
}
