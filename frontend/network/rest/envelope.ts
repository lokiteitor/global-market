/**
 * network/rest/envelope — envoltura `{data, meta}` del contrato y su mapper.
 *
 * Toda respuesta exitosa del backend llega como `{data, meta}` (openapi.yaml,
 * schema Meta). El DTO crudo de `meta` NO sale de network/ (O5): se mapea a
 * `ResponseMeta`, que usa el tipo branded `SimTime` del kernel. Es la forma
 * que consume el SimClock vía el callback `onMeta` del cliente REST.
 */

import type { SimTime } from '~shared/simtime'
import { simTime } from '~shared/simtime'
import type { components } from '../../types/api'
import { appErrorFromProtocol } from './errors'

type MetaDto = components['schemas']['Meta']

/** Meta de respuesta mapeada a dominio (lo único de la envoltura que sale de network/). */
export interface ResponseMeta {
  /**
   * Sim-time del mundo al responder (forma canónica para clientes). `null` si
   * `sim_time_seconds` falta o no es un entero válido — el SimClock no se
   * re-ancla con datos dudosos.
   */
  readonly simTimeSeconds: SimTime | null
  /** Sim-time legible `AÑO-DÍA-HH:MM` tal como lo formatea el servidor. */
  readonly simTimeLabel: string
  /** Wall-clock del servidor en ms de epoch (solo informativo); `null` si no parsea. */
  readonly serverTimeMs: number | null
  /** Cursor de paginación; `null` si no hay más resultados. */
  readonly nextCursor: string | null
}

/** Envoltura de éxito ya mapeada: lo que devuelve `RestClient.request`. */
export interface Enveloped<TData> {
  readonly data: TData
  readonly meta: ResponseMeta
}

function mapMeta(dto: MetaDto): ResponseMeta {
  const seconds = dto.sim_time_seconds
  const serverTimeMs = Date.parse(dto.server_time)
  return {
    simTimeSeconds:
      seconds !== undefined && Number.isSafeInteger(seconds) && seconds >= 0
        ? simTime(seconds)
        : null,
    simTimeLabel: dto.sim_time,
    serverTimeMs: Number.isNaN(serverTimeMs) ? null : serverTimeMs,
    nextCursor: dto.next_cursor ?? null,
  }
}

/**
 * Valida la forma del envelope de éxito y lo mapea. Un 2xx sin `{data, meta}`
 * bien formado es una violación de contrato → AppError `protocol`.
 */
export function unwrapEnvelope<TData>(body: unknown, status: number): Enveloped<TData> {
  if (typeof body !== 'object' || body === null) {
    throw appErrorFromProtocol('la respuesta exitosa no es un objeto JSON', status)
  }
  const raw = body as Record<string, unknown>
  if (!('data' in raw)) {
    throw appErrorFromProtocol('falta el campo "data" del envelope', status)
  }
  const meta = raw['meta']
  if (typeof meta !== 'object' || meta === null) {
    throw appErrorFromProtocol('falta el campo "meta" del envelope', status)
  }
  const metaDto = meta as Record<string, unknown>
  if (typeof metaDto['sim_time'] !== 'string' || typeof metaDto['server_time'] !== 'string') {
    throw appErrorFromProtocol('meta sin sim_time/server_time requeridos', status)
  }
  return {
    data: raw['data'] as TData,
    meta: mapMeta(meta as MetaDto),
  }
}
