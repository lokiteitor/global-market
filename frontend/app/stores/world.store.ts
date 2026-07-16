/**
 * stores/world.store.ts — bounded context: catálogos estáticos del mundo.
 *
 * Regiones, productos, tipos de edificio, recetas, tipos de vehículo y
 * yacimientos: datos de referencia que se cargan por REST (pull) una vez por
 * sesión. No llegan por rooms WS, así que aquí no hay applyPatch; solo
 * setters idempotentes de catálogo completo + flags de carga.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { BuildingType, Product, Recipe, Region, ResourceDeposit, VehicleType } from '~/lib/api/types'

function indexById<T extends { id: string }>(items: readonly T[]): Record<string, T> {
  const map: Record<string, T> = {}
  for (const item of items) map[item.id] = item
  return map
}

export const useWorldStore = defineStore('world', () => {
  // ── Estado (normalizado byId) ──
  const regions = ref<Record<string, Region>>({})
  const products = ref<Record<string, Product>>({})
  const buildingTypes = ref<Record<string, BuildingType>>({})
  const recipes = ref<Record<string, Recipe>>({})
  const vehicleTypes = ref<Record<string, VehicleType>>({})
  const deposits = ref<Record<string, ResourceDeposit>>({})

  const loaded = ref({
    regions: false,
    products: false,
    buildingTypes: false,
    recipes: false,
    vehicleTypes: false,
    deposits: false
  })

  // ── Getters ──
  const regionList = computed(() => Object.values(regions.value))
  const productList = computed(() => Object.values(products.value))
  const fuelProducts = computed(() => productList.value.filter((p) => p.is_fuel))
  const recipesByBuildingType = computed(() => {
    const map: Record<string, Recipe[]> = {}
    for (const recipe of Object.values(recipes.value)) {
      ;(map[recipe.building_type_id] ??= []).push(recipe)
    }
    return map
  })
  const depositsByRegion = computed(() => {
    const map: Record<string, ResourceDeposit[]> = {}
    for (const deposit of Object.values(deposits.value)) {
      ;(map[deposit.region_id] ??= []).push(deposit)
    }
    return map
  })
  const allLoaded = computed(() => Object.values(loaded.value).every(Boolean))

  // ── Acciones (reemplazo de catálogo completo; idempotentes) ──
  function setRegions(items: readonly Region[]): void {
    regions.value = indexById(items)
    loaded.value.regions = true
  }

  function setProducts(items: readonly Product[]): void {
    products.value = indexById(items)
    loaded.value.products = true
  }

  function setBuildingTypes(items: readonly BuildingType[]): void {
    buildingTypes.value = indexById(items)
    loaded.value.buildingTypes = true
  }

  function setRecipes(items: readonly Recipe[]): void {
    recipes.value = indexById(items)
    loaded.value.recipes = true
  }

  function setVehicleTypes(items: readonly VehicleType[]): void {
    vehicleTypes.value = indexById(items)
    loaded.value.vehicleTypes = true
  }

  function setDeposits(items: readonly ResourceDeposit[]): void {
    deposits.value = indexById(items)
    loaded.value.deposits = true
  }

  return {
    regions,
    products,
    buildingTypes,
    recipes,
    vehicleTypes,
    deposits,
    loaded,
    regionList,
    productList,
    fuelProducts,
    recipesByBuildingType,
    depositsByRegion,
    allLoaded,
    setRegions,
    setProducts,
    setBuildingTypes,
    setRecipes,
    setVehicleTypes,
    setDeposits
  }
})
