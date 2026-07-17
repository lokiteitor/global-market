/**
 * network/rest/http — puerto de transporte HTTP (P3: fronteras = puertos).
 *
 * El cliente REST no conoce `$fetch` ni `fetch`: habla contra esta interfaz
 * mínima. La app inyecta el adaptador real sobre `$fetch.raw` de Nuxt
 * (app/plugins/network.ts) y los tests inyectan un doble en memoria, sin
 * mockear módulos globales.
 *
 * Contrato del transporte:
 * - Resuelve con la respuesta HTTP SEA CUAL SEA el status (4xx/5xx incluidos):
 *   el mapeo de errores es responsabilidad del cliente REST, no del transporte.
 * - Rechaza SOLO ante fallo de red sin respuesta (offline, DNS, abort).
 * - `body` llega ya parseado como JSON (o `undefined` si no hay cuerpo, p. ej. 204).
 */

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

/** Métodos con efectos: llevan Idempotency-Key (FAD §12.8, P6). */
export function isMutation(method: HttpMethod): boolean {
  return method === 'POST' || method === 'PUT' || method === 'PATCH' || method === 'DELETE'
}

export interface HttpRequest {
  readonly method: HttpMethod
  /** URL ya construida (base + path + query). */
  readonly url: string
  readonly headers: Readonly<Record<string, string>>
  /** Cuerpo JSON-serializable; `undefined` si la petición no lleva cuerpo. */
  readonly body?: unknown
}

export interface HttpReply {
  readonly status: number
  /** Lectura de cabeceras de respuesta, case-insensitive (Retry-After…). */
  readonly getHeader: (name: string) => string | null
  /** Cuerpo JSON parseado; `undefined` si no hay cuerpo (204). */
  readonly body: unknown
}

export type HttpTransport = (request: HttpRequest) => Promise<HttpReply>
