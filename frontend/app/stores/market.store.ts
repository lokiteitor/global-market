/**
 * stores/market.store.ts — bounded context: mercado (tablón CCRI).
 *
 * El tablón es PULL (C10): boardResults refleja la ÚLTIMA consulta REST con
 * sus filtros; no hay stream del tablón entero. Lo propio (myPublications,
 * contracts) llega por la room `corp:` y sus patches. Sin lógica económica:
 * garantías, sorteos y liquidaciones son del servidor (P1).
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Acceptance, Contract, PatchOp, Publication } from '~/lib/api/types'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

export interface BoardFilters {
  kind?: 'sell' | 'buy' | 'freight'
  productId?: string
  regionId?: string
  maxPrice?: string
  minQuantity?: string
}

export const useMarketStore = defineStore('market', () => {
  // ── Estado ──
  /** Resultado de la última consulta del tablón (pull, con filtros). */
  const boardResults = ref<Publication[]>([])
  const boardFilters = ref<BoardFilters>({})
  const boardFetchedAtSim = ref<number | null>(null)

  /** Publicaciones propias (room corp: + patches). */
  const myPublications = ref<ReplicatedCollection<Publication>>(emptyCollection<Publication>())
  /** Aceptaciones propias (pull REST). */
  const acceptancesById = ref<Record<string, Acceptance>>({})
  /** Contratos donde la corporación es parte (room corp: + patches). */
  const contracts = ref<ReplicatedCollection<Contract>>(emptyCollection<Contract>())

  // ── Getters ──
  const myPublicationList = computed(() => Object.values(myPublications.value.byId))
  const myOpenPublications = computed(() =>
    myPublicationList.value.filter((p) => p.status === 'open' || p.status === 'draw_window' || p.status === 'micro_window')
  )
  const contractsById = computed(() => contracts.value.byId)
  const activeContracts = computed(() => Object.values(contracts.value.byId).filter((c) => c.status === 'active'))
  const acceptanceList = computed(() => Object.values(acceptancesById.value))
  const pendingAcceptances = computed(() => acceptanceList.value.filter((a) => a.status === 'pending_draw'))

  // ── Acciones ──
  /** Pull REST GET /contracts/board — reemplaza el resultado y recuerda los filtros. */
  function setBoardResults(filters: BoardFilters, results: readonly Publication[], simSeconds?: number): void {
    boardFilters.value = { ...filters }
    boardResults.value = [...results]
    boardFetchedAtSim.value = simSeconds ?? null
  }

  function applySnapshot(room: string, data: { publications?: Publication[]; contracts?: Contract[] }): void {
    applySnapshotTo(myPublications.value, room, data.publications ?? [])
    applySnapshotTo(contracts.value, room, data.contracts ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity === 'publication') {
        if (op.op === 'upsert') upsertTo(myPublications.value, op.data as Publication)
        else removeFrom(myPublications.value, op.id)
      } else if (op.entity === 'contract') {
        if (op.op === 'upsert') upsertTo(contracts.value, op.data as Contract)
        else removeFrom(contracts.value, op.id)
      }
    }
  }

  /** Pull REST de aceptaciones propias; upsert idempotente. */
  function upsertAcceptances(items: readonly Acceptance[]): void {
    for (const a of items) acceptancesById.value[a.id] = a
  }

  return {
    boardResults,
    boardFilters,
    boardFetchedAtSim,
    myPublications,
    acceptancesById,
    contracts,
    myPublicationList,
    myOpenPublications,
    contractsById,
    activeContracts,
    acceptanceList,
    pendingAcceptances,
    setBoardResults,
    applySnapshot,
    applyPatch,
    upsertAcceptances
  }
})
