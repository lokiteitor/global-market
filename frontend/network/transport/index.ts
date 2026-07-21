/**
 * network/transport — superficie pública de la capa de tiempo real
 * (FAD §4.4/§12, ADR-FE-004).
 *
 * Lo exportado aquí es lo ÚNICO que puede consumir la app: el puerto
 * `NetworkTransport`, la ACL del Gateway, el orquestador de sincronización y
 * sus tipos. Los frames crudos del protocolo WS no salen de esta capa (O5).
 */

export type {
  ConnectionState,
  DomainEvent,
  JoinResult,
  NetworkTransport,
  SequenceGap,
  Unsubscribe,
  WebSocketFactory,
  WsHandle,
  WsHandlers,
} from './port'
export type { GatewayTransport, GatewayTransportOptions } from './gateway.adapter'
export { createGatewayTransport } from './gateway.adapter'
export type {
  EventApplier,
  ResyncHandler,
  ResyncReason,
  SyncOrchestrator,
  SyncOrchestratorOptions,
} from './sync'
export { createSyncOrchestrator } from './sync'
