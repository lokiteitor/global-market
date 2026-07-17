/**
 * network/rest/errors — taxonomía de errores del contrato → AppError (FAD §13.7).
 *
 * El backend responde `{error: {code, message, details}}` con códigos estables
 * documentados (docs/api/openapi.yaml, schema Error). Esta capa los mapea a una
 * única clase `AppError` tipada que es lo ÚNICO que sale de network/ ante un
 * fallo: ni el body crudo ni las excepciones del transporte cruzan la frontera.
 *
 * Tres orígenes (`kind`):
 * - `http`      → el servidor respondió >= 400 (con o sin envelope de error).
 * - `network`   → el transporte falló sin respuesta HTTP (offline, DNS, abort).
 * - `protocol`  → el servidor respondió 2xx pero el payload viola el contrato
 *                 (envelope malformado, UUID inválido…): bug de servidor o de
 *                 versión de contrato, nunca culpa del jugador.
 *
 * 503 `MAINTENANCE_WINDOW` NO es un error genérico (FAD §12.9): se expone como
 * `isMaintenance` y el cliente REST lo notifica por el callback `onMaintenance`
 * para que el SimClock entre en estado `frozen`.
 */

/** Códigos estables documentados por el contrato (schema Error.code). */
export const API_ERROR_CODES = [
  'INSUFFICIENT_COLLATERAL',
  'INSUFFICIENT_FUNDS',
  'PUBLICATION_EXHAUSTED',
  'CANCEL_COOLDOWN_ACTIVE',
  'BELOW_MIN_LOT',
  'STOCK_ALREADY_RESERVED',
  'PLACEMENT_INVALID',
  'NOT_RESOURCE_OWNER',
  'VEHICLE_SEALED',
  'NO_ROUTE_FOUND',
  'MAINTENANCE_WINDOW',
  'RATE_LIMITED',
  'VALIDATION_ERROR',
  'NOT_FOUND',
  'UNAUTHORIZED',
  'INTERNAL',
] as const

export type ApiErrorCode = (typeof API_ERROR_CODES)[number]

export type AppErrorKind = 'http' | 'network' | 'protocol'

export function isApiErrorCode(value: string): value is ApiErrorCode {
  return (API_ERROR_CODES as readonly string[]).includes(value)
}

interface AppErrorInit {
  readonly kind: AppErrorKind
  readonly code: ApiErrorCode
  readonly message: string
  /** Status HTTP; 0 cuando no hubo respuesta (kind network/protocol). */
  readonly status: number
  /** Segundos de la cabecera `Retry-After` (429/503) si existe. */
  readonly retryAfterSeconds?: number | null
  /** `error.details` del envelope (importes como strings de punto fijo). */
  readonly details?: Readonly<Record<string, unknown>> | null
  /** `error.code` tal como llegó del servidor (diagnóstico), catalogado o no. */
  readonly rawCode?: string | null
  readonly cause?: unknown
}

/** Error tipado de la capa de red: la única forma de fallo que sale de network/. */
export class AppError extends Error {
  readonly kind: AppErrorKind
  readonly code: ApiErrorCode
  readonly status: number
  readonly retryAfterSeconds: number | null
  readonly details: Readonly<Record<string, unknown>> | null
  readonly rawCode: string | null

  constructor(init: AppErrorInit) {
    super(init.message, init.cause === undefined ? undefined : { cause: init.cause })
    this.name = 'AppError'
    this.kind = init.kind
    this.code = init.code
    this.status = init.status
    this.retryAfterSeconds = init.retryAfterSeconds ?? null
    this.details = init.details ?? null
    this.rawCode = init.rawCode ?? null
  }

  /** Ventana de mantenimiento: estado `frozen` de la app, no error genérico (FAD §12.9). */
  get isMaintenance(): boolean {
    return this.code === 'MAINTENANCE_WINDOW'
  }
}

/**
 * Código por defecto cuando el servidor no envía `error.code` catalogado.
 * Cobertura por status del contrato: 400/422 forma-dominio, 401 sesión,
 * 403 propiedad (C13: toda denegación 403 es de propiedad/estado del recurso),
 * 404 UUID no resuelto, 429 rate limit, 503 mantenimiento; resto INTERNAL.
 */
export function errorCodeFromStatus(status: number): ApiErrorCode {
  switch (status) {
    case 400:
    case 409:
    case 422:
      return 'VALIDATION_ERROR'
    case 401:
      return 'UNAUTHORIZED'
    case 403:
      return 'NOT_RESOURCE_OWNER'
    case 404:
      return 'NOT_FOUND'
    case 429:
      return 'RATE_LIMITED'
    case 503:
      return 'MAINTENANCE_WINDOW'
    default:
      return 'INTERNAL'
  }
}

/** Forma runtime del envelope de error del contrato (validación defensiva). */
interface RawErrorEnvelope {
  readonly code: string | null
  readonly message: string | null
  readonly details: Readonly<Record<string, unknown>> | null
}

function readErrorEnvelope(body: unknown): RawErrorEnvelope | null {
  if (typeof body !== 'object' || body === null) {
    return null
  }
  const error = (body as Record<string, unknown>)['error']
  if (typeof error !== 'object' || error === null) {
    return null
  }
  const raw = error as Record<string, unknown>
  const details = raw['details']
  return {
    code: typeof raw['code'] === 'string' ? raw['code'] : null,
    message: typeof raw['message'] === 'string' ? raw['message'] : null,
    details:
      typeof details === 'object' && details !== null ? (details as Record<string, unknown>) : null,
  }
}

/**
 * Retry-After efectivo: cabecera (entero en segundos, forma del contrato) y,
 * como respaldo, `details.retry_after_seconds` del ejemplo de mantenimiento.
 */
function readRetryAfterSeconds(
  header: string | null,
  details: Readonly<Record<string, unknown>> | null,
): number | null {
  if (header !== null) {
    const parsed = Number.parseInt(header, 10)
    if (Number.isSafeInteger(parsed) && parsed >= 0) {
      return parsed
    }
  }
  const fromDetails = details?.['retry_after_seconds']
  if (typeof fromDetails === 'number' && Number.isSafeInteger(fromDetails) && fromDetails >= 0) {
    return fromDetails
  }
  return null
}

/**
 * Mapea una respuesta HTTP >= 400 a AppError. Códigos catalogados pasan tal
 * cual; códigos desconocidos o envelope ausente caen al código derivado del
 * status (el `rawCode` original se conserva para diagnóstico).
 */
export function appErrorFromHttp(
  status: number,
  body: unknown,
  getHeader: (name: string) => string | null,
): AppError {
  const envelope = readErrorEnvelope(body)
  const rawCode = envelope?.code ?? null
  const code = rawCode !== null && isApiErrorCode(rawCode) ? rawCode : errorCodeFromStatus(status)
  return new AppError({
    kind: 'http',
    code,
    status,
    message: envelope?.message ?? `HTTP ${String(status)}`,
    retryAfterSeconds: readRetryAfterSeconds(getHeader('retry-after'), envelope?.details ?? null),
    details: envelope?.details ?? null,
    rawCode,
  })
}

/** Fallo de transporte sin respuesta HTTP (offline, DNS, socket abortado). */
export function appErrorFromTransport(cause: unknown): AppError {
  const message = cause instanceof Error ? cause.message : String(cause)
  return new AppError({
    kind: 'network',
    code: 'INTERNAL',
    status: 0,
    message: `Fallo de red sin respuesta del servidor: ${message}`,
    cause,
  })
}

/** Respuesta 2xx cuyo payload viola el contrato (envelope/DTO malformado). */
export function appErrorFromProtocol(description: string, status: number = 0): AppError {
  return new AppError({
    kind: 'protocol',
    code: 'INTERNAL',
    status,
    message: `Respuesta fuera de contrato: ${description}`,
  })
}
