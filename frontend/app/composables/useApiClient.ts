/**
 * composables/useApiClient.ts — acceso REST tipado para la capa de UI.
 *
 * Superficie genérica `request()` sobre el contrato specs/openapi.yaml v1.1.0:
 * toda respuesta exitosa es `{ data, meta }` y todo error un ApiRequestError
 * tipado (P10 — errores tipados, nunca excepciones hacia los componentes).
 *
 * INTEGRADO con la capa de red: delega en el HttpClient compartido de
 * lib/api/client.ts, que el plugin 02.network.client provee como `$http`
 * (mismo token del session.store, mismo onMeta → SimClock, Idempotency-Key
 * automática en todos los POST de comando — P6/ADR-IMPL-09). Si el plugin no
 * está (SSR del portal, tests sin Nuxt), se crea perezosamente un fallback
 * equivalente sobre las stores; y `provideApiClient()` permite inyectar
 * dobles en tests de componentes.
 */
import { inject, provide, type InjectionKey } from 'vue'
import { createHttpClient, type ApiRequestError, type HttpClient } from '~/lib/api/client'
import type { DataEnvelope } from '~/lib/api/types'
import type { Result } from '~/lib/kernel/result'
import { useSessionStore } from '~/stores/session.store'
import { useSimStore } from '~/stores/sim.store'

/** Base del API; espeja runtimeConfig.public.apiBase (nuxt.config.ts). */
const API_BASE = '/api/v1'

export type HttpMethod = 'GET' | 'POST' | 'PATCH' | 'DELETE'

export interface RequestOptions {
  /** Query params; los undefined se omiten. */
  query?: Record<string, string | number | boolean | undefined>
  /** Cuerpo JSON (comandos POST/PATCH). */
  body?: unknown
  /** Cabecera Idempotency-Key explícita (por defecto se autogenera en POSTs de comando). */
  idempotencyKey?: string
}

export type ApiResult<T> = Result<DataEnvelope<T>, ApiRequestError>

export interface ApiClient {
  request<T>(method: HttpMethod, path: string, opts?: RequestOptions): Promise<ApiResult<T>>
}

export const API_CLIENT_KEY: InjectionKey<ApiClient> = Symbol('imperio.api-client')

/** Fallback sin plugin (SSR/tests): mismo HttpClient, cableado a las stores. */
let fallbackClient: HttpClient | null = null

function createFallbackClient(): HttpClient {
  return createHttpClient({
    baseURL: API_BASE,
    getToken: () => useSessionStore().token,
    onMeta: (meta) => {
      const sim = useSimStore()
      if (meta.sim_time_seconds !== undefined) sim.syncFromServer(meta.sim_time_seconds, sim.frozen)
      else sim.markContact()
    }
  })
}

/** Inyecta un cliente concreto (dobles en tests de componentes). */
export function provideApiClient(client: ApiClient): void {
  provide(API_CLIENT_KEY, client)
}

/** Cliente REST del árbol actual: inyectado > $http del plugin > fallback. */
export function useApiClient(): ApiClient {
  const injected = inject(API_CLIENT_KEY, null)
  if (injected !== null) return injected

  let shared: HttpClient | undefined
  try {
    shared = useNuxtApp().$http as HttpClient | undefined
  } catch {
    shared = undefined // fuera de contexto Nuxt (tests unitarios)
  }
  if (shared !== undefined) return shared

  if (fallbackClient === null) fallbackClient = createFallbackClient()
  return fallbackClient
}
