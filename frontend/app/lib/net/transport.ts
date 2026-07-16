/**
 * lib/net/transport.ts — puerto NetworkTransport (FAD §4.4 / ADR-FE-004).
 *
 * Modelo de sincronización canónico independiente del cable: rooms (áreas de
 * interés), snapshot (estado autoritativo completo de una room), patch (delta
 * ordenado) y message (evento puntual). La aplicación y la UI solo conocen
 * este puerto (P3); el cable real es el WebSocket propio del Notification/
 * Event Gateway (specs/ws-protocol.md) absorbido por gateway-transport.ts
 * como ACL pura. Un transporte mock/falso implementa este mismo puerto en
 * tests (tests/net/*).
 */
import type { PatchOp } from '../api/types'

// ─── Frames del modelo de sincronización ─────────────────────────────────────

/** Estado completo y autoritativo de una room en el momento del join (seq base = 0). */
export interface SnapshotFrame {
  room: string
  seq: number
  simSeconds: number
  /** Claves por room: corp → buildings/vehicles/shipments/publications/contracts/ledger_accounts; viewport → cities/buildings/vehicles. */
  data: Record<string, unknown>
}

/** Delta ordenado (seq monotónico por conexión y room; upsert = entidad completa). */
export interface PatchFrame {
  room: string
  seq: number
  simSeconds: number
  ops: PatchOp[]
}

/** Evento puntual sin estado persistente (deriva de outbox.events). */
export interface MessageFrame {
  room: string
  event: string
  simSeconds: number
  data: Record<string, unknown>
}

// ─── Estado de conexión ──────────────────────────────────────────────────────

export type TransportState =
  | 'idle' // nunca conectado
  | 'connecting' // primer intento de conexión
  | 'authenticating' // socket abierto, hello enviado, esperando hello_ok
  | 'open' // hello_ok recibido; rooms unidas
  | 'reconnecting' // conexión perdida; backoff en curso
  | 'closed' // cierre ordenado (logout/teardown) o token rechazado

// ─── Puerto ──────────────────────────────────────────────────────────────────

export interface NetworkTransport {
  /** Abre (o reintenta) la conexión. Idempotente si ya está conectando/abierta. */
  connect(): void
  /** Cierre ordenado: sin reconexión automática; olvida las rooms activas. */
  close(): void
  /**
   * Suscribe a una room. Si la conexión está abierta envía el join ya; si no,
   * queda registrada y se (re)envía tras cada hello_ok (re-join automático,
   * ws-protocol.md §6). Una room `viewport:` REEMPLAZA cualquier viewport
   * anterior (solo hay un viewport activo por conexión, ws-protocol.md §2).
   */
  join(room: string): void
  leave(room: string): void
  /** Rooms actualmente deseadas (las que se re-unen tras una reconexión). */
  rooms(): readonly string[]
  connectionState(): TransportState

  /** Suscripciones; devuelven la función de desuscripción. */
  onSnapshot(handler: (frame: SnapshotFrame) => void): () => void
  onPatch(handler: (frame: PatchFrame) => void): () => void
  onMessage(handler: (frame: MessageFrame) => void): () => void
  onStateChange(handler: (state: TransportState) => void): () => void
}

// ─── Nombres de room (specs/ws-protocol.md §3) ───────────────────────────────

export interface ViewportBBox {
  minLon: number
  minLat: number
  maxLon: number
  maxLat: number
}

export function corpRoom(accountId: string): string {
  return `corp:${accountId}`
}

export function viewportRoom(bbox: ViewportBBox): string {
  return `viewport:${bbox.minLon},${bbox.minLat},${bbox.maxLon},${bbox.maxLat}`
}

export function alertsRoom(accountId: string): string {
  return `alerts:${accountId}`
}

export function isViewportRoom(room: string): boolean {
  return room.startsWith('viewport:')
}
