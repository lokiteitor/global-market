/**
 * stores/shipments.store.ts — bounded context: cargamentos (shipments).
 *
 * Llegan por snapshot/patch de la room `corp:` (los propios). El cliente solo
 * los localiza (a bordo de un vehículo XOR en un nodo) y los presenta.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { PatchOp, Shipment } from '~/lib/api/types'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

export const useShipmentsStore = defineStore('shipments', () => {
  // ── Estado ──
  const collection = ref<ReplicatedCollection<Shipment>>(emptyCollection<Shipment>())

  // ── Getters ──
  const byId = computed(() => collection.value.byId)
  const list = computed(() => Object.values(collection.value.byId))
  const inTransit = computed(() => list.value.filter((s) => s.status === 'in_transit'))
  const byContract = computed(() => {
    const map: Record<string, Shipment[]> = {}
    for (const s of list.value) {
      if (s.contract_id !== undefined) (map[s.contract_id] ??= []).push(s)
    }
    return map
  })
  const byVehicle = computed(() => {
    const map: Record<string, Shipment[]> = {}
    for (const s of list.value) {
      if (s.vehicle_id !== undefined) (map[s.vehicle_id] ??= []).push(s)
    }
    return map
  })

  // ── Acciones apply* ──
  function applySnapshot(room: string, data: { shipments?: Shipment[] }): void {
    applySnapshotTo(collection.value, room, data.shipments ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity !== 'shipment') continue
      if (op.op === 'upsert') upsertTo(collection.value, op.data as Shipment)
      else removeFrom(collection.value, op.id)
    }
  }

  return { collection, byId, list, inTransit, byContract, byVehicle, applySnapshot, applyPatch }
})
