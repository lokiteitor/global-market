/**
 * composables/useApi.ts — endpoints tipados del contrato REST para la UI.
 *
 * Cada método es un envío de INTENCIÓN o una consulta pull (P1): el cliente
 * jamás decide reglas económicas; refleja el resultado (éxito o error tipado).
 * Dinero/cantidades viajan como strings de punto fijo (Money/Quantity).
 */
import type {
  Acceptance,
  Building,
  BuildingType,
  City,
  CityDemand,
  Concession,
  Contract,
  ContractDelivery,
  GeoPolygon,
  InventoryItem,
  LedgerAccount,
  LedgerEntry,
  NetworkNode,
  OhlcCandle,
  Product,
  ProductionBatch,
  Publication,
  Recipe,
  Region,
  ResourceDeposit,
  Route,
  RoutePlan,
  SessionCreated,
  Vehicle,
  VehicleType
} from '~/lib/api/types'
import type { Money, Quantity } from '~/lib/kernel/money'
import type { SimTime } from '~/lib/kernel/simtime'
import { useApiClient, type ApiClient, type ApiResult } from './useApiClient'

export interface BoardQuery {
  kind?: 'sell' | 'buy' | 'freight'
  product_id?: string
  origin_region_id?: string
  max_unit_price?: string
  min_quantity_remaining?: string
  sort?: 'unit_price_asc' | 'unit_price_desc' | 'published_at_desc' | 'deadline_asc'
  limit?: number
}

export interface PublicationCreateBody {
  kind: 'sell' | 'buy'
  product_id: string
  quantity_total: Quantity
  unit_price: Money
  min_lot?: Quantity
  origin_node_id?: string
  destination_node_id?: string
  delivery_sim_seconds: number
}

export interface Api {
  // auth
  createSession(accountName: string, secret: string): Promise<ApiResult<SessionCreated>>
  deleteCurrentSession(): Promise<ApiResult<void>>
  // ledger
  listLedgerAccounts(query?: { kind?: string; cursor?: string; limit?: number }): Promise<ApiResult<LedgerAccount[]>>
  listLedgerEntries(accountId: string, query?: { cursor?: string; limit?: number }): Promise<ApiResult<LedgerEntry[]>>
  // contratos / tablón
  queryBoard(query: BoardQuery): Promise<ApiResult<Publication[]>>
  createPublication(body: PublicationCreateBody): Promise<ApiResult<Publication>>
  cancelPublication(publicationId: string): Promise<ApiResult<Publication>>
  acceptPublication(publicationId: string, quantity: Quantity): Promise<ApiResult<Acceptance>>
  listContracts(query?: { role?: 'buyer' | 'seller'; status?: string }): Promise<ApiResult<Contract[]>>
  listContractDeliveries(contractId: string): Promise<ApiResult<ContractDelivery[]>>
  getMarketOhlc(query: {
    product_id: string
    region_id?: string
    bucket_sim_secs?: number
    limit?: number
  }): Promise<ApiResult<OhlcCandle[]>>
  // world: catálogos
  listRegions(): Promise<ApiResult<Region[]>>
  listResourceDeposits(): Promise<ApiResult<ResourceDeposit[]>>
  listProducts(): Promise<ApiResult<Product[]>>
  listBuildingTypes(): Promise<ApiResult<BuildingType[]>>
  listRecipes(): Promise<ApiResult<Recipe[]>>
  listVehicleTypes(): Promise<ApiResult<VehicleType[]>>
  // world: ciudades
  listCities(): Promise<ApiResult<City[]>>
  getCityDemand(cityId: string): Promise<ApiResult<CityDemand[]>>
  // world: suelo
  listConcessions(): Promise<ApiResult<Concession[]>>
  createConcession(regionId: string, parcel: GeoPolygon): Promise<ApiResult<Concession>>
  renewConcession(concessionId: string): Promise<ApiResult<Concession>>
  // world: edificios y producción
  listBuildings(): Promise<ApiResult<Building[]>>
  createBuilding(body: { building_type_id: string; concession_id: string; footprint: GeoPolygon }): Promise<ApiResult<Building>>
  updateBuilding(buildingId: string, body: { active_recipe_id?: string | null; start_maintenance?: boolean }): Promise<ApiResult<Building>>
  upgradeBuilding(buildingId: string): Promise<ApiResult<Building>>
  getBuildingInventory(buildingId: string): Promise<ApiResult<InventoryItem[]>>
  listProductionBatches(buildingId: string): Promise<ApiResult<ProductionBatch[]>>
  queueProductionBatches(buildingId: string, recipeId: string, batches: number): Promise<ApiResult<ProductionBatch>>
  cancelProductionBatch(batchId: string): Promise<ApiResult<ProductionBatch>>
  // world: flota
  listVehicles(): Promise<ApiResult<Vehicle[]>>
  purchaseVehicle(vehicleTypeId: string, deliveryNodeId: string): Promise<ApiResult<Vehicle>>
  updateVehicle(vehicleId: string, body: { route_id?: string | null; schedule_maintenance?: boolean }): Promise<ApiResult<Vehicle>>
  // logistics
  listNetworkNodes(query?: { region_id?: string; limit?: number }): Promise<ApiResult<NetworkNode[]>>
  createRoutePlan(originNodeId: string, destinationNodeId: string): Promise<ApiResult<RoutePlan>>
  listRoutes(): Promise<ApiResult<Route[]>>
  createRoute(name: string, kind: 'fixed_line' | 'on_demand', legs: string[]): Promise<ApiResult<Route>>
}

const CATALOG_LIMIT = 200

export function createApi(client: ApiClient): Api {
  return {
    createSession: (accountName, secret) =>
      client.request<SessionCreated>('POST', '/auth/sessions', { body: { account_name: accountName, secret } }),
    deleteCurrentSession: () => client.request<void>('DELETE', '/auth/sessions/current'),

    listLedgerAccounts: (query = {}) => client.request<LedgerAccount[]>('GET', '/ledger/accounts', { query: { limit: CATALOG_LIMIT, ...query } }),
    listLedgerEntries: (accountId, query = {}) =>
      client.request<LedgerEntry[]>('GET', `/ledger/accounts/${accountId}/entries`, { query: { ...query } }),

    queryBoard: (query) => client.request<Publication[]>('GET', '/contracts/board', { query: { ...query } }),
    createPublication: (body) => client.request<Publication>('POST', '/contracts/publications', { body }),
    cancelPublication: (publicationId) => client.request<Publication>('DELETE', `/contracts/publications/${publicationId}`),
    acceptPublication: (publicationId, quantity) =>
      client.request<Acceptance>('POST', `/contracts/publications/${publicationId}/acceptances`, { body: { quantity } }),
    listContracts: (query = {}) => client.request<Contract[]>('GET', '/contracts/contracts', { query: { ...query } }),
    listContractDeliveries: (contractId) => client.request<ContractDelivery[]>('GET', `/contracts/contracts/${contractId}/deliveries`),
    getMarketOhlc: (query) => client.request<OhlcCandle[]>('GET', '/market/ohlc', { query: { ...query } }),

    listRegions: () => client.request<Region[]>('GET', '/world/regions', { query: { limit: CATALOG_LIMIT } }),
    listResourceDeposits: () => client.request<ResourceDeposit[]>('GET', '/world/resource-deposits', { query: { limit: CATALOG_LIMIT } }),
    listProducts: () => client.request<Product[]>('GET', '/world/products', { query: { limit: CATALOG_LIMIT } }),
    listBuildingTypes: () => client.request<BuildingType[]>('GET', '/world/building-types', { query: { limit: CATALOG_LIMIT } }),
    listRecipes: () => client.request<Recipe[]>('GET', '/world/recipes', { query: { limit: CATALOG_LIMIT } }),
    listVehicleTypes: () => client.request<VehicleType[]>('GET', '/world/vehicle-types', { query: { limit: CATALOG_LIMIT } }),

    listCities: () => client.request<City[]>('GET', '/world/cities', { query: { limit: CATALOG_LIMIT } }),
    getCityDemand: (cityId) => client.request<CityDemand[]>('GET', `/world/cities/${cityId}/demand`),

    listConcessions: () => client.request<Concession[]>('GET', '/world/concessions', { query: { limit: CATALOG_LIMIT } }),
    createConcession: (regionId, parcel) =>
      client.request<Concession>('POST', '/world/concessions', { body: { region_id: regionId, parcel } }),
    renewConcession: (concessionId) => client.request<Concession>('POST', `/world/concessions/${concessionId}/renew`),

    listBuildings: () => client.request<Building[]>('GET', '/world/buildings', { query: { limit: CATALOG_LIMIT } }),
    createBuilding: (body) => client.request<Building>('POST', '/world/buildings', { body }),
    updateBuilding: (buildingId, body) => client.request<Building>('PATCH', `/world/buildings/${buildingId}`, { body }),
    upgradeBuilding: (buildingId) => client.request<Building>('POST', `/world/buildings/${buildingId}/upgrade`),
    getBuildingInventory: (buildingId) => client.request<InventoryItem[]>('GET', `/world/buildings/${buildingId}/inventory`),
    listProductionBatches: (buildingId) =>
      client.request<ProductionBatch[]>('GET', `/world/buildings/${buildingId}/production-batches`),
    queueProductionBatches: (buildingId, recipeId, batches) =>
      client.request<ProductionBatch>('POST', `/world/buildings/${buildingId}/production-batches`, {
        body: { recipe_id: recipeId, batches_queued: batches }
      }),
    cancelProductionBatch: (batchId) => client.request<ProductionBatch>('DELETE', `/world/production-batches/${batchId}`),

    listVehicles: () => client.request<Vehicle[]>('GET', '/world/vehicles', { query: { limit: CATALOG_LIMIT } }),
    purchaseVehicle: (vehicleTypeId, deliveryNodeId) =>
      client.request<Vehicle>('POST', '/world/vehicles', { body: { vehicle_type_id: vehicleTypeId, delivery_node_id: deliveryNodeId } }),
    updateVehicle: (vehicleId, body) => client.request<Vehicle>('PATCH', `/world/vehicles/${vehicleId}`, { body }),

    listNetworkNodes: (query = {}) =>
      client.request<NetworkNode[]>('GET', '/logistics/network/nodes', { query: { limit: 500, ...query } }),
    createRoutePlan: (originNodeId, destinationNodeId) =>
      client.request<RoutePlan>('POST', '/logistics/route-plans', {
        body: { origin_node_id: originNodeId, destination_node_id: destinationNodeId }
      }),
    listRoutes: () => client.request<Route[]>('GET', '/logistics/routes', { query: { limit: CATALOG_LIMIT } }),
    createRoute: (name, kind, legs) => client.request<Route>('POST', '/logistics/routes', { body: { name, kind, legs } })
  }
}

/** Endpoints tipados sobre el cliente del árbol actual (inyectable en tests). */
export function useApi(): Api {
  return createApi(useApiClient())
}

/** Plazo del formulario: horas de JUEGO → segundos de sim-time (entero). */
export function gameHoursToSimSeconds(hours: number): SimTime {
  return Math.max(0, Math.round(hours * 3600)) as SimTime
}
