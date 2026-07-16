/**
 * stores/buildings.store.ts — bounded context: edificios, inventarios y
 * producción.
 *
 * Edificios: llegan por `corp:` (los propios) y por `viewport:` (los visibles
 * de cualquier corporación). Inventarios y lotes de producción: pull REST por
 * edificio + patches de `corp:`. Observable ≠ comandable (C13): la distinción
 * se deriva comparando owner_account_id, no se decide aquí.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { Building, InventoryItem, PatchOp, ProductionBatch } from '~/lib/api/types'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

export const useBuildingsStore = defineStore('buildings', () => {
  // ── Estado ──
  const collection = ref<ReplicatedCollection<Building>>(emptyCollection<Building>())
  /** Inventario físico por edificio (pull REST; reemplazo completo por edificio). */
  const inventoryByBuilding = ref<Record<string, InventoryItem[]>>({})
  /** Lotes de producción normalizados byId. */
  const batchesById = ref<Record<string, ProductionBatch>>({})

  // ── Getters ──
  const byId = computed(() => collection.value.byId)
  const list = computed(() => Object.values(collection.value.byId))
  const idsByRegion = computed(() => {
    const map: Record<string, string[]> = {}
    for (const b of list.value) (map[b.region_id] ??= []).push(b.id)
    return map
  })
  const batchesByBuilding = computed(() => {
    const map: Record<string, ProductionBatch[]> = {}
    for (const batch of Object.values(batchesById.value)) {
      ;(map[batch.building_id] ??= []).push(batch)
    }
    for (const queue of Object.values(map)) queue.sort((a, b) => a.queue_position - b.queue_position)
    return map
  })

  /** Edificios de una corporación (los "comandables" si es la propia). */
  function ownedBy(accountId: string): Building[] {
    return list.value.filter((b) => b.owner_account_id === accountId)
  }

  function inventoryOf(buildingId: string): InventoryItem[] {
    return inventoryByBuilding.value[buildingId] ?? []
  }

  // ── Acciones apply* ──
  function applySnapshot(room: string, data: { buildings?: Building[] }): void {
    applySnapshotTo(collection.value, room, data.buildings ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity !== 'building') continue
      if (op.op === 'upsert') upsertTo(collection.value, op.data as Building)
      else removeFrom(collection.value, op.id)
    }
  }

  /** Pull REST GET /world/buildings/{id}/inventory. */
  function setInventory(buildingId: string, items: readonly InventoryItem[]): void {
    inventoryByBuilding.value[buildingId] = [...items]
  }

  /** Pull REST GET /world/buildings/{id}/batches — reemplaza la cola del edificio. */
  function setBatches(buildingId: string, batches: readonly ProductionBatch[]): void {
    for (const [id, batch] of Object.entries(batchesById.value)) {
      if (batch.building_id === buildingId) delete batchesById.value[id]
    }
    for (const batch of batches) batchesById.value[batch.id] = batch
  }

  /** Upsert puntual de un lote (evento batch.completed/batch.paused). */
  function upsertBatch(batch: ProductionBatch): void {
    batchesById.value[batch.id] = batch
  }

  return {
    collection,
    inventoryByBuilding,
    batchesById,
    byId,
    list,
    idsByRegion,
    batchesByBuilding,
    ownedBy,
    inventoryOf,
    applySnapshot,
    applyPatch,
    setInventory,
    setBatches,
    upsertBatch
  }
})
