/**
 * app/stores/buildings.store — bounded context Buildings (FAD §9.1, §20).
 *
 * Edificios PROPIOS + inventario por edificio + órdenes de producción por
 * edificio. Estado replicado con la tríada idempotente. El inventario se
 * normaliza por (edificio, producto): `applyInventorySnapshot` REEMPLAZA el
 * subárbol del edificio (FAD §20.4 — converge tras re-pull). El progreso de
 * lote NO se calcula aquí: `startedAtSim` + receta + SimClock lo derivan en
 * presentación (P1).
 */

import { computed, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import type { Quantity } from '~domain/quantity'
import { ZERO_QUANTITY } from '~domain/quantity'
import type {
  BatchId,
  Building,
  BuildingId,
  InventoryItem,
  ProductionBatch,
} from '~domain/buildings'
import type { ProductId, RegionId } from '~domain/world'
import { createEntityCollection, indexBy } from './entity-collection'

type BuildingInventory = Readonly<Record<ProductId, InventoryItem>>

export const useBuildingsStore = defineStore('buildings', () => {
  const buildings = createEntityCollection<BuildingId, Building>((b) => b.id)
  const batches = createEntityCollection<BatchId, ProductionBatch>((b) => b.id)

  /** Inventario normalizado por edificio → producto (fuente única por partida). */
  const inventoryByBuilding = shallowRef<Readonly<Record<BuildingId, BuildingInventory>>>({})

  // Índices derivados.
  const buildingIdsByRegion = indexBy(buildings, (b) => b.regionId)
  const buildingIdsByType = indexBy(buildings, (b) => b.buildingTypeId)
  const buildingIdsByConcession = indexBy(buildings, (b) => b.concessionId)
  const buildingIdsByStatus = indexBy(buildings, (b) => b.status)
  const batchIdsByBuilding = indexBy(batches, (b) => b.buildingId)

  function buildingsInRegion(regionId: RegionId): readonly Building[] {
    return (buildingIdsByRegion.value[regionId] ?? []).flatMap((id) => {
      const building = buildings.get(id)
      return building === null ? [] : [building]
    })
  }

  /**
   * Reemplaza el inventario COMPLETO de un edificio (respuesta de
   * GET /buildings/{id}/inventory). Partidas de otros edificios se ignoran
   * (el subárbol es del edificio indicado). Idempotente.
   */
  function applyInventorySnapshot(buildingId: BuildingId, items: readonly InventoryItem[]): void {
    const subtree: Record<ProductId, InventoryItem> = {}
    for (const item of items) {
      if (item.buildingId === buildingId) {
        subtree[item.productId] = item
      }
    }
    inventoryByBuilding.value = { ...inventoryByBuilding.value, [buildingId]: subtree }
  }

  /** Upsert de una partida de inventario (delta puntual). Idempotente. */
  function applyInventoryItem(item: InventoryItem): void {
    const current = inventoryByBuilding.value[item.buildingId] ?? {}
    inventoryByBuilding.value = {
      ...inventoryByBuilding.value,
      [item.buildingId]: { ...current, [item.productId]: item },
    }
  }

  /** Baja del inventario de un edificio (p. ej. edificio eliminado). No-op si no existe. */
  function removeBuildingInventory(buildingId: BuildingId): void {
    if (inventoryByBuilding.value[buildingId] === undefined) {
      return
    }
    inventoryByBuilding.value = Object.fromEntries(
      Object.entries(inventoryByBuilding.value).filter(([key]) => key !== buildingId),
    ) as Readonly<Record<BuildingId, BuildingInventory>>
  }

  function inventoryOf(buildingId: BuildingId): readonly InventoryItem[] {
    return Object.values(inventoryByBuilding.value[buildingId] ?? {})
  }

  /** Stock físico de un producto en un edificio; ZERO si no hay partida. */
  function stockOf(buildingId: BuildingId, productId: ProductId): Quantity {
    return inventoryByBuilding.value[buildingId]?.[productId]?.quantity ?? ZERO_QUANTITY
  }

  /**
   * Reemplaza la cola de producción de UN edificio (respuesta de
   * GET /buildings/{id}/production-batches) conservando la de los demás.
   * Idempotente.
   */
  function applyBuildingBatchesSnapshot(
    buildingId: BuildingId,
    items: readonly ProductionBatch[],
  ): void {
    batches.applyScopedSnapshot((b) => b.buildingId === buildingId, items)
  }

  /** Cola de producción del edificio, ordenada por posición de cola. */
  function batchesOfBuilding(buildingId: BuildingId): readonly ProductionBatch[] {
    return (batchIdsByBuilding.value[buildingId] ?? [])
      .flatMap((id) => {
        const batch = batches.get(id)
        return batch === null ? [] : [batch]
      })
      .toSorted((a, b) => a.queuePosition - b.queuePosition)
  }

  /** Lote actualmente en marcha en el edificio (o `null`). */
  function runningBatchOfBuilding(buildingId: BuildingId): ProductionBatch | null {
    return batchesOfBuilding(buildingId).find((b) => b.status === 'running') ?? null
  }

  /** Lotes que exigen atención (pausados por combustible o trabajadores). */
  const pausedBatches = computed(() =>
    batches.list.value.filter(
      (b) => b.status === 'paused_no_fuel' || b.status === 'paused_no_workers',
    ),
  )

  function clear(): void {
    buildings.clear()
    batches.clear()
    inventoryByBuilding.value = {}
  }

  return {
    // Edificios
    buildingById: buildings.byId,
    buildingList: buildings.list,
    getBuilding: buildings.get,
    applyBuildingsSnapshot: buildings.applySnapshot,
    applyBuilding: buildings.applyOne,
    removeBuilding: buildings.remove,
    buildingIdsByRegion,
    buildingIdsByType,
    buildingIdsByConcession,
    buildingIdsByStatus,
    buildingsInRegion,
    // Inventario
    inventoryByBuilding,
    applyInventorySnapshot,
    applyInventoryItem,
    removeBuildingInventory,
    inventoryOf,
    stockOf,
    // Producción
    batchById: batches.byId,
    batchList: batches.list,
    getBatch: batches.get,
    applyBatchesSnapshot: batches.applySnapshot,
    applyBuildingBatchesSnapshot,
    applyBatch: batches.applyOne,
    removeBatch: batches.remove,
    batchIdsByBuilding,
    batchesOfBuilding,
    runningBatchOfBuilding,
    pausedBatches,
    // Global
    clear,
  }
})
