/**
 * network/market.api — puerto MarketApi contra /contracts/* y /market/* (FAD §12.8).
 *
 * Tablón CCRI global (pull con filtros — nunca hay push del tablón mundial,
 * ADR-023), publicaciones con garantía bloqueada al publicar, aceptaciones en
 * ventana de sorteo (la latencia no otorga ventaja), contratos propios con sus
 * entregas acumulativas y velas OHLC de contratos liquidados.
 *
 * Patrón de network/auth.api: puerto + factoría sobre `RestClient` (unwrap del
 * envelope, `AppError`, Idempotency-Key automática en mutaciones, P6). Tipos
 * ÍNTEGROS del contrato generado; precios/cantidades son strings de punto fijo
 * (`MoneyAmount`/`StockQty`) y se manipulan SOLO con `~shared/money` (C11).
 * ACL DTO→dominio pendiente de sus stores (FAD §9.5, decisión consciente).
 */

import type { components, operations } from '../types/api'
import type { Page } from './mappers/page.mapper'
import { requestPage } from './mappers/page.mapper'
import type { RestClient } from './rest'

type Schemas = components['schemas']

export type PublicationDto = Schemas['Publication']
export type PublicationCreateDto = Schemas['PublicationCreate']
export type AcceptanceDto = Schemas['Acceptance']
export type AcceptanceCreateDto = Schemas['AcceptanceCreate']
export type ContractDto = Schemas['Contract']
export type ContractDeliveryDto = Schemas['ContractDelivery']
export type OhlcCandleDto = Schemas['OhlcCandle']

// ——— Filtros de query, derivados de `operations` (nunca a mano) ———
export type BoardQuery = NonNullable<operations['queryBoard']['parameters']['query']>
export type ContractListQuery = NonNullable<operations['listContracts']['parameters']['query']>
/** Query de OHLC: `product_id` es REQUERIDO por contrato. */
export type OhlcQuery = operations['getMarketOhlc']['parameters']['query']

/** Puerto del mercado CCRI: tablón, publicaciones, aceptaciones, contratos y OHLC. */
export interface MarketApi {
  /** GET /contracts/board — tablón global (toda publicación visible es ejecutable al 100%). */
  queryBoard(query?: BoardQuery): Promise<Page<PublicationDto>>
  /** POST /contracts/publications — publica y bloquea la garantía íntegra (ADR-014). */
  createPublication(publication: PublicationCreateDto): Promise<PublicationDto>
  /** GET /contracts/publications/{publicationId} */
  getPublication(publicationId: Schemas['PublicationId']): Promise<PublicationDto>
  /** DELETE /contracts/publications/{publicationId} — respeta el cooldown anti-parpadeo (409). */
  cancelPublication(publicationId: Schemas['PublicationId']): Promise<PublicationDto>
  /**
   * POST /contracts/publications/{publicationId}/acceptances — acepta K de N en
   * la ventana de sorteo (ADR-011). `originNodeId` es requerido al aceptar
   * publicaciones `buy` (almacén propio del que sale el stock); ignorado en `sell`.
   */
  acceptPublication(
    publicationId: Schemas['PublicationId'],
    quantity: Schemas['StockQty'],
    originNodeId?: Schemas['NodeId'],
  ): Promise<AcceptanceDto>
  /** GET /contracts/acceptances/{acceptanceId} — resultado tras el sorteo. */
  getAcceptance(acceptanceId: Schemas['AcceptanceId']): Promise<AcceptanceDto>

  /** GET /contracts/contracts — contratos CCRI propios. */
  listContracts(query?: ContractListQuery): Promise<Page<ContractDto>>
  /** GET /contracts/contracts/{contractId} */
  getContract(contractId: Schemas['ContractId']): Promise<ContractDto>
  /** GET /contracts/contracts/{contractId}/deliveries — entregas parciales (sin cursor). */
  listContractDeliveries(contractId: Schemas['ContractId']): Promise<readonly ContractDeliveryDto[]>

  /** GET /market/ohlc — velas de contratos liquidados, buckets en sim-time (sin cursor). */
  getMarketOhlc(query: OhlcQuery): Promise<readonly OhlcCandleDto[]>
}

export function createMarketApi(rest: RestClient): MarketApi {
  return {
    queryBoard(query) {
      return requestPage<PublicationDto>(rest, {
        method: 'GET',
        path: '/contracts/board',
        query: query ?? {},
      })
    },

    async createPublication(publication) {
      const { data } = await rest.request<PublicationDto>({
        method: 'POST',
        path: '/contracts/publications',
        body: publication,
      })
      return data
    },

    async getPublication(publicationId) {
      const { data } = await rest.request<PublicationDto>({
        method: 'GET',
        path: `/contracts/publications/${encodeURIComponent(publicationId)}`,
      })
      return data
    },

    async cancelPublication(publicationId) {
      const { data } = await rest.request<PublicationDto>({
        method: 'DELETE',
        path: `/contracts/publications/${encodeURIComponent(publicationId)}`,
      })
      return data
    },

    async acceptPublication(publicationId, quantity, originNodeId) {
      const body: AcceptanceCreateDto = {
        quantity,
        ...(originNodeId === undefined ? {} : { origin_node_id: originNodeId }),
      }
      const { data } = await rest.request<AcceptanceDto>({
        method: 'POST',
        path: `/contracts/publications/${encodeURIComponent(publicationId)}/acceptances`,
        body,
      })
      return data
    },

    async getAcceptance(acceptanceId) {
      const { data } = await rest.request<AcceptanceDto>({
        method: 'GET',
        path: `/contracts/acceptances/${encodeURIComponent(acceptanceId)}`,
      })
      return data
    },

    listContracts(query) {
      return requestPage<ContractDto>(rest, {
        method: 'GET',
        path: '/contracts/contracts',
        query: query ?? {},
      })
    },

    async getContract(contractId) {
      const { data } = await rest.request<ContractDto>({
        method: 'GET',
        path: `/contracts/contracts/${encodeURIComponent(contractId)}`,
      })
      return data
    },

    async listContractDeliveries(contractId) {
      const { data } = await rest.request<readonly ContractDeliveryDto[]>({
        method: 'GET',
        path: `/contracts/contracts/${encodeURIComponent(contractId)}/deliveries`,
      })
      return data
    },

    async getMarketOhlc(query) {
      const { data } = await rest.request<readonly OhlcCandleDto[]>({
        method: 'GET',
        path: '/market/ohlc',
        query,
      })
      return data
    },
  }
}
