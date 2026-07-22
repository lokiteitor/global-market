/**
 * tests/nuxt/game-fakes — dobles compartidos por los specs de la fase UI de
 * /play: puertos REST del juego (useGameApis lee useNuxtApp()) y el reloj
 * reactivo del plugin sim-clock. SOLO se importa desde *.spec.ts.
 */

import { ref } from 'vue'
import type { Ref } from 'vue'
import { vi } from 'vitest'
import type { SimTime } from '~shared/simtime'
import { simTime } from '~shared/simtime'
import type { FleetApi } from '~network/fleet.api'
import type { LedgerApi } from '~network/ledger.api'
import type { LogisticsApi } from '~network/logistics.api'
import type { MarketApi } from '~network/market.api'
import type { WorldApi } from '~network/world.api'

/** Puerto con todos los métodos como vi.fn que rechazan si se llaman sin doble. */
function unimplemented(name: string) {
  return vi.fn(() => Promise.reject(new Error(`fake api: ${name} sin doblar`)))
}

export interface FakeApis {
  readonly world: WorldApi
  readonly market: MarketApi
  readonly fleet: FleetApi
  readonly logistics: LogisticsApi
  readonly ledger: LedgerApi
}

export function fakeApis(): FakeApis {
  const make = <T extends object>(methods: readonly string[]): T =>
    Object.fromEntries(methods.map((method) => [method, unimplemented(method)])) as T

  return {
    world: make<WorldApi>([
      'listRegions',
      'listProducts',
      'listBuildingTypes',
      'listRecipes',
      'listResourceDeposits',
      'listCities',
      'getCityDemand',
      'listConcessions',
      'createConcession',
      'getConcession',
      'renewConcession',
      'createConcessionTransfer',
      'listBuildings',
      'createBuilding',
      'getBuilding',
      'updateBuilding',
      'upgradeBuilding',
      'getBuildingInventory',
      'listProductionBatches',
      'queueProductionBatches',
      'cancelProductionBatch',
    ]),
    market: make<MarketApi>([
      'queryBoard',
      'createPublication',
      'getPublication',
      'cancelPublication',
      'acceptPublication',
      'getAcceptance',
      'listContracts',
      'getContract',
      'listContractDeliveries',
      'listFreightContracts',
      'getFreightContract',
      'getMarketOhlc',
    ]),
    fleet: make<FleetApi>([
      'listVehicleTypes',
      'listVehicles',
      'purchaseVehicle',
      'getVehicle',
      'updateVehicle',
      'listShipments',
      'getShipment',
      'dispatchShipment',
      'repositionVehicle',
      'getTerminal',
      'listTerminalSlots',
      'purchaseTerminalSlot',
    ]),
    logistics: make<LogisticsApi>([
      'listNetworkNodes',
      'listNetworkLinks',
      'planRoute',
      'listRoutes',
      'createRoute',
      'getRoute',
      'updateRoute',
      'deleteRoute',
    ]),
    ledger: make<LedgerApi>(['listLedgerAccounts', 'listLedgerEntries']),
  }
}

export interface StubbedNuxtApp {
  readonly apis: FakeApis
  readonly simNow: Ref<SimTime | null>
  readonly simFrozen: Ref<boolean>
}

/** Dobla el global `useNuxtApp` con los puertos y el reloj del plugin. */
export function stubNuxtApp(simNowSeconds: number | null = 1_000): StubbedNuxtApp {
  const apis = fakeApis()
  const simNow = ref<SimTime | null>(simNowSeconds === null ? null : simTime(simNowSeconds))
  const simFrozen = ref(false)
  vi.stubGlobal('useNuxtApp', () => ({
    $simNow: simNow,
    $simFrozen: simFrozen,
    $simClock: {
      now: () => simNow.value,
      isFrozen: () => simFrozen.value,
    },
    $worldApi: apis.world,
    $marketApi: apis.market,
    $fleetApi: apis.fleet,
    $logisticsApi: apis.logistics,
    $ledgerApi: apis.ledger,
  }))
  return { apis, simNow, simFrozen }
}
