import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { ZERO_QUANTITY } from '~domain/quantity'
import { useBuildingsStore } from './buildings.store'
import { building, inventoryItem, productionBatch, qty, uid } from './testing/fixtures'

const REGION_A = uid<'Region'>(1)
const BUILDING_A = uid<'Building'>(70)
const BUILDING_B = uid<'Building'>(71)
const PRODUCT_IRON = uid<'Product'>(10)
const PRODUCT_COAL = uid<'Product'>(11)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/buildings.store — edificios', () => {
  it('tríada idempotente e índices por región/tipo/concesión/estado', () => {
    const store = useBuildingsStore()
    const b1 = building({ id: BUILDING_A, regionId: REGION_A, status: 'operational' })
    const b2 = building({ id: BUILDING_B, regionId: REGION_A, status: 'damaged' })
    store.applyBuildingsSnapshot([b1, b2])
    store.applyBuildingsSnapshot([b1, b2])

    expect(store.buildingList).toHaveLength(2)
    expect(store.buildingIdsByRegion[REGION_A]).toEqual([BUILDING_A, BUILDING_B])
    expect(store.buildingIdsByStatus['damaged']).toEqual([BUILDING_B])
    expect(store.buildingsInRegion(REGION_A)).toEqual([b1, b2])

    store.removeBuilding(BUILDING_B)
    store.removeBuilding(BUILDING_B)
    expect(store.buildingIdsByRegion[REGION_A]).toEqual([BUILDING_A])
  })
})

describe('app/stores/buildings.store — inventario por edificio', () => {
  it('applyInventorySnapshot reemplaza SOLO el subárbol del edificio', () => {
    const store = useBuildingsStore()
    const ironA = inventoryItem({ buildingId: BUILDING_A, productId: PRODUCT_IRON })
    const coalA = inventoryItem({ buildingId: BUILDING_A, productId: PRODUCT_COAL })
    const ironB = inventoryItem({ buildingId: BUILDING_B, productId: PRODUCT_IRON })
    store.applyInventorySnapshot(BUILDING_A, [ironA, coalA])
    store.applyInventorySnapshot(BUILDING_B, [ironB])

    // Nuevo snapshot de A sin carbón: la partida cae; B queda intacto.
    store.applyInventorySnapshot(BUILDING_A, [ironA])
    expect(store.inventoryOf(BUILDING_A)).toEqual([ironA])
    expect(store.inventoryOf(BUILDING_B)).toEqual([ironB])
  })

  it('ignora partidas de OTRO edificio dentro del snapshot (subárbol acotado)', () => {
    const store = useBuildingsStore()
    const foreign = inventoryItem({ buildingId: BUILDING_B, productId: PRODUCT_IRON })
    store.applyInventorySnapshot(BUILDING_A, [foreign])
    expect(store.inventoryOf(BUILDING_A)).toEqual([])
  })

  it('applyInventoryItem upserta idempotente y stockOf devuelve ZERO sin partida', () => {
    const store = useBuildingsStore()
    const item = inventoryItem({
      buildingId: BUILDING_A,
      productId: PRODUCT_IRON,
      quantity: qty('500'),
    })
    store.applyInventoryItem(item)
    store.applyInventoryItem(item)

    expect(store.inventoryOf(BUILDING_A)).toEqual([item])
    expect(store.stockOf(BUILDING_A, PRODUCT_IRON)).toBe('500')
    expect(store.stockOf(BUILDING_A, PRODUCT_COAL)).toBe(ZERO_QUANTITY)
    expect(store.stockOf(BUILDING_B, PRODUCT_IRON)).toBe(ZERO_QUANTITY)

    const updated = inventoryItem({
      buildingId: BUILDING_A,
      productId: PRODUCT_IRON,
      quantity: qty('750'),
    })
    store.applyInventoryItem(updated)
    expect(store.stockOf(BUILDING_A, PRODUCT_IRON)).toBe('750')
  })

  it('removeBuildingInventory es no-op si el edificio no tiene inventario', () => {
    const store = useBuildingsStore()
    store.applyInventoryItem(inventoryItem({ buildingId: BUILDING_A }))
    store.removeBuildingInventory(BUILDING_B)
    store.removeBuildingInventory(BUILDING_A)
    store.removeBuildingInventory(BUILDING_A)
    expect(store.inventoryOf(BUILDING_A)).toEqual([])
  })
})

describe('app/stores/buildings.store — cola de producción', () => {
  it('applyBuildingBatchesSnapshot reemplaza la cola de UN edificio', () => {
    const store = useBuildingsStore()
    const a1 = productionBatch({ id: uid<'Batch'>(80), buildingId: BUILDING_A, queuePosition: 1 })
    const a2 = productionBatch({ id: uid<'Batch'>(81), buildingId: BUILDING_A, queuePosition: 0 })
    const b1 = productionBatch({ id: uid<'Batch'>(82), buildingId: BUILDING_B, queuePosition: 0 })
    store.applyBuildingBatchesSnapshot(BUILDING_A, [a1, a2])
    store.applyBuildingBatchesSnapshot(BUILDING_B, [b1])

    // Ordenados por posición de cola, no por llegada.
    expect(store.batchesOfBuilding(BUILDING_A)).toEqual([a2, a1])

    // Re-pull de A sin a1: cae de la cola; B intacto. Reaplicar = no-op.
    store.applyBuildingBatchesSnapshot(BUILDING_A, [a2])
    store.applyBuildingBatchesSnapshot(BUILDING_A, [a2])
    expect(store.batchesOfBuilding(BUILDING_A)).toEqual([a2])
    expect(store.batchesOfBuilding(BUILDING_B)).toEqual([b1])
  })

  it('runningBatchOfBuilding y pausedBatches derivan del estado', () => {
    const store = useBuildingsStore()
    const running = productionBatch({
      id: uid<'Batch'>(80),
      buildingId: BUILDING_A,
      status: 'running',
      queuePosition: 0,
    })
    const queued = productionBatch({
      id: uid<'Batch'>(81),
      buildingId: BUILDING_A,
      status: 'queued',
      queuePosition: 1,
    })
    const paused = productionBatch({
      id: uid<'Batch'>(82),
      buildingId: BUILDING_B,
      status: 'paused_no_fuel',
      queuePosition: 0,
    })
    store.applyBatchesSnapshot([running, queued, paused])

    expect(store.runningBatchOfBuilding(BUILDING_A)).toEqual(running)
    expect(store.runningBatchOfBuilding(BUILDING_B)).toBeNull()
    expect(store.pausedBatches).toEqual([paused])
  })

  it('clear purga edificios, lotes e inventario', () => {
    const store = useBuildingsStore()
    store.applyBuilding(building())
    store.applyBatch(productionBatch())
    store.applyInventoryItem(inventoryItem())
    store.clear()
    expect(store.buildingList).toHaveLength(0)
    expect(store.batchList).toHaveLength(0)
    expect(store.inventoryOf(BUILDING_A)).toEqual([])
  })
})
