import { describe, expect, it, vi } from 'vitest'
import { createEventBus, type AppEvents } from '~/lib/kernel/event-bus'

describe('kernel/event-bus', () => {
  it('entrega el payload tipado a los suscriptores del evento', () => {
    const bus = createEventBus<AppEvents>()
    const onSelect = vi.fn()
    bus.on('world:select', onSelect)

    bus.emit('world:select', { kind: 'city', id: 'abc' })

    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith({ kind: 'city', id: 'abc' })
  })

  it('no cruza eventos entre nombres distintos', () => {
    const bus = createEventBus<AppEvents>()
    const onSelect = vi.fn()
    const onNotify = vi.fn()
    bus.on('world:select', onSelect)
    bus.on('ui:notify', onNotify)

    bus.emit('ui:notify', { level: 'error', text: 'boom' })

    expect(onSelect).not.toHaveBeenCalled()
    expect(onNotify).toHaveBeenCalledWith({ level: 'error', text: 'boom' })
  })

  it('off y la función de desuscripción retiran el handler', () => {
    const bus = createEventBus<AppEvents>()
    const a = vi.fn()
    const b = vi.fn()
    const unsubscribeA = bus.on('camera:flyTo', a)
    bus.on('camera:flyTo', b)

    unsubscribeA()
    bus.off('camera:flyTo', b)
    bus.emit('camera:flyTo', { lon: 1, lat: 2 })

    expect(a).not.toHaveBeenCalled()
    expect(b).not.toHaveBeenCalled()
  })

  it('emitir sin suscriptores no lanza', () => {
    const bus = createEventBus<AppEvents>()
    expect(() => bus.emit('ui:openPanel', { panel: 'market' })).not.toThrow()
  })

  it('un handler puede desuscribirse durante la emisión sin saltarse a los demás', () => {
    const bus = createEventBus<AppEvents>()
    const calls: string[] = []
    const first = () => {
      calls.push('first')
      bus.off('ui:notify', first)
    }
    bus.on('ui:notify', first)
    bus.on('ui:notify', () => calls.push('second'))

    bus.emit('ui:notify', { level: 'info', text: 'x' })
    bus.emit('ui:notify', { level: 'info', text: 'y' })

    expect(calls).toEqual(['first', 'second', 'second'])
  })

  it('clear elimina todas las suscripciones', () => {
    const bus = createEventBus<AppEvents>()
    const handler = vi.fn()
    bus.on('ui:openPanel', handler)
    bus.clear()
    bus.emit('ui:openPanel', { panel: 'fleet' })
    expect(handler).not.toHaveBeenCalled()
  })
})
