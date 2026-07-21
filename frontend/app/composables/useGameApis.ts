/**
 * app/composables/useGameApis — acceso tipado a los puertos REST del juego.
 *
 * El plugin de red (app/plugins/network) crea e inyecta los puertos; este
 * composable es la única puerta de la UI hacia ellos (los componentes jamás
 * importan factorías de network/ ni tocan el RestClient). En tests, se dobla
 * `useNuxtApp` con fakes de los puertos (mismo patrón que $simNow).
 */

import type { FleetApi } from '~network/fleet.api'
import type { LedgerApi } from '~network/ledger.api'
import type { LogisticsApi } from '~network/logistics.api'
import type { MarketApi } from '~network/market.api'
import type { WorldApi } from '~network/world.api'

export interface GameApis {
  readonly world: WorldApi
  readonly market: MarketApi
  readonly fleet: FleetApi
  readonly logistics: LogisticsApi
  readonly ledger: LedgerApi
}

interface ProvidedApis {
  readonly $worldApi?: WorldApi
  readonly $marketApi?: MarketApi
  readonly $fleetApi?: FleetApi
  readonly $logisticsApi?: LogisticsApi
  readonly $ledgerApi?: LedgerApi
}

export function useGameApis(): GameApis {
  const app = useNuxtApp() as unknown as ProvidedApis
  const { $worldApi, $marketApi, $fleetApi, $logisticsApi, $ledgerApi } = app
  if (
    $worldApi === undefined ||
    $marketApi === undefined ||
    $fleetApi === undefined ||
    $logisticsApi === undefined ||
    $ledgerApi === undefined
  ) {
    throw new Error(
      'useGameApis: puertos REST no inyectados — el plugin de red debe registrarlos antes',
    )
  }
  return {
    world: $worldApi,
    market: $marketApi,
    fleet: $fleetApi,
    logistics: $logisticsApi,
    ledger: $ledgerApi,
  }
}
