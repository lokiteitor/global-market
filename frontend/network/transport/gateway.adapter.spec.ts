import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { AppError } from '~network/rest/errors'
import type { GatewayTransport } from '~network/transport/gateway.adapter'
import { createGatewayTransport } from '~network/transport/gateway.adapter'
import type {
  ConnectionState,
  DomainEvent,
  SequenceGap,
  WebSocketFactory,
  WsHandlers,
} from '~network/transport/port'

const URL = 'ws://test.local/api/v1/ws'
const TOKEN = 'b0c1d2e3-f4a5-4b6c-8d7e-9f0a1b2c3d4e'
const EVENT_ID = '0197a4c8-1f02-7e33-b0aa-9e5d6f7a8b9c'
const AGGREGATE_ID = '0197a4b0-55e1-7c44-8d21-1a2b3c4d5e6f'

/**
 * Doble guionizado del socket (implementa el puerto secundario `WsHandle`):
 * registra los frames que envía la ACL y permite al test actuar de servidor
 * (abrir, mandar frames JSON, cerrar con código).
 */
class FakeWebSocket {
  readonly url: string
  readonly sentRaw: string[] = []
  closedWith: number | null = null

  private readonly handlers: WsHandlers

  constructor(url: string, handlers: WsHandlers) {
    this.url = url
    this.handlers = handlers
  }

  // --- WsHandle (lado ACL) ---
  send(data: string): void {
    this.sentRaw.push(data)
  }

  close(code?: number): void {
    this.closedWith = code ?? 1000
    // Como el WebSocket real: el evento close llega tras cerrar localmente.
    this.handlers.onClose(this.closedWith)
  }

  // --- guion del "servidor" (lado test) ---
  serverOpen(): void {
    this.handlers.onOpen()
  }

  serverSend(frame: Record<string, unknown>): void {
    this.handlers.onMessage(JSON.stringify(frame))
  }

  serverSendRaw(data: unknown): void {
    this.handlers.onMessage(data)
  }

  serverClose(code: number): void {
    this.handlers.onClose(code)
  }

  /** Frames enviados por la ACL, parseados. */
  sent(): Record<string, unknown>[] {
    return this.sentRaw.map((raw) => JSON.parse(raw) as Record<string, unknown>)
  }
}

function makeFactory() {
  const sockets: FakeWebSocket[] = []
  const createSocket: WebSocketFactory = (url, handlers) => {
    const socket = new FakeWebSocket(url, handlers)
    sockets.push(socket)
    return socket
  }
  return { sockets, createSocket }
}

interface Harness {
  transport: GatewayTransport
  sockets: FakeWebSocket[]
  states: ConnectionState[]
  events: DomainEvent[]
  gaps: SequenceGap[]
  rejoins: number[]
  violations: AppError[]
  last(): FakeWebSocket
}

function makeHarness(overrides: { random?: () => number } = {}): Harness {
  const { sockets, createSocket } = makeFactory()
  const violations: AppError[] = []
  const transport = createGatewayTransport({
    url: URL,
    createSocket,
    random: overrides.random ?? (() => 0), // jitter determinista por defecto
    onProtocolViolation: (error) => violations.push(error),
  })
  const states: ConnectionState[] = []
  const events: DomainEvent[] = []
  const gaps: SequenceGap[] = []
  const rejoins: number[] = []
  transport.onStateChange((state) => states.push(state))
  transport.onEvent((event) => events.push(event))
  transport.onGap((gap) => gaps.push(gap))
  transport.onRejoined((result) => rejoins.push(result.watermark))
  return {
    transport,
    sockets,
    states,
    events,
    gaps,
    rejoins,
    violations,
    last() {
      const socket = sockets.at(-1)
      if (socket === undefined) {
        throw new Error('harness: sin sockets creados')
      }
      return socket
    },
  }
}

/** Guion completo hasta la room unida: auth → auth_ok → join → joined. */
async function joinUpTo(h: Harness, watermark: number): Promise<number> {
  h.transport.connect(TOKEN)
  const join = h.transport.joinCorp()
  h.last().serverOpen()
  h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 1000 })
  h.last().serverSend({ type: 'joined', room: 'corp', watermark })
  const result = await join
  return result.watermark
}

function eventFrame(seq: number, overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    type: 'event',
    room: 'corp',
    seq,
    event_id: EVENT_ID,
    event_type: 'contract.settled',
    sim_time: 31_190_400,
    aggregate_type: 'contract',
    aggregate_id: AGGREGATE_ID,
    payload: { unit_price: '120', quantity_delivered: '500' },
    ...overrides,
  }
}

beforeEach(() => {
  vi.useFakeTimers()
})

afterEach(() => {
  vi.useRealTimers()
})

describe('gateway.adapter — handshake (ws-protocol.md §3)', () => {
  it('conecta, manda auth como PRIMER frame y pasa a open con auth_ok', () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)

    expect(h.states).toEqual(['connecting'])
    expect(h.last().url).toBe(URL)
    expect(h.last().sentRaw).toHaveLength(0) // nada antes del open del socket

    h.last().serverOpen()
    expect(h.last().sent()).toEqual([{ type: 'auth', token: TOKEN }])

    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 1000 })
    expect(h.states).toEqual(['connecting', 'open'])
  })

  it('rechaza connect() doble mientras la conexión vive', () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    expect(() => h.transport.connect(TOKEN)).toThrow(AppError)
  })

  it('no repite estados idénticos en onStateChange', async () => {
    const h = makeHarness()
    await joinUpTo(h, 10)
    expect(h.states).toEqual(['connecting', 'open'])
  })
})

describe('gateway.adapter — join de la room corp y watermark (§3/§6)', () => {
  it('joinCorp manda join tras auth_ok y resuelve con el watermark del joined', async () => {
    const h = makeHarness()
    const watermark = await joinUpTo(h, 18_234)

    expect(watermark).toBe(18_234)
    expect(h.last().sent()).toEqual([
      { type: 'auth', token: TOKEN },
      { type: 'join', room: 'corp' },
    ])
  })

  it('joinCorp llamado antes del auth_ok queda en cola y sale al autenticar', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    const join = h.transport.joinCorp()
    h.last().serverOpen()
    // Aún sin auth_ok: solo el frame auth ha salido.
    expect(h.last().sent()).toEqual([{ type: 'auth', token: TOKEN }])

    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 1 })
    expect(h.last().sent().at(-1)).toEqual({ type: 'join', room: 'corp' })

    h.last().serverSend({ type: 'joined', room: 'corp', watermark: 7 })
    await expect(join).resolves.toEqual({ watermark: 7 })
  })

  it('varios joinCorp concurrentes comparten UN solo frame join', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    const a = h.transport.joinCorp()
    const b = h.transport.joinCorp()
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 1 })
    h.last().serverSend({ type: 'joined', room: 'corp', watermark: 3 })

    const joins = h.last().sent().filter((frame) => frame['type'] === 'join')
    expect(joins).toHaveLength(1)
    await expect(a).resolves.toEqual({ watermark: 3 })
    await expect(b).resolves.toEqual({ watermark: 3 })
  })

  it('joinCorp sin connect() previo rechaza con AppError', async () => {
    const h = makeHarness()
    await expect(h.transport.joinCorp()).rejects.toBeInstanceOf(AppError)
  })

  it('leave() manda el frame leave y anula el re-join automático', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    h.transport.leave()
    expect(h.last().sent().at(-1)).toEqual({ type: 'leave', room: 'corp' })

    // Cae la conexión: reconecta pero NO re-une (wantsJoin = false).
    h.last().serverClose(1006)
    vi.advanceTimersByTime(60_000)
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 2 })
    const joins = h.last().sent().filter((frame) => frame['type'] === 'join')
    expect(joins).toHaveLength(0)
    expect(h.rejoins).toEqual([])
  })
})

describe('gateway.adapter — frames event → DomainEvent (§5)', () => {
  it('mapea el frame event fiel al contrato, con payload intacto (strings)', async () => {
    const h = makeHarness()
    await joinUpTo(h, 18_234)

    h.last().serverSend(eventFrame(18_235))

    expect(h.events).toHaveLength(1)
    const event = h.events[0]
    expect(event).toEqual({
      seq: 18_235,
      eventId: EVENT_ID,
      eventType: 'contract.settled',
      simTime: 31_190_400,
      aggregateType: 'contract',
      aggregateId: AGGREGATE_ID,
      payload: { unit_price: '120', quantity_delivered: '500' },
    })
    expect(h.gaps).toEqual([])
  })

  it('entrega eventos consecutivos en orden y sin señales de hueco', async () => {
    const h = makeHarness()
    await joinUpTo(h, 100)
    h.last().serverSend(eventFrame(101, { event_type: 'batch.queued' }))
    h.last().serverSend(eventFrame(102, { event_type: 'batch.completed' }))
    h.last().serverSend(eventFrame(103, { event_type: 'contract.confirmed' }))

    expect(h.events.map((event) => event.seq)).toEqual([101, 102, 103])
    expect(h.events.map((event) => event.eventType)).toEqual([
      'batch.queued',
      'batch.completed',
      'contract.confirmed',
    ])
    expect(h.gaps).toEqual([])
  })

  it('normaliza los UUID a minúsculas (forma canónica)', async () => {
    const h = makeHarness()
    await joinUpTo(h, 0)
    h.last().serverSend(
      eventFrame(1, {
        event_id: EVENT_ID.toUpperCase(),
        aggregate_id: AGGREGATE_ID.toUpperCase(),
      }),
    )
    expect(h.events[0]?.eventId).toBe(EVENT_ID)
    expect(h.events[0]?.aggregateId).toBe(AGGREGATE_ID)
  })

  it('descarta y reporta frames event fuera de contrato (sin romper el flujo)', async () => {
    const h = makeHarness()
    await joinUpTo(h, 10)

    h.last().serverSend(eventFrame(11, { event_id: 'no-es-uuid' }))
    h.last().serverSend(eventFrame(11, { seq: 'once' }))
    h.last().serverSend(eventFrame(11, { sim_time: -5 }))
    expect(h.events).toEqual([])
    expect(h.violations).toHaveLength(3)
    expect(h.violations.every((error) => error.kind === 'protocol')).toBe(true)

    // El flujo sigue vivo: el siguiente evento válido se entrega.
    h.last().serverSend(eventFrame(11))
    expect(h.events.map((event) => event.seq)).toEqual([11])
  })

  it('reporta frames no-JSON y de type desconocido como violación de protocolo', async () => {
    const h = makeHarness()
    await joinUpTo(h, 10)
    h.last().serverSendRaw('esto no es json')
    h.last().serverSend({ type: 'snapshot' })
    expect(h.violations).toHaveLength(2)
    expect(h.events).toEqual([])
  })
})

describe('gateway.adapter — detección de huecos por seq (§6)', () => {
  it('salto de seq ⇒ onGap kind jump, y el evento se entrega igualmente', async () => {
    const h = makeHarness()
    await joinUpTo(h, 100)
    h.last().serverSend(eventFrame(101))
    h.last().serverSend(eventFrame(105))

    expect(h.gaps).toEqual([{ kind: 'jump', lastSeq: 101, receivedSeq: 105 }])
    expect(h.events.map((event) => event.seq)).toEqual([101, 105])
  })

  it('seq no creciente ⇒ onGap kind stale y el duplicado NO se re-entrega', async () => {
    const h = makeHarness()
    await joinUpTo(h, 100)
    h.last().serverSend(eventFrame(101))
    h.last().serverSend(eventFrame(101)) // duplicado at-least-once
    h.last().serverSend(eventFrame(99)) // regresión bajo el watermark

    expect(h.gaps).toEqual([
      { kind: 'stale', lastSeq: 101, receivedSeq: 101 },
      { kind: 'stale', lastSeq: 101, receivedSeq: 99 },
    ])
    expect(h.events.map((event) => event.seq)).toEqual([101])
  })

  it('el primer evento tras el joined se compara contra el watermark', async () => {
    const h = makeHarness()
    await joinUpTo(h, 100)
    h.last().serverSend(eventFrame(100)) // seq == watermark: ya despachado
    expect(h.gaps).toEqual([{ kind: 'stale', lastSeq: 100, receivedSeq: 100 }])
    expect(h.events).toEqual([])
  })

  it('un event antes del joined es violación de protocolo, no evento', () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 1 })
    h.last().serverSend(eventFrame(5))
    expect(h.events).toEqual([])
    expect(h.violations).toHaveLength(1)
  })
})

describe('gateway.adapter — reconexión con backoff + re-join (§6, FAD §12.12)', () => {
  it('ante cierre no solicitado reconecta tras el backoff y re-une publicando el watermark nuevo', async () => {
    const h = makeHarness()
    await joinUpTo(h, 40)

    h.last().serverClose(1013) // consumidor lento: reconectar y re-pull (§5)
    expect(h.states.at(-1)).toBe('reconnecting')

    // Backoff del intento 1 con random()=0: exactamente 1000 ms.
    vi.advanceTimersByTime(999)
    expect(h.sockets).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(h.sockets).toHaveLength(2)

    // Re-handshake completo: auth de nuevo como primer frame + re-join.
    h.last().serverOpen()
    expect(h.last().sent()).toEqual([{ type: 'auth', token: TOKEN }])
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 2 })
    expect(h.last().sent().at(-1)).toEqual({ type: 'join', room: 'corp' })
    expect(h.states.at(-1)).toBe('open')

    h.last().serverSend({ type: 'joined', room: 'corp', watermark: 50 })
    expect(h.rejoins).toEqual([50]) // el orquestador decidirá re-pull por REST
  })

  it('el backoff crece exponencialmente 1s→2s→4s y se resetea al autenticar', async () => {
    const h = makeHarness()
    await joinUpTo(h, 1)

    // Intento 1: 1000 ms.
    h.last().serverClose(1006)
    vi.advanceTimersByTime(1000)
    expect(h.sockets).toHaveLength(2)

    // El socket nuevo cae sin llegar a auth_ok → intento 2: 2000 ms.
    h.last().serverClose(1006)
    vi.advanceTimersByTime(1999)
    expect(h.sockets).toHaveLength(2)
    vi.advanceTimersByTime(1)
    expect(h.sockets).toHaveLength(3)

    // Cae de nuevo → intento 3: 4000 ms.
    h.last().serverClose(1006)
    vi.advanceTimersByTime(4000)
    expect(h.sockets).toHaveLength(4)

    // Handshake completo: el contador vuelve a cero → próxima caída: 1000 ms.
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 3 })
    h.last().serverClose(1006)
    vi.advanceTimersByTime(1000)
    expect(h.sockets).toHaveLength(5)
  })

  it('el backoff respeta el techo de 30 s incluso con jitter máximo', async () => {
    const h = makeHarness({ random: () => 0.999_999 })
    await joinUpTo(h, 1)
    // 10 caídas seguidas sin autenticar: el delay queda acotado a 30 s.
    for (let attempt = 1; attempt <= 10; attempt += 1) {
      h.last().serverClose(1006)
      vi.advanceTimersByTime(30_000)
      expect(h.sockets).toHaveLength(attempt + 1)
    }
  })

  it('un joinCorp pendiente sobrevive a la reconexión y resuelve con el joined nuevo', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    const join = h.transport.joinCorp()
    h.last().serverOpen()
    h.last().serverClose(1006) // cae antes del auth_ok

    vi.advanceTimersByTime(1000)
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 2 })
    h.last().serverSend({ type: 'joined', room: 'corp', watermark: 9 })

    await expect(join).resolves.toEqual({ watermark: 9 })
    // Primer join efectivo: NO es un re-join (nada que re-sincronizar aún).
    expect(h.rejoins).toEqual([])
  })

  it('tras reconectar, la secuencia parte del watermark nuevo (sin falsos huecos)', async () => {
    const h = makeHarness()
    await joinUpTo(h, 10)
    h.last().serverSend(eventFrame(11))

    h.last().serverClose(1013)
    vi.advanceTimersByTime(1000)
    h.last().serverOpen()
    h.last().serverSend({ type: 'auth_ok', account_id: AGGREGATE_ID, sim_time_seconds: 2 })
    h.last().serverSend({ type: 'joined', room: 'corp', watermark: 30 })
    h.last().serverSend(eventFrame(31))

    expect(h.gaps).toEqual([])
    expect(h.events.map((event) => event.seq)).toEqual([11, 31])
  })
})

describe('gateway.adapter — fallos fatales sin reconexión', () => {
  it('cierre 4401 ⇒ closed, sin reintentos, y el joinCorp pendiente rechaza UNAUTHORIZED', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    const join = h.transport.joinCorp()
    h.last().serverOpen()
    h.last().serverClose(4401)

    expect(h.states.at(-1)).toBe('closed')
    vi.advanceTimersByTime(120_000)
    expect(h.sockets).toHaveLength(1) // jamás reconecta con token inválido
    await expect(join).rejects.toMatchObject({ code: 'UNAUTHORIZED' })
  })

  it('frame error TOO_MANY_CONNECTIONS ⇒ cierre definitivo sin reconexión', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    const join = h.transport.joinCorp()
    h.last().serverOpen()
    h.last().serverSend({
      type: 'error',
      code: 'TOO_MANY_CONNECTIONS',
      message: 'máximo de conexiones por cuenta superado',
    })

    expect(h.states.at(-1)).toBe('closed')
    expect(h.last().closedWith).toBe(1000)
    vi.advanceTimersByTime(120_000)
    expect(h.sockets).toHaveLength(1)
    await expect(join).rejects.toBeInstanceOf(AppError)
  })

  it('frames error no fatales (BAD_FRAME…) se reportan y la conexión sigue viva', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    h.last().serverSend({ type: 'error', code: 'BAD_FRAME', message: 'json inválido' })
    expect(h.states.at(-1)).toBe('open')
    expect(h.violations).toHaveLength(1)

    h.last().serverSend(eventFrame(6))
    expect(h.events.map((event) => event.seq)).toEqual([6])
  })

  it('tras un cierre fatal se puede reconectar con connect() y token fresco', async () => {
    const h = makeHarness()
    h.transport.connect(TOKEN)
    h.last().serverOpen()
    h.last().serverClose(4401)
    expect(h.states.at(-1)).toBe('closed')

    h.transport.connect('token-fresco')
    h.last().serverOpen()
    expect(h.last().sent()).toEqual([{ type: 'auth', token: 'token-fresco' }])
  })
})

describe('gateway.adapter — cierre limpio (FAD §12.14)', () => {
  it('close() cierra con 1000, pasa a closed y no reconecta', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    h.transport.close()

    expect(h.last().closedWith).toBe(1000)
    expect(h.states.at(-1)).toBe('closed')
    vi.advanceTimersByTime(120_000)
    expect(h.sockets).toHaveLength(1)
  })

  it('close() durante el backoff cancela la reconexión pendiente', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    h.last().serverClose(1006)
    expect(h.states.at(-1)).toBe('reconnecting')

    h.transport.close()
    vi.advanceTimersByTime(120_000)
    expect(h.sockets).toHaveLength(1)
    expect(h.states.at(-1)).toBe('closed')
  })

  it('close() es idempotente y los frames del socket viejo quedan sellados', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    const socket = h.last()
    h.transport.close()
    h.transport.close()

    // Un frame tardío del socket cerrado no reactiva nada.
    socket.serverSend(eventFrame(6))
    expect(h.events).toEqual([])
    expect(h.states.at(-1)).toBe('closed')
  })
})

describe('gateway.adapter — ping/pong de aplicación (§4/§5)', () => {
  it('ping() manda nonce y resuelve con el pong de eco', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    const ping = h.transport.ping()

    const frame = h.last().sent().at(-1)
    expect(frame?.['type']).toBe('ping')
    const nonce = frame?.['nonce']
    expect(typeof nonce).toBe('string')

    h.last().serverSend({ type: 'pong', nonce })
    await expect(ping).resolves.toBeUndefined()
  })

  it('un pong de nonce desconocido es inocuo', async () => {
    const h = makeHarness()
    await joinUpTo(h, 5)
    h.last().serverSend({ type: 'pong', nonce: 'fantasma' })
    expect(h.violations).toEqual([])
  })

  it('ping() sin conexión autenticada rechaza; y el ping en vuelo rechaza al caer el socket', async () => {
    const h = makeHarness()
    await expect(h.transport.ping()).rejects.toBeInstanceOf(AppError)

    await joinUpTo(h, 5)
    const ping = h.transport.ping()
    h.last().serverClose(1006)
    await expect(ping).rejects.toBeInstanceOf(AppError)
  })
})

describe('gateway.adapter — bajas de suscripción', () => {
  it('los unsubscribe devueltos dan de baja el callback', async () => {
    const h = makeHarness()
    const seen: DomainEvent[] = []
    const unsubscribe = h.transport.onEvent((event) => seen.push(event))
    await joinUpTo(h, 0)

    h.last().serverSend(eventFrame(1))
    unsubscribe()
    h.last().serverSend(eventFrame(2))

    expect(seen.map((event) => event.seq)).toEqual([1])
    expect(h.events.map((event) => event.seq)).toEqual([1, 2]) // el del harness sigue vivo
  })
})
