/**
 * network/transport/sync — orquestador de sincronización (capa de aplicación,
 * FAD §12.5/§12.13, ADR-023 §6: bootstrap REST + deltas WS + re-pull ante hueco).
 *
 * Coordina el puerto `NetworkTransport` con la aplicación SIN conocer stores
 * ni frameworks (inversión de dependencias, como `onMeta` del cliente REST):
 *
 * - `start(token)` conecta tras el login y hace `joinCorp()`; devuelve el
 *   watermark para que la app haga su bootstrap REST inicial ("todo evento
 *   posterior llega con seq > watermark", ws-protocol.md §6).
 * - Los `DomainEvent` se aplican EN ORDEN de llegada a los *appliers*
 *   registrados por prefijo de `eventType` (`"contract."` → handler de
 *   contratos, `""` → catch-all). Los appliers los registra la capa de
 *   aplicación (plugin/stores) desde fuera; deben ser idempotentes (P6:
 *   entrega at-least-once — un efecto ya reflejado debe ser inocuo).
 * - Ante hueco de secuencia (`onGap`) o reconexión con re-join (`onRejoined`)
 *   invoca los handlers de `onResync`: el socket no tiene replay, así que la
 *   única recuperación correcta es re-pull por REST (estado marcado stale).
 */

import type { DomainEvent, JoinResult, NetworkTransport, Unsubscribe } from './port'

/** Aplica un evento de dominio al estado (registrado por la capa de app). */
export type EventApplier = (event: DomainEvent) => void

/** Por qué hay que re-sincronizar por REST (FAD §12.13). */
export type ResyncReason = 'gap' | 'reconnect'

/**
 * Handler global de re-sincronización. `watermark` es el nuevo watermark del
 * re-join cuando la causa es `reconnect`; `null` cuando la causa es un hueco
 * detectado dentro de la conexión (el estado sigue avanzando por el socket).
 */
export type ResyncHandler = (reason: ResyncReason, watermark: number | null) => void

export interface SyncOrchestratorOptions {
  /**
   * Excepción lanzada por un applier al aplicar un evento: se reporta aquí y
   * el despacho CONTINÚA con los demás appliers (un applier roto no puede
   * dejar al resto del estado sin sus deltas). Sin este callback, la
   * excepción se propaga (los appliers deben ser totales).
   */
  readonly onApplierError?: (error: unknown, event: DomainEvent) => void
}

export interface SyncOrchestrator {
  /**
   * Registra un applier para los eventos cuyo `eventType` empiece por
   * `prefix` (`"contract."`, `"vehicle."`, `""` = todos). Varios appliers
   * pueden solapar un mismo evento; se invocan en orden de registro.
   */
  registerApplier(prefix: string, applier: EventApplier): Unsubscribe

  /** Registra un handler global de re-sincronización (REST re-pull). */
  onResync(handler: ResyncHandler): Unsubscribe

  /**
   * Conecta el transporte (tras el login REST) y se une a la room `corp`.
   * Resuelve con el watermark del join: la app bootstrapea por REST desde
   * ahí. Idempotente frente a dobles arranques (rechaza si ya arrancó).
   */
  start(token: string): Promise<JoinResult>

  /** Cierre limpio (FAD §12.14): baja de la room, cierra el transporte. */
  stop(): void
}

interface RegisteredApplier {
  readonly prefix: string
  readonly applier: EventApplier
}

export function createSyncOrchestrator(
  transport: NetworkTransport,
  options: SyncOrchestratorOptions = {},
): SyncOrchestrator {
  /** Orden de registro = orden de invocación (determinismo de aplicación). */
  const appliers: RegisteredApplier[] = []
  const resyncHandlers = new Set<ResyncHandler>()
  let subscriptions: Unsubscribe[] = []
  let started = false

  function dispatch(event: DomainEvent): void {
    for (const entry of [...appliers]) {
      if (!event.eventType.startsWith(entry.prefix)) {
        continue
      }
      try {
        entry.applier(event)
      } catch (error) {
        if (options.onApplierError === undefined) {
          throw error
        }
        options.onApplierError(error, event)
      }
    }
  }

  function requestResync(reason: ResyncReason, watermark: number | null): void {
    for (const handler of resyncHandlers) {
      handler(reason, watermark)
    }
  }

  function stop(): void {
    if (!started) {
      return
    }
    started = false
    for (const unsubscribe of subscriptions) {
      unsubscribe()
    }
    subscriptions = []
    transport.leave()
    transport.close()
  }

  return {
    registerApplier(prefix, applier): Unsubscribe {
      const entry: RegisteredApplier = { prefix, applier }
      appliers.push(entry)
      return () => {
        const index = appliers.indexOf(entry)
        if (index !== -1) {
          appliers.splice(index, 1)
        }
      }
    },

    onResync(handler): Unsubscribe {
      resyncHandlers.add(handler)
      return () => resyncHandlers.delete(handler)
    },

    async start(token): Promise<JoinResult> {
      if (started) {
        throw new Error('SyncOrchestrator.start(): ya arrancado')
      }
      started = true
      subscriptions = [
        transport.onEvent(dispatch),
        // Hueco dentro de la conexión: re-pull; el socket sigue entregando.
        transport.onGap(() => {
          requestResync('gap', null)
        }),
        // Reconexión + re-join automático: el estado previo es stale respecto
        // al watermark nuevo — re-pull completo por REST (ws-protocol.md §6).
        transport.onRejoined((result) => {
          requestResync('reconnect', result.watermark)
        }),
      ]
      try {
        transport.connect(token)
        return await transport.joinCorp()
      } catch (error) {
        stop()
        throw error
      }
    },

    stop,
  }
}
