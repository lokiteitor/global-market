import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'

import { CONCESSION_EXPIRY_WARNING_SIM_SECONDS } from '~domain/cadastre'
import { useConcessionAlerts } from '~/composables/useConcessionAlerts'
import { useCadastreStore } from '~/stores/cadastre.store'
import { concession, st, uid } from '~/stores/testing/fixtures'
import { stubNuxtApp } from './game-fakes'

const NOW = 1_000_000

describe('composables/useConcessionAlerts', () => {
  beforeEach(() => {
    stubNuxtApp(NOW)
    setActivePinia(createPinia())
  })

  it('sin concesiones en riesgo: severidad none', () => {
    const cadastre = useCadastreStore()
    cadastre.applyConcessionsSnapshot([
      concession({ status: 'active', expiresAtSim: st(NOW + CONCESSION_EXPIRY_WARNING_SIM_SECONDS + 1) }),
    ])
    const alerts = useConcessionAlerts()
    expect(alerts.severity.value).toBe('none')
    expect(alerts.count.value).toBe(0)
  })

  it('activa que vence dentro del umbral (7 días-sim): warn', () => {
    const cadastre = useCadastreStore()
    cadastre.applyConcessionsSnapshot([
      concession({ status: 'active', expiresAtSim: st(NOW + 86_400) }),
    ])
    const alerts = useConcessionAlerts()
    expect(alerts.severity.value).toBe('warn')
    expect(alerts.expiringSoon.value).toHaveLength(1)
  })

  it('impago/gracia dominan sobre el aviso de vencimiento: danger', () => {
    const cadastre = useCadastreStore()
    cadastre.applyConcessionsSnapshot([
      concession({ status: 'active', expiresAtSim: st(NOW + 86_400) }),
      concession({ id: uid<'Concession'>(61), status: 'grace', expiresAtSim: st(NOW - 1_000) }),
    ])
    const alerts = useConcessionAlerts()
    expect(alerts.severity.value).toBe('danger')
    expect(alerts.atRisk.value).toHaveLength(1)
    expect(alerts.count.value).toBe(2)
  })

  it('las revertidas no cuentan (ya se perdieron: no hay acción posible)', () => {
    const cadastre = useCadastreStore()
    cadastre.applyConcessionsSnapshot([
      concession({ status: 'reverted', expiresAtSim: st(NOW - 1_000) }),
    ])
    const alerts = useConcessionAlerts()
    expect(alerts.severity.value).toBe('none')
  })
})
