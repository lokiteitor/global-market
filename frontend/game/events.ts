/**
 * game/events — emisor de eventos tipado mínimo (salida de intents, FAD §11.1).
 *
 * Canal ÚNICO de salida del render hacia la app: Phaser emite eventos
 * espaciales (selección, hover…) y la fase UI los consume vía
 * `worldApi.on(...)`. Sin dependencia de Phaser ni de Vue (puro, testeable).
 */

type Handler = (payload: unknown) => void

export class TypedEmitter<E extends Record<string, unknown>> {
  private readonly handlers = new Map<keyof E, Set<Handler>>()

  /** Suscribe y devuelve la función de baja (para limpieza en shutdown). */
  on<K extends keyof E>(event: K, handler: (payload: E[K]) => void): () => void {
    let set = this.handlers.get(event)
    if (!set) {
      set = new Set()
      this.handlers.set(event, set)
    }
    const erased = handler as Handler
    set.add(erased)
    return () => {
      set.delete(erased)
    }
  }

  emit<K extends keyof E>(event: K, payload: E[K]): void {
    const set = this.handlers.get(event)
    if (!set) {
      return
    }
    for (const handler of set) {
      handler(payload)
    }
  }

  /** Da de baja todo (shutdown de escena, FAD §11.13). */
  removeAll(): void {
    this.handlers.clear()
  }
}
