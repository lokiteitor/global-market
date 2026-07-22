/**
 * app/stores/testing/fixtures — constructores de entidades de dominio para
 * los tests de stores y políticas. SOLO se importa desde *.spec.ts (no entra
 * en el bundle: ningún módulo de producción lo referencia).
 */

import type { EntityId } from '~shared/ids'
import { asEntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import { parseMoney } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import { simTime } from '~shared/simtime'
import type { AccountId } from '~domain/auth'
import type { Quantity } from '~domain/quantity'
import { parseQuantity } from '~domain/quantity'
import type { Concession } from '~domain/cadastre'
import type { Building, InventoryItem, ProductionBatch } from '~domain/buildings'
import type {
  LinkSegment,
  NetworkLink,
  NetworkNode,
  Route,
  Terminal,
  TerminalSlot,
} from '~domain/logistics'
import type { Shipment, Vehicle, VehicleType } from '~domain/fleet'
import type {
  Acceptance,
  Contract,
  FreightContract,
  OhlcCandle,
  Publication,
} from '~domain/market'
import type { LedgerAccount, LedgerEntry } from '~domain/finance'
import type { BuildingType, City, Product, Recipe, Region, ResourceDeposit } from '~domain/world'

/** UUID determinista n-ésimo (v7 sintético) tipado por entidad. */
export function uid<Brand extends string>(n: number): EntityId<Brand> {
  return asEntityId<Brand>(`00000000-0000-7000-8000-${String(n).padStart(12, '0')}`)
}

export function mon(input: string): Money {
  const result = parseMoney(input)
  if (!result.ok) {
    throw new RangeError(`fixture mon("${input}") inválido`)
  }
  return result.value
}

export function qty(input: string): Quantity {
  const result = parseQuantity(input)
  if (!result.ok) {
    throw new RangeError(`fixture qty("${input}") inválido`)
  }
  return result.value
}

export function st(seconds: number): SimTime {
  return simTime(seconds)
}

export const MY_ACCOUNT: AccountId = uid<'Account'>(900)
export const OTHER_ACCOUNT: AccountId = uid<'Account'>(901)

export function region(over: Partial<Region> = {}): Region {
  return {
    id: uid(1),
    name: 'Askadia Norte',
    gridX: 0,
    gridY: 0,
    boundsM: null,
    biome: 'plains',
    taxRateBp: 500,
    customsRateBp: 200,
    canonBase: mon('1000'),
    openedAtSim: st(0),
    ...over,
  }
}

export function product(over: Partial<Product> = {}): Product {
  return {
    id: uid(10),
    code: 'IRON_ORE',
    name: 'Mineral de hierro',
    productClass: 'basic',
    unitVolume: 1,
    basePrice: mon('100'),
    priceFloor: mon('50'),
    priceCeiling: mon('200'),
    isFuel: false,
    ...over,
  }
}

export function buildingType(over: Partial<BuildingType> = {}): BuildingType {
  return {
    id: uid(20),
    code: 'MINE',
    name: 'Mina',
    footprintCells: 4,
    maxLevel: 5,
    baseStorage: qty('1000'),
    placementRules: null,
    levelCurve: null,
    buildCost: mon('50000'),
    maintenanceCost: mon('120'),
    ...over,
  }
}

export function recipe(over: Partial<Recipe> = {}): Recipe {
  return {
    id: uid(30),
    buildingTypeId: uid(20),
    code: 'MINE_IRON',
    name: 'Extraer hierro',
    batchSimSeconds: 3_600,
    fuelProductId: null,
    fuelPerBatch: qty('0'),
    workersRequired: 10,
    minCityLevel: 1,
    changeoverSeconds: 600,
    ingredients: [],
    ...over,
  }
}

export function deposit(over: Partial<ResourceDeposit> = {}): ResourceDeposit {
  return {
    id: uid(40),
    regionId: uid(1),
    productId: uid(10),
    locationM: [1_000, 2_000],
    initialAmount: qty('100000'),
    remainingAmount: qty('90000'),
    renewable: false,
    regenPerSimDay: qty('0'),
    ...over,
  }
}

export function city(over: Partial<City> = {}): City {
  return {
    id: uid(50),
    regionId: uid(1),
    accountId: uid(902),
    name: 'Puerto Askadia',
    locationM: [25_000, 25_000],
    level: 2,
    population: 12_000,
    supplyIndex: 0.7,
    influenceRadiusM: 5_000,
    baseSalary: mon('12'),
    ...over,
  }
}

export function concession(over: Partial<Concession> = {}): Concession {
  return {
    id: uid(60),
    regionId: uid(1),
    holderAccountId: MY_ACCOUNT,
    parcelM: [
      [
        [0, 0],
        [500, 0],
        [500, 500],
        [0, 500],
        [0, 0],
      ],
    ],
    canonAmount: mon('1000'),
    periodSimDays: 90,
    expiresAtSim: st(7_776_000),
    status: 'active',
    grantedAtSim: st(0),
    ...over,
  }
}

export function building(over: Partial<Building> = {}): Building {
  return {
    id: uid(70),
    ownerAccountId: MY_ACCOUNT,
    regionId: uid(1),
    concessionId: uid(60),
    buildingTypeId: uid(20),
    footprintM: [
      [
        [0, 0],
        [250, 0],
        [250, 250],
        [0, 250],
        [0, 0],
      ],
    ],
    level: 1,
    status: 'operational',
    activeRecipeId: null,
    conditionPct: 100,
    fuelStock: qty('0'),
    updatedAtSim: st(100),
    ...over,
  }
}

export function inventoryItem(over: Partial<InventoryItem> = {}): InventoryItem {
  return {
    buildingId: uid(70),
    productId: uid(10),
    quantity: qty('500'),
    updatedAtSim: st(100),
    ...over,
  }
}

export function productionBatch(over: Partial<ProductionBatch> = {}): ProductionBatch {
  return {
    id: uid(80),
    buildingId: uid(70),
    recipeId: uid(30),
    batchesQueued: 5,
    batchesDone: 1,
    status: 'running',
    queuePosition: 0,
    startedAtSim: st(1_000),
    progressPctObserved: 25,
    etaSim: st(4_600),
    ...over,
  }
}

export function segment(over: Partial<LinkSegment> = {}): LinkSegment {
  return {
    id: uid(110),
    regionId: uid(1),
    seq: 0,
    lengthM: 10_000,
    congestionEma: 1,
    updatedAtSim: st(100),
    ...over,
  }
}

export function node(over: Partial<NetworkNode> = {}): NetworkNode {
  return {
    id: uid(100),
    kind: 'warehouse',
    regionId: uid(1),
    buildingId: null,
    cityId: null,
    locationM: [1_000, 1_000],
    terminalId: null,
    ...over,
  }
}

export function terminal(over: Partial<Terminal> = {}): Terminal {
  return {
    id: uid(120),
    nodeId: uid(100),
    ownerAccountId: OTHER_ACCOUNT,
    transshipmentPerHour: 40,
    queueLength: 0,
    updatedAtSim: st(100),
    ...over,
  }
}

export function terminalSlot(over: Partial<TerminalSlot> = {}): TerminalSlot {
  return {
    id: uid(125),
    terminalId: uid(120),
    priorityTier: 1,
    price: mon('30000'),
    holderAccountId: null,
    validUntilSim: null,
    ...over,
  }
}

export function link(over: Partial<NetworkLink> = {}): NetworkLink {
  return {
    id: uid(120),
    mode: 'road',
    fromNodeId: uid(100),
    toNodeId: uid(101),
    pathM: [
      [0, 0],
      [10_000, 0],
    ],
    lengthM: 10_000,
    capacityPerHour: 100,
    baseSpeedKmh: 60,
    segments: [segment()],
    ...over,
  }
}

export function route(over: Partial<Route> = {}): Route {
  return {
    id: uid(130),
    ownerAccountId: MY_ACCOUNT,
    name: 'Ruta minera',
    kind: 'fixed_line',
    active: true,
    legs: [{ legIndex: 0, linkId: uid(120) }],
    ...over,
  }
}

export function vehicleType(over: Partial<VehicleType> = {}): VehicleType {
  return {
    id: uid(141),
    code: 'truck_s',
    name: 'Camión ligero',
    mode: 'road',
    cargoCapacity: qty('500'),
    speedKmh: 60,
    fuelProductId: uid(11),
    fuelPer100km: qty('10'),
    autonomyKm: 1_000,
    purchasePrice: mon('50000'),
    operatingCostPerDay: mon('100'),
    ...over,
  }
}

export function vehicle(over: Partial<Vehicle> = {}): Vehicle {
  return {
    id: uid(140),
    vehicleTypeId: uid(141),
    ownerAccountId: MY_ACCOUNT,
    status: 'idle',
    wearPct: 10,
    fuel: qty('100'),
    routeId: null,
    routeLegIndex: null,
    position: { kind: 'at-node', nodeId: uid(100), locationM: [1_000, 1_000] },
    repairUntilSim: null,
    observedAtSim: st(1_000),
    ...over,
  }
}

export function shipment(over: Partial<Shipment> = {}): Shipment {
  return {
    id: uid(150),
    ownerAccountId: MY_ACCOUNT,
    productId: uid(10),
    quantity: qty('200'),
    contractId: null,
    freightContractId: null,
    vehicleId: null,
    atNodeId: uid(100),
    status: 'in_warehouse',
    updatedAtSim: st(1_000),
    ...over,
  }
}

export function publication(over: Partial<Publication> = {}): Publication {
  return {
    id: uid(160),
    kind: 'sell',
    publisherAccountId: MY_ACCOUNT,
    channel: 'board',
    counterpartyAccountId: null,
    productId: uid(10),
    quantityTotal: qty('500'),
    quantityRemaining: qty('300'),
    unitPrice: mon('120'),
    minLot: qty('50'),
    originNodeId: uid(100),
    destinationNodeId: null,
    deliverySimSeconds: 172_800,
    status: 'open',
    windowClosesAtMs: null,
    cancelCooldownUntilMs: null,
    declaredValue: null,
    publishedAtSim: st(1_000),
    ...over,
  }
}

export function acceptance(over: Partial<Acceptance> = {}): Acceptance {
  return {
    id: uid(170),
    publicationId: uid(160),
    acceptorAccountId: MY_ACCOUNT,
    quantity: qty('100'),
    quantityServed: qty('0'),
    status: 'pending_draw',
    drawOrder: null,
    contractId: null,
    freightContractId: null,
    acceptedAtMs: 1_700_000_000_000,
    resolvedAtMs: null,
    ...over,
  }
}

export function contract(over: Partial<Contract> = {}): Contract {
  return {
    id: uid(180),
    publicationId: uid(160),
    channel: 'board',
    buyerAccountId: OTHER_ACCOUNT,
    sellerAccountId: MY_ACCOUNT,
    productId: uid(10),
    quantityAgreed: qty('100'),
    quantityDelivered: qty('0'),
    unitPrice: mon('120'),
    originNodeId: uid(100),
    destinationNodeId: uid(101),
    deadlineSim: st(200_000),
    status: 'active',
    fillBp: null,
    confirmedAtSim: st(2_000),
    settledAtSim: null,
    ...over,
  }
}

export function freightContract(over: Partial<FreightContract> = {}): FreightContract {
  return {
    id: uid(185),
    publicationId: uid(160),
    channel: 'board',
    shipperAccountId: MY_ACCOUNT,
    carrierAccountId: OTHER_ACCOUNT,
    originNodeId: uid(100),
    destinationNodeId: uid(101),
    freightPrice: mon('5000'),
    declaredValue: mon('60000'),
    deadlineSim: st(200_000),
    status: 'active',
    fillBp: null,
    escrowAccountId: uid(191),
    carrierGuaranteeAccountId: uid(192),
    custodyAccountId: uid(193),
    confirmedAtSim: st(2_000),
    settledAtSim: null,
    ...over,
  }
}

export function candle(over: Partial<OhlcCandle> = {}): OhlcCandle {
  return {
    productId: uid(10),
    regionId: uid(1),
    bucketStartSim: st(0),
    bucketSimSecs: 86_400,
    openPrice: mon('100'),
    highPrice: mon('130'),
    lowPrice: mon('90'),
    closePrice: mon('120'),
    volume: qty('1000'),
    contractCount: 4,
    ...over,
  }
}

export function ledgerAccount(over: Partial<LedgerAccount> = {}): LedgerAccount {
  return {
    id: uid(190),
    kind: 'cash',
    ownerAccountId: MY_ACCOUNT,
    productId: null,
    warehouseBuildingId: null,
    referenceId: null,
    balance: mon('50000'),
    updatedAtMs: null,
    createdAtMs: 1_700_000_000_000,
    ...over,
  }
}

export function ledgerEntry(over: Partial<LedgerEntry> = {}): LedgerEntry {
  return {
    id: uid(200),
    transactionId: uid(201),
    accountId: uid(190),
    amount: '-1000' as LedgerEntry['amount'],
    transactionKind: 'maintenance',
    referenceId: null,
    description: null,
    simTimeAt: st(1_000),
    createdAtMs: 1_700_000_000_000,
    ...over,
  }
}
