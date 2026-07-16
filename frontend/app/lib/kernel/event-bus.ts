/**
 * kernel/event-bus.ts — event bus tipado (FAD §19).
 *
 * Único canal de INTENCIONES entre el mundo Phaser y la UI Vue: game/ emite
 * intents espaciales, components/ emite intents de UI; ambos LEEN estado por
 * stores. Ni Phaser conoce Vue ni Vue conoce Phaser (O2).
 */

/**
 * Catálogo inicial de eventos de la aplicación (crece por feature).
 * Type alias (no interface) para que satisfaga Record<string, unknown>.
 */
export type AppEvents = {
  /** Selección de una entidad del mundo (clic/tap sobre el canvas o una lista). */
  'world:select': { kind: 'city' | 'building' | 'vehicle' | 'deposit' | 'node'; id: string }
  /** Petición de viaje de cámara a una coordenada del mundo (lon/lat, SRID 4326). */
  'camera:flyTo': { lon: number; lat: number }
  /** Apertura de un panel de gestión del HUD. */
  'ui:openPanel': { panel: string }
  /** Notificación efímera (toast) hacia el feed de notificaciones. */
  'ui:notify': { level: 'info' | 'success' | 'warning' | 'error'; text: string }
}

export type EventName = keyof AppEvents

export type Handler<P> = (payload: P) => void

export interface EventBus<E extends Record<string, unknown>> {
  /** Suscribe; devuelve la función de desuscripción. */
  on<K extends keyof E>(event: K, handler: Handler<E[K]>): () => void
  off<K extends keyof E>(event: K, handler: Handler<E[K]>): void
  emit<K extends keyof E>(event: K, payload: E[K]): void
  /** Elimina todas las suscripciones (teardown de tests y de la escena). */
  clear(): void
}

export function createEventBus<E extends Record<string, unknown>>(): EventBus<E> {
  const handlers = new Map<keyof E, Set<Handler<never>>>()

  return {
    on(event, handler) {
      let set = handlers.get(event)
      if (!set) {
        set = new Set()
        handlers.set(event, set)
      }
      set.add(handler as Handler<never>)
      return () => this.off(event, handler)
    },

    off(event, handler) {
      handlers.get(event)?.delete(handler as Handler<never>)
    },

    emit(event, payload) {
      const set = handlers.get(event)
      if (!set) return
      // Copia defensiva: un handler puede (des)suscribir durante la emisión.
      for (const handler of [...set]) {
        ;(handler as Handler<E[typeof event]>)(payload)
      }
    },

    clear() {
      handlers.clear()
    }
  }
}

/** Bus global de la aplicación (una instancia por pestaña/sesión). */
export const appBus: EventBus<AppEvents> = createEventBus<AppEvents>()
