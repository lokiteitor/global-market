/**
 * network/rest/client — cliente REST tipado del contrato (FAD §12.8, §13.7, O5).
 *
 * Responsabilidades:
 * - Construir la petición (base + path + query) y despacharla por el puerto
 *   `HttpTransport` (en producción, `$fetch.raw` de Nuxt vía el plugin de red).
 * - Inyectar `Authorization: Bearer` desde el token provider inyectado y una
 *   `Idempotency-Key` (UUIDv7 del kernel) en TODO POST/PUT/PATCH/DELETE (P6);
 *   el reintento de un comando debe reusar la misma clave (`idempotencyKey`).
 * - Hacer unwrap del envelope `{data, meta}` y NOTIFICAR la meta por el
 *   callback `onMeta` (inversión de dependencias: esta capa jamás importa
 *   stores ni el SimClock — el plugin de la app conecta ambos).
 * - Mapear todo fallo a `AppError` tipado; 503 `MAINTENANCE_WINDOW` además
 *   dispara `onMaintenance` (estado frozen del mundo, FAD §12.9), porque el
 *   mantenimiento es un estado de la app, no un error genérico.
 */

import { uuidv7 } from '~shared/ids'
import type { Enveloped, ResponseMeta } from './envelope'
import { unwrapEnvelope } from './envelope'
import { appErrorFromHttp, appErrorFromTransport } from './errors'
import type { HttpMethod, HttpReply, HttpTransport } from './http'
import { isMutation } from './http'

/** Valores admitidos en query (undefined = parámetro omitido). */
export type QueryValue = string | number | boolean | undefined

export interface RequestSpec {
  readonly method: HttpMethod
  /** Ruta del contrato relativa a la base, p. ej. `/auth/sessions`. */
  readonly path: string
  readonly query?: Readonly<Record<string, QueryValue>>
  /** Cuerpo JSON (DTO de request del contrato). */
  readonly body?: unknown
  /**
   * Clave de idempotencia a reutilizar en un REINTENTO del mismo comando
   * (FAD §12.8). Si se omite, se genera un UUIDv7 nuevo por petición mutante.
   */
  readonly idempotencyKey?: string
}

export interface RestClient {
  /** Petición con envelope `{data, meta}`: 2xx con cuerpo. */
  request<TData>(spec: RequestSpec): Promise<Enveloped<TData>>
  /** Petición sin cuerpo de respuesta (204, p. ej. DELETE /auth/sessions/current). */
  requestVoid(spec: RequestSpec): Promise<void>
}

/** Provee el token de sesión vigente; `null` si no hay sesión (FAD §24.2). */
export type TokenProvider = () => string | null

export interface RestClientOptions {
  /** Prefijo del contrato, p. ej. `/api/v1` (runtimeConfig.public.apiBase). */
  readonly baseUrl: string
  readonly transport: HttpTransport
  readonly tokenProvider: TokenProvider
  /** Notificación de meta de CADA respuesta exitosa (alimenta el SimClock). */
  readonly onMeta?: (meta: ResponseMeta) => void
  /** Ventana de mantenimiento detectada (503 MAINTENANCE_WINDOW): estado frozen. */
  readonly onMaintenance?: (retryAfterSeconds: number | null) => void
  /** Generador de Idempotency-Key inyectable (tests); default uuidv7 del kernel. */
  readonly generateIdempotencyKey?: () => string
}

function buildUrl(baseUrl: string, path: string, query?: Readonly<Record<string, QueryValue>>) {
  const params = new URLSearchParams()
  if (query !== undefined) {
    for (const [key, value] of Object.entries(query)) {
      if (value !== undefined) {
        params.set(key, String(value))
      }
    }
  }
  const queryString = params.size > 0 ? `?${params.toString()}` : ''
  return `${baseUrl}${path}${queryString}`
}

export function createRestClient(options: RestClientOptions): RestClient {
  const generateKey = options.generateIdempotencyKey ?? uuidv7

  async function perform(spec: RequestSpec): Promise<HttpReply> {
    const headers: Record<string, string> = { accept: 'application/json' }

    const token = options.tokenProvider()
    if (token !== null) {
      headers['authorization'] = `Bearer ${token}`
    }
    if (isMutation(spec.method)) {
      headers['idempotency-key'] = spec.idempotencyKey ?? generateKey()
    }
    if (spec.body !== undefined) {
      headers['content-type'] = 'application/json'
    }

    let reply: HttpReply
    try {
      reply = await options.transport({
        method: spec.method,
        url: buildUrl(options.baseUrl, spec.path, spec.query),
        headers,
        ...(spec.body !== undefined ? { body: spec.body } : {}),
      })
    } catch (cause) {
      throw appErrorFromTransport(cause)
    }

    if (reply.status >= 400) {
      const error = appErrorFromHttp(reply.status, reply.body, reply.getHeader)
      if (error.isMaintenance) {
        // El mantenimiento no es un error genérico: además de propagarse
        // tipado (isMaintenance), congela el mundo vía callback (FAD §12.9).
        options.onMaintenance?.(error.retryAfterSeconds)
      }
      throw error
    }
    return reply
  }

  return {
    async request<TData>(spec: RequestSpec): Promise<Enveloped<TData>> {
      const reply = await perform(spec)
      const enveloped = unwrapEnvelope<TData>(reply.body, reply.status)
      options.onMeta?.(enveloped.meta)
      return enveloped
    },

    async requestVoid(spec: RequestSpec): Promise<void> {
      await perform(spec)
      // 204 sin cuerpo: no hay meta que notificar.
    },
  }
}
