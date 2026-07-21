import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useCadastreStore } from './cadastre.store'
import { concession, st, uid } from './testing/fixtures'

const REGION_A = uid<'Region'>(1)
const REGION_B = uid<'Region'>(2)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/cadastre.store', () => {
  it('tríada idempotente: snapshot reemplaza, applyOne upserta, remove es no-op sin id', () => {
    const store = useCadastreStore()
    const c1 = concession({ id: uid<'Concession'>(60) })
    const c2 = concession({ id: uid<'Concession'>(61) })
    store.applyConcessionsSnapshot([c1, c2])
    store.applyConcessionsSnapshot([c1, c2])
    expect(store.concessionList).toHaveLength(2)

    store.applyConcessionsSnapshot([c1])
    expect(store.getConcession(c2.id)).toBeNull()

    store.applyConcession(c2)
    store.applyConcession(c2)
    expect(store.concessionList).toHaveLength(2)

    store.removeConcession(uid<'Concession'>(99))
    expect(store.concessionList).toHaveLength(2)
  })

  it('índices por región y por estado', () => {
    const store = useCadastreStore()
    const active = concession({ id: uid<'Concession'>(60), regionId: REGION_A, status: 'active' })
    const grace = concession({ id: uid<'Concession'>(61), regionId: REGION_B, status: 'grace' })
    const delinquent = concession({
      id: uid<'Concession'>(62),
      regionId: REGION_A,
      status: 'delinquent',
    })
    store.applyConcessionsSnapshot([active, grace, delinquent])

    expect(store.concessionIdsByRegion[REGION_A]).toEqual([active.id, delinquent.id])
    expect(store.concessionIdsByStatus['grace']).toEqual([grace.id])
    expect(store.concessionsInRegion(REGION_B)).toEqual([grace])
    expect(store.activeConcessions).toEqual([active])
    expect(store.atRiskConcessions).toEqual([grace, delinquent])
  })

  it('concessionsExpiringBefore avisa de renovaciones (sin contar revertidas)', () => {
    const store = useCadastreStore()
    const soon = concession({ id: uid<'Concession'>(60), expiresAtSim: st(1_000) })
    const later = concession({ id: uid<'Concession'>(61), expiresAtSim: st(9_999_999) })
    const reverted = concession({
      id: uid<'Concession'>(62),
      expiresAtSim: st(500),
      status: 'reverted',
    })
    store.applyConcessionsSnapshot([soon, later, reverted])

    expect(store.concessionsExpiringBefore(st(2_000))).toEqual([soon])
  })

  it('clear purga', () => {
    const store = useCadastreStore()
    store.applyConcession(concession())
    store.clear()
    expect(store.concessionList).toHaveLength(0)
  })
})
