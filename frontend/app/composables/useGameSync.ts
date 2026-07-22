/**
 * app/composables/useGameSync — orquestación de sincronización de /play
 * (FAD §12/§13, ADR-023 §6: bootstrap REST + deltas WS + re-pull ante hueco).
 *
 * Ciclo v1 (patrón "evento = invalidación dirigida + refetch puntual"):
 *
 * 1. `start()` conecta el `GatewayTransportAdapter` con el token en memoria,
 *    hace `joinCorp()` (watermark) y entonces BOOTSTRAPEA por REST: catálogos
 *    del mundo + red logística (comunes) y TODO lo propio (concesiones,
 *    edificios con inventario y colas, flota, cargamentos, rutas, contratos,
 *    cuentas del ledger). Cada respuesta pasa por la ACL de mappers
 *    (network/mappers/domain.mapper) y entra a las stores con la tríada
 *    idempotente — el estado replicado SOLO se escribe así (FAD §20.4).
 * 2. Cada `DomainEvent` de la room corp NO aplica su payload directamente:
 *    invalida el aggregate afectado y lo RE-CONSULTA por REST (GET puntual).
 *    Es deliberadamente simple y correcto: la respuesta REST es siempre la
 *    verdad más fresca y el apply es idempotente (at-least-once, P6). Los
 *    payloads del outbox quedan como optimización futura documentada.
 * 3. Ante hueco de `seq` o reconexión (`onResync`) se repite el bootstrap
 *    PROPIO completo (el socket no tiene replay, ws-protocol.md §6).
 * 4. Refresco suave periódico de lo ajeno visible (enlaces con congestión,
 *    ciudades y yacimientos cada 30 s) y re-anclaje de la flota propia cada
 *    10 s mientras haya vehículos `in_transit` (extrapolación cinemática).
 *
 * Nota v1: el contrato no expone "mis publicaciones" ni "mis aceptaciones"
 * como listado; se reconstruyen desde el tablón (las vivas, filtrando por
 * publisher) + respuestas de comandos + eventos WS. Documentado como hueco
 * de contrato conocido.
 *
 * Nota fletes: los eventos `freight.*` se enrutan por WS desde v1.7.0
 * (incremento 11 del backend); antes no llegaban. Como defensa en
 * profundidad, los appliers de `acceptance.` y `shipment.` también refrescan
 * el freight contract ligado (mantiene fill/status frescos aunque un push se
 * pierda), y el bootstrap consulta ambos roles (shipper y carrier).
 */

import { readonly, ref } from 'vue'
import type { Ref } from 'vue'
import { asEntityId } from '~shared/ids'
import { simTime } from '~shared/simtime'
import type { SimTime } from '~shared/simtime'
import type { SimClock } from '~domain/simclock'
import type { Page } from '~network/mappers/page.mapper'
import {
  mapAcceptance,
  mapBuilding,
  mapBuildingType,
  mapCity,
  mapConcession,
  mapContract,
  mapDeposit,
  mapFreightContract,
  mapInventoryItem,
  mapLedgerAccount,
  mapLink,
  mapNode,
  mapProduct,
  mapProductionBatch,
  mapPublication,
  mapRecipe,
  mapRegion,
  mapRoute,
  mapShipment,
  mapVehicle,
  mapVehicleType,
} from '~network/mappers/domain.mapper'
import { AppError } from '~network/rest'
import type {
  ConnectionState,
  DomainEvent,
  NetworkTransport,
  SyncOrchestrator,
  WebSocketFactory,
} from '~network/transport'
import { createGatewayTransport, createSyncOrchestrator } from '~network/transport'
import type { GameApis } from '~/composables/useGameApis'
import { useGameApis } from '~/composables/useGameApis'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCadastreStore } from '~/stores/cadastre.store'
import { useFinanceStore } from '~/stores/finance.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'
import { useWorldStore } from '~/stores/world.store'

export type SyncPhase = 'idle' | 'bootstrapping' | 'ready' | 'error'

export interface GameSync {
  readonly phase: Readonly<Ref<SyncPhase>>
  readonly connection: Readonly<Ref<ConnectionState>>
  /** `true` mientras un resync (hueco/reconexión) re-consulta el estado propio. */
  readonly stale: Readonly<Ref<boolean>>
  readonly lastError: Readonly<Ref<unknown>>
  start(): Promise<void>
  stop(): void
}

/** Tamaño de página de los pulls de bootstrap. */
const PAGE_LIMIT = 200
/** Cota de seguridad de paginación (evita bucles ante un cursor roto). */
const MAX_PAGES = 50
/** Refresco suave de lo ajeno visible (congestión, ciudades, yacimientos). */
const WORLD_REFRESH_MS = 30_000
/** Re-anclaje de la flota propia mientras haya vehículos en tránsito. */
const FLEET_REFRESH_MS = 10_000
/** Coalescing del refresco del ledger tras eventos (casi todos mueven valor). */
const LEDGER_DEBOUNCE_MS = 2_000

/** Fábrica de producción sobre el WebSocket nativo del navegador (FAD §12.2). */
const nativeSocketFactory: WebSocketFactory = (url, handlers) => {
  const ws = new WebSocket(url)
  ws.onopen = () => {
    handlers.onOpen()
  }
  ws.onmessage = (event) => {
    handlers.onMessage(event.data)
  }
  ws.onclose = (event) => {
    handlers.onClose(event.code)
  }
  ws.onerror = () => {
    handlers.onError()
  }
  return ws
}

/**
 * URL del gateway WS: `wsBase` explícito si está configurado (dev: directo al
 * gateway Go, ver nuxt.config `$development`); si no, derivada del apiBase
 * sobre el mismo origen (`/api/v1` → `ws(s)://host/api/v1/ws`).
 */
function gatewayUrl(apiBase: string, wsBase: string): string {
  if (wsBase !== '') {
    return wsBase
  }
  const base = new URL(apiBase, window.location.origin)
  const wsProtocol = base.protocol === 'https:' ? 'wss:' : 'ws:'
  const path = base.pathname.replace(/\/$/, '')
  return `${wsProtocol}//${base.host}${path}/ws`
}

/** Agota una lista paginada por cursor (bootstrap). */
async function fetchAll<T>(
  fetchPage: (cursor: string | undefined) => Promise<Page<T>>,
): Promise<T[]> {
  const items: T[] = []
  let cursor: string | undefined = undefined
  for (let page = 0; page < MAX_PAGES; page++) {
    const result = await fetchPage(cursor)
    items.push(...result.items)
    if (result.nextCursor === null) {
      return items
    }
    cursor = result.nextCursor
  }
  return items
}

/** Lee un campo uuid del payload crudo de un evento (defensivo, sin tipar). */
function readUuidField(payload: unknown, field: string): string | null {
  if (typeof payload !== 'object' || payload === null) {
    return null
  }
  const value = (payload as Record<string, unknown>)[field]
  return typeof value === 'string' && value.length > 0 ? value : null
}

function isNotFound(error: unknown): boolean {
  return error instanceof AppError && error.code === 'NOT_FOUND'
}

export function useGameSync(): GameSync {
  const apis: GameApis = useGameApis()
  const { apiBase, wsBase } = useRuntimeConfig().public
  const nuxtApp = useNuxtApp() as { $simClock?: SimClock }

  const session = useSessionStore()
  const world = useWorldStore()
  const buildings = useBuildingsStore()
  const logistics = useLogisticsStore()
  const fleet = useFleetStore()
  const cadastre = useCadastreStore()
  const market = useMarketStore()
  const finance = useFinanceStore()

  const phase = ref<SyncPhase>('idle')
  const connection = ref<ConnectionState>('closed')
  const stale = ref(false)
  const lastError = ref<unknown>(null)

  let transport: NetworkTransport | null = null
  let orchestrator: SyncOrchestrator | null = null
  let worldTimer: ReturnType<typeof setInterval> | null = null
  let fleetTimer: ReturnType<typeof setInterval> | null = null
  let ledgerTimer: ReturnType<typeof setTimeout> | null = null
  const subscriptions: (() => void)[] = []

  /** Sim-time de observación para entidades cinemáticas (fallback génesis). */
  function nowSim(): SimTime {
    return nuxtApp.$simClock?.now() ?? simTime(0)
  }

  function report(error: unknown): void {
    lastError.value = error
  }

  // ── Bootstrap: catálogos y red común ───────────────────────────────────────

  async function bootstrapCatalogs(): Promise<void> {
    const [regions, products, buildingTypes, recipes, deposits, cities, vehicleTypes] =
      await Promise.all([
        fetchAll((cursor) =>
          apis.world.listRegions({ limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) }),
        ),
        fetchAll((cursor) =>
          apis.world.listProducts({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.world.listBuildingTypes({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.world.listRecipes({ limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) }),
        ),
        fetchAll((cursor) =>
          apis.world.listResourceDeposits({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.world.listCities({ limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) }),
        ),
        fetchAll((cursor) =>
          apis.fleet.listVehicleTypes({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
      ])
    world.applyRegionsSnapshot(regions.map(mapRegion))
    world.applyProductsSnapshot(products.map(mapProduct))
    world.applyBuildingTypesSnapshot(buildingTypes.map(mapBuildingType))
    world.applyRecipesSnapshot(recipes.map(mapRecipe))
    world.applyDepositsSnapshot(deposits.map(mapDeposit))
    world.applyCitiesSnapshot(cities.map(mapCity))
    fleet.applyVehicleTypesSnapshot(vehicleTypes.map(mapVehicleType))

    const [nodes, links] = await Promise.all([
      fetchAll((cursor) =>
        apis.logistics.listNetworkNodes({
          limit: PAGE_LIMIT,
          ...(cursor === undefined ? {} : { cursor }),
        }),
      ),
      fetchAll((cursor) =>
        apis.logistics.listNetworkLinks({
          limit: PAGE_LIMIT,
          ...(cursor === undefined ? {} : { cursor }),
        }),
      ),
    ])
    logistics.applyNodesSnapshot(nodes.map(mapNode))
    logistics.applyLinksSnapshot(links.map(mapLink))
  }

  // ── Bootstrap: estado propio (repetible en cada resync) ────────────────────

  async function bootstrapOwn(): Promise<void> {
    const [
      concessions,
      ownBuildings,
      vehicles,
      shipments,
      routes,
      contracts,
      freightsAsShipper,
      freightsAsCarrier,
      accounts,
      board,
    ] = await Promise.all([
        fetchAll((cursor) =>
          apis.world.listConcessions({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.world.listBuildings({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.fleet.listVehicles({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.fleet.listShipments({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.logistics.listRoutes({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.market.listContracts({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.market.listFreightContracts({
            role: 'shipper',
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.market.listFreightContracts({
            role: 'carrier',
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        fetchAll((cursor) =>
          apis.ledger.listLedgerAccounts({
            limit: PAGE_LIMIT,
            ...(cursor === undefined ? {} : { cursor }),
          }),
        ),
        apis.market.queryBoard({ limit: PAGE_LIMIT }),
      ])

    cadastre.applyConcessionsSnapshot(concessions.map(mapConcession))
    const mappedBuildings = ownBuildings.map(mapBuilding)
    buildings.applyBuildingsSnapshot(mappedBuildings)
    const observedAt = nowSim()
    fleet.applyVehiclesSnapshot(vehicles.map((dto) => mapVehicle(dto, observedAt)))
    fleet.applyShipmentsSnapshot(shipments.map(mapShipment))
    logistics.applyRoutesSnapshot(routes.map(mapRoute))
    market.applyContractsSnapshot(contracts.map(mapContract))
    // Ambos roles del CCRI-Flete (dedupe por id: una corp puede ser cargador
    // de unos fletes y transportista de otros; jamás ambos del mismo).
    const freightById = new Map(
      [...freightsAsShipper, ...freightsAsCarrier].map((dto) => [dto.id, dto]),
    )
    market.applyFreightsSnapshot([...freightById.values()].map(mapFreightContract))
    finance.applyLedgerAccountsSnapshot(accounts.map(mapLedgerAccount))

    // Tablón: pull inicial (efímero) + reconstrucción de MIS publicaciones
    // vivas (el contrato no expone listado propio; ver docblock).
    const boardPublications = board.items.map(mapPublication)
    market.applyBoardSnapshot(boardPublications, observedAt)
    const myAccountId = session.account?.id ?? null
    for (const publication of boardPublications) {
      if (myAccountId !== null && publication.publisherAccountId === myAccountId) {
        market.applyPublication(publication)
      }
    }

    // Inventario + cola de producción por edificio propio (pulls acotados).
    await Promise.all(
      mappedBuildings.map(async (building) => {
        const [inventory, batches] = await Promise.all([
          apis.world.getBuildingInventory(building.id),
          fetchAll((cursor) =>
            apis.world.listProductionBatches(building.id, {
              limit: PAGE_LIMIT,
              ...(cursor === undefined ? {} : { cursor }),
            }),
          ),
        ])
        buildings.applyInventorySnapshot(building.id, inventory.map(mapInventoryItem))
        buildings.applyBuildingBatchesSnapshot(building.id, batches.map(mapProductionBatch))
      }),
    )
  }

  // ── Refetches dirigidos por evento (appliers idempotentes) ─────────────────

  async function refetchBuilding(buildingId: string): Promise<void> {
    try {
      buildings.applyBuilding(mapBuilding(await apis.world.getBuilding(buildingId)))
    } catch (error) {
      if (isNotFound(error)) {
        buildings.removeBuilding(asEntityId<'Building'>(buildingId))
        return
      }
      throw error
    }
  }

  async function refetchBuildingProduction(buildingId: string): Promise<void> {
    const branded = asEntityId<'Building'>(buildingId)
    const [inventory, batches] = await Promise.all([
      apis.world.getBuildingInventory(buildingId),
      fetchAll((cursor) =>
        apis.world.listProductionBatches(buildingId, {
          limit: PAGE_LIMIT,
          ...(cursor === undefined ? {} : { cursor }),
        }),
      ),
    ])
    buildings.applyInventorySnapshot(branded, inventory.map(mapInventoryItem))
    buildings.applyBuildingBatchesSnapshot(branded, batches.map(mapProductionBatch))
  }

  async function refetchFreightContract(freightContractId: string): Promise<void> {
    try {
      market.applyFreightContract(
        mapFreightContract(await apis.market.getFreightContract(freightContractId)),
      )
    } catch (error) {
      if (isNotFound(error)) {
        market.removeFreightContract(asEntityId<'FreightContract'>(freightContractId))
        return
      }
      throw error
    }
  }

  async function refetchLedgerAccounts(): Promise<void> {
    const accounts = await fetchAll((cursor) =>
      apis.ledger.listLedgerAccounts({
        limit: PAGE_LIMIT,
        ...(cursor === undefined ? {} : { cursor }),
      }),
    )
    finance.applyLedgerAccountsSnapshot(accounts.map(mapLedgerAccount))
  }

  /** Casi todo evento mueve valor: refresco del ledger con coalescing. */
  function scheduleLedgerRefresh(): void {
    if (ledgerTimer !== null) {
      return
    }
    ledgerTimer = setTimeout(() => {
      ledgerTimer = null
      refetchLedgerAccounts().catch(report)
    }, LEDGER_DEBOUNCE_MS)
  }

  /** Adapta un handler async a `EventApplier` (errores reportados, no lanzados). */
  function applier(handle: (event: DomainEvent) => Promise<void>) {
    return (event: DomainEvent): void => {
      handle(event).catch(report)
    }
  }

  function registerAppliers(sync: SyncOrchestrator): void {
    subscriptions.push(
      sync.registerApplier(
        'building.',
        applier(async (event) => refetchBuilding(event.aggregateId)),
      ),
      sync.registerApplier(
        'batch.',
        applier(async (event) => {
          const buildingId = readUuidField(event.payload, 'building_id')
          if (buildingId !== null) {
            await Promise.all([refetchBuildingProduction(buildingId), refetchBuilding(buildingId)])
            return
          }
          // Sin building_id en el payload: re-pull de colas de TODOS los propios.
          await Promise.all(
            buildings.buildingList.map((building) => refetchBuildingProduction(building.id)),
          )
        }),
      ),
      sync.registerApplier(
        'concession.',
        applier(async (event) => {
          try {
            cadastre.applyConcession(mapConcession(await apis.world.getConcession(event.aggregateId)))
          } catch (error) {
            if (isNotFound(error)) {
              cadastre.removeConcession(asEntityId<'Concession'>(event.aggregateId))
              return
            }
            throw error
          }
        }),
      ),
      sync.registerApplier(
        'vehicle.',
        applier(async (event) => {
          fleet.applyVehicle(mapVehicle(await apis.fleet.getVehicle(event.aggregateId), nowSim()))
        }),
      ),
      sync.registerApplier(
        'shipment.',
        applier(async (event) => {
          const shipment = mapShipment(await apis.fleet.getShipment(event.aggregateId))
          fleet.applyShipment(shipment)
          // Cargamento de flete: refresca también el freight contract (fill/
          // status cambian con cada hito físico; defensa si un push se pierde).
          if (shipment.freightContractId !== null) {
            await refetchFreightContract(shipment.freightContractId)
          }
        }),
      ),
      sync.registerApplier(
        'contract.',
        applier(async (event) => {
          market.applyContract(mapContract(await apis.market.getContract(event.aggregateId)))
        }),
      ),
      // freight.confirmed/settled/expired_undelivered (agregado freight_contract).
      sync.registerApplier(
        'freight.',
        applier(async (event) => refetchFreightContract(event.aggregateId)),
      ),
      sync.registerApplier(
        'publication.',
        applier(async (event) => {
          market.applyPublication(
            mapPublication(await apis.market.getPublication(event.aggregateId)),
          )
        }),
      ),
      sync.registerApplier(
        'acceptance.',
        applier(async (event) => {
          const acceptance = mapAcceptance(await apis.market.getAcceptance(event.aggregateId))
          market.applyAcceptance(acceptance)
          if (acceptance.contractId !== null) {
            market.applyContract(mapContract(await apis.market.getContract(acceptance.contractId)))
          }
          if (acceptance.freightContractId !== null) {
            await refetchFreightContract(acceptance.freightContractId)
          }
        }),
      ),
      // Catch-all: el ledger refleja casi cualquier hito (refresco coalescido).
      sync.registerApplier('', () => {
        scheduleLedgerRefresh()
      }),
    )
  }

  // ── Refrescos periódicos suaves ────────────────────────────────────────────

  async function refreshWorldSoft(): Promise<void> {
    const [links, cities, deposits] = await Promise.all([
      fetchAll((cursor) =>
        apis.logistics.listNetworkLinks({
          limit: PAGE_LIMIT,
          ...(cursor === undefined ? {} : { cursor }),
        }),
      ),
      fetchAll((cursor) =>
        apis.world.listCities({ limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) }),
      ),
      fetchAll((cursor) =>
        apis.world.listResourceDeposits({
          limit: PAGE_LIMIT,
          ...(cursor === undefined ? {} : { cursor }),
        }),
      ),
    ])
    // Merge suave (no elimina): la congestión EMA y los agregados cambian solos.
    logistics.mergeLinks(links.map(mapLink))
    world.applyCitiesSnapshot(cities.map(mapCity))
    world.applyDepositsSnapshot(deposits.map(mapDeposit))
  }

  async function refreshFleetIfMoving(): Promise<void> {
    if (fleet.vehiclesOnSegments.length === 0 && fleet.vehicleCountByStatus.in_transit === 0) {
      return
    }
    const vehicles = await fetchAll((cursor) =>
      apis.fleet.listVehicles({ limit: PAGE_LIMIT, ...(cursor === undefined ? {} : { cursor }) }),
    )
    const observedAt = nowSim()
    fleet.applyVehiclesSnapshot(vehicles.map((dto) => mapVehicle(dto, observedAt)))
  }

  function startTimers(): void {
    worldTimer = setInterval(() => {
      refreshWorldSoft().catch(report)
    }, WORLD_REFRESH_MS)
    fleetTimer = setInterval(() => {
      refreshFleetIfMoving().catch(report)
    }, FLEET_REFRESH_MS)
  }

  function stopTimers(): void {
    if (worldTimer !== null) {
      clearInterval(worldTimer)
      worldTimer = null
    }
    if (fleetTimer !== null) {
      clearInterval(fleetTimer)
      fleetTimer = null
    }
    if (ledgerTimer !== null) {
      clearTimeout(ledgerTimer)
      ledgerTimer = null
    }
  }

  // ── Ciclo de vida ──────────────────────────────────────────────────────────

  async function start(): Promise<void> {
    if (phase.value === 'bootstrapping' || phase.value === 'ready') {
      return
    }
    const token = session.token
    if (token === null) {
      phase.value = 'error'
      report(new Error('useGameSync.start(): sin sesión (token nulo)'))
      return
    }
    phase.value = 'bootstrapping'
    lastError.value = null

    transport = createGatewayTransport({
      url: gatewayUrl(apiBase, wsBase),
      createSocket: nativeSocketFactory,
      onProtocolViolation: report,
    })
    orchestrator = createSyncOrchestrator(transport, { onApplierError: report })
    subscriptions.push(
      transport.onStateChange((state) => {
        connection.value = state
      }),
      orchestrator.onResync(() => {
        stale.value = true
        bootstrapOwn()
          .then(() => {
            stale.value = false
          })
          .catch((error: unknown) => {
            report(error)
          })
      }),
    )
    registerAppliers(orchestrator)

    try {
      // Join primero: todo evento posterior llega con seq > watermark, así que
      // el bootstrap REST posterior nunca pierde deltas (ADR-023 §6).
      await orchestrator.start(token)
      await bootstrapCatalogs()
      await bootstrapOwn()
      phase.value = 'ready'
      startTimers()
    } catch (error) {
      report(error)
      phase.value = 'error'
      stopTimers()
      orchestrator.stop()
    }
  }

  function stop(): void {
    stopTimers()
    for (const unsubscribe of subscriptions) {
      unsubscribe()
    }
    subscriptions.length = 0
    orchestrator?.stop()
    orchestrator = null
    transport = null
    phase.value = 'idle'
    connection.value = 'closed'
    stale.value = false
  }

  return {
    phase: readonly(phase),
    connection: readonly(connection),
    stale: readonly(stale),
    lastError: readonly(lastError),
    start,
    stop,
  }
}
