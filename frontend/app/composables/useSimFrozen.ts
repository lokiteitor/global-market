/**
 * app/composables/useSimFrozen — ¿está el mundo pausado? (FAD §12.9, C9).
 *
 * Cara reactiva del estado `frozen` del SimClock, actualizada por el mismo
 * ticker visible-aware del plugin `sim-clock.client`. `false` en SSR y hasta
 * que el cliente sepa lo contrario (el estado de mantenimiento llega por la
 * red: 503 MAINTENANCE_WINDOW → freeze()).
 */

import type { Ref } from 'vue'
import { ref } from 'vue'

export function useSimFrozen(): Readonly<Ref<boolean>> {
  const { $simFrozen } = useNuxtApp()
  // En SSR el plugin client-only no existe: ref inerte a false.
  return ($simFrozen as Readonly<Ref<boolean>> | undefined) ?? ref(false)
}
