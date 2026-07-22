/**
 * network/mappers/domain.mapper — ACL DTO → dominio (FAD §9.5, O5).
 *
 * ÚNICO punto donde los DTO crudos del contrato se convierten en entidades de
 * dominio del cliente (los DTO no salen de network/): ids con brand
 * (`parseEntityId`), dinero/stock validados con `parseMoney`/`parseQuantity`
 * (strings de punto fijo, jamás floats — C11), sim-time con `simTime`,
 * instantes wall-clock (`date-time`) a ms de epoch y geometría GeoJSON-like a
 * tuplas de metros de mundo (ADR-019).
 *
 * Un DTO que viole el contrato (uuid inválido, importe no numérico, geometría
 * rota) lanza `AppError` kind `protocol`: bug de servidor o de versión de
 * contrato, nunca culpa del jugador (misma taxonomía que network/rest/errors).
 *
 * Los mapeos son funciones puras totales; la capa de aplicación las invoca al
 * aplicar respuestas REST y refetches por evento WS a las stores.
 */

import { parseEntityId } from '~shared/ids'
import type { EntityId } from '~shared/ids'
import { parseMoney } from '~shared/money'
import type { Money } from '~shared/money'
import { simTime } from '~shared/simtime'
import type { SimTime } from '~shared/simtime'
import { parseQuantity } from '~domain/quantity'
import type { Quantity } from '~domain/quantity'
import { isWorldPointM } from '~domain/geo'
import type { WorldPathM, WorldPointM, WorldPolygonM } from '~domain/geo'
import type {
  Building,
  InventoryItem,
  ProductionBatch,
} from '~domain/buildings'
import type { Concession } from '~domain/cadastre'
import type { LedgerAccount, LedgerEntry, SignedAmount } from '~domain/finance'
import { isSignedAmount } from '~domain/finance'
import type { Shipment, Vehicle, VehiclePosition, VehicleType } from '~domain/fleet'
import type {
  LinkSegment,
  NetworkLink,
  NetworkNode,
  Route,
  Terminal,
  TerminalSlot,
} from '~domain/logistics'
import type {
  Acceptance,
  Contract,
  FreightContract,
  OhlcCandle,
  Publication,
} from '~domain/market'
import type {
  BuildingType,
  City,
  CityDemand,
  Product,
  Recipe,
  RecipeIngredient,
  Region,
  ResourceDeposit,
} from '~domain/world'
import { appErrorFromProtocol } from '../rest/errors'
import type { GeoLineString, GeoPoint, GeoPolygon } from './geometry'
import type {
  BuildingDto,
  BuildingTypeDto,
  CityDemandDto,
  CityDto,
  ConcessionDto,
  InventoryItemDto,
  ProductDto,
  ProductionBatchDto,
  RecipeDto,
  RegionDto,
  ResourceDepositDto,
} from '../world.api'
import type { LedgerAccountDto, LedgerEntryDto } from '../ledger.api'
import type {
  ShipmentDto,
  TerminalDto,
  TerminalSlotDto,
  VehicleDto,
  VehiclePositionDto,
  VehicleTypeDto,
} from '../fleet.api'
import type { LinkSegmentDto, NetworkLinkDto, NetworkNodeDto, RouteDto } from '../logistics.api'
import type {
  AcceptanceDto,
  ContractDto,
  FreightContractDto,
  OhlcCandleDto,
  PublicationDto,
} from '../market.api'

// ── Helpers de forma (violación ⇒ AppError `protocol`) ──────────────────────

function violation(field: string, value: unknown): never {
  throw appErrorFromProtocol(`${field} inválido en DTO (${JSON.stringify(value)})`)
}

function id<Brand extends string>(value: string, field: string): EntityId<Brand> {
  const parsed = parseEntityId<Brand>(value)
  if (!parsed.ok) {
    violation(field, value)
  }
  return parsed.value
}

function idOrNull<Brand extends string>(
  value: string | undefined,
  field: string,
): EntityId<Brand> | null {
  return value === undefined ? null : id<Brand>(value, field)
}

function money(value: string, field: string): Money {
  const parsed = parseMoney(value)
  if (!parsed.ok) {
    violation(field, value)
  }
  return parsed.value
}

function moneyOrNull(value: string | undefined, field: string): Money | null {
  return value === undefined ? null : money(value, field)
}

function qty(value: string, field: string): Quantity {
  const parsed = parseQuantity(value)
  if (!parsed.ok) {
    violation(field, value)
  }
  return parsed.value
}

function sim(value: number, field: string): SimTime {
  try {
    return simTime(value)
  } catch {
    violation(field, value)
  }
}

function simOrNull(value: number | undefined, field: string): SimTime | null {
  return value === undefined ? null : sim(value, field)
}

/** Instante `date-time` del contrato → ms de epoch. */
function wallMs(value: string, field: string): number {
  const ms = Date.parse(value)
  if (Number.isNaN(ms)) {
    violation(field, value)
  }
  return ms
}

function wallMsOrNull(value: string | undefined, field: string): number | null {
  return value === undefined ? null : wallMs(value, field)
}

function pointM(point: GeoPoint, field: string): WorldPointM {
  const coords = point.coordinates
  if (!isWorldPointM(coords)) {
    violation(field, point)
  }
  return [coords[0], coords[1]]
}

function pathM(line: GeoLineString, field: string): WorldPathM {
  return line.coordinates.map((vertex, index) => {
    if (!isWorldPointM(vertex)) {
      violation(`${field}[${String(index)}]`, vertex)
    }
    return [vertex[0], vertex[1]] as const
  })
}

function polygonM(polygon: GeoPolygon, field: string): WorldPolygonM {
  return polygon.coordinates.map((ring, ringIndex) =>
    ring.map((vertex, index) => {
      if (!isWorldPointM(vertex)) {
        violation(`${field}[${String(ringIndex)}][${String(index)}]`, vertex)
      }
      return [vertex[0], vertex[1]] as const
    }),
  )
}

// ── World (catálogos y entidades del mundo) ──────────────────────────────────

export function mapRegion(dto: RegionDto): Region {
  return {
    id: id(dto.id, 'Region.id'),
    name: dto.name,
    gridX: dto.grid_x,
    gridY: dto.grid_y,
    boundsM: dto.bounds === undefined ? null : polygonM(dto.bounds, 'Region.bounds'),
    biome: dto.biome,
    taxRateBp: dto.tax_rate_bp,
    customsRateBp: dto.customs_rate_bp,
    canonBase: money(dto.canon_base, 'Region.canon_base'),
    openedAtSim: sim(dto.opened_at_sim, 'Region.opened_at_sim'),
  }
}

export function mapProduct(dto: ProductDto): Product {
  return {
    id: id(dto.id, 'Product.id'),
    code: dto.code,
    name: dto.name,
    productClass: dto.class,
    unitVolume: dto.unit_volume,
    basePrice: money(dto.base_price, 'Product.base_price'),
    priceFloor: money(dto.price_floor, 'Product.price_floor'),
    priceCeiling: money(dto.price_ceiling, 'Product.price_ceiling'),
    isFuel: dto.is_fuel,
  }
}

export function mapBuildingType(dto: BuildingTypeDto): BuildingType {
  return {
    id: id(dto.id, 'BuildingType.id'),
    code: dto.code,
    name: dto.name,
    footprintCells: dto.footprint_cells,
    maxLevel: dto.max_level,
    baseStorage: qty(dto.base_storage, 'BuildingType.base_storage'),
    placementRules: dto.placement_rules ?? null,
    levelCurve: dto.level_curve ?? null,
    buildCost: money(dto.build_cost, 'BuildingType.build_cost'),
    maintenanceCost: money(dto.maintenance_cost, 'BuildingType.maintenance_cost'),
  }
}

function mapIngredient(dto: RecipeDto['ingredients'][number]): RecipeIngredient {
  return {
    productId: id(dto.product_id, 'RecipeIngredient.product_id'),
    role: dto.role,
    quantity: qty(dto.quantity, 'RecipeIngredient.quantity'),
  }
}

export function mapRecipe(dto: RecipeDto): Recipe {
  return {
    id: id(dto.id, 'Recipe.id'),
    buildingTypeId: id(dto.building_type_id, 'Recipe.building_type_id'),
    code: dto.code,
    name: dto.name,
    batchSimSeconds: dto.batch_sim_seconds,
    fuelProductId: idOrNull(dto.fuel_product_id, 'Recipe.fuel_product_id'),
    fuelPerBatch: qty(dto.fuel_per_batch, 'Recipe.fuel_per_batch'),
    workersRequired: dto.workers_required,
    minCityLevel: dto.min_city_level,
    changeoverSeconds: dto.changeover_seconds,
    ingredients: dto.ingredients.map(mapIngredient),
  }
}

export function mapDeposit(dto: ResourceDepositDto): ResourceDeposit {
  return {
    id: id(dto.id, 'ResourceDeposit.id'),
    regionId: id(dto.region_id, 'ResourceDeposit.region_id'),
    productId: id(dto.product_id, 'ResourceDeposit.product_id'),
    locationM: pointM(dto.location, 'ResourceDeposit.location'),
    initialAmount: qty(dto.initial_amount, 'ResourceDeposit.initial_amount'),
    remainingAmount: qty(dto.remaining_amount, 'ResourceDeposit.remaining_amount'),
    renewable: dto.renewable,
    regenPerSimDay: qty(dto.regen_per_sim_day, 'ResourceDeposit.regen_per_sim_day'),
  }
}

export function mapCity(dto: CityDto): City {
  return {
    id: id(dto.id, 'City.id'),
    regionId: id(dto.region_id, 'City.region_id'),
    accountId: id(dto.account_id, 'City.account_id'),
    name: dto.name,
    locationM: pointM(dto.location, 'City.location'),
    level: dto.level,
    population: dto.population,
    supplyIndex: dto.supply_index,
    influenceRadiusM: dto.influence_radius_m,
    baseSalary: money(dto.base_salary, 'City.base_salary'),
  }
}

export function mapCityDemand(dto: CityDemandDto): CityDemand {
  return {
    cityId: id(dto.city_id, 'CityDemand.city_id'),
    productId: id(dto.product_id, 'CityDemand.product_id'),
    d0PerSimDay: qty(dto.d0_per_sim_day, 'CityDemand.d0_per_sim_day'),
    saturationFactor: dto.saturation_factor,
    currentPrice: money(dto.current_price, 'CityDemand.current_price'),
    unlockedAtLevel: dto.unlocked_at_level,
    updatedAtSim: sim(dto.updated_at_sim, 'CityDemand.updated_at_sim'),
  }
}

// ── Cadastre ─────────────────────────────────────────────────────────────────

export function mapConcession(dto: ConcessionDto): Concession {
  return {
    id: id(dto.id, 'Concession.id'),
    regionId: id(dto.region_id, 'Concession.region_id'),
    holderAccountId: id(dto.holder_account_id, 'Concession.holder_account_id'),
    parcelM: polygonM(dto.parcel, 'Concession.parcel'),
    canonAmount: money(dto.canon_amount, 'Concession.canon_amount'),
    periodSimDays: dto.period_sim_days,
    expiresAtSim: sim(dto.expires_at_sim, 'Concession.expires_at_sim'),
    status: dto.status,
    grantedAtSim: sim(dto.granted_at_sim, 'Concession.granted_at_sim'),
  }
}

// ── Buildings ────────────────────────────────────────────────────────────────

export function mapBuilding(dto: BuildingDto): Building {
  return {
    id: id(dto.id, 'Building.id'),
    ownerAccountId: id(dto.owner_account_id, 'Building.owner_account_id'),
    regionId: id(dto.region_id, 'Building.region_id'),
    concessionId: id(dto.concession_id, 'Building.concession_id'),
    buildingTypeId: id(dto.building_type_id, 'Building.building_type_id'),
    footprintM: polygonM(dto.footprint, 'Building.footprint'),
    level: dto.level,
    status: dto.status,
    activeRecipeId: idOrNull(dto.active_recipe_id, 'Building.active_recipe_id'),
    conditionPct: dto.condition_pct,
    fuelStock: qty(dto.fuel_stock, 'Building.fuel_stock'),
    updatedAtSim: simOrNull(dto.updated_at_sim, 'Building.updated_at_sim'),
  }
}

export function mapInventoryItem(dto: InventoryItemDto): InventoryItem {
  return {
    buildingId: id(dto.building_id, 'InventoryItem.building_id'),
    productId: id(dto.product_id, 'InventoryItem.product_id'),
    quantity: qty(dto.quantity, 'InventoryItem.quantity'),
    updatedAtSim: sim(dto.updated_at_sim, 'InventoryItem.updated_at_sim'),
  }
}

export function mapProductionBatch(dto: ProductionBatchDto): ProductionBatch {
  return {
    id: id(dto.id, 'ProductionBatch.id'),
    buildingId: id(dto.building_id, 'ProductionBatch.building_id'),
    recipeId: id(dto.recipe_id, 'ProductionBatch.recipe_id'),
    batchesQueued: dto.batches_queued,
    batchesDone: dto.batches_done,
    status: dto.status,
    queuePosition: dto.queue_position,
    startedAtSim: simOrNull(dto.started_at_sim, 'ProductionBatch.started_at_sim'),
    progressPctObserved: dto.progress_pct ?? null,
    etaSim: simOrNull(dto.eta_sim, 'ProductionBatch.eta_sim'),
  }
}

// ── Logistics ────────────────────────────────────────────────────────────────

export function mapNode(dto: NetworkNodeDto): NetworkNode {
  return {
    id: id(dto.id, 'NetworkNode.id'),
    kind: dto.kind,
    regionId: id(dto.region_id, 'NetworkNode.region_id'),
    buildingId: idOrNull(dto.building_id, 'NetworkNode.building_id'),
    cityId: idOrNull(dto.city_id, 'NetworkNode.city_id'),
    locationM: pointM(dto.location, 'NetworkNode.location'),
    terminalId: idOrNull(dto.terminal_id, 'NetworkNode.terminal_id'),
  }
}

export function mapTerminal(dto: TerminalDto): Terminal {
  return {
    id: id(dto.id, 'Terminal.id'),
    nodeId: id(dto.node_id, 'Terminal.node_id'),
    ownerAccountId: id(dto.owner_account_id, 'Terminal.owner_account_id'),
    transshipmentPerHour: dto.transshipment_per_hour,
    queueLength: dto.queue_length,
    updatedAtSim: simOrNull(dto.updated_at_sim, 'Terminal.updated_at_sim'),
  }
}

export function mapTerminalSlot(dto: TerminalSlotDto): TerminalSlot {
  return {
    id: id(dto.id, 'TerminalSlot.id'),
    terminalId: id(dto.terminal_id, 'TerminalSlot.terminal_id'),
    priorityTier: dto.priority_tier,
    price: money(dto.price, 'TerminalSlot.price'),
    holderAccountId: idOrNull(dto.holder_account_id, 'TerminalSlot.holder_account_id'),
    validUntilSim: simOrNull(dto.valid_until_sim, 'TerminalSlot.valid_until_sim'),
  }
}

function mapSegment(dto: LinkSegmentDto): LinkSegment {
  return {
    id: id(dto.id, 'LinkSegment.id'),
    regionId: id(dto.region_id, 'LinkSegment.region_id'),
    seq: dto.seq,
    lengthM: dto.length_m,
    congestionEma: dto.congestion_ema,
    updatedAtSim: sim(dto.updated_at_sim, 'LinkSegment.updated_at_sim'),
  }
}

export function mapLink(dto: NetworkLinkDto): NetworkLink {
  return {
    id: id(dto.id, 'NetworkLink.id'),
    mode: dto.mode,
    fromNodeId: id(dto.from_node_id, 'NetworkLink.from_node_id'),
    toNodeId: id(dto.to_node_id, 'NetworkLink.to_node_id'),
    pathM: dto.path === undefined ? null : pathM(dto.path, 'NetworkLink.path'),
    lengthM: dto.length_m,
    capacityPerHour: dto.capacity_per_hour,
    baseSpeedKmh: dto.base_speed_kmh,
    // El dominio garantiza segmentos ordenados por `seq` (contrato del mapper).
    segments: dto.segments.map(mapSegment).sort((a, b) => a.seq - b.seq),
  }
}

export function mapRoute(dto: RouteDto): Route {
  return {
    id: id(dto.id, 'Route.id'),
    ownerAccountId: id(dto.owner_account_id, 'Route.owner_account_id'),
    name: dto.name,
    kind: dto.kind,
    active: dto.active,
    legs: dto.legs
      .map((leg) => ({
        legIndex: leg.leg_index,
        linkId: id<'Link'>(leg.link_id, 'Route.legs.link_id'),
      }))
      .sort((a, b) => a.legIndex - b.legIndex),
  }
}

// ── Fleet ────────────────────────────────────────────────────────────────────

export function mapVehicleType(dto: VehicleTypeDto): VehicleType {
  return {
    id: id(dto.id, 'VehicleType.id'),
    code: dto.code,
    name: dto.name,
    mode: dto.mode,
    cargoCapacity: qty(dto.cargo_capacity, 'VehicleType.cargo_capacity'),
    speedKmh: dto.speed_kmh,
    fuelProductId: id(dto.fuel_product_id, 'VehicleType.fuel_product_id'),
    fuelPer100km: qty(dto.fuel_per_100km, 'VehicleType.fuel_per_100km'),
    autonomyKm: dto.autonomy_km,
    purchasePrice: money(dto.purchase_price, 'VehicleType.purchase_price'),
    operatingCostPerDay: money(dto.operating_cost_per_day, 'VehicleType.operating_cost_per_day'),
  }
}

function mapVehiclePosition(dto: VehiclePositionDto): VehiclePosition {
  const locationM = dto.location === undefined ? null : pointM(dto.location, 'Vehicle.location')
  if (dto.on_segment_id !== undefined) {
    return {
      kind: 'on-segment',
      segmentId: id(dto.on_segment_id, 'Vehicle.on_segment_id'),
      progressPct: dto.segment_progress_pct ?? 0,
      locationM,
    }
  }
  if (dto.at_node_id !== undefined) {
    return {
      kind: 'at-node',
      nodeId: id(dto.at_node_id, 'Vehicle.at_node_id'),
      locationM,
    }
  }
  violation('Vehicle.position', dto)
}

/**
 * `observedAtSimFallback`: sim-time de la observación cuando el DTO no trae
 * `updated_at_sim` — el llamador pasa el simNow del SimClock al recibir la
 * respuesta (base de la extrapolación de domain/kinematics).
 */
export function mapVehicle(dto: VehicleDto, observedAtSimFallback: SimTime): Vehicle {
  return {
    id: id(dto.id, 'Vehicle.id'),
    vehicleTypeId: id(dto.vehicle_type_id, 'Vehicle.vehicle_type_id'),
    ownerAccountId: id(dto.owner_account_id, 'Vehicle.owner_account_id'),
    status: dto.status,
    wearPct: dto.wear_pct,
    fuel: qty(dto.fuel, 'Vehicle.fuel'),
    routeId: idOrNull(dto.route_id, 'Vehicle.route_id'),
    routeLegIndex: dto.route_leg_index ?? null,
    position: mapVehiclePosition(dto.position),
    repairUntilSim: simOrNull(dto.repair_until_sim, 'Vehicle.repair_until_sim'),
    observedAtSim:
      simOrNull(dto.updated_at_sim, 'Vehicle.updated_at_sim') ?? observedAtSimFallback,
  }
}

export function mapShipment(dto: ShipmentDto): Shipment {
  return {
    id: id(dto.id, 'Shipment.id'),
    ownerAccountId: id(dto.owner_account_id, 'Shipment.owner_account_id'),
    productId: id(dto.product_id, 'Shipment.product_id'),
    quantity: qty(dto.quantity, 'Shipment.quantity'),
    contractId: idOrNull(dto.contract_id, 'Shipment.contract_id'),
    freightContractId: idOrNull(dto.freight_contract_id, 'Shipment.freight_contract_id'),
    vehicleId: idOrNull(dto.vehicle_id, 'Shipment.vehicle_id'),
    atNodeId: idOrNull(dto.at_node_id, 'Shipment.at_node_id'),
    status: dto.status,
    updatedAtSim: simOrNull(dto.updated_at_sim, 'Shipment.updated_at_sim'),
  }
}

// ── Market / CCRI ────────────────────────────────────────────────────────────

export function mapPublication(dto: PublicationDto): Publication {
  return {
    id: id(dto.id, 'Publication.id'),
    kind: dto.kind,
    publisherAccountId: id(dto.publisher_account_id, 'Publication.publisher_account_id'),
    channel: dto.channel,
    counterpartyAccountId: idOrNull(
      dto.counterparty_account_id,
      'Publication.counterparty_account_id',
    ),
    productId: idOrNull(dto.product_id, 'Publication.product_id'),
    quantityTotal: qty(dto.quantity_total, 'Publication.quantity_total'),
    quantityRemaining: qty(dto.quantity_remaining, 'Publication.quantity_remaining'),
    unitPrice: money(dto.unit_price, 'Publication.unit_price'),
    minLot: qty(dto.min_lot, 'Publication.min_lot'),
    originNodeId: idOrNull(dto.origin_node_id, 'Publication.origin_node_id'),
    destinationNodeId: idOrNull(dto.destination_node_id, 'Publication.destination_node_id'),
    deliverySimSeconds: dto.delivery_sim_seconds,
    status: dto.status,
    windowClosesAtMs: wallMsOrNull(dto.window_closes_at, 'Publication.window_closes_at'),
    cancelCooldownUntilMs: wallMsOrNull(
      dto.cancel_cooldown_until,
      'Publication.cancel_cooldown_until',
    ),
    declaredValue: moneyOrNull(dto.declared_value, 'Publication.declared_value'),
    publishedAtSim: sim(dto.published_at_sim, 'Publication.published_at_sim'),
  }
}

export function mapAcceptance(dto: AcceptanceDto): Acceptance {
  return {
    id: id(dto.id, 'Acceptance.id'),
    publicationId: id(dto.publication_id, 'Acceptance.publication_id'),
    acceptorAccountId: id(dto.acceptor_account_id, 'Acceptance.acceptor_account_id'),
    quantity: qty(dto.quantity, 'Acceptance.quantity'),
    quantityServed: qty(dto.quantity_served, 'Acceptance.quantity_served'),
    status: dto.status,
    drawOrder: dto.draw_order ?? null,
    contractId: idOrNull(dto.contract_id, 'Acceptance.contract_id'),
    freightContractId: idOrNull(dto.freight_contract_id, 'Acceptance.freight_contract_id'),
    acceptedAtMs: wallMs(dto.accepted_at, 'Acceptance.accepted_at'),
    resolvedAtMs: wallMsOrNull(dto.resolved_at, 'Acceptance.resolved_at'),
  }
}

export function mapContract(dto: ContractDto): Contract {
  return {
    id: id(dto.id, 'Contract.id'),
    publicationId: idOrNull(dto.publication_id, 'Contract.publication_id'),
    channel: dto.channel,
    buyerAccountId: id(dto.buyer_account_id, 'Contract.buyer_account_id'),
    sellerAccountId: id(dto.seller_account_id, 'Contract.seller_account_id'),
    productId: id(dto.product_id, 'Contract.product_id'),
    quantityAgreed: qty(dto.quantity_agreed, 'Contract.quantity_agreed'),
    quantityDelivered: qty(dto.quantity_delivered, 'Contract.quantity_delivered'),
    unitPrice: money(dto.unit_price, 'Contract.unit_price'),
    originNodeId: id(dto.origin_node_id, 'Contract.origin_node_id'),
    destinationNodeId: id(dto.destination_node_id, 'Contract.destination_node_id'),
    deadlineSim: sim(dto.deadline_sim, 'Contract.deadline_sim'),
    status: dto.status,
    fillBp: dto.fill_bp ?? null,
    confirmedAtSim: sim(dto.confirmed_at_sim, 'Contract.confirmed_at_sim'),
    settledAtSim: simOrNull(dto.settled_at_sim, 'Contract.settled_at_sim'),
  }
}

export function mapFreightContract(dto: FreightContractDto): FreightContract {
  return {
    id: id(dto.id, 'FreightContract.id'),
    publicationId: idOrNull(dto.publication_id, 'FreightContract.publication_id'),
    channel: dto.channel,
    shipperAccountId: id(dto.shipper_account_id, 'FreightContract.shipper_account_id'),
    carrierAccountId: id(dto.carrier_account_id, 'FreightContract.carrier_account_id'),
    originNodeId: id(dto.origin_node_id, 'FreightContract.origin_node_id'),
    destinationNodeId: id(dto.destination_node_id, 'FreightContract.destination_node_id'),
    freightPrice: money(dto.freight_price, 'FreightContract.freight_price'),
    declaredValue: money(dto.declared_value, 'FreightContract.declared_value'),
    deadlineSim: sim(dto.deadline_sim, 'FreightContract.deadline_sim'),
    status: dto.status,
    fillBp: dto.fill_bp ?? null,
    escrowAccountId: idOrNull(dto.escrow_account_id, 'FreightContract.escrow_account_id'),
    carrierGuaranteeAccountId: idOrNull(
      dto.carrier_guarantee_account_id,
      'FreightContract.carrier_guarantee_account_id',
    ),
    custodyAccountId: idOrNull(dto.custody_account_id, 'FreightContract.custody_account_id'),
    confirmedAtSim: sim(dto.confirmed_at_sim, 'FreightContract.confirmed_at_sim'),
    settledAtSim: simOrNull(dto.settled_at_sim, 'FreightContract.settled_at_sim'),
  }
}

export function mapOhlcCandle(dto: OhlcCandleDto): OhlcCandle {
  return {
    productId: id(dto.product_id, 'OhlcCandle.product_id'),
    regionId: id(dto.region_id, 'OhlcCandle.region_id'),
    bucketStartSim: sim(dto.bucket_start_sim, 'OhlcCandle.bucket_start_sim'),
    bucketSimSecs: dto.bucket_sim_secs,
    openPrice: money(dto.open_price, 'OhlcCandle.open_price'),
    highPrice: money(dto.high_price, 'OhlcCandle.high_price'),
    lowPrice: money(dto.low_price, 'OhlcCandle.low_price'),
    closePrice: money(dto.close_price, 'OhlcCandle.close_price'),
    volume: qty(dto.volume, 'OhlcCandle.volume'),
    contractCount: dto.contract_count,
  }
}

// ── Finance / ledger ─────────────────────────────────────────────────────────

export function mapLedgerAccount(dto: LedgerAccountDto): LedgerAccount {
  return {
    id: id(dto.id, 'LedgerAccount.id'),
    kind: dto.kind,
    ownerAccountId: idOrNull(dto.owner_account_id, 'LedgerAccount.owner_account_id'),
    productId: idOrNull(dto.product_id, 'LedgerAccount.product_id'),
    warehouseBuildingId: idOrNull(
      dto.warehouse_building_id,
      'LedgerAccount.warehouse_building_id',
    ),
    referenceId: dto.reference_id ?? null,
    balance: money(dto.balance, 'LedgerAccount.balance'),
    updatedAtMs: wallMsOrNull(dto.updated_at, 'LedgerAccount.updated_at'),
    createdAtMs: wallMs(dto.created_at, 'LedgerAccount.created_at'),
  }
}

function signedAmount(value: string, field: string): SignedAmount {
  if (!isSignedAmount(value)) {
    violation(field, value)
  }
  return value
}

export function mapLedgerEntry(dto: LedgerEntryDto): LedgerEntry {
  return {
    id: id(dto.id, 'LedgerEntry.id'),
    transactionId: id(dto.transaction_id, 'LedgerEntry.transaction_id'),
    accountId: id(dto.account_id, 'LedgerEntry.account_id'),
    amount: signedAmount(dto.amount, 'LedgerEntry.amount'),
    transactionKind: dto.transaction_kind,
    referenceId: dto.reference_id ?? null,
    description: dto.description ?? null,
    simTimeAt: sim(dto.sim_time_at, 'LedgerEntry.sim_time_at'),
    createdAtMs: wallMs(dto.created_at, 'LedgerEntry.created_at'),
  }
}
