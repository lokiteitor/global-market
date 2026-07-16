/**
 * composables/useSession.ts — ciclo de vida de la sesión desde la UI.
 *
 * SIMPLIFICACIÓN v1 (aceptada): token en memoria + respaldo en sessionStorage
 * (dev) — lo gestiona session.store; aquí solo se orquesta login/logout contra
 * POST /auth/sessions y DELETE /auth/sessions/current. El servidor decide (P1):
 * el error de credenciales llega tipado y se muestra tal cual.
 */
import type { ApiError, SessionCreated } from '~/lib/api/types'
import type { Result } from '~/lib/kernel/result'
import { err, ok } from '~/lib/kernel/result'
import type { NetworkTransport } from '~/lib/net/transport'
import { useSessionStore } from '~/stores/session.store'
import { useApi } from './useApi'

export interface SessionActions {
  login(accountName: string, secret: string): Promise<Result<SessionCreated, ApiError>>
  logout(): Promise<void>
  /** Restaura la sesión respaldada en sessionStorage (solo cliente). */
  restore(): boolean
}

export function useSession(): SessionActions {
  const store = useSessionStore()
  const api = useApi()
  // Se captura aquí (contexto síncrono de setup); tras un await ya no habría
  // instancia Nuxt disponible.
  let transport: NetworkTransport | undefined
  try {
    transport = useNuxtApp().$transport as NetworkTransport | undefined
  } catch {
    transport = undefined // fuera de contexto Nuxt (tests)
  }

  async function login(accountName: string, secret: string): Promise<Result<SessionCreated, ApiError>> {
    store.beginAuthentication()
    const result = await api.createSession(accountName.trim(), secret)
    if (!result.ok) {
      store.clearSession()
      return err(result.error)
    }
    store.setSession(result.value.data)
    return ok(result.value.data)
  }

  async function logout(): Promise<void> {
    // Best-effort: aunque el servidor falle, la sesión local se descarta.
    await api.deleteCurrentSession()
    // Cierre ordenado del WS: olvida las rooms activas y no reconecta
    // (FAD §12.12). El transporte lo provee el plugin 02.network.client.
    transport?.close()
    store.clearSession()
  }

  function restore(): boolean {
    if (store.isAuthenticated) return true
    return store.restore()
  }

  return { login, logout, restore }
}
