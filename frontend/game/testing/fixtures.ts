/**
 * game/testing/fixtures — constructores de entidades de dominio para los specs
 * de game/ (bridge, input). SOLO se importa desde *.spec.ts. game/ no puede
 * importar app/ (frontera ESLint), así que no reutiliza las fixtures de
 * app/stores/testing: duplica el subconjunto que el mundo vivo necesita.
 */

import type { EntityId } from '~shared/ids'
import { asEntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import { parseMoney } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import { simTime } from '~shared/simtime'
import type { AccountId } from '~domain/auth'
import type { Building } from '~domain/buildings'
import type { Vehicle } from '~domain/fleet'
import type { LinkSegment, NetworkLink, NetworkNode } from '~domain/logistics'
import type { Quantity } from '~domain/quantity'
import { parseQuantity } from '~domain/quantity'
import type { City, Region, ResourceDeposit } from '~domain/world'

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
    boundsM: [
      [
        [0, 0],
        [10_000, 0],
        [10_000, 10_000],
        [0, 10_000],
        [0, 0],
      ],
    ],
    biome: 'plains',
    taxRateBp: 500,
    customsRateBp: 200,
    canonBase: mon('1000'),
    openedAtSim: st(0),
    ...over,
  }
}

export function city(over: Partial<City> = {}): City {
  return {
    id: uid(50),
    regionId: uid(1),
    accountId: uid(902),
    name: 'Puerto Askadia',
    locationM: [5_000, 5_000],
    level: 2,
    population: 12_000,
    supplyIndex: 0.7,
    influenceRadiusM: 5_000,
    baseSalary: mon('12'),
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

export function building(over: Partial<Building> = {}): Building {
  return {
    id: uid(70),
    ownerAccountId: MY_ACCOUNT,
    regionId: uid(1),
    concessionId: uid(60),
    buildingTypeId: uid(20),
    footprintM: [
      [
        [1_000, 1_000],
        [1_250, 1_000],
        [1_250, 1_250],
        [1_000, 1_250],
        [1_000, 1_000],
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
