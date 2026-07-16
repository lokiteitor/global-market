/**
 * composables/useRooms.ts — suscripción a áreas de interés (rooms WS).
 *
 * corp:<account_id> (todo lo propio), viewport:<bbox> (entidades espaciales
 * visibles; un join reemplaza el viewport anterior — solo hay uno activo por
 * conexión) y alerts:<account_id> (messages puntuales). El re-join tras una
 * reconexión lo hace el transporte automáticamente.
 */
import { alertsRoom, corpRoom, viewportRoom, type NetworkTransport, type ViewportBBox } from '~/lib/net/transport'
import { useSessionStore } from '~/stores/session.store'

export function useRooms() {
  const session = useSessionStore()
  // Capturado en contexto síncrono de setup (tras un await no hay instancia Nuxt).
  const injected = useNuxtApp().$transport as NetworkTransport | undefined

  function requireTransport(): NetworkTransport {
    if (injected === undefined) throw new Error('useRooms(): la capa de red es client-only (plugin 02.network.client)')
    return injected
  }

  /** Room de la corporación propia (edificios, flota, contratos, ledger…). */
  function joinCorp(): void {
    const id = session.accountId
    if (id === null) return // sin sesión no hay room propia
    requireTransport().join(corpRoom(id))
  }

  /** Área de interés espacial; REEMPLAZA cualquier viewport anterior. */
  function joinViewport(bbox: ViewportBBox): void {
    requireTransport().join(viewportRoom(bbox))
  }

  /** Alertas puntuales (sorteos, liquidaciones, mantenimiento). */
  function joinAlerts(): void {
    const id = session.accountId
    if (id === null) return
    requireTransport().join(alertsRoom(id))
  }

  /** Une las tres áreas estándar de una sesión de juego. */
  function joinAll(bbox?: ViewportBBox): void {
    joinCorp()
    joinAlerts()
    if (bbox !== undefined) joinViewport(bbox)
  }

  /** Abandona todas las rooms activas (salida de /play, FAD §12.12). */
  function leaveAll(): void {
    const transport = requireTransport()
    for (const room of transport.rooms()) transport.leave(room)
  }

  function activeRooms(): readonly string[] {
    return requireTransport().rooms()
  }

  return { joinCorp, joinViewport, joinAlerts, joinAll, leaveAll, activeRooms }
}
