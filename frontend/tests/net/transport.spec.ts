/**
 * tests/net/transport.spec.ts — GatewayTransportAdapter contra un WebSocket
 * FALSO inyectado (wsFactory): handshake hello/hello_ok, join/leave, ping con
 * watchdog 2×, reconexión con backoff + re-hello + re-join, pong → SimClock,
 * frames error y cierre 4401. Sin red real (specs/ws-protocol.md 1:1).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createGatewayTransport, type WebSocketLike } from '~/lib/net/gateway-transport'
import type { TransportState } from '~/lib/net/transport'

// ─── WebSocket falso controlable ─────────────────────────────────────────────

class FakeWebSocket implements WebSocketLike {
  static instances: FakeWebSocket[] = []

  readyState = 0 // CONNECTING
  sent: string[] = []
  onopen: ((ev?: unknown) => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev?: { code?: number; reason?: string }) => void) | null = null
  onerror: ((ev?: unknown) => void) | null = null

  constructor(public url: string) {
    FakeWebSocket.instances.push(this)
  }

  send(data: string): void {
    this.sent.push(data)
  }

  close(code?: number): void {
    this.readyState = 3
    this.onclose?.({ code: code ?? 1000 })
  }

  // ── Helpers de "servidor" ──
  serverOpen(): void {
    this.readyState = 1
    this.onopen?.()
  }

  serverFrame(frame: Record<string, unknown>): void {
    this.onmessage?.({ data: JSON.stringify(frame) })
  }

  serverHelloOk(simSeconds = 1000, frozen = false): void {
    this.serverFrame({
      type: 'hello_ok',
      account: { id: 'acc-1', name: 'Aurora Corp', kind: 'human' },
      sim: { sim_seconds: simSeconds, frozen },
      server_time: '2026-07-15T10:00:00Z'
    })
  }

  serverDrop(code = 1006): void {
    this.readyState = 3
    this.onclose?.({ code })
  }

  frames(): Array<Record<string, unknown>> {
    return this.sent.map((s) => JSON.parse(s) as Record<string, unknown>)
  }

  framesOfType(type: string): Array<Record<string, unknown>> {
    return this.frames().filter((f) => f['type'] === type)
  }
}

function lastSocket(): FakeWebSocket {
  const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
  if (socket === undefined) throw new Error('no hay sockets falsos creados')
  return socket
}

// ─── Arnés común ─────────────────────────────────────────────────────────────

const PING_MS = 20_000

function makeTransport() {
  const simSyncs: Array<{ simSeconds: number; frozen: boolean }> = []
  const protocolErrors: Array<{ code: string; message: string }> = []
  const states: TransportState[] = []

  const transport = createGatewayTransport({
    url: 'ws://test/ws',
    getToken: () => 'tok-1',
    wsFactory: (url) => new FakeWebSocket(url),
    pingIntervalMs: PING_MS,
    random: () => 1, // jitter determinista (extremo superior)
    onSimSync: (simSeconds, frozen) => simSyncs.push({ simSeconds, frozen }),
    onProtocolError: (code, message) => protocolErrors.push({ code, message })
  })
  transport.onStateChange((s) => states.push(s))
  return { transport, simSyncs, protocolErrors, states }
}

beforeEach(() => {
  vi.useFakeTimers()
  FakeWebSocket.instances = []
})

afterEach(() => {
  vi.useRealTimers()
})

// ─── Tests ───────────────────────────────────────────────────────────────────

describe('GatewayTransportAdapter — handshake y rooms', () => {
  it('hello con token como primer frame; hello_ok abre y sincroniza el SimClock', () => {
    const { transport, simSyncs, states } = makeTransport()
    transport.connect()
    const ws = lastSocket()

    ws.serverOpen()
    expect(ws.frames()[0]).toEqual({ type: 'hello', token: 'tok-1' })
    expect(transport.connectionState()).toBe('authenticating')

    ws.serverHelloOk(123_456, false)
    expect(transport.connectionState()).toBe('open')
    expect(simSyncs).toEqual([{ simSeconds: 123_456, frozen: false }])
    expect(states).toEqual(['connecting', 'authenticating', 'open'])
  })

  it('join antes de abrir queda registrado y se envía tras hello_ok; leave notifica', () => {
    const { transport } = makeTransport()
    transport.join('corp:acc-1') // implica connect()
    transport.join('alerts:acc-1')

    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    const joins = ws.framesOfType('join').map((f) => f['room'])
    expect(joins).toEqual(['corp:acc-1', 'alerts:acc-1'])

    transport.leave('alerts:acc-1')
    expect(ws.framesOfType('leave')).toEqual([{ type: 'leave', room: 'alerts:acc-1' }])
    expect(transport.rooms()).toEqual(['corp:acc-1'])
  })

  it('un join viewport: reemplaza el viewport anterior (solo uno activo)', () => {
    const { transport } = makeTransport()
    transport.connect()
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    transport.join('viewport:0,0,1,1')
    transport.join('viewport:2,2,3,3')

    expect(transport.rooms()).toEqual(['viewport:2,2,3,3'])
    const joins = ws.framesOfType('join').map((f) => f['room'])
    expect(joins).toEqual(['viewport:0,0,1,1', 'viewport:2,2,3,3'])
  })

  it('reparte snapshot/patch/message a sus handlers', () => {
    const { transport } = makeTransport()
    const snapshots: unknown[] = []
    const patches: unknown[] = []
    const messages: unknown[] = []
    transport.onSnapshot((f) => snapshots.push(f))
    transport.onPatch((f) => patches.push(f))
    transport.onMessage((f) => messages.push(f))

    transport.connect()
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    ws.serverFrame({ type: 'snapshot', room: 'corp:acc-1', seq: 0, sim_seconds: 10, data: { buildings: [] } })
    ws.serverFrame({ type: 'patch', room: 'corp:acc-1', seq: 1, sim_seconds: 11, ops: [] })
    ws.serverFrame({ type: 'message', room: 'alerts:acc-1', event: 'contract.settled', sim_seconds: 12, data: {} })

    expect(snapshots).toEqual([{ room: 'corp:acc-1', seq: 0, simSeconds: 10, data: { buildings: [] } }])
    expect(patches).toEqual([{ room: 'corp:acc-1', seq: 1, simSeconds: 11, ops: [] }])
    expect(messages).toEqual([{ room: 'alerts:acc-1', event: 'contract.settled', simSeconds: 12, data: {} }])
  })

  it('frame error → onProtocolError (la UI lo enruta a notifications)', () => {
    const { transport, protocolErrors } = makeTransport()
    transport.connect()
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    ws.serverFrame({ type: 'error', code: 'FORBIDDEN', message: 'room ajena' })
    expect(protocolErrors).toEqual([{ code: 'FORBIDDEN', message: 'room ajena' }])
  })
})

describe('GatewayTransportAdapter — heartbeat, watchdog y reconexión', () => {
  it('envía ping periódico y el pong re-sincroniza el SimClock', () => {
    const { transport, simSyncs } = makeTransport()
    transport.connect()
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    vi.advanceTimersByTime(PING_MS)
    const pings = ws.framesOfType('ping')
    expect(pings).toHaveLength(1)

    ws.serverFrame({ type: 'pong', t: pings[0]?.['t'], sim_seconds: 2_000, frozen: true })
    expect(simSyncs.at(-1)).toEqual({ simSeconds: 2_000, frozen: true })
  })

  it('watchdog: sin pong en 2× el intervalo reconecta, re-hello y re-join', () => {
    const { transport } = makeTransport()
    transport.join('corp:acc-1')
    transport.join('viewport:0,0,1,1')
    const first = lastSocket()
    first.serverOpen()
    first.serverHelloOk()
    expect(FakeWebSocket.instances).toHaveLength(1)

    // t=20s: ping (watchdog armado para t=60s); nunca llega pong.
    vi.advanceTimersByTime(PING_MS) // t = 20 s
    vi.advanceTimersByTime(2 * PING_MS) // t = 60 s → watchdog dispara
    expect(transport.connectionState()).toBe('reconnecting')

    // backoff intento 1 (random=1): 1 s.
    vi.advanceTimersByTime(1_000)
    expect(FakeWebSocket.instances).toHaveLength(2)

    const second = lastSocket()
    second.serverOpen()
    expect(second.frames()[0]).toEqual({ type: 'hello', token: 'tok-1' })
    second.serverHelloOk()
    expect(transport.connectionState()).toBe('open')
    // Re-join automático de las rooms activas (snapshots nuevos = resync).
    expect(second.framesOfType('join').map((f) => f['room'])).toEqual(['corp:acc-1', 'viewport:0,0,1,1'])
  })

  it('caída del socket → backoff exponencial con jitter acotado (1 s .. 30 s)', () => {
    const { transport } = makeTransport()
    transport.connect()
    const first = lastSocket()
    first.serverOpen()
    first.serverHelloOk()

    // Caída 1: reintento tras 1 s (2^0 × base, random=1 → extremo superior).
    first.serverDrop()
    vi.advanceTimersByTime(999)
    expect(FakeWebSocket.instances).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances).toHaveLength(2)

    // Caída 2 sin llegar a hello_ok… el backoff crece (2 s).
    lastSocket().serverOpen()
    lastSocket().serverDrop()
    vi.advanceTimersByTime(1_999)
    expect(FakeWebSocket.instances).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(FakeWebSocket.instances).toHaveLength(3)

    // hello_ok resetea los intentos: la siguiente caída vuelve a 1 s.
    lastSocket().serverOpen()
    lastSocket().serverHelloOk()
    lastSocket().serverDrop()
    vi.advanceTimersByTime(1_000)
    expect(FakeWebSocket.instances).toHaveLength(4)
  })

  it('close code 4401 (token inválido): cierre definitivo, sin bucle de reconexión', () => {
    const { transport, protocolErrors } = makeTransport()
    transport.connect()
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverDrop(4401)

    expect(transport.connectionState()).toBe('closed')
    expect(protocolErrors.at(-1)?.code).toBe('NOT_AUTHENTICATED')
    vi.advanceTimersByTime(120_000)
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it('close() ordenado: no reconecta y olvida las rooms', () => {
    const { transport } = makeTransport()
    transport.join('corp:acc-1')
    const ws = lastSocket()
    ws.serverOpen()
    ws.serverHelloOk()

    transport.close()
    expect(transport.connectionState()).toBe('closed')
    expect(transport.rooms()).toEqual([])
    vi.advanceTimersByTime(120_000)
    expect(FakeWebSocket.instances).toHaveLength(1)
  })
})
