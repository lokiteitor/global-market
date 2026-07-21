import { describe, expect, it } from 'vitest'

import type {
  ConnectionState,
  DomainEvent,
  JoinResult,
  NetworkTransport,
  SequenceGap,
  Unsubscribe,
} from '~network/transport/port'
import type { ResyncReason } from '~network/transport/sync'
import { createSyncOrchestrator } from '~network/transport/sync'
import { simTime } from '~shared/simtime'

const EVENT_ID = '0197a4c8-1f02-7e33-b0aa-9e5d6f7a8b9c'
const AGGREGATE_ID = '0197a4b0-55e1-7c44-8d21-1a2b3c4d5e6f'

function domainEvent(seq: number, eventType: string): DomainEvent {
  return {
    seq,
    eventId: EVENT_ID,
    eventType,
    simTime: simTime(1_000),
    aggregateType: eventType.split('.')[0] ?? '',
    aggregateId: AGGREGATE_ID,
    payload: { amount: '120' },
  }
}

/** Doble del puerto NetworkTransport: guionizable desde el test. */
class FakeTransport implements NetworkTransport {
  connectCalls: string[] = []
  joinCalls = 0
  leaveCalls = 0
  closeCalls = 0
  watermark = 100
  joinError: Error | null = null

  private eventCallbacks = new Set<(event: DomainEvent) => void>()
  private gapCallbacks = new Set<(gap: SequenceGap) => void>()
  private rejoinedCallbacks = new Set<(result: JoinResult) => void>()
  private stateCallbacks = new Set<(state: ConnectionState) => void>()

  connect(token: string): void {
    this.connectCalls.push(token)
  }

  joinCorp(): Promise<JoinResult> {
    this.joinCalls += 1
    if (this.joinError !== null) {
      return Promise.reject(this.joinError)
    }
    return Promise.resolve({ watermark: this.watermark })
  }

  leave(): void {
    this.leaveCalls += 1
  }

  close(): void {
    this.closeCalls += 1
  }

  onEvent(callback: (event: DomainEvent) => void): Unsubscribe {
    this.eventCallbacks.add(callback)
    return () => this.eventCallbacks.delete(callback)
  }

  onStateChange(callback: (state: ConnectionState) => void): Unsubscribe {
    this.stateCallbacks.add(callback)
    return () => this.stateCallbacks.delete(callback)
  }

  onGap(callback: (gap: SequenceGap) => void): Unsubscribe {
    this.gapCallbacks.add(callback)
    return () => this.gapCallbacks.delete(callback)
  }

  onRejoined(callback: (result: JoinResult) => void): Unsubscribe {
    this.rejoinedCallbacks.add(callback)
    return () => this.rejoinedCallbacks.delete(callback)
  }

  // --- guion del test ---
  emitEvent(event: DomainEvent): void {
    for (const callback of this.eventCallbacks) {
      callback(event)
    }
  }

  emitGap(gap: SequenceGap): void {
    for (const callback of this.gapCallbacks) {
      callback(gap)
    }
  }

  emitRejoined(watermark: number): void {
    for (const callback of this.rejoinedCallbacks) {
      callback({ watermark })
    }
  }

  subscriberCount(): number {
    return this.eventCallbacks.size + this.gapCallbacks.size + this.rejoinedCallbacks.size
  }
}

describe('sync — arranque: connect tras login + joinCorp con watermark', () => {
  it('start(token) conecta, se une a corp y devuelve el watermark del bootstrap', async () => {
    const transport = new FakeTransport()
    transport.watermark = 18_234
    const sync = createSyncOrchestrator(transport)

    const result = await sync.start('token-abc')

    expect(transport.connectCalls).toEqual(['token-abc'])
    expect(transport.joinCalls).toBe(1)
    expect(result).toEqual({ watermark: 18_234 })
  })

  it('rechaza un doble start sin stop intermedio', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    await sync.start('t')
    await expect(sync.start('t')).rejects.toThrow('ya arrancado')
  })

  it('si el join falla, start propaga el error y limpia sus suscripciones', async () => {
    const transport = new FakeTransport()
    transport.joinError = new Error('sin conexión')
    const sync = createSyncOrchestrator(transport)

    await expect(sync.start('t')).rejects.toThrow('sin conexión')
    expect(transport.subscriberCount()).toBe(0)
    expect(transport.closeCalls).toBe(1)

    // Y se puede volver a arrancar tras el fallo.
    transport.joinError = null
    await expect(sync.start('t')).resolves.toEqual({ watermark: 100 })
  })
})

describe('sync — appliers por prefijo de eventType', () => {
  it('despacha cada evento a los appliers cuyo prefijo casa, en orden de registro', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const log: string[] = []
    sync.registerApplier('contract.', (event) => log.push(`contract:${String(event.seq)}`))
    sync.registerApplier('vehicle.', (event) => log.push(`vehicle:${String(event.seq)}`))
    sync.registerApplier('', (event) => log.push(`all:${String(event.seq)}`))
    await sync.start('t')

    transport.emitEvent(domainEvent(101, 'contract.settled'))
    transport.emitEvent(domainEvent(102, 'vehicle.arrived'))
    transport.emitEvent(domainEvent(103, 'shipment.created'))

    expect(log).toEqual([
      'contract:101',
      'all:101',
      'vehicle:102',
      'all:102',
      'all:103', // shipment.* solo lo ve el catch-all
    ])
  })

  it('aplica los eventos en el orden de llegada del transporte', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const seqs: number[] = []
    sync.registerApplier('batch.', (event) => seqs.push(event.seq))
    await sync.start('t')

    transport.emitEvent(domainEvent(1, 'batch.queued'))
    transport.emitEvent(domainEvent(2, 'batch.completed'))
    transport.emitEvent(domainEvent(3, 'batch.paused'))

    expect(seqs).toEqual([1, 2, 3])
  })

  it('el prefijo distingue jerarquías: "contract." no casa "contracts.x"', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const seen: string[] = []
    sync.registerApplier('contract.', (event) => seen.push(event.eventType))
    await sync.start('t')

    transport.emitEvent(domainEvent(1, 'contracts.other'))
    transport.emitEvent(domainEvent(2, 'contract.confirmed'))

    expect(seen).toEqual(['contract.confirmed'])
  })

  it('un applier se puede dar de baja con su unsubscribe', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const seqs: number[] = []
    const unsubscribe = sync.registerApplier('contract.', (event) => seqs.push(event.seq))
    await sync.start('t')

    transport.emitEvent(domainEvent(1, 'contract.confirmed'))
    unsubscribe()
    transport.emitEvent(domainEvent(2, 'contract.settled'))

    expect(seqs).toEqual([1])
  })

  it('se pueden registrar appliers después de start (stores tardías)', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    await sync.start('t')

    transport.emitEvent(domainEvent(1, 'building.created')) // nadie lo escucha aún
    const seqs: number[] = []
    sync.registerApplier('building.', (event) => seqs.push(event.seq))
    transport.emitEvent(domainEvent(2, 'building.updated'))

    expect(seqs).toEqual([2])
  })

  it('con onApplierError, un applier que lanza no bloquea a los demás', async () => {
    const transport = new FakeTransport()
    const failures: unknown[] = []
    const sync = createSyncOrchestrator(transport, {
      onApplierError: (error) => failures.push(error),
    })
    const seqs: number[] = []
    sync.registerApplier('contract.', () => {
      throw new Error('applier roto')
    })
    sync.registerApplier('contract.', (event) => seqs.push(event.seq))
    await sync.start('t')

    transport.emitEvent(domainEvent(7, 'contract.settled'))

    expect(seqs).toEqual([7])
    expect(failures).toHaveLength(1)
    expect(failures[0]).toBeInstanceOf(Error)
  })
})

describe('sync — resync ante hueco o reconexión (ws-protocol.md §6)', () => {
  it('un gap del transporte dispara resync("gap", null)', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const calls: [ResyncReason, number | null][] = []
    sync.onResync((reason, watermark) => calls.push([reason, watermark]))
    await sync.start('t')

    transport.emitGap({ kind: 'jump', lastSeq: 10, receivedSeq: 15 })

    expect(calls).toEqual([['gap', null]])
  })

  it('una reconexión con re-join dispara resync("reconnect", watermarkNuevo)', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const calls: [ResyncReason, number | null][] = []
    sync.onResync((reason, watermark) => calls.push([reason, watermark]))
    await sync.start('t')

    transport.emitRejoined(2_500)

    expect(calls).toEqual([['reconnect', 2_500]])
  })

  it('admite varios handlers de resync y bajas individuales', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const a: ResyncReason[] = []
    const b: ResyncReason[] = []
    sync.onResync((reason) => a.push(reason))
    const unsubscribe = sync.onResync((reason) => b.push(reason))
    await sync.start('t')

    transport.emitGap({ kind: 'stale', lastSeq: 1, receivedSeq: 1 })
    unsubscribe()
    transport.emitRejoined(9)

    expect(a).toEqual(['gap', 'reconnect'])
    expect(b).toEqual(['gap'])
  })
})

describe('sync — parada limpia', () => {
  it('stop() se desuscribe del transporte, hace leave y close', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    const seqs: number[] = []
    sync.registerApplier('', (event) => seqs.push(event.seq))
    await sync.start('t')
    expect(transport.subscriberCount()).toBe(3)

    sync.stop()

    expect(transport.subscriberCount()).toBe(0)
    expect(transport.leaveCalls).toBe(1)
    expect(transport.closeCalls).toBe(1)
    transport.emitEvent(domainEvent(1, 'contract.settled'))
    expect(seqs).toEqual([]) // nada llega tras stop
  })

  it('stop() sin start es inocuo, y tras stop se puede volver a arrancar', async () => {
    const transport = new FakeTransport()
    const sync = createSyncOrchestrator(transport)
    sync.stop()
    expect(transport.closeCalls).toBe(0)

    await sync.start('t')
    sync.stop()
    await expect(sync.start('t')).resolves.toEqual({ watermark: 100 })
    expect(transport.connectCalls).toEqual(['t', 't'])
  })
})
