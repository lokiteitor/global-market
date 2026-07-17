/**
 * app/composables/useSimNow — cara reactiva del SimClock (ADR-FE-007).
 *
 * Devuelve un ref de solo lectura con el sim-time actual, actualizado ~1 s
 * por el ticker visible-aware del plugin `sim-clock.client`. `null` mientras
 * no haya llegado ninguna meta del servidor (reloj sin anclar) y siempre en
 * SSR (el sim-time en vivo es presentación de cliente, no se hidrata).
 */

import type { Ref } from 'vue'
import { ref } from 'vue'
import type { SimTime } from '~shared/simtime'

export function useSimNow(): Readonly<Ref<SimTime | null>> {
  const { $simNow } = useNuxtApp()
  // En SSR el plugin client-only no existe: ref inerte a null.
  return ($simNow as Readonly<Ref<SimTime | null>> | undefined) ?? ref<SimTime | null>(null)
}
