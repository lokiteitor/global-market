/**
 * stores/fleet.store.ts — bounded context: flota de vehículos.
 *
 * Vehículos con su estado cinemático (VehiclePosition: hito + progreso
 * derivado). La INTERPOLACIÓN entre hitos es presentación y vive en game/
 * (FE-2+); esta store solo espeja el último estado autoritativo (P1/P4).
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { PatchOp, Vehicle } from '~/lib/api/types'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

export const useFleetStore = defineStore('fleet', () => {
  // ── Estado ──
  const collection = ref<ReplicatedCollection<Vehicle>>(emptyCollection<Vehicle>())

  // ── Getters ──
  const byId = computed(() => collection.value.byId)
  const list = computed(() => Object.values(collection.value.byId))
  const inTransit = computed(() => list.value.filter((v) => v.status === 'in_transit'))
  const idle = computed(() => list.value.filter((v) => v.status === 'idle'))
  const byStatus = computed(() => {
    const map: Record<string, Vehicle[]> = {}
    for (const v of list.value) (map[v.status] ??= []).push(v)
    return map
  })

  /** Flota de una corporación (comandable solo si es la propia, C13). */
  function ownedBy(accountId: string): Vehicle[] {
    return list.value.filter((v) => v.owner_account_id === accountId)
  }

  // ── Acciones apply* ──
  function applySnapshot(room: string, data: { vehicles?: Vehicle[] }): void {
    applySnapshotTo(collection.value, room, data.vehicles ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity !== 'vehicle') continue
      if (op.op === 'upsert') upsertTo(collection.value, op.data as Vehicle)
      else removeFrom(collection.value, op.id)
    }
  }

  return { collection, byId, list, inTransit, idle, byStatus, ownedBy, applySnapshot, applyPatch }
})
