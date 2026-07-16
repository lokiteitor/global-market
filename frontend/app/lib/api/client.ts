/**
 * lib/api/client.ts — puerto RestApi + implementación fetch (FAD §12.8, C14).
 *
 * Cliente tipado de specs/openapi.yaml v1.1.0. Reglas de la frontera:
 *
 *   - Toda respuesta exitosa se devuelve como { data, meta } tipado; todo
 *     error se devuelve como Result de error con un ApiRequestError tipado
 *     { code, message, details?, status, retryAfterSeconds? }. Este cliente
 *     NUNCA lanza (ni strings ni excepciones): fallo de red = Result de error.
 *   - meta.sim_time_seconds alimenta el SimClock en CADA respuesta (callback
 *     onMeta inyectado por el plugin de red; sin imports de stores — P3).
 *   - El bearer token se obtiene de getToken() inyectado (session.store lo
 *     posee; así no hay import circular stores ↔ api).
 *   - Todo POST de comando lleva Idempotency-Key (crypto.randomUUID) para
 *     tolerar reintentos sin doble ejecución (P6 / ADR-IMPL-09). POST
 *     /logistics/route-plans es solo cálculo (no comando): sin clave.
 *   - 429/503: el error expone retryAfterSeconds (cabecera Retry-After);
 *     QUIÉN reintenta y cuándo lo decide el composable/caso de uso, no este
 *     cliente (backpressure consciente, C17/FAD §12.10).
 */
import type { Result } from '../kernel/result'
import { err, ok } from '../kernel/result'
import type {
  Acceptance,
  Account,
  Building,
  BuildingType,
  City,
  CityDemand,
  Concession,
  Contract,
  ContractDelivery,
  DataEnvelope,
  ErrorEnvelope,
  InventoryItem,
  LedgerAccount,
  LedgerEntry,
  Meta,
  NetworkLink,
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
  Shipment,
  Vehicle,
  VehicleType
} from './types'
import type {
  AcceptanceCreate,
  BoardQuery,
  BuildingCreate,
  BuildingUpdate,
  BuildingsQuery,
  CitiesQuery,
  CityDemandQuery,
  ConcessionCreate,
  ConcessionTransfer,
  ConcessionTransferCreate,
  ConcessionsQuery,
  ContractsQuery,
  DepositsQuery,
  LedgerAccountsQuery,
  LedgerEntriesQuery,
  LinksQuery,
  NodesQuery,
  OhlcQuery,
  PageQuery,
  ProductionBatchCreate,
  ProductionBatchesQuery,
  ProductsQuery,
  PublicationCreate,
  RecipesQuery,
  RegionsQuery,
  RoutePlanRequest,
  RouteCreate,
  RouteUpdate,
  RoutesQuery,
  SessionCreateRequest,
  ShipmentsQuery,
  VehiclePurchase,
  VehicleTypesQuery,
  VehicleUpdate,
  VehiclesQuery
} from './requests'

// ─── Error tipado de la frontera REST ────────────────────────────────────────

export interface ApiRequestError {
  /** Código estable del servidor (INSUFFICIENT_COLLATERAL, …) o sintético del cliente (NETWORK_ERROR, INVALID_RESPONSE). */
  code: string
  message: string
  /** Contexto estructurado del error (importes como strings de punto fijo). */
  details?: Record<string, unknown>
  /** HTTP status; 0 si la petición no llegó a resolverse (fallo de red). */
  status: number
  /** Cabecera Retry-After en 429 (rate limit) y 503 (ventana de mantenimiento). */
  retryAfterSeconds?: number
}

export type ApiResult<T> = Result<DataEnvelope<T>, ApiRequestError>
export type ApiVoidResult = Result<null, ApiRequestError>

// ─── Cliente HTTP genérico (base compartida) ─────────────────────────────────
// Lo consumen createRestApi (métodos tipados) y el composable useApiClient
// (superficie genérica request() que ya usa la capa de UI).

export interface HttpRequestOptions {
  /** Query params; los undefined se omiten. */
  query?: Record<string, string | number | boolean | undefined> | undefined
  /** Cuerpo JSON. */
  body?: unknown
  /**
   * Marca de POST de comando. Si se omite, se infiere: todo POST es comando
   * salvo los dos del contrato sin Idempotency-Key (login y route-plans).
   */
  command?: boolean
  /** Clave explícita (reintentos controlados por el caso de uso). */
  idempotencyKey?: string
}

export interface HttpClient {
  /** Petición con envoltura { data, meta }; en 204 data es undefined. */
  // Nota: tipo expandido (no el alias ApiResult<T>) — en el proyecto Nuxt
  // convive un alias global homónimo auto-importado (useApiClient.ts) y el
  // checker de TS confunde ambas instancias al relacionar firmas genéricas.
  request<T>(method: string, path: string, opts?: HttpRequestOptions): Promise<Result<DataEnvelope<T>, ApiRequestError>>
  /** Petición cuyo éxito esperado es 204 No Content. */
  requestVoid(method: string, path: string, opts?: HttpRequestOptions): Promise<ApiVoidResult>
}

// ─── Puerto RestApi ──────────────────────────────────────────────────────────

export interface RestApi {
  // auth
  createSession(body: SessionCreateRequest): Promise<ApiResult<SessionCreated>>
  deleteCurrentSession(): Promise<ApiVoidResult>
  getCurrentAccount(): Promise<ApiResult<Account>>

  // ledger
  listLedgerAccounts(query?: LedgerAccountsQuery): Promise<ApiResult<LedgerAccount[]>>
  listLedgerEntries(ledgerAccountId: string, query?: LedgerEntriesQuery): Promise<ApiResult<LedgerEntry[]>>

  // contracts: tablón
  queryBoard(query?: BoardQuery): Promise<ApiResult<Publication[]>>
  createPublication(body: PublicationCreate): Promise<ApiResult<Publication>>
  getPublication(publicationId: string): Promise<ApiResult<Publication>>
  cancelPublication(publicationId: string): Promise<ApiResult<Publication>>
  acceptPublication(publicationId: string, body: AcceptanceCreate): Promise<ApiResult<Acceptance>>
  getAcceptance(acceptanceId: string): Promise<ApiResult<Acceptance>>

  // contracts: CCRI
  listContracts(query?: ContractsQuery): Promise<ApiResult<Contract[]>>
  getContract(contractId: string): Promise<ApiResult<Contract>>
  listContractDeliveries(contractId: string): Promise<ApiResult<ContractDelivery[]>>

  // market
  getOhlc(query: OhlcQuery): Promise<ApiResult<OhlcCandle[]>>

  // world: catálogos
  listRegions(query?: RegionsQuery): Promise<ApiResult<Region[]>>
  getRegion(regionId: string): Promise<ApiResult<Region>>
  listProducts(query?: ProductsQuery): Promise<ApiResult<Product[]>>
  listBuildingTypes(query?: PageQuery): Promise<ApiResult<BuildingType[]>>
  listRecipes(query?: RecipesQuery): Promise<ApiResult<Recipe[]>>
  listResourceDeposits(query?: DepositsQuery): Promise<ApiResult<ResourceDeposit[]>>

  // world: ciudades
  listCities(query?: CitiesQuery): Promise<ApiResult<City[]>>
  getCity(cityId: string): Promise<ApiResult<City>>
  getCityDemand(cityId: string, query?: CityDemandQuery): Promise<ApiResult<CityDemand[]>>

  // world: suelo
  listConcessions(query?: ConcessionsQuery): Promise<ApiResult<Concession[]>>
  createConcession(body: ConcessionCreate): Promise<ApiResult<Concession>>
  getConcession(concessionId: string): Promise<ApiResult<Concession>>
  renewConcession(concessionId: string): Promise<ApiResult<Concession>>
  createConcessionTransfer(body: ConcessionTransferCreate): Promise<ApiResult<ConcessionTransfer>>

  // world: edificios y producción
  listBuildings(query?: BuildingsQuery): Promise<ApiResult<Building[]>>
  createBuilding(body: BuildingCreate): Promise<ApiResult<Building>>
  getBuilding(buildingId: string): Promise<ApiResult<Building>>
  updateBuilding(buildingId: string, body: BuildingUpdate): Promise<ApiResult<Building>>
  upgradeBuilding(buildingId: string): Promise<ApiResult<Building>>
  getBuildingInventory(buildingId: string): Promise<ApiResult<InventoryItem[]>>
  listProductionBatches(buildingId: string, query?: ProductionBatchesQuery): Promise<ApiResult<ProductionBatch[]>>
  queueProductionBatches(buildingId: string, body: ProductionBatchCreate): Promise<ApiResult<ProductionBatch>>
  cancelProductionBatch(batchId: string): Promise<ApiResult<ProductionBatch>>

  // world: flota y cargamentos
  listVehicleTypes(query?: VehicleTypesQuery): Promise<ApiResult<VehicleType[]>>
  listVehicles(query?: VehiclesQuery): Promise<ApiResult<Vehicle[]>>
  purchaseVehicle(body: VehiclePurchase): Promise<ApiResult<Vehicle>>
  getVehicle(vehicleId: string): Promise<ApiResult<Vehicle>>
  updateVehicle(vehicleId: string, body: VehicleUpdate): Promise<ApiResult<Vehicle>>
  listShipments(query?: ShipmentsQuery): Promise<ApiResult<Shipment[]>>
  getShipment(shipmentId: string): Promise<ApiResult<Shipment>>

  // logistics
  listNetworkNodes(query?: NodesQuery): Promise<ApiResult<NetworkNode[]>>
  listNetworkLinks(query?: LinksQuery): Promise<ApiResult<NetworkLink[]>>
  createRoutePlan(body: RoutePlanRequest): Promise<ApiResult<RoutePlan>>
  listRoutes(query?: RoutesQuery): Promise<ApiResult<Route[]>>
  createRoute(body: RouteCreate): Promise<ApiResult<Route>>
  getRoute(routeId: string): Promise<ApiResult<Route>>
  updateRoute(routeId: string, body: RouteUpdate): Promise<ApiResult<Route>>
  deleteRoute(routeId: string): Promise<ApiVoidResult>
}

// ─── Implementación fetch ────────────────────────────────────────────────────

export interface RestApiOptions {
  /** Base del API (p. ej. '/api/v1' de runtimeConfig). */
  baseURL: string
  /** Token bearer vigente (session.store lo posee; aquí solo se lee). */
  getToken: () => string | null
  /** Se invoca con el meta de CADA respuesta exitosa (alimenta el SimClock). */
  onMeta?: (meta: Meta) => void
  /** fetch inyectable (tests). Por defecto, el global. */
  fetchImpl?: typeof fetch
}

type QueryValue = string | number | boolean | undefined
type QueryRecord = Record<string, QueryValue>

function newIdempotencyKey(): string {
  // crypto.randomUUID existe en todos los navegadores evergreen (C18) y Node 19+.
  return globalThis.crypto.randomUUID()
}

/** POSTs del contrato SIN Idempotency-Key: login (crea sesión) y route-plans (solo cálculo). */
const POSTS_WITHOUT_IDEMPOTENCY = new Set(['/auth/sessions', '/logistics/route-plans'])

function isCommandPost(method: string, path: string, opts: HttpRequestOptions): boolean {
  if (opts.command !== undefined) return opts.command
  return method === 'POST' && !POSTS_WITHOUT_IDEMPOTENCY.has(path)
}

/** meta sintética para respuestas 204 (sin cuerpo): mantiene la envoltura tipada. */
const EMPTY_META: Meta = { sim_time: '', server_time: '' }

function buildQueryString(query: QueryRecord | undefined): string {
  if (query === undefined) return ''
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined) continue
    params.set(key, String(value))
  }
  const s = params.toString()
  return s === '' ? '' : `?${s}`
}

function parseRetryAfter(headers: Headers): number | undefined {
  const raw = headers.get('Retry-After')
  if (raw === null) return undefined
  const seconds = Number.parseInt(raw, 10)
  return Number.isFinite(seconds) ? seconds : undefined
}

export function createHttpClient(options: RestApiOptions): HttpClient {
  const fetchImpl = options.fetchImpl ?? ((...args: Parameters<typeof fetch>) => fetch(...args))

  async function perform(method: string, path: string, spec: HttpRequestOptions): Promise<Result<{ status: number; json: unknown }, ApiRequestError>> {
    const url = `${options.baseURL}${path}${buildQueryString(spec.query)}`
    const headers: Record<string, string> = { Accept: 'application/json' }
    const token = options.getToken()
    if (token !== null) headers['Authorization'] = `Bearer ${token}`
    if (spec.body !== undefined) headers['Content-Type'] = 'application/json'
    // P6 / ADR-IMPL-09: todo POST de comando lleva Idempotency-Key.
    if (isCommandPost(method, path, spec)) headers['Idempotency-Key'] = spec.idempotencyKey ?? newIdempotencyKey()

    let response: Response
    try {
      response = await fetchImpl(url, {
        method,
        headers,
        ...(spec.body !== undefined ? { body: JSON.stringify(spec.body) } : {})
      })
    } catch (cause) {
      return err({
        code: 'NETWORK_ERROR',
        message: cause instanceof Error ? cause.message : 'Fallo de red',
        status: 0
      })
    }

    if (response.status === 204) return ok({ status: 204, json: null })

    let json: unknown = null
    try {
      json = await response.json()
    } catch {
      if (response.ok) {
        return err({ code: 'INVALID_RESPONSE', message: 'Respuesta sin cuerpo JSON válido', status: response.status })
      }
    }

    if (!response.ok) {
      const envelope = json as Partial<ErrorEnvelope> | null
      const serverError = envelope?.error
      const retryAfterSeconds = parseRetryAfter(response.headers)
      return err({
        code: serverError?.code ?? `HTTP_${response.status}`,
        message: serverError?.message ?? `Error HTTP ${response.status}`,
        ...(serverError?.details !== undefined ? { details: serverError.details } : {}),
        status: response.status,
        ...(retryAfterSeconds !== undefined ? { retryAfterSeconds } : {})
      })
    }

    return ok({ status: response.status, json })
  }

  /** Petición con envoltura { data, meta } (toda respuesta exitosa del contrato). */
  async function request<T>(method: string, path: string, opts?: HttpRequestOptions): Promise<ApiResult<T>> {
    const result = await perform(method, path, opts ?? {})
    if (!result.ok) return result
    if (result.value.status === 204) {
      return ok({ data: undefined as T, meta: EMPTY_META })
    }
    const envelope = result.value.json as Partial<DataEnvelope<T>> | null
    if (envelope === null || envelope.data === undefined || envelope.meta === undefined) {
      return err({ code: 'INVALID_RESPONSE', message: 'Respuesta sin envoltura { data, meta }', status: result.value.status })
    }
    options.onMeta?.(envelope.meta)
    return ok({ data: envelope.data, meta: envelope.meta })
  }

  /** Petición cuyo éxito es 204 No Content (sin envoltura ni meta). */
  async function requestVoid(method: string, path: string, opts?: HttpRequestOptions): Promise<ApiVoidResult> {
    const result = await perform(method, path, opts ?? {})
    if (!result.ok) return result
    return ok(null)
  }

  return { request, requestVoid }
}

export function createRestApi(http: HttpClient): RestApi {
  const { request, requestVoid } = http
  const toQuery = (q: object | undefined): QueryRecord | undefined => q as QueryRecord | undefined

  return {
    // ── auth ──
    createSession: (body) => request<SessionCreated>('POST', '/auth/sessions', { body }),
    deleteCurrentSession: () => requestVoid('DELETE', '/auth/sessions/current'),
    getCurrentAccount: () => request<Account>('GET', '/auth/me'),

    // ── ledger ──
    listLedgerAccounts: (query) => request<LedgerAccount[]>('GET', '/ledger/accounts', { query: toQuery(query) }),
    listLedgerEntries: (ledgerAccountId, query) =>
      request<LedgerEntry[]>('GET', `/ledger/accounts/${ledgerAccountId}/entries`, { query: toQuery(query) }),

    // ── contracts: tablón ──
    queryBoard: (query) => request<Publication[]>('GET', '/contracts/board', { query: toQuery(query) }),
    createPublication: (body) => request<Publication>('POST', '/contracts/publications', { body, command: true }),
    getPublication: (publicationId) => request<Publication>('GET', `/contracts/publications/${publicationId}`),
    cancelPublication: (publicationId) => request<Publication>('DELETE', `/contracts/publications/${publicationId}`),
    acceptPublication: (publicationId, body) =>
      request<Acceptance>('POST', `/contracts/publications/${publicationId}/acceptances`, { body, command: true }),
    getAcceptance: (acceptanceId) => request<Acceptance>('GET', `/contracts/acceptances/${acceptanceId}`),

    // ── contracts: CCRI ──
    listContracts: (query) => request<Contract[]>('GET', '/contracts/contracts', { query: toQuery(query) }),
    getContract: (contractId) => request<Contract>('GET', `/contracts/contracts/${contractId}`),
    listContractDeliveries: (contractId) => request<ContractDelivery[]>('GET', `/contracts/contracts/${contractId}/deliveries`),

    // ── market ──
    getOhlc: (query) => request<OhlcCandle[]>('GET', '/market/ohlc', { query: toQuery(query) }),

    // ── world: catálogos ──
    listRegions: (query) => request<Region[]>('GET', '/world/regions', { query: toQuery(query) }),
    getRegion: (regionId) => request<Region>('GET', `/world/regions/${regionId}`),
    listProducts: (query) => request<Product[]>('GET', '/world/products', { query: toQuery(query) }),
    listBuildingTypes: (query) => request<BuildingType[]>('GET', '/world/building-types', { query: toQuery(query) }),
    listRecipes: (query) => request<Recipe[]>('GET', '/world/recipes', { query: toQuery(query) }),
    listResourceDeposits: (query) => request<ResourceDeposit[]>('GET', '/world/resource-deposits', { query: toQuery(query) }),

    // ── world: ciudades ──
    listCities: (query) => request<City[]>('GET', '/world/cities', { query: toQuery(query) }),
    getCity: (cityId) => request<City>('GET', `/world/cities/${cityId}`),
    getCityDemand: (cityId, query) => request<CityDemand[]>('GET', `/world/cities/${cityId}/demand`, { query: toQuery(query) }),

    // ── world: suelo ──
    listConcessions: (query) => request<Concession[]>('GET', '/world/concessions', { query: toQuery(query) }),
    createConcession: (body) => request<Concession>('POST', '/world/concessions', { body, command: true }),
    getConcession: (concessionId) => request<Concession>('GET', `/world/concessions/${concessionId}`),
    renewConcession: (concessionId) => request<Concession>('POST', `/world/concessions/${concessionId}/renew`, { command: true }),
    createConcessionTransfer: (body) => request<ConcessionTransfer>('POST', '/world/concession-transfers', { body, command: true }),

    // ── world: edificios y producción ──
    listBuildings: (query) => request<Building[]>('GET', '/world/buildings', { query: toQuery(query) }),
    createBuilding: (body) => request<Building>('POST', '/world/buildings', { body, command: true }),
    getBuilding: (buildingId) => request<Building>('GET', `/world/buildings/${buildingId}`),
    updateBuilding: (buildingId, body) => request<Building>('PATCH', `/world/buildings/${buildingId}`, { body }),
    upgradeBuilding: (buildingId) => request<Building>('POST', `/world/buildings/${buildingId}/upgrade`, { command: true }),
    getBuildingInventory: (buildingId) => request<InventoryItem[]>('GET', `/world/buildings/${buildingId}/inventory`),
    listProductionBatches: (buildingId, query) =>
      request<ProductionBatch[]>('GET', `/world/buildings/${buildingId}/production-batches`, { query: toQuery(query) }),
    queueProductionBatches: (buildingId, body) =>
      request<ProductionBatch>('POST', `/world/buildings/${buildingId}/production-batches`, { body, command: true }),
    cancelProductionBatch: (batchId) => request<ProductionBatch>('DELETE', `/world/production-batches/${batchId}`),

    // ── world: flota y cargamentos ──
    listVehicleTypes: (query) => request<VehicleType[]>('GET', '/world/vehicle-types', { query: toQuery(query) }),
    listVehicles: (query) => request<Vehicle[]>('GET', '/world/vehicles', { query: toQuery(query) }),
    purchaseVehicle: (body) => request<Vehicle>('POST', '/world/vehicles', { body, command: true }),
    getVehicle: (vehicleId) => request<Vehicle>('GET', `/world/vehicles/${vehicleId}`),
    updateVehicle: (vehicleId, body) => request<Vehicle>('PATCH', `/world/vehicles/${vehicleId}`, { body }),
    listShipments: (query) => request<Shipment[]>('GET', '/world/shipments', { query: toQuery(query) }),
    getShipment: (shipmentId) => request<Shipment>('GET', `/world/shipments/${shipmentId}`),

    // ── logistics ──
    listNetworkNodes: (query) => request<NetworkNode[]>('GET', '/logistics/network/nodes', { query: toQuery(query) }),
    listNetworkLinks: (query) => request<NetworkLink[]>('GET', '/logistics/network/links', { query: toQuery(query) }),
    // Solo cálculo (no persiste): POST sin Idempotency-Key, como fija el contrato.
    createRoutePlan: (body) => request<RoutePlan>('POST', '/logistics/route-plans', { body }),
    listRoutes: (query) => request<Route[]>('GET', '/logistics/routes', { query: toQuery(query) }),
    createRoute: (body) => request<Route>('POST', '/logistics/routes', { body, command: true }),
    getRoute: (routeId) => request<Route>('GET', `/logistics/routes/${routeId}`),
    updateRoute: (routeId, body) => request<Route>('PATCH', `/logistics/routes/${routeId}`, { body }),
    deleteRoute: (routeId) => requestVoid('DELETE', `/logistics/routes/${routeId}`)
  }
}
