/**
 * app/stores/world.store — bounded context World (FAD §9.1, §20).
 *
 * Catálogos y entidades del mundo: regiones, productos, tipos de edificio,
 * recetas (catálogos con índice byCode), yacimientos y ciudades. Estado
 * REPLICADO: solo se escribe aplicando respuestas del servidor vía la tríada
 * `apply*Snapshot`/`apply*`/`remove*` (idempotente, FAD §20.4) — nunca desde
 * la UI. Sin reglas de negocio: los getters son proyecciones puras.
 */

import { computed } from 'vue'
import { defineStore } from 'pinia'
import type {
  BuildingType,
  BuildingTypeId,
  City,
  CityId,
  DepositId,
  Product,
  ProductId,
  Recipe,
  RecipeId,
  Region,
  RegionId,
  ResourceDeposit,
} from '~domain/world'
import { createEntityCollection, indexBy, uniqueIndexBy } from './entity-collection'

export const useWorldStore = defineStore('world', () => {
  const regions = createEntityCollection<RegionId, Region>((r) => r.id)
  const products = createEntityCollection<ProductId, Product>((p) => p.id)
  const buildingTypes = createEntityCollection<BuildingTypeId, BuildingType>((bt) => bt.id)
  const recipes = createEntityCollection<RecipeId, Recipe>((r) => r.id)
  const deposits = createEntityCollection<DepositId, ResourceDeposit>((d) => d.id)
  const cities = createEntityCollection<CityId, City>((c) => c.id)

  // Índices derivados (memoizados; jamás almacenados a mano, FAD §20.5).
  const productIdByCode = uniqueIndexBy(products, (p) => p.code)
  const buildingTypeIdByCode = uniqueIndexBy(buildingTypes, (bt) => bt.code)
  const recipeIdByCode = uniqueIndexBy(recipes, (r) => r.code)
  const recipeIdsByBuildingType = indexBy(recipes, (r) => r.buildingTypeId)
  const depositIdsByRegion = indexBy(deposits, (d) => d.regionId)
  const cityIdsByRegion = indexBy(cities, (c) => c.regionId)

  /** Productos que sirven como combustible (la energía es combustible físico en v1). */
  const fuelProducts = computed(() => products.list.value.filter((p) => p.isFuel))

  function productByCode(code: string): Product | null {
    return products.get(productIdByCode.value[code] ?? null)
  }

  function buildingTypeByCode(code: string): BuildingType | null {
    return buildingTypes.get(buildingTypeIdByCode.value[code] ?? null)
  }

  function recipeByCode(code: string): Recipe | null {
    return recipes.get(recipeIdByCode.value[code] ?? null)
  }

  function recipesForBuildingType(buildingTypeId: BuildingTypeId): readonly Recipe[] {
    return (recipeIdsByBuildingType.value[buildingTypeId] ?? []).flatMap((id) => {
      const recipe = recipes.get(id)
      return recipe === null ? [] : [recipe]
    })
  }

  function depositsInRegion(regionId: RegionId): readonly ResourceDeposit[] {
    return (depositIdsByRegion.value[regionId] ?? []).flatMap((id) => {
      const deposit = deposits.get(id)
      return deposit === null ? [] : [deposit]
    })
  }

  function citiesInRegion(regionId: RegionId): readonly City[] {
    return (cityIdsByRegion.value[regionId] ?? []).flatMap((id) => {
      const city = cities.get(id)
      return city === null ? [] : [city]
    })
  }

  /** Purga total (logout / cambio de mundo). */
  function clear(): void {
    regions.clear()
    products.clear()
    buildingTypes.clear()
    recipes.clear()
    deposits.clear()
    cities.clear()
  }

  return {
    // Regiones
    regionById: regions.byId,
    regionList: regions.list,
    getRegion: regions.get,
    applyRegionsSnapshot: regions.applySnapshot,
    applyRegion: regions.applyOne,
    removeRegion: regions.remove,
    // Productos
    productById: products.byId,
    productList: products.list,
    getProduct: products.get,
    applyProductsSnapshot: products.applySnapshot,
    applyProduct: products.applyOne,
    removeProduct: products.remove,
    productIdByCode,
    productByCode,
    fuelProducts,
    // Tipos de edificio
    buildingTypeById: buildingTypes.byId,
    buildingTypeList: buildingTypes.list,
    getBuildingType: buildingTypes.get,
    applyBuildingTypesSnapshot: buildingTypes.applySnapshot,
    applyBuildingType: buildingTypes.applyOne,
    removeBuildingType: buildingTypes.remove,
    buildingTypeIdByCode,
    buildingTypeByCode,
    // Recetas
    recipeById: recipes.byId,
    recipeList: recipes.list,
    getRecipe: recipes.get,
    applyRecipesSnapshot: recipes.applySnapshot,
    applyRecipe: recipes.applyOne,
    removeRecipe: recipes.remove,
    recipeIdByCode,
    recipeByCode,
    recipeIdsByBuildingType,
    recipesForBuildingType,
    // Yacimientos
    depositById: deposits.byId,
    depositList: deposits.list,
    getDeposit: deposits.get,
    applyDepositsSnapshot: deposits.applySnapshot,
    applyDeposit: deposits.applyOne,
    removeDeposit: deposits.remove,
    depositIdsByRegion,
    depositsInRegion,
    // Ciudades
    cityById: cities.byId,
    cityList: cities.list,
    getCity: cities.get,
    applyCitiesSnapshot: cities.applySnapshot,
    applyCity: cities.applyOne,
    removeCity: cities.remove,
    cityIdsByRegion,
    citiesInRegion,
    // Global
    clear,
  }
})
