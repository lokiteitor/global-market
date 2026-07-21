/**
 * network/transport/gateway.adapter — ACL del Notification/Event Gateway
 * (ADR-FE-004; protocolo: ADR-023 + docs/api/ws-protocol.md, fiel frame a frame).
 *
 * Traduce el protocolo real del Gateway al puerto `NetworkTransport`:
 *
 * - Handshake (§3): upgrade → `auth` como PRIMER frame → `auth_ok` →
 *   `join {room:"corp"}` → `joined {watermark}` → flujo de frames `event`.
 * - Cada frame `event` (§5) se valida y mapea a `DomainEvent`; un frame fuera
 *   de contrato NO cruza la frontera (se descarta y se reporta como violación
 *   de protocolo) — misma disciplina que los mappers REST (O5, FAD §9.5).
 * - Secuencia (§6): `seq` estrictamente creciente dentro de la conexión desde
 *   el watermark. `seq` no creciente ⇒ duplicado at-least-once: se descarta y
 *   se señala `onGap {kind:'stale'}`. Salto de `seq` ⇒ `onGap {kind:'jump'}`
 *   y el evento SÍ se entrega (perderlo agravaría el hueco). Nota (§6): los
 *   `seq` son globales del outbox — entre eventos propios puede haber `seq`
 *   intermedios ajenos, así que `jump` es una señal conservadora: decidir el
 *   re-pull es de la capa de aplicación (sync.ts), no de la ACL.
 * - Reconexión (§6, FAD §12.12): ante cierre no solicitado (1013 consumidor
 *   lento, 1006, caída de red…), backoff exponencial con jitter (1 s → 30 s)
 *   y re-handshake completo; si había join activo, re-join automático
 *   publicando el watermark NUEVO por `onRejoined` (el orquestador decide
 *   re-sincronizar por REST). Las promesas de `joinCorp` pendientes
 *   sobreviven a la reconexión y resuelven con el `joined` que llegue.
 * - Fatales: cierre `4401` (auth requerida/inválida) y frames `error` con
 *   `UNAUTHORIZED`/`TOO_MANY_CONNECTIONS` NO se reintentan — reintentar con
 *   las mismas credenciales solo repite el fallo. Estado `closed`; la app
 *   renueva sesión (REST) y llama a `connect` de nuevo.
 * - Ping/pong de aplicación (§4/§5): `ping {nonce}` ↔ `pong` de eco, expuesto
 *   como diagnóstico (RTT, FAD §21.10); el keepalive de protocolo (ping WS
 *   nativo) lo lleva el servidor y no aparece como frame JSON.
 *
 * Los importes/cantidades del `payload` viajan como strings del contrato y se
 * entregan tal cual (`unknown`): esta capa jamás los convierte a number (C11).
 */

import { isUuid } from '~shared/ids'
import { simTime } from '~shared/simtime'
import type { AppError } from '../rest/errors'
import { AppError as TypedAppError, appErrorFromProtocol } from '../rest/errors'
import type {
  ConnectionState,
  DomainEvent,
  JoinResult,
  NetworkTransport,
  SequenceGap,
  Unsubscribe,
  WebSocketFactory,
  WsHandle,
} from './port'

/** Room única del protocolo v1 (ws-protocol.md §7). */
const CORP_ROOM = 'corp'

/** Cierre WS: autenticación requerida o token inválido (ws-protocol.md §5). */
const WS_CLOSE_UNAUTHORIZED = 4401

/** Backoff de reconexión (FAD §12.12): 1 s → 30 s, exponencial + jitter. */
const DEFAULT_RECONNECT_BASE_MS = 1_000
const DEFAULT_RECONNECT_MAX_MS = 30_000

export interface GatewayTransportOptions {
  /** URL del endpoint WS, p. ej. `ws://localhost:8080/api/v1/ws` (§2). */
  readonly url: string
  /** Fábrica de sockets (puerto secundario): nativa en prod, doble en tests. */
  readonly createSocket: WebSocketFactory
  /** Base del backoff exponencial en ms (default 1000 — "1s" del mandato). */
  readonly reconnectBaseMs?: number
  /** Techo del backoff en ms (default 30000 — "30s" del mandato). */
  readonly reconnectMaxMs?: number
  /** Fuente de aleatoriedad del jitter, inyectable en tests (default Math.random). */
  readonly random?: () => number
  /**
   * Frame del servidor fuera de contrato (JSON inválido, `event` malformado,
   * `error` de protocolo…): se reporta aquí como AppError `protocol` y se
   * descarta — nunca rompe el flujo ni cruza la frontera (FAD §9.5).
   */
  readonly onProtocolViolation?: (error: AppError) => void
}

/**
 * Superficie del adaptador real: el puerto `NetworkTransport` más el ping de
 * aplicación como diagnóstico (no forma parte del puerto — la app no lo
 * necesita para sincronizar).
 */
export interface GatewayTransport extends NetworkTransport {
  /** Envía `ping {nonce}` y resuelve al recibir el `pong` de eco (§4/§5). */
  ping(): Promise<void>
}

interface PendingJoin {
  readonly resolve: (result: JoinResult) => void
  readonly reject: (error: AppError) => void
}

interface PendingPing {
  readonly nonce: string
  readonly resolve: () => void
  readonly reject: (error: AppError) => void
}

/** Lectura defensiva de un frame JSON del servidor (forma runtime). */
function readFrame(data: unknown): Record<string, unknown> | null {
  if (typeof data !== 'string') {
    return null
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(data)
  } catch {
    return null
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    return null
  }
  return parsed as Record<string, unknown>
}

/** Entero seguro ≥ 0 (forma de `seq`, `watermark`, `sim_time` en el contrato). */
function isNonNegativeInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

/**
 * Mapea un frame `event` (ws-protocol.md §5) a `DomainEvent`. Devuelve un
 * string de diagnóstico si el frame viola el contrato (campo ausente o mal
 * tipado): el evento NO se entrega en ese caso.
 */
function mapEventFrame(frame: Record<string, unknown>): DomainEvent | string {
  const seq = frame['seq']
  if (!isNonNegativeInt(seq)) {
    return `event.seq no es entero >= 0 (${JSON.stringify(frame['seq'])})`
  }
  const eventId = frame['event_id']
  if (typeof eventId !== 'string' || !isUuid(eventId)) {
    return `event.event_id no es un UUID (${JSON.stringify(frame['event_id'])})`
  }
  const eventType = frame['event_type']
  if (typeof eventType !== 'string' || eventType.length === 0) {
    return 'event.event_type vacío o ausente'
  }
  const rawSimTime = frame['sim_time']
  if (!isNonNegativeInt(rawSimTime)) {
    return `event.sim_time no es entero >= 0 (${JSON.stringify(frame['sim_time'])})`
  }
  const aggregateType = frame['aggregate_type']
  if (typeof aggregateType !== 'string' || aggregateType.length === 0) {
    return 'event.aggregate_type vacío o ausente'
  }
  const aggregateId = frame['aggregate_id']
  if (typeof aggregateId !== 'string' || !isUuid(aggregateId)) {
    return `event.aggregate_id no es un UUID (${JSON.stringify(frame['aggregate_id'])})`
  }
  return {
    seq,
    eventId: eventId.toLowerCase(),
    eventType,
    simTime: simTime(rawSimTime),
    aggregateType,
    aggregateId: aggregateId.toLowerCase(),
    payload: frame['payload'],
  }
}

/** Error tipado para fallos de la sesión WS (misma taxonomía que REST, §13.7). */
function wsError(code: 'UNAUTHORIZED' | 'INTERNAL', message: string): AppError {
  return new TypedAppError({ kind: 'network', code, status: 0, message })
}

export function createGatewayTransport(options: GatewayTransportOptions): GatewayTransport {
  const baseMs = options.reconnectBaseMs ?? DEFAULT_RECONNECT_BASE_MS
  const maxMs = options.reconnectMaxMs ?? DEFAULT_RECONNECT_MAX_MS
  const random = options.random ?? Math.random

  // --- estado de conexión -------------------------------------------------
  let state: ConnectionState = 'closed'
  let token: string | null = null
  let socket: WsHandle | null = null
  /** Sella callbacks de sockets viejos (reconexión, cierre): solo el socket vigente habla. */
  let generation = 0
  let authed = false
  let reconnectAttempt = 0
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null

  // --- estado de la room corp ---------------------------------------------
  let wantsJoin = false
  let joinInFlight = false
  let everJoined = false
  /** Último `seq` visto (arranca en el watermark del `joined`); null = sin join. */
  let lastSeq: number | null = null
  let pendingJoins: PendingJoin[] = []
  let pendingPings: PendingPing[] = []
  let pingNonce = 0

  // --- suscriptores --------------------------------------------------------
  const eventCallbacks = new Set<(event: DomainEvent) => void>()
  const stateCallbacks = new Set<(next: ConnectionState) => void>()
  const gapCallbacks = new Set<(gap: SequenceGap) => void>()
  const rejoinedCallbacks = new Set<(result: JoinResult) => void>()

  function setState(next: ConnectionState): void {
    if (next === state) {
      return
    }
    state = next
    for (const callback of stateCallbacks) {
      callback(next)
    }
  }

  function reportViolation(description: string): void {
    options.onProtocolViolation?.(appErrorFromProtocol(description))
  }

  function clearReconnectTimer(): void {
    if (reconnectTimer !== null) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  function rejectPendingJoins(error: AppError): void {
    const joins = pendingJoins
    pendingJoins = []
    for (const pending of joins) {
      pending.reject(error)
    }
  }

  function rejectPendingPings(error: AppError): void {
    const pings = pendingPings
    pendingPings = []
    for (const pending of pings) {
      pending.reject(error)
    }
  }

  /** Cierre definitivo (voluntario o fatal): sin reconexión posterior. */
  function shutdown(error: AppError): void {
    generation += 1 // los callbacks del socket vigente quedan sellados
    clearReconnectTimer()
    const current = socket
    socket = null
    authed = false
    joinInFlight = false
    rejectPendingJoins(error)
    rejectPendingPings(error)
    current?.close(1000)
    setState('closed')
  }

  function sendFrame(frame: Record<string, unknown>): void {
    socket?.send(JSON.stringify(frame))
  }

  function sendJoin(): void {
    if (!authed || joinInFlight) {
      return
    }
    joinInFlight = true
    sendFrame({ type: 'join', room: CORP_ROOM })
  }

  /**
   * Backoff exponencial + jitter aditivo, acotado a [base, max] (mandato
   * "1s..30s"): intento n ∈ [base·2ⁿ⁻¹, base·2ⁿ) con techo `max`.
   */
  function reconnectDelayMs(attempt: number): number {
    const raw = Math.min(maxMs, baseMs * 2 ** (attempt - 1))
    return Math.min(maxMs, raw + Math.floor(random() * raw))
  }

  function scheduleReconnect(): void {
    setState('reconnecting')
    reconnectAttempt += 1
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      openSocket()
    }, reconnectDelayMs(reconnectAttempt))
  }

  function handleAuthOk(): void {
    authed = true
    reconnectAttempt = 0 // handshake completo: el backoff arranca de cero
    setState('open')
    if (wantsJoin) {
      sendJoin()
    }
  }

  function handleJoined(frame: Record<string, unknown>): void {
    if (frame['room'] !== CORP_ROOM) {
      reportViolation(`joined.room desconocida (${JSON.stringify(frame['room'])})`)
      return
    }
    const watermark = frame['watermark']
    if (!isNonNegativeInt(watermark)) {
      reportViolation(`joined.watermark no es entero >= 0 (${JSON.stringify(frame['watermark'])})`)
      return
    }
    joinInFlight = false
    lastSeq = watermark
    const result: JoinResult = { watermark }
    const joins = pendingJoins
    pendingJoins = []
    for (const pending of joins) {
      pending.resolve(result)
    }
    if (everJoined) {
      // Re-join automático tras reconexión: watermark NUEVO → la aplicación
      // debe re-sincronizar por REST (ws-protocol.md §6).
      for (const callback of rejoinedCallbacks) {
        callback(result)
      }
    }
    everJoined = true
  }

  function emitGap(gap: SequenceGap): void {
    for (const callback of gapCallbacks) {
      callback(gap)
    }
  }

  function handleEvent(frame: Record<string, unknown>): void {
    const mapped = mapEventFrame(frame)
    if (typeof mapped === 'string') {
      reportViolation(mapped)
      return
    }
    if (lastSeq === null) {
      // Contrato §3: los `event` solo fluyen tras el `joined` de la room.
      reportViolation(`frame event antes del joined (seq ${String(mapped.seq)})`)
      return
    }
    if (mapped.seq <= lastSeq) {
      // At-least-once (§6): duplicado o regresión — no se re-entrega.
      emitGap({ kind: 'stale', lastSeq, receivedSeq: mapped.seq })
      return
    }
    if (mapped.seq > lastSeq + 1) {
      // Salto de secuencia: posible hueco (no hay replay por el socket).
      emitGap({ kind: 'jump', lastSeq, receivedSeq: mapped.seq })
    }
    lastSeq = mapped.seq
    for (const callback of eventCallbacks) {
      callback(mapped)
    }
  }

  function handlePong(frame: Record<string, unknown>): void {
    const nonce = frame['nonce']
    if (typeof nonce !== 'string') {
      reportViolation(`pong.nonce no es string (${JSON.stringify(frame['nonce'])})`)
      return
    }
    const index = pendingPings.findIndex((pending) => pending.nonce === nonce)
    if (index === -1) {
      return // pong sin ping propio: eco tardío inocuo
    }
    const [resolved] = pendingPings.splice(index, 1)
    resolved?.resolve()
  }

  function handleErrorFrame(frame: Record<string, unknown>): void {
    const code = typeof frame['code'] === 'string' ? frame['code'] : 'INTERNAL'
    const message = typeof frame['message'] === 'string' ? frame['message'] : ''
    if (code === 'UNAUTHORIZED' || code === 'TOO_MANY_CONNECTIONS') {
      // Fatales por contrato (§3/§5): reintentar sin cambiar nada solo
      // repetiría el fallo — cierre definitivo; la app renueva la sesión.
      shutdown(wsError('UNAUTHORIZED', `el Gateway rechazó la sesión WS (${code}): ${message}`))
      return
    }
    // BAD_FRAME / UNSUPPORTED_ROOM / INTERNAL no cierran la conexión (§5),
    // pero delatan un desacuerdo de protocolo — se reportan como violación.
    reportViolation(`frame error del Gateway (${code}): ${message}`)
  }

  function handleMessage(data: unknown): void {
    const frame = readFrame(data)
    if (frame === null) {
      reportViolation('frame del Gateway no es un objeto JSON')
      return
    }
    switch (frame['type']) {
      case 'auth_ok':
        handleAuthOk()
        break
      case 'joined':
        handleJoined(frame)
        break
      case 'event':
        handleEvent(frame)
        break
      case 'pong':
        handlePong(frame)
        break
      case 'error':
        handleErrorFrame(frame)
        break
      default:
        reportViolation(`frame de type desconocido (${JSON.stringify(frame['type'])})`)
    }
  }

  function handleClose(code: number): void {
    generation += 1 // sella el socket caído: callbacks tardíos se ignoran
    socket = null
    authed = false
    joinInFlight = false
    rejectPendingPings(wsError('INTERNAL', `conexión WS cerrada (código ${String(code)})`))
    if (code === WS_CLOSE_UNAUTHORIZED) {
      // Token inválido/caducado (§5): reconectar con el mismo token es un
      // bucle. La app renueva sesión por REST y llama a connect() de nuevo.
      rejectPendingJoins(wsError('UNAUTHORIZED', 'el Gateway cerró con 4401: sesión no válida'))
      setState('closed')
      return
    }
    // Cierre no solicitado (1013 consumidor lento, 1006, caída de red…):
    // reconexión con backoff. Los joinCorp pendientes SOBREVIVEN — el re-join
    // automático los resolverá con el watermark del `joined` nuevo.
    scheduleReconnect()
  }

  function openSocket(): void {
    generation += 1
    const myGeneration = generation
    socket = options.createSocket(options.url, {
      onOpen: () => {
        if (myGeneration !== generation) {
          return
        }
        // `auth` como PRIMER frame, dentro del plazo del servidor (§3).
        sendFrame({ type: 'auth', token })
      },
      onMessage: (data: unknown) => {
        if (myGeneration !== generation) {
          return
        }
        handleMessage(data)
      },
      onClose: (code: number) => {
        if (myGeneration !== generation) {
          return
        }
        handleClose(code)
      },
      onError: () => {
        // El cierre efectivo llega siempre después por onClose.
      },
    })
  }

  return {
    connect(sessionToken: string): void {
      if (state !== 'closed') {
        throw wsError('INTERNAL', `connect() con la conexión en estado "${state}"`)
      }
      token = sessionToken
      reconnectAttempt = 0
      lastSeq = null
      everJoined = false
      setState('connecting')
      openSocket()
    },

    joinCorp(): Promise<JoinResult> {
      if (state === 'closed') {
        return Promise.reject(wsError('INTERNAL', 'joinCorp() sin conexión: llama a connect()'))
      }
      wantsJoin = true
      const promise = new Promise<JoinResult>((resolve, reject) => {
        pendingJoins.push({ resolve, reject })
      })
      sendJoin() // si aún no hay auth_ok, el join saldrá al autenticar
      return promise
    },

    leave(): void {
      wantsJoin = false
      if (authed) {
        sendFrame({ type: 'leave', room: CORP_ROOM })
      }
    },

    close(): void {
      if (state === 'closed') {
        return
      }
      wantsJoin = false
      shutdown(wsError('INTERNAL', 'conexión cerrada por el cliente'))
    },

    ping(): Promise<void> {
      if (!authed || socket === null) {
        return Promise.reject(wsError('INTERNAL', 'ping() sin conexión autenticada'))
      }
      pingNonce += 1
      const nonce = String(pingNonce)
      const promise = new Promise<void>((resolve, reject) => {
        pendingPings.push({ nonce, resolve, reject })
      })
      sendFrame({ type: 'ping', nonce })
      return promise
    },

    onEvent(callback): Unsubscribe {
      eventCallbacks.add(callback)
      return () => eventCallbacks.delete(callback)
    },

    onStateChange(callback): Unsubscribe {
      stateCallbacks.add(callback)
      return () => stateCallbacks.delete(callback)
    },

    onGap(callback): Unsubscribe {
      gapCallbacks.add(callback)
      return () => gapCallbacks.delete(callback)
    },

    onRejoined(callback): Unsubscribe {
      rejoinedCallbacks.add(callback)
      return () => rejoinedCallbacks.delete(callback)
    },
  }
}
