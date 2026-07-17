/**
 * network/rest — superficie pública del cliente REST (FAD §12).
 *
 * Lo exportado aquí es lo ÚNICO de esta capa que puede consumir la app:
 * el puerto RestClient, su factoría, la meta mapeada y el error tipado.
 * Los DTO crudos del contrato no se re-exportan (O5).
 */

export type { Enveloped, ResponseMeta } from './envelope'
export type { HttpMethod, HttpReply, HttpRequest, HttpTransport } from './http'
export type {
  QueryValue,
  RequestSpec,
  RestClient,
  RestClientOptions,
  TokenProvider,
} from './client'
export { createRestClient } from './client'
export type { ApiErrorCode, AppErrorKind } from './errors'
export { API_ERROR_CODES, AppError, isApiErrorCode } from './errors'
