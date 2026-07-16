/**
 * stores/session.store.ts — bounded context: sesión/identidad.
 *
 * SIMPLIFICACIÓN v1 (aceptada): el token de sesión vive en memoria y se
 * respalda en sessionStorage (solo dev, solo cliente). El endurecimiento
 * (cookies httpOnly vía BFF u otro esquema) queda para una fase posterior.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Account, SessionCreated } from '~/lib/api/types'

const STORAGE_KEY = 'imperio.session'

export type AuthStatus = 'anonymous' | 'authenticating' | 'authenticated'

export const useSessionStore = defineStore('session', () => {
  // ── Estado ──
  const account = ref<Account | null>(null)
  const token = ref<string | null>(null)
  const sessionId = ref<string | null>(null)
  const expiresAt = ref<string | null>(null)
  const status = ref<AuthStatus>('anonymous')

  // ── Getters ──
  const isAuthenticated = computed(() => status.value === 'authenticated' && token.value !== null)
  const accountId = computed(() => account.value?.id ?? null)
  const accountName = computed(() => account.value?.name ?? null)

  // ── Acciones ──
  function beginAuthentication(): void {
    status.value = 'authenticating'
  }

  /** Eco de POST /auth/sessions — el servidor decide, el cliente refleja (P1). */
  function setSession(created: SessionCreated): void {
    account.value = created.account
    token.value = created.token
    sessionId.value = created.session_id
    expiresAt.value = created.expires_at
    status.value = 'authenticated'
    persist()
  }

  function clearSession(): void {
    account.value = null
    token.value = null
    sessionId.value = null
    expiresAt.value = null
    status.value = 'anonymous'
    if (typeof window !== 'undefined') window.sessionStorage.removeItem(STORAGE_KEY)
  }

  function persist(): void {
    if (typeof window === 'undefined') return
    window.sessionStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        account: account.value,
        token: token.value,
        sessionId: sessionId.value,
        expiresAt: expiresAt.value
      })
    )
  }

  /** Restaura la sesión respaldada (llamado desde un plugin client-only). */
  function restore(): boolean {
    if (typeof window === 'undefined') return false
    const raw = window.sessionStorage.getItem(STORAGE_KEY)
    if (raw === null) return false
    try {
      const parsed = JSON.parse(raw) as {
        account: Account | null
        token: string | null
        sessionId: string | null
        expiresAt: string | null
      }
      if (parsed.token === null || parsed.account === null) return false
      account.value = parsed.account
      token.value = parsed.token
      sessionId.value = parsed.sessionId
      expiresAt.value = parsed.expiresAt
      status.value = 'authenticated'
      return true
    } catch {
      window.sessionStorage.removeItem(STORAGE_KEY)
      return false
    }
  }

  return {
    account,
    token,
    sessionId,
    expiresAt,
    status,
    isAuthenticated,
    accountId,
    accountName,
    beginAuthentication,
    setSession,
    clearSession,
    restore
  }
})
