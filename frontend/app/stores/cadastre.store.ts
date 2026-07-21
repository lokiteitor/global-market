/**
 * app/stores/cadastre.store — bounded context Cadastre (FAD §9.1, §20).
 *
 * Concesiones de suelo PROPIAS (el listado del contrato es por corporación).
 * Estado replicado con la tríada idempotente; getters puros. La política de
 * titularidad (holder) la aplica domain/ownership en la UI.
 */

import { computed } from 'vue'
import { defineStore } from 'pinia'
import type { SimTime } from '~shared/simtime'
import type { Concession, ConcessionId } from '~domain/cadastre'
import type { RegionId } from '~domain/world'
import { createEntityCollection, indexBy } from './entity-collection'

export const useCadastreStore = defineStore('cadastre', () => {
  const concessions = createEntityCollection<ConcessionId, Concession>((c) => c.id)

  const concessionIdsByRegion = indexBy(concessions, (c) => c.regionId)
  const concessionIdsByStatus = indexBy(concessions, (c) => c.status)

  /** Concesiones plenamente vigentes (sin impago ni gracia). */
  const activeConcessions = computed(() =>
    concessions.list.value.filter((c) => c.status === 'active'),
  )

  /** Concesiones que exigen atención del jugador (canon impagado o en gracia). */
  const atRiskConcessions = computed(() =>
    concessions.list.value.filter((c) => c.status === 'delinquent' || c.status === 'grace'),
  )

  function concessionsInRegion(regionId: RegionId): readonly Concession[] {
    return (concessionIdsByRegion.value[regionId] ?? []).flatMap((id) => {
      const concession = concessions.get(id)
      return concession === null ? [] : [concession]
    })
  }

  /** Concesiones no revertidas que expiran antes de `deadline` (aviso de renovación). */
  function concessionsExpiringBefore(deadline: SimTime): readonly Concession[] {
    return concessions.list.value.filter(
      (c) => c.status !== 'reverted' && c.expiresAtSim < deadline,
    )
  }

  function clear(): void {
    concessions.clear()
  }

  return {
    concessionById: concessions.byId,
    concessionList: concessions.list,
    getConcession: concessions.get,
    applyConcessionsSnapshot: concessions.applySnapshot,
    applyConcession: concessions.applyOne,
    removeConcession: concessions.remove,
    concessionIdsByRegion,
    concessionIdsByStatus,
    activeConcessions,
    atRiskConcessions,
    concessionsInRegion,
    concessionsExpiringBefore,
    clear,
  }
})
