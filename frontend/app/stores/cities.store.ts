/**
 * stores/cities.store.ts — bounded context: ciudades y su demanda.
 *
 * Ciudades: llegan por snapshot/patch de rooms `viewport:` (ws-protocol §3).
 * Demanda (CityDemand): pull REST por ciudad. Sin lógica económica: el precio
 * y la saturación los calcula el servidor (P1).
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { City, CityDemand, PatchOp } from '~/lib/api/types'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

export const useCitiesStore = defineStore('cities', () => {
  // ── Estado ──
  const collection = ref<ReplicatedCollection<City>>(emptyCollection<City>())
  /** Demanda por ciudad y producto: demandByCity[cityId][productId]. */
  const demandByCity = ref<Record<string, Record<string, CityDemand>>>({})

  // ── Getters ──
  const byId = computed(() => collection.value.byId)
  const list = computed(() => Object.values(collection.value.byId))
  const byRegion = computed(() => {
    const map: Record<string, City[]> = {}
    for (const city of list.value) (map[city.region_id] ??= []).push(city)
    return map
  })

  function demandOf(cityId: string): CityDemand[] {
    return Object.values(demandByCity.value[cityId] ?? {})
  }

  // ── Acciones apply* (única vía de escritura del estado replicado) ──
  function applySnapshot(room: string, data: { cities?: City[] }): void {
    applySnapshotTo(collection.value, room, data.cities ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity !== 'city') continue
      if (op.op === 'upsert') upsertTo(collection.value, op.data as City)
      else removeFrom(collection.value, op.id)
    }
  }

  /** Pull REST GET /world/cities/{id}/demand — reemplaza la demanda de esa ciudad. */
  function setDemand(cityId: string, demands: readonly CityDemand[]): void {
    const map: Record<string, CityDemand> = {}
    for (const d of demands) map[d.product_id] = d
    demandByCity.value[cityId] = map
  }

  return { collection, demandByCity, byId, list, byRegion, demandOf, applySnapshot, applyPatch, setDemand }
})
