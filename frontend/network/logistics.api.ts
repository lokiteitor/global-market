/**
 * network/logistics.api — puerto LogisticsApi contra /logistics/* (FAD §12.8).
 *
 * Grafo logístico de uso común (nodos y enlaces con congestión EMA por
 * segmento — la señal que consume el pathfinding), asistente de ruta óptima
 * (solo cálculo, no persiste) y rutas propias (CRUD).
 *
 * Patrón de network/auth.api: puerto + factoría sobre `RestClient` (unwrap del
 * envelope, `AppError`, Idempotency-Key automática en mutaciones, P6). Tipos
 * ÍNTEGROS del contrato generado; los `path` de los enlaces son GeoLineString
 * con vértices en metros planos de mundo (ADR-019, ver mappers/geometry).
 * ACL DTO→dominio pendiente de sus stores (FAD §9.5, decisión consciente).
 */

import type { components, operations } from '../types/api'
import type { Page } from './mappers/page.mapper'
import { requestPage } from './mappers/page.mapper'
import type { RestClient } from './rest'

type Schemas = components['schemas']

export type NetworkNodeDto = Schemas['NetworkNode']
export type NetworkLinkDto = Schemas['NetworkLink']
export type LinkSegmentDto = Schemas['LinkSegment']
export type RoutePlanRequestDto = Schemas['RoutePlanRequest']
export type RoutePlanDto = Schemas['RoutePlan']
export type RoutePlanLegDto = Schemas['RoutePlanLeg']
export type RouteDto = Schemas['Route']
export type RouteCreateDto = Schemas['RouteCreate']
export type RouteUpdateDto = Schemas['RouteUpdate']

// ——— Filtros de query, derivados de `operations` (nunca a mano) ———
export type NetworkNodeListQuery = NonNullable<
  operations['listNetworkNodes']['parameters']['query']
>
export type NetworkLinkListQuery = NonNullable<
  operations['listNetworkLinks']['parameters']['query']
>
export type RouteListQuery = NonNullable<operations['listRoutes']['parameters']['query']>

/** Puerto de logística: red común, plan de ruta asistido y rutas propias. */
export interface LogisticsApi {
  /** GET /logistics/network/nodes — nodos del grafo. */
  listNetworkNodes(query?: NetworkNodeListQuery): Promise<Page<NetworkNodeDto>>
  /** GET /logistics/network/links — enlaces con segmentos y congestión EMA. */
  listNetworkLinks(query?: NetworkLinkListQuery): Promise<Page<NetworkLinkDto>>
  /** POST /logistics/route-plans — pathfinding asistido; ETAs informativas, no garantías. */
  planRoute(request: RoutePlanRequestDto): Promise<RoutePlanDto>

  /** GET /logistics/routes — rutas propias. */
  listRoutes(query?: RouteListQuery): Promise<Page<RouteDto>>
  /** POST /logistics/routes — define una línea fija o servicio bajo demanda. */
  createRoute(route: RouteCreateDto): Promise<RouteDto>
  /** GET /logistics/routes/{routeId} */
  getRoute(routeId: Schemas['RouteId']): Promise<RouteDto>
  /** PATCH /logistics/routes/{routeId} */
  updateRoute(routeId: Schemas['RouteId'], update: RouteUpdateDto): Promise<RouteDto>
  /** DELETE /logistics/routes/{routeId} — 204; los vehículos asignados quedan `idle`. */
  deleteRoute(routeId: Schemas['RouteId']): Promise<void>
}

export function createLogisticsApi(rest: RestClient): LogisticsApi {
  return {
    listNetworkNodes(query) {
      return requestPage<NetworkNodeDto>(rest, {
        method: 'GET',
        path: '/logistics/network/nodes',
        query: query ?? {},
      })
    },

    listNetworkLinks(query) {
      return requestPage<NetworkLinkDto>(rest, {
        method: 'GET',
        path: '/logistics/network/links',
        query: query ?? {},
      })
    },

    async planRoute(request) {
      const { data } = await rest.request<RoutePlanDto>({
        method: 'POST',
        path: '/logistics/route-plans',
        body: request,
      })
      return data
    },

    listRoutes(query) {
      return requestPage<RouteDto>(rest, {
        method: 'GET',
        path: '/logistics/routes',
        query: query ?? {},
      })
    },

    async createRoute(route) {
      const { data } = await rest.request<RouteDto>({
        method: 'POST',
        path: '/logistics/routes',
        body: route,
      })
      return data
    },

    async getRoute(routeId) {
      const { data } = await rest.request<RouteDto>({
        method: 'GET',
        path: `/logistics/routes/${encodeURIComponent(routeId)}`,
      })
      return data
    },

    async updateRoute(routeId, update) {
      const { data } = await rest.request<RouteDto>({
        method: 'PATCH',
        path: `/logistics/routes/${encodeURIComponent(routeId)}`,
        body: update,
      })
      return data
    },

    async deleteRoute(routeId) {
      await rest.requestVoid({
        method: 'DELETE',
        path: `/logistics/routes/${encodeURIComponent(routeId)}`,
      })
    },
  }
}
