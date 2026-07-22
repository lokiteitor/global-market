/**
 * app/composables/useConcessionAlerts — aviso de concesiones en riesgo.
 *
 * Dos niveles sobre el estado replicado (cadastre.store) y el sim-time vivo
 * (useSimNow, reactivo ~1 s — la cuenta atrás avanza sola):
 * - `danger`: concesiones YA en impago o gracia (la cascada de embargo corre).
 * - `warn`: concesiones activas que vencen dentro del umbral de aviso
 *   (CONCESSION_EXPIRY_WARNING_SIM_SECONDS): renovar a tiempo evita entrar
 *   en la cascada (GDD §5.9 4º escalón).
 * Solo presentación: la verdad del ciclo de vida es del servidor.
 */

import { computed } from 'vue'
import type { ComputedRef } from 'vue'
import { simTime } from '~shared/simtime'
import type { Concession } from '~domain/cadastre'
import { CONCESSION_EXPIRY_WARNING_SIM_SECONDS } from '~domain/cadastre'
import { useSimNow } from '~/composables/useSimNow'
import { useCadastreStore } from '~/stores/cadastre.store'

export type ConcessionAlertSeverity = 'none' | 'warn' | 'danger'

export interface ConcessionAlerts {
  /** Activas que vencen dentro del umbral de aviso (renovar a tiempo). */
  readonly expiringSoon: ComputedRef<readonly Concession[]>
  /** Ya en impago o gracia (cascada de embargo en curso). */
  readonly atRisk: ComputedRef<readonly Concession[]>
  readonly severity: ComputedRef<ConcessionAlertSeverity>
  readonly count: ComputedRef<number>
}

export function useConcessionAlerts(): ConcessionAlerts {
  const cadastre = useCadastreStore()
  const simNow = useSimNow()

  const expiringSoon = computed<readonly Concession[]>(() => {
    const now = simNow.value
    if (now === null) {
      return []
    }
    return cadastre
      .concessionsExpiringBefore(simTime(now + CONCESSION_EXPIRY_WARNING_SIM_SECONDS))
      .filter((c) => c.status === 'active')
  })

  const atRisk = computed(() => cadastre.atRiskConcessions)

  const severity = computed<ConcessionAlertSeverity>(() => {
    if (atRisk.value.length > 0) {
      return 'danger'
    }
    return expiringSoon.value.length > 0 ? 'warn' : 'none'
  })

  const count = computed(() => atRisk.value.length + expiringSoon.value.length)

  return { expiringSoon, atRisk, severity, count }
}
