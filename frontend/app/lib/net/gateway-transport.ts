/**
 * lib/net/gateway-transport.ts — GatewayTransportAdapter (ADR-FE-004).
 *
 * Implementación de referencia del puerto NetworkTransport contra el
 * protocolo REAL del Notification/Event Gateway (specs/ws-protocol.md):
 *
 *   - handshake: primer frame `hello` con el token de sesión → `hello_ok`
 *     (cierre 4401 si el token es inválido);
 *   - `join`/`leave` de rooms; un join `viewport:` reemplaza el anterior;
 *   - heartbeat: `ping` periódico (20 s) con watchdog a 2× el intervalo —
 *     sin pong a tiempo la conexión se considera muerta (FAD §12.11);
 *   - reconexión con backoff exponencial + jitter (1 s .. 30 s, FAD §12.12),
 *     re-`hello` y re-`join` automático de las rooms activas (resync por
 *     snapshot, ws-protocol.md §6);
 *   - `pong` y `hello_ok` re-sincronizan el SimClock (callback onSimSync);
 *   - frames `error` → callback onProtocolError (la UI los enruta a
 *     notifications; aquí no se toca ninguna store — ACL pura).
 *
 * Es una ACL: ninguna idiosincrasia del cable (formato de frame, seq, close
 * codes) sale de este archivo. El WebSocket es inyectable (wsFactory) para
 * poder probar la reconexión con un socket falso sin red real.
 */
import type { PatchOp } from '../api/types'
import { isViewportRoom, type MessageFrame, type NetworkTransport, type PatchFrame, type SnapshotFrame, type TransportState } from './transport'

// ─── WebSocket inyectable (subconjunto del estándar que usamos) ──────────────

export interface WebSocketLike {
  readyState: number
  send(data: string): void
  close(code?: number, reason?: string): void
  onopen: ((ev?: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev?: { code?: number; reason?: string }) => void) | null
  onerror: ((ev?: unknown) => void) | null
}

export type WebSocketFactory = (url: string) => WebSocketLike

const WS_OPEN = 1

/** Close code del gateway para token inválido (ws-protocol.md §1). */
const CLOSE_UNAUTHORIZED = 4401

// ─── Frames del protocolo (cable) ────────────────────────────────────────────

interface HelloOkWire {
  type: 'hello_ok'
  account: { id: string; name: string; kind: string }
  sim: { sim_seconds: number; frozen: boolean }
  server_time: string
}

interface SnapshotWire {
  type: 'snapshot'
  room: string
  seq: number
  sim_seconds: number
  data: Record<string, unknown>
}

interface PatchWire {
  type: 'patch'
  room: string
  seq: number
  sim_seconds: number
  ops: PatchOp[]
}

interface MessageWire {
  type: 'message'
  room: string
  event: string
  sim_seconds: number
  data: Record<string, unknown>
}

interface PongWire {
  type: 'pong'
  t: number
  sim_seconds: number
  frozen: boolean
}

interface ErrorWire {
  type: 'error'
  code: string
  message: string
}

type ServerWire = HelloOkWire | SnapshotWire | PatchWire | MessageWire | PongWire | ErrorWire

// ─── Opciones ────────────────────────────────────────────────────────────────

export interface GatewayTransportOptions {
  /** URL del endpoint WS (o función que la deriva en el momento de conectar). */
  url: string | (() => string)
  /** Token de sesión REST (inyectado; sin import de stores — P3). */
  getToken: () => string | null
  /** Fábrica de WebSocket (inyectable en tests). Por defecto, el nativo. */
  wsFactory?: WebSocketFactory
  /** Intervalo de ping (ms). Watchdog = 2× este valor. */
  pingIntervalMs?: number
  /** Base del backoff exponencial (ms). */
  reconnectBaseMs?: number
  /** Techo del backoff (ms). */
  reconnectCapMs?: number
  /** Fuente de aleatoriedad del jitter (inyectable en tests). */
  random?: () => number
  /** hello_ok y pong: muestra autoritativa de sim-time para el SimClock. */
  onSimSync?: (simSeconds: number, frozen: boolean) => void
  /** Frames `error` del gateway (FORBIDDEN, NOT_AUTHENTICATED, …). */
  onProtocolError?: (code: string, message: string) => void
}

const DEFAULT_PING_INTERVAL_MS = 20_000
const DEFAULT_RECONNECT_BASE_MS = 1_000
const DEFAULT_RECONNECT_CAP_MS = 30_000

function defaultWsFactory(url: string): WebSocketLike {
  return new WebSocket(url) as unknown as WebSocketLike
}

// ─── Adaptador ───────────────────────────────────────────────────────────────

export function createGatewayTransport(options: GatewayTransportOptions): NetworkTransport {
  const pingIntervalMs = options.pingIntervalMs ?? DEFAULT_PING_INTERVAL_MS
  const reconnectBaseMs = options.reconnectBaseMs ?? DEFAULT_RECONNECT_BASE_MS
  const reconnectCapMs = options.reconnectCapMs ?? DEFAULT_RECONNECT_CAP_MS
  const random = options.random ?? Math.random
  const wsFactory = options.wsFactory ?? defaultWsFactory

  let state: TransportState = 'idle'
  let ws: WebSocketLike | null = null
  let closedByUser = false
  let reconnectAttempts = 0
  let pingCounter = 0

  let pingTimer: ReturnType<typeof setInterval> | null = null
  let watchdogTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null

  /** Rooms deseadas: se re-unen tras cada hello_ok (resync por construcción). */
  const desiredRooms = new Set<string>()

  const snapshotHandlers = new Set<(f: SnapshotFrame) => void>()
  const patchHandlers = new Set<(f: PatchFrame) => void>()
  const messageHandlers = new Set<(f: MessageFrame) => void>()
  const stateHandlers = new Set<(s: TransportState) => void>()

  function setState(next: TransportState): void {
    if (state === next) return
    state = next
    for (const handler of [...stateHandlers]) handler(next)
  }

  function resolveUrl(): string {
    return typeof options.url === 'function' ? options.url() : options.url
  }

  function send(frame: Record<string, unknown>): void {
    if (ws !== null && ws.readyState === WS_OPEN) ws.send(JSON.stringify(frame))
  }

  // ── Heartbeat + watchdog (FAD §12.11) ──
  function startHeartbeat(): void {
    stopHeartbeat()
    pingTimer = setInterval(() => {
      pingCounter += 1
      send({ type: 'ping', t: pingCounter })
      // Watchdog: si el pong de ESTE ping no llega en 2× el intervalo, la
      // conexión está muerta (half-open) y se fuerza la reconexión.
      if (watchdogTimer === null) {
        watchdogTimer = setTimeout(() => {
          watchdogTimer = null
          forceReconnect()
        }, 2 * pingIntervalMs)
      }
    }, pingIntervalMs)
  }

  function stopHeartbeat(): void {
    if (pingTimer !== null) {
      clearInterval(pingTimer)
      pingTimer = null
    }
    clearWatchdog()
  }

  function clearWatchdog(): void {
    if (watchdogTimer !== null) {
      clearTimeout(watchdogTimer)
      watchdogTimer = null
    }
  }

  // ── Ciclo de vida del socket ──
  function detach(socket: WebSocketLike): void {
    socket.onopen = null
    socket.onmessage = null
    socket.onclose = null
    socket.onerror = null
  }

  function openSocket(): void {
    closedByUser = false
    setState(reconnectAttempts > 0 ? 'reconnecting' : 'connecting')
    const socket = wsFactory(resolveUrl())
    ws = socket

    socket.onopen = () => {
      // Primer frame obligatorio: hello con el token de sesión (ws-protocol §1).
      setState('authenticating')
      send({ type: 'hello', token: options.getToken() ?? '' })
    }

    socket.onmessage = (ev) => {
      let frame: ServerWire
      try {
        frame = JSON.parse(String(ev.data)) as ServerWire
      } catch {
        return // frame no-JSON: se ignora (tolerancia; el protocolo es JSON puro)
      }
      handleFrame(frame)
    }

    socket.onclose = (ev) => {
      if (ws !== socket) return
      handleClose(ev?.code)
    }

    socket.onerror = () => {
      // El evento error viene siempre seguido de close; no se duplica el manejo.
    }
  }

  function handleFrame(frame: ServerWire): void {
    switch (frame.type) {
      case 'hello_ok': {
        reconnectAttempts = 0
        setState('open')
        options.onSimSync?.(frame.sim.sim_seconds, frame.sim.frozen)
        // Re-join automático de las rooms activas → el gateway responde con
        // snapshots nuevos y el estado local converge por construcción.
        for (const room of desiredRooms) send({ type: 'join', room })
        startHeartbeat()
        break
      }
      case 'snapshot': {
        const f: SnapshotFrame = { room: frame.room, seq: frame.seq, simSeconds: frame.sim_seconds, data: frame.data }
        for (const handler of [...snapshotHandlers]) handler(f)
        break
      }
      case 'patch': {
        const f: PatchFrame = { room: frame.room, seq: frame.seq, simSeconds: frame.sim_seconds, ops: frame.ops }
        for (const handler of [...patchHandlers]) handler(f)
        break
      }
      case 'message': {
        const f: MessageFrame = { room: frame.room, event: frame.event, simSeconds: frame.sim_seconds, data: frame.data }
        for (const handler of [...messageHandlers]) handler(f)
        break
      }
      case 'pong': {
        clearWatchdog()
        options.onSimSync?.(frame.sim_seconds, frame.frozen)
        break
      }
      case 'error': {
        options.onProtocolError?.(frame.code, frame.message)
        break
      }
    }
  }

  function handleClose(code?: number): void {
    stopHeartbeat()
    ws = null
    if (closedByUser) {
      setState('closed')
      return
    }
    if (code === CLOSE_UNAUTHORIZED) {
      // Token inválido: reintentar en bucle no lo arreglará. Cierre definitivo;
      // una nueva sesión (login) volverá a llamar a connect().
      options.onProtocolError?.('NOT_AUTHENTICATED', 'El gateway rechazó el token de sesión')
      setState('closed')
      return
    }
    scheduleReconnect()
  }

  function forceReconnect(): void {
    const socket = ws
    if (socket !== null) {
      detach(socket)
      try {
        socket.close()
      } catch {
        // el socket puede estar ya muerto; irrelevante
      }
    }
    stopHeartbeat()
    ws = null
    if (!closedByUser) scheduleReconnect()
  }

  /** Backoff exponencial con jitter, acotado a [base, cap] (FAD §12.12). */
  function scheduleReconnect(): void {
    if (reconnectTimer !== null) return
    setState('reconnecting')
    reconnectAttempts += 1
    const raw = Math.min(reconnectCapMs, reconnectBaseMs * 2 ** (reconnectAttempts - 1))
    const delay = Math.max(reconnectBaseMs, raw / 2 + random() * (raw / 2))
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null
      openSocket()
    }, delay)
  }

  function connect(): void {
    if (state === 'connecting' || state === 'authenticating' || state === 'open') return
    if (reconnectTimer !== null) return // ya hay un reintento programado
    openSocket()
  }

  // ── Puerto ──
  return {
    connect,

    close() {
      closedByUser = true
      if (reconnectTimer !== null) {
        clearTimeout(reconnectTimer)
        reconnectTimer = null
      }
      stopHeartbeat()
      desiredRooms.clear()
      reconnectAttempts = 0
      const socket = ws
      ws = null
      if (socket !== null) {
        detach(socket)
        try {
          socket.close(1000, 'client close')
        } catch {
          // ya cerrado
        }
      }
      setState('closed')
    },

    join(room: string) {
      if (isViewportRoom(room)) {
        // Solo hay un viewport activo por conexión: el nuevo reemplaza al
        // anterior también en el conjunto local (así el re-join tras una
        // reconexión solo une el último bbox). El gateway ya hace el
        // reemplazo server-side sin necesidad de leave explícito.
        for (const existing of [...desiredRooms]) {
          if (isViewportRoom(existing) && existing !== room) desiredRooms.delete(existing)
        }
      }
      const isNew = !desiredRooms.has(room)
      desiredRooms.add(room)
      if (state === 'open') {
        // El re-join de un viewport idéntico también fuerza snapshot fresco.
        if (isNew || isViewportRoom(room)) send({ type: 'join', room })
      } else if (state === 'idle' || state === 'closed') {
        // Unirse a una room expresa la intención de estar conectado.
        connect()
      }
    },

    leave(room: string) {
      if (!desiredRooms.delete(room)) return
      if (state === 'open') send({ type: 'leave', room })
    },

    rooms() {
      return [...desiredRooms]
    },

    connectionState() {
      return state
    },

    onSnapshot(handler) {
      snapshotHandlers.add(handler)
      return () => snapshotHandlers.delete(handler)
    },

    onPatch(handler) {
      patchHandlers.add(handler)
      return () => patchHandlers.delete(handler)
    },

    onMessage(handler) {
      messageHandlers.add(handler)
      return () => messageHandlers.delete(handler)
    },

    onStateChange(handler) {
      stateHandlers.add(handler)
      return () => stateHandlers.delete(handler)
    }
  }
}
