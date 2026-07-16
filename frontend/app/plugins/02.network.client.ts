/**
 * plugins/02.network.client.ts — composición de la capa de red (client-only).
 *
 * Instancia y cablea, por inyección de dependencias (P3), los tres servicios
 * de red y los expone vía provide/inject de Nuxt ($api, $transport, $simClock,
 * $sync):
 *
 *   - SimClock (estado en sim.store; única conversión sim↔wall);
 *   - RestApi (fetch contra runtimeConfig.public.apiBase; token del
 *     session.store leído por callback — sin import circular; el meta de cada
 *     respuesta alimenta el SimClock);
 *   - GatewayTransportAdapter (WebSocket real; client-only por naturaleza);
 *   - pipeline de sync (frames → stores dueñas + efectos).
 *
 * Es un plugin `.client`: el estado de dominio es EN VIVO y no se hidrata
 * desde SSR (FAD §6.2); los composables de red solo operan en cliente.
 */
import { createHttpClient, createRestApi } from '~/lib/api/client'
import type { Contract, PatchOp } from '~/lib/api/types'
import { createGatewayTransport } from '~/lib/net/gateway-transport'
import { createSimClock } from '~/lib/net/simclock'
import { createSyncPipeline } from '~/lib/net/sync'
import type { TransportState } from '~/lib/net/transport'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useCitiesStore } from '~/stores/cities.store'
import { useFinanceStore } from '~/stores/finance.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useMarketStore } from '~/stores/market.store'
import { useNotificationsStore } from '~/stores/notifications.store'
import { useSessionStore } from '~/stores/session.store'
import { useShipmentsStore } from '~/stores/shipments.store'
import { useSimStore } from '~/stores/sim.store'
import type { ConnectionState } from '~/stores/sim.store'

/** El transporte distingue fases internas; la UI colapsa a los estados de sim.store. */
function toConnectionState(state: TransportState): ConnectionState {
  switch (state) {
    case 'connecting':
    case 'authenticating':
      return 'connecting'
    case 'open':
      return 'open'
    case 'reconnecting':
      return 'reconnecting'
    case 'idle':
    case 'closed':
      return 'closed'
  }
}

export default defineNuxtPlugin(() => {
  const config = useRuntimeConfig()

  const sessionStore = useSessionStore()
  const simStore = useSimStore()
  const notificationsStore = useNotificationsStore()
  const buildingsStore = useBuildingsStore()
  const fleetStore = useFleetStore()
  const shipmentsStore = useShipmentsStore()
  const marketStore = useMarketStore()
  const financeStore = useFinanceStore()
  const citiesStore = useCitiesStore()

  // SIMPLIFICACIÓN v1 (aceptada): sesión respaldada en sessionStorage (dev);
  // el session.store es el dueño de la persistencia y la restaura aquí, en el
  // arranque del cliente, antes de crear los servicios que leen el token.
  sessionStore.restore()

  const simClock = createSimClock(simStore)

  const http = createHttpClient({
    baseURL: config.public.apiBase,
    getToken: () => sessionStore.token,
    // meta.sim_time_seconds de CADA respuesta REST re-sincroniza el SimClock.
    onMeta: (meta) => {
      if (meta.sim_time_seconds !== undefined) simClock.sync(meta.sim_time_seconds)
      else simStore.markContact()
    }
  })
  const api = createRestApi(http)

  const transport = createGatewayTransport({
    // La URL se deriva al conectar (el edge sirve /ws en el mismo host, C16).
    url: () => {
      // NUXT_PUBLIC_WS_URL permite apuntar directo al gateway en dev (:3000, sin edge).
      if (config.public.wsUrl) return config.public.wsUrl
      const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
      return `${proto}://${window.location.host}${config.public.wsPath}`
    },
    getToken: () => sessionStore.token,
    onSimSync: (simSeconds, frozen) => simClock.sync(simSeconds, frozen),
    onProtocolError: (code, message) =>
      notificationsStore.push({ level: 'error', text: `Gateway: ${message}`, event: code })
  })

  transport.onStateChange((state) => simStore.setConnectionState(toConnectionState(state)))

  const sync = createSyncPipeline(transport, {
    corp: {
      buildings: buildingsStore,
      fleet: fleetStore,
      shipments: shipmentsStore,
      market: marketStore,
      finance: financeStore
    },
    viewport: {
      cities: citiesStore,
      buildings: buildingsStore,
      fleet: fleetStore
    },
    notifications: notificationsStore,
    simClock,
    effects: {
      // acceptance.resolved: como ACEPTANTE el contrato nuevo no llega por la
      // room corp: (el patch va a las partes tras el sorteo, pero el refresco
      // pull garantiza convergencia aunque el message gane la carrera).
      // Se refresca por REST y se aplica como upserts idempotentes (P6).
      onAcceptanceResolved: () => {
        void api.listContracts({ status: 'active' }).then((result) => {
          if (!result.ok) return
          const ops: PatchOp[] = result.value.data.map((contract: Contract) => ({
            op: 'upsert',
            entity: 'contract',
            id: contract.id,
            data: contract
          }))
          marketStore.applyPatch(ops)
        })
      }
    }
  })

  return {
    provide: {
      /** Cliente HTTP genérico { data, meta } (lo consume useApiClient). */
      http,
      /** Métodos tipados 1:1 del contrato REST. */
      api,
      transport,
      simClock,
      sync
    }
  }
})
