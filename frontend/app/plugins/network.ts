/**
 * app/plugins/network — composición de la capa de red (FAD §12, P3).
 *
 * Aquí —y solo aquí— se atan los puertos con sus adaptadores concretos.
 * Este plugin es el único lugar que conoce a la vez red, stores y SimClock;
 * ninguna de esas piezas se importa entre sí (inversión de dependencias):
 *
 * - transporte: adaptador de `$fetch.raw` de Nuxt sobre el puerto HttpTransport
 *   (resuelve 4xx/5xx sin lanzar; el mapeo a AppError es del cliente REST).
 * - tokenProvider: closure que lee el token EN MEMORIA de la session store
 *   (la capa network/ nunca importa stores).
 * - onMeta → SimClock.update(): cada respuesta re-ancla el reloj del mundo.
 * - onMaintenance → SimClock.freeze(): 503 MAINTENANCE_WINDOW = mundo pausado
 *   (FAD §12.9), no error genérico.
 * - AuthApi se inyecta en la session store vía `configure()` (puerto, no módulo).
 *
 * `$simClock` se resuelve de forma perezosa DENTRO de los callbacks: existe
 * solo en cliente (plugin .client) y así este plugin no depende del orden de
 * carga ni rompe el SSR del shell.
 */

import type { SimClock } from '~domain/simclock'
import { createAuthApi } from '~network/auth.api'
import { createFleetApi } from '~network/fleet.api'
import { createLedgerApi } from '~network/ledger.api'
import { createLogisticsApi } from '~network/logistics.api'
import { createMarketApi } from '~network/market.api'
import type { HttpTransport } from '~network/rest'
import { createRestClient } from '~network/rest'
import { createWorldApi } from '~network/world.api'
import { useSessionStore } from '../stores/session.store'

export default defineNuxtPlugin((nuxtApp) => {
  const { apiBase } = useRuntimeConfig().public

  const transport: HttpTransport = async (request) => {
    const response = await $fetch.raw(request.url, {
      method: request.method,
      headers: request.headers as Record<string, string>,
      ...(request.body !== undefined ? { body: request.body as Record<string, unknown> } : {}),
      // 4xx/5xx no lanzan: el cliente REST los mapea a AppError tipado.
      ignoreResponseError: true,
    })
    return {
      status: response.status,
      getHeader: (name: string) => response.headers.get(name),
      body: response._data as unknown,
    }
  }

  const resolveSimClock = (): SimClock | null => (nuxtApp.$simClock as SimClock | undefined) ?? null

  const session = useSessionStore()

  const restClient = createRestClient({
    baseUrl: apiBase,
    transport,
    tokenProvider: () => session.token,
    onMeta: (meta) => {
      if (meta.simTimeSeconds !== null) {
        resolveSimClock()?.update({ simTimeSeconds: meta.simTimeSeconds })
      }
    },
    onMaintenance: () => {
      resolveSimClock()?.freeze()
    },
  })

  const authApi = createAuthApi(restClient)
  session.configure(authApi)

  return {
    provide: {
      restClient,
      authApi,
      // Puertos REST del juego (Incremento 5): mismos patrones que authApi.
      // La UI los consume vía useGameApis() — nunca importa network/ directo.
      worldApi: createWorldApi(restClient),
      marketApi: createMarketApi(restClient),
      fleetApi: createFleetApi(restClient),
      logisticsApi: createLogisticsApi(restClient),
      ledgerApi: createLedgerApi(restClient),
    },
  }
})
