/**
 * app/stores/session.store — bounded context Session & Identity (FAD §9.1, §20.2).
 *
 * Estado de sesión de la corporación autenticada. El TOKEN VIVE SOLO EN
 * MEMORIA (FAD §24.2): jamás localStorage/sessionStorage/cookies desde JS;
 * un hard-reload pierde la sesión y exige re-login — es el comportamiento
 * diseñado, no un bug.
 *
 * Inyección sin ciclos: la store consume el PUERTO `AuthApi` (solo el tipo,
 * import type) que el plugin de red le inyecta con `configure()` al arrancar.
 * A su vez, el cliente REST lee el token de aquí vía el `tokenProvider`
 * (closure creada por el plugin) — la dependencia runtime va en ambos casos
 * de la app hacia los puertos, nunca entre módulos concretos.
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Account } from '~domain/auth'
import type { AuthApi } from '~network/auth.api'
import { AppError } from '~network/rest'

export type SessionStatus = 'idle' | 'authenticating' | 'authenticated' | 'error'

export const useSessionStore = defineStore('session', () => {
  /** Puerto inyectado por el plugin de red (doblado en tests). No es estado reactivo. */
  let authApi: AuthApi | null = null

  const account = ref<Account | null>(null)
  /** Token bearer de la sesión — SOLO memoria (FAD §24.2). */
  const token = ref<string | null>(null)
  const status = ref<SessionStatus>('idle')
  /** Último error de autenticación (código estable del contrato), para la UI de login. */
  const lastErrorCode = ref<string | null>(null)

  const isAuthenticated = computed(() => status.value === 'authenticated' && token.value !== null)

  function configure(api: AuthApi): void {
    authApi = api
  }

  function requireApi(): AuthApi {
    if (authApi === null) {
      throw new Error(
        'session.store: AuthApi no configurado — el plugin de red debe llamar a configure() antes de usar la store',
      )
    }
    return authApi
  }

  function purge(): void {
    token.value = null
    account.value = null
  }

  /** POST /auth/sessions: guarda token (memoria) + cuenta. Relanza AppError para la UI. */
  async function login(accountName: string, secret: string): Promise<void> {
    status.value = 'authenticating'
    lastErrorCode.value = null
    try {
      const session = await requireApi().createSession(accountName, secret)
      token.value = session.token
      account.value = session.account
      status.value = 'authenticated'
    } catch (error) {
      purge()
      status.value = 'error'
      lastErrorCode.value = error instanceof AppError ? error.code : null
      throw error
    }
  }

  /**
   * DELETE /auth/sessions/current + purga local. La purga es incondicional:
   * aunque el servidor no responda, el cierre local de sesión se completa
   * (el token del servidor expira solo; no se deja al jugador "atrapado").
   */
  async function logout(): Promise<void> {
    const api = requireApi()
    try {
      await api.deleteCurrentSession()
    } catch {
      // Cierre best-effort en servidor: la sesión local se purga igualmente.
    } finally {
      purge()
      status.value = 'idle'
      lastErrorCode.value = null
    }
  }

  /** GET /auth/me: refresca la cuenta. Un 401 purga la sesión (token caducado). */
  async function fetchMe(): Promise<void> {
    try {
      account.value = await requireApi().getCurrentAccount()
    } catch (error) {
      if (error instanceof AppError && error.code === 'UNAUTHORIZED') {
        purge()
        status.value = 'idle'
      }
      throw error
    }
  }

  return {
    account,
    token,
    status,
    lastErrorCode,
    isAuthenticated,
    configure,
    login,
    logout,
    fetchMe,
  }
})
