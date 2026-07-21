/**
 * network/world.api — puerto WorldApi contra /world/* del contrato (FAD §12.8).
 *
 * Sigue el patrón de network/auth.api: puerto + factoría sobre el `RestClient`
 * tipado, que centraliza el unwrap del envelope `{data, meta}`, el mapeo de
 * fallos a `AppError` y la `Idempotency-Key` automática de toda mutación (P6).
 *
 * Los tipos se toman ÍNTEGROS del contrato generado (`types/api.d.ts`): DTOs
 * como alias `*Dto` y filtros de query derivados de `operations` — jamás DTOs
 * a mano. Importes (`MoneyAmount`) y cantidades (`StockQty`) viajan como
 * strings de punto fijo y se manipulan SOLO con `~shared/money` (C11).
 *
 * ACL pendiente (FAD §9.5, decisión consciente): el mapper DTO→dominio de
 * estos contexts llega con sus stores; hasta entonces estos alias del contrato
 * son la forma que consume la capa de aplicación y este módulo garantiza la
 * forma exacta de la petición (ruta/query/body) y el error tipado.
 */

import type { components, operations } from '../types/api'
import type { Page } from './mappers/page.mapper'
import { requestPage } from './mappers/page.mapper'
import type { RestClient } from './rest'

type Schemas = components['schemas']

// ——— Catálogos y mundo (read-only) ———
export type RegionDto = Schemas['Region']
export type ProductDto = Schemas['Product']
export type BuildingTypeDto = Schemas['BuildingType']
export type RecipeDto = Schemas['Recipe']
export type ResourceDepositDto = Schemas['ResourceDeposit']
export type CityDto = Schemas['City']
export type CityDemandDto = Schemas['CityDemand']

// ——— Concesiones de suelo ———
export type ConcessionDto = Schemas['Concession']
export type ConcessionCreateDto = Schemas['ConcessionCreate']
export type ConcessionTransferDto = Schemas['ConcessionTransfer']
export type ConcessionTransferCreateDto = Schemas['ConcessionTransferCreate']

// ——— Edificios y producción ———
export type BuildingDto = Schemas['Building']
export type BuildingCreateDto = Schemas['BuildingCreate']
export type BuildingUpdateDto = Schemas['BuildingUpdate']
export type InventoryItemDto = Schemas['InventoryItem']
export type ProductionBatchDto = Schemas['ProductionBatch']
export type ProductionBatchCreateDto = Schemas['ProductionBatchCreate']

// ——— Filtros de query, derivados de `operations` (nunca a mano) ———
export type RegionListQuery = NonNullable<operations['listRegions']['parameters']['query']>
export type ProductListQuery = NonNullable<operations['listProducts']['parameters']['query']>
export type BuildingTypeListQuery = NonNullable<
  operations['listBuildingTypes']['parameters']['query']
>
export type RecipeListQuery = NonNullable<operations['listRecipes']['parameters']['query']>
export type ResourceDepositListQuery = NonNullable<
  operations['listResourceDeposits']['parameters']['query']
>
export type CityListQuery = NonNullable<operations['listCities']['parameters']['query']>
export type ConcessionListQuery = NonNullable<operations['listConcessions']['parameters']['query']>
export type BuildingListQuery = NonNullable<operations['listBuildings']['parameters']['query']>
export type ProductionBatchListQuery = NonNullable<
  operations['listProductionBatches']['parameters']['query']
>

/** Puerto del mundo físico: catálogos, suelo (concesiones), edificios y producción. */
export interface WorldApi {
  /** GET /world/regions — macro-regiones con bounds y parámetros fiscales. */
  listRegions(query?: RegionListQuery): Promise<Page<RegionDto>>
  /** GET /world/products — catálogo de bienes con clamps de precio. */
  listProducts(query?: ProductListQuery): Promise<Page<ProductDto>>
  /** GET /world/building-types — catálogo de tipos de edificio. */
  listBuildingTypes(query?: BuildingTypeListQuery): Promise<Page<BuildingTypeDto>>
  /** GET /world/recipes — catálogo de recetas con insumos/productos. */
  listRecipes(query?: RecipeListQuery): Promise<Page<RecipeDto>>
  /** GET /world/resource-deposits — yacimientos (finitos o renovables). */
  listResourceDeposits(query?: ResourceDepositListQuery): Promise<Page<ResourceDepositDto>>
  /** GET /world/cities — ciudades (único consumidor final). */
  listCities(query?: CityListQuery): Promise<Page<CityDto>>
  /** GET /world/cities/{cityId}/demand — curva de demanda vigente (sin cursor en contrato). */
  getCityDemand(
    cityId: Schemas['CityId'],
    productId?: Schemas['ProductId'],
  ): Promise<readonly CityDemandDto[]>

  /** GET /world/concessions — concesiones propias. */
  listConcessions(query?: ConcessionListQuery): Promise<Page<ConcessionDto>>
  /** POST /world/concessions — obtener concesión (cobra el primer canon). */
  createConcession(concession: ConcessionCreateDto): Promise<ConcessionDto>
  /** GET /world/concessions/{concessionId} */
  getConcession(concessionId: Schemas['ConcessionId']): Promise<ConcessionDto>
  /** POST /world/concessions/{concessionId}/renew — extiende un periodo pagando el canon. */
  renewConcession(concessionId: Schemas['ConcessionId']): Promise<ConcessionDto>
  /** POST /world/concession-transfers — traspaso a otra corporación (con `system_fee`). */
  createConcessionTransfer(transfer: ConcessionTransferCreateDto): Promise<ConcessionTransferDto>

  /** GET /world/buildings — edificios propios. */
  listBuildings(query?: BuildingListQuery): Promise<Page<BuildingDto>>
  /** POST /world/buildings — construir sobre concesión propia (422 `PLACEMENT_INVALID`). */
  createBuilding(building: BuildingCreateDto): Promise<BuildingDto>
  /** GET /world/buildings/{buildingId} */
  getBuilding(buildingId: Schemas['BuildingId']): Promise<BuildingDto>
  /** PATCH /world/buildings/{buildingId} — receta activa o mantenimiento. */
  updateBuilding(buildingId: Schemas['BuildingId'], update: BuildingUpdateDto): Promise<BuildingDto>
  /** POST /world/buildings/{buildingId}/upgrade — sube nivel (coste no lineal). */
  upgradeBuilding(buildingId: Schemas['BuildingId']): Promise<BuildingDto>
  /** GET /world/buildings/{buildingId}/inventory — stock físico por producto. */
  getBuildingInventory(buildingId: Schemas['BuildingId']): Promise<readonly InventoryItemDto[]>

  /** GET /world/buildings/{buildingId}/production-batches — cola de producción. */
  listProductionBatches(
    buildingId: Schemas['BuildingId'],
    query?: ProductionBatchListQuery,
  ): Promise<Page<ProductionBatchDto>>
  /** POST /world/buildings/{buildingId}/production-batches — encolar lotes. */
  queueProductionBatches(
    buildingId: Schemas['BuildingId'],
    order: ProductionBatchCreateDto,
  ): Promise<ProductionBatchDto>
  /** DELETE /world/production-batches/{batchId} — cancela lo no producido. */
  cancelProductionBatch(batchId: Schemas['BatchId']): Promise<ProductionBatchDto>
}

export function createWorldApi(rest: RestClient): WorldApi {
  return {
    listRegions(query) {
      return requestPage<RegionDto>(rest, {
        method: 'GET',
        path: '/world/regions',
        query: query ?? {},
      })
    },

    listProducts(query) {
      return requestPage<ProductDto>(rest, {
        method: 'GET',
        path: '/world/products',
        query: query ?? {},
      })
    },

    listBuildingTypes(query) {
      return requestPage<BuildingTypeDto>(rest, {
        method: 'GET',
        path: '/world/building-types',
        query: query ?? {},
      })
    },

    listRecipes(query) {
      return requestPage<RecipeDto>(rest, {
        method: 'GET',
        path: '/world/recipes',
        query: query ?? {},
      })
    },

    listResourceDeposits(query) {
      return requestPage<ResourceDepositDto>(rest, {
        method: 'GET',
        path: '/world/resource-deposits',
        query: query ?? {},
      })
    },

    listCities(query) {
      return requestPage<CityDto>(rest, {
        method: 'GET',
        path: '/world/cities',
        query: query ?? {},
      })
    },

    async getCityDemand(cityId, productId) {
      const { data } = await rest.request<readonly CityDemandDto[]>({
        method: 'GET',
        path: `/world/cities/${encodeURIComponent(cityId)}/demand`,
        query: productId === undefined ? {} : { product_id: productId },
      })
      return data
    },

    listConcessions(query) {
      return requestPage<ConcessionDto>(rest, {
        method: 'GET',
        path: '/world/concessions',
        query: query ?? {},
      })
    },

    async createConcession(concession) {
      const { data } = await rest.request<ConcessionDto>({
        method: 'POST',
        path: '/world/concessions',
        body: concession,
      })
      return data
    },

    async getConcession(concessionId) {
      const { data } = await rest.request<ConcessionDto>({
        method: 'GET',
        path: `/world/concessions/${encodeURIComponent(concessionId)}`,
      })
      return data
    },

    async renewConcession(concessionId) {
      const { data } = await rest.request<ConcessionDto>({
        method: 'POST',
        path: `/world/concessions/${encodeURIComponent(concessionId)}/renew`,
      })
      return data
    },

    async createConcessionTransfer(transfer) {
      const { data } = await rest.request<ConcessionTransferDto>({
        method: 'POST',
        path: '/world/concession-transfers',
        body: transfer,
      })
      return data
    },

    listBuildings(query) {
      return requestPage<BuildingDto>(rest, {
        method: 'GET',
        path: '/world/buildings',
        query: query ?? {},
      })
    },

    async createBuilding(building) {
      const { data } = await rest.request<BuildingDto>({
        method: 'POST',
        path: '/world/buildings',
        body: building,
      })
      return data
    },

    async getBuilding(buildingId) {
      const { data } = await rest.request<BuildingDto>({
        method: 'GET',
        path: `/world/buildings/${encodeURIComponent(buildingId)}`,
      })
      return data
    },

    async updateBuilding(buildingId, update) {
      const { data } = await rest.request<BuildingDto>({
        method: 'PATCH',
        path: `/world/buildings/${encodeURIComponent(buildingId)}`,
        body: update,
      })
      return data
    },

    async upgradeBuilding(buildingId) {
      const { data } = await rest.request<BuildingDto>({
        method: 'POST',
        path: `/world/buildings/${encodeURIComponent(buildingId)}/upgrade`,
      })
      return data
    },

    async getBuildingInventory(buildingId) {
      const { data } = await rest.request<readonly InventoryItemDto[]>({
        method: 'GET',
        path: `/world/buildings/${encodeURIComponent(buildingId)}/inventory`,
      })
      return data
    },

    listProductionBatches(buildingId, query) {
      return requestPage<ProductionBatchDto>(rest, {
        method: 'GET',
        path: `/world/buildings/${encodeURIComponent(buildingId)}/production-batches`,
        query: query ?? {},
      })
    },

    async queueProductionBatches(buildingId, order) {
      const { data } = await rest.request<ProductionBatchDto>({
        method: 'POST',
        path: `/world/buildings/${encodeURIComponent(buildingId)}/production-batches`,
        body: order,
      })
      return data
    },

    async cancelProductionBatch(batchId) {
      const { data } = await rest.request<ProductionBatchDto>({
        method: 'DELETE',
        path: `/world/production-batches/${encodeURIComponent(batchId)}`,
      })
      return data
    },
  }
}
