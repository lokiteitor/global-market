/**
 * network/transport/port — puerto `NetworkTransport` (FAD §4.4/§12.2, ADR-FE-004).
 *
 * Contrato de tiempo real que consume la capa de aplicación. La app y el
 * orquestador de sincronización (`sync.ts`) SOLO conocen este puerto; el
 * protocolo real del Gateway (ADR-023, docs/api/ws-protocol.md) vive
 * encapsulado en la ACL `gateway.adapter.ts`. Cambiar de transporte
 * (real ↔ mock guionizado) es configuración, no refactor.
 *
 * Modelo (ADR-023): NO hay snapshots por el socket — el bootstrap es REST a
 * partir del `watermark` del `joined`; el socket entrega deltas (`DomainEvent`)
 * con `seq` creciente. Ante hueco o reconexión, la aplicación re-sincroniza
 * por REST (el transporte solo LO SEÑALA: `onGap` / `onRejoined`).
 *
 * Este módulo es kernel framework-agnostic: sin Vue/Nuxt/Pinia y sin tocar
 * `WebSocket` global (SSR-safety) — el socket entra por el puerto secundario
 * `WebSocketFactory`, inyectado por la app (plugin client-only) o por un doble
 * guionizado en tests.
 */

import type { SimTime } from '~shared/simtime'

/**
 * Estado observable de la conexión de tiempo real (FAD §12.12):
 * - `connecting`   — primer arranque: socket + auth en curso.
 * - `open`         — sesión autenticada (`auth_ok`); fluyen frames.
 * - `reconnecting` — caída detectada: backoff + re-handshake en curso.
 * - `closed`       — cierre limpio o fallo fatal (auth inválida); no se reintenta.
 */
export type ConnectionState = 'connecting' | 'open' | 'reconnecting' | 'closed'

/**
 * Evento de dominio del outbox, ya traducido del frame `event` del Gateway
 * (ws-protocol.md §5). El `payload` viaja TAL CUAL se emitió (dinero/stock
 * como strings — jamás se convierte a number aquí): son los appliers de la
 * capa de aplicación quienes lo mapean a dominio con sus mappers.
 */
export interface DomainEvent {
  /** `seq` global del outbox (orden total; creciente dentro de la conexión). */
  readonly seq: number
  /** UUID del evento — clave de dedup para consumidores idempotentes (P6). */
  readonly eventId: string
  /** Tipo jerárquico con prefijo por agregado, p. ej. `contract.settled`. */
  readonly eventType: string
  /** Sim-time de emisión (segundos desde el génesis, kernel shared/simtime). */
  readonly simTime: SimTime
  /** Agregado afectado, p. ej. `contract`. */
  readonly aggregateType: string
  /** UUID del agregado afectado. */
  readonly aggregateId: string
  /** Payload crudo del evento; lo tipan los appliers, no el transporte. */
  readonly payload: unknown
}

/** Resultado del join a la room `corp`: último `seq` ya despachado. */
export interface JoinResult {
  /** Todo evento posterior llega con `seq > watermark` (ws-protocol.md §6). */
  readonly watermark: number
}

/**
 * Anomalía de secuencia detectada por el transporte (ws-protocol.md §6):
 * - `stale` — llegó un `seq` NO creciente (repetido o anterior al watermark);
 *   el evento se descarta (at-least-once ⇒ duplicado), pero se señala.
 * - `jump`  — llegó un `seq` con salto sobre el último visto; el socket no
 *   tiene replay: la aplicación debe marcar estado stale y re-pull por REST.
 */
export interface SequenceGap {
  readonly kind: 'stale' | 'jump'
  /** Último `seq` aplicado (o watermark del join si aún no llegó ninguno). */
  readonly lastSeq: number
  /** `seq` del frame que disparó la detección. */
  readonly receivedSeq: number
}

/** Baja de una suscripción a callbacks del puerto. */
export type Unsubscribe = () => void

/**
 * Puerto de transporte de tiempo real (ADR-FE-004). Implementaciones:
 * `GatewayTransportAdapter` (ACL del protocolo real) y dobles de test.
 */
export interface NetworkTransport {
  /**
   * Arranca el ciclo de vida de la conexión con el bearer del login REST.
   * No bloquea: el progreso se observa por `onStateChange`. Solo es válido
   * desde parado (`closed`/inicial); reconectar tras un fallo fatal (p. ej.
   * token caducado) requiere un `connect` nuevo con token fresco.
   */
  connect(token: string): void

  /**
   * Suscribe a la room `corp` (única del protocolo v1). Resuelve con el
   * `watermark` del `joined` — la señal de "bootstrap por REST desde aquí".
   * Puede llamarse antes de completar el handshake: el join se emite al
   * autenticar. Tras una reconexión el re-join es automático y el watermark
   * nuevo se publica por `onRejoined` (no por esta promesa).
   */
  joinCorp(): Promise<JoinResult>

  /** Baja voluntaria de la room `corp` (y del re-join automático). */
  leave(): void

  /**
   * Cierre limpio y definitivo (FAD §12.14): cancela reconexiones, cierra el
   * socket con código 1000 y deja el estado en `closed`. Idempotente.
   */
  close(): void

  /** Eventos de dominio en orden de llegada (deltas tras el watermark). */
  onEvent(callback: (event: DomainEvent) => void): Unsubscribe

  /** Cambios de estado de la conexión (solo transiciones, sin repetidos). */
  onStateChange(callback: (state: ConnectionState) => void): Unsubscribe

  /** Anomalía de `seq` detectada: la aplicación debe re-sincronizar por REST. */
  onGap(callback: (gap: SequenceGap) => void): Unsubscribe

  /**
   * Re-join automático completado tras una reconexión, con el watermark NUEVO
   * (equivalente al `Reconnected()` del SDK de bots): el estado local previo
   * es potencialmente stale y debe re-sincronizarse por REST.
   */
  onRejoined(callback: (result: JoinResult) => void): Unsubscribe
}

// ---------------------------------------------------------------------------
// Puerto secundario: fábrica de WebSocket (driven port de la ACL)
// ---------------------------------------------------------------------------

/**
 * Callbacks que la ACL entrega a la fábrica al crear un socket. Entregarlos
 * en la creación (en lugar de propiedades mutables `onmessage`…) evita la
 * ventana en la que un frame podría perderse y las trampas de varianza del
 * tipo `WebSocket` del DOM.
 */
export interface WsHandlers {
  /** El socket quedó abierto: la ACL enviará `auth` como PRIMER frame. */
  onOpen(): void
  /** Frame entrante; para el Gateway siempre texto JSON (`string`). */
  onMessage(data: unknown): void
  /** El socket se cerró (limpio o no), con el código WS de cierre. */
  onClose(code: number): void
  /** Error de bajo nivel; el cierre llega después por `onClose`. */
  onError(): void
}

/** Superficie mínima del socket que la ACL necesita de vuelta. */
export interface WsHandle {
  send(data: string): void
  close(code?: number, reason?: string): void
}

/**
 * Fábrica de sockets inyectada en la ACL (tests y SSR-safety: el kernel no
 * toca el global `WebSocket`). La implementación de producción (plugin
 * client-only) envuelve el `WebSocket` nativo:
 *
 * ```ts
 * const createSocket: WebSocketFactory = (url, handlers) => {
 *   const ws = new WebSocket(url)
 *   ws.onopen = () => handlers.onOpen()
 *   ws.onmessage = (event) => handlers.onMessage(event.data)
 *   ws.onclose = (event) => handlers.onClose(event.code)
 *   ws.onerror = () => handlers.onError()
 *   return ws
 * }
 * ```
 */
export type WebSocketFactory = (url: string, handlers: WsHandlers) => WsHandle
