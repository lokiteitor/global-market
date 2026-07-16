/**
 * composables/useOwnership.ts — OwnershipPolicy de la UI (FAD §5.3, C13).
 *
 * Observable vs Comandable: los controles de mando se deshabilitan
 * preventivamente sobre entidades no propias, con nota explicativa. NO es
 * seguridad (el servidor revalida con 403); es UX honesta.
 */
import { computed, type ComputedRef } from 'vue'
import { useSessionStore } from '~/stores/session.store'

export const NOT_YOURS_NOTE = 'No es de tu corporación: solo observable (el servidor rechazaría el comando).'

export interface OwnershipPolicy {
  /** Id de la corporación autenticada (null si no hay sesión). */
  myAccountId: ComputedRef<string | null>
  /** true si la entidad con ese owner es comandable por el jugador. */
  isMine(ownerAccountId: string | undefined): boolean
}

export function useOwnership(): OwnershipPolicy {
  const session = useSessionStore()
  const myAccountId = computed(() => session.accountId)

  function isMine(ownerAccountId: string | undefined): boolean {
    return ownerAccountId !== undefined && myAccountId.value !== null && ownerAccountId === myAccountId.value
  }

  return { myAccountId, isMine }
}
