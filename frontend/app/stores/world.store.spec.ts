import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { buildingType, city, deposit, product, recipe, region, uid } from './testing/fixtures'
import { useWorldStore } from './world.store'

const REGION_A = uid<'Region'>(1)
const REGION_B = uid<'Region'>(2)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/world.store — tríada apply idempotente', () => {
  it('applySnapshot reemplaza el subárbol (bajas incluidas) y reaplicarlo es no-op', () => {
    const store = useWorldStore()
    const p1 = product({ id: uid<'Product'>(10), code: 'IRON' })
    const p2 = product({ id: uid<'Product'>(11), code: 'COAL' })
    store.applyProductsSnapshot([p1, p2])
    expect(store.productList).toHaveLength(2)

    // El snapshot siguiente ya no trae p2: debe desaparecer (no fusión ciega).
    store.applyProductsSnapshot([p1])
    expect(store.productList).toHaveLength(1)
    expect(store.getProduct(p2.id)).toBeNull()

    // Reaplicar el mismo snapshot deja el estado equivalente.
    store.applyProductsSnapshot([p1])
    expect(store.productList).toHaveLength(1)
    expect(store.getProduct(p1.id)).toEqual(p1)
  })

  it('applyOne upserta sin duplicar (idempotente) y actualiza la entidad', () => {
    const store = useWorldStore()
    const r = region({ id: REGION_A, name: 'Norte' })
    store.applyRegion(r)
    store.applyRegion(r)
    expect(store.regionList).toHaveLength(1)

    store.applyRegion(region({ id: REGION_A, name: 'Norte renombrado' }))
    expect(store.regionList).toHaveLength(1)
    expect(store.getRegion(REGION_A)?.name).toBe('Norte renombrado')
  })

  it('remove es no-op sobre ids inexistentes', () => {
    const store = useWorldStore()
    store.applyRegion(region({ id: REGION_A }))
    store.removeRegion(REGION_B)
    store.removeRegion(REGION_A)
    store.removeRegion(REGION_A)
    expect(store.regionList).toHaveLength(0)
  })
})

describe('app/stores/world.store — índices y getters de catálogo', () => {
  it('productByCode / buildingTypeByCode / recipeByCode resuelven por código', () => {
    const store = useWorldStore()
    const iron = product({ id: uid<'Product'>(10), code: 'IRON' })
    const mine = buildingType({ id: uid<'BuildingType'>(20), code: 'MINE' })
    const rec = recipe({ id: uid<'Recipe'>(30), code: 'MINE_IRON' })
    store.applyProductsSnapshot([iron])
    store.applyBuildingTypesSnapshot([mine])
    store.applyRecipesSnapshot([rec])

    expect(store.productByCode('IRON')).toEqual(iron)
    expect(store.buildingTypeByCode('MINE')).toEqual(mine)
    expect(store.recipeByCode('MINE_IRON')).toEqual(rec)
    expect(store.productByCode('NOPE')).toBeNull()
  })

  it('los índices byCode siguen las bajas del snapshot', () => {
    const store = useWorldStore()
    const iron = product({ id: uid<'Product'>(10), code: 'IRON' })
    store.applyProductsSnapshot([iron])
    expect(store.productByCode('IRON')).not.toBeNull()
    store.applyProductsSnapshot([])
    expect(store.productByCode('IRON')).toBeNull()
  })

  it('recipesForBuildingType filtra por tipo', () => {
    const store = useWorldStore()
    const typeA = uid<'BuildingType'>(20)
    const typeB = uid<'BuildingType'>(21)
    const r1 = recipe({ id: uid<'Recipe'>(30), buildingTypeId: typeA, code: 'A1' })
    const r2 = recipe({ id: uid<'Recipe'>(31), buildingTypeId: typeA, code: 'A2' })
    const r3 = recipe({ id: uid<'Recipe'>(32), buildingTypeId: typeB, code: 'B1' })
    store.applyRecipesSnapshot([r1, r2, r3])

    expect(store.recipesForBuildingType(typeA)).toEqual([r1, r2])
    expect(store.recipesForBuildingType(typeB)).toEqual([r3])
    expect(store.recipesForBuildingType(uid<'BuildingType'>(99))).toEqual([])
  })

  it('depositsInRegion y citiesInRegion agrupan por región', () => {
    const store = useWorldStore()
    const d1 = deposit({ id: uid<'Deposit'>(40), regionId: REGION_A })
    const d2 = deposit({ id: uid<'Deposit'>(41), regionId: REGION_B })
    const c1 = city({ id: uid<'City'>(50), regionId: REGION_A })
    store.applyDepositsSnapshot([d1, d2])
    store.applyCitiesSnapshot([c1])

    expect(store.depositsInRegion(REGION_A)).toEqual([d1])
    expect(store.depositsInRegion(REGION_B)).toEqual([d2])
    expect(store.citiesInRegion(REGION_A)).toEqual([c1])
    expect(store.citiesInRegion(REGION_B)).toEqual([])
  })

  it('fuelProducts deriva del catálogo', () => {
    const store = useWorldStore()
    const coal = product({ id: uid<'Product'>(10), code: 'COAL', isFuel: true })
    const iron = product({ id: uid<'Product'>(11), code: 'IRON', isFuel: false })
    store.applyProductsSnapshot([coal, iron])
    expect(store.fuelProducts).toEqual([coal])
  })

  it('clear purga todas las colecciones', () => {
    const store = useWorldStore()
    store.applyRegion(region())
    store.applyProduct(product())
    store.applyCity(city())
    store.clear()
    expect(store.regionList).toHaveLength(0)
    expect(store.productList).toHaveLength(0)
    expect(store.cityList).toHaveLength(0)
  })
})
