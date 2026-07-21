/**
 * network/fleet.api — puerto FleetApi contra /world/vehicle*, /world/vehicles y
 * /world/shipments del contrato (FAD §12.8).
 *
 * Flota propia (posición derivada ANALÍTICAMENTE al observarla — el cliente la
 * extrapola con el SimClock, nunca la inventa), cargamentos etiquetados por
 * contrato (nada se teletransporta, tampoco en los fallos) y el despacho: la
 * ejecución logística del CCRI (la llegada física confirma la entrega).
 *
 * Patrón de network/auth.api: puerto + factoría sobre `RestClient` (unwrap del
 * envelope, `AppError`, Idempotency-Key automática en mutaciones, P6). Tipos
 * ÍNTEGROS del contrato generado; cantidades/combustible son `StockQty`
 * (strings de punto fijo, SOLO `~shared/money`, C11).
 * ACL DTO→dominio pendiente de sus stores (FAD §9.5, decisión consciente).
 */

import type { components, operations } from '../types/api'
import type { Page } from './mappers/page.mapper'
import { requestPage } from './mappers/page.mapper'
import type { RestClient } from './rest'

type Schemas = components['schemas']

export type VehicleTypeDto = Schemas['VehicleType']
export type VehicleDto = Schemas['Vehicle']
export type VehiclePositionDto = Schemas['VehiclePosition']
export type VehiclePurchaseDto = Schemas['VehiclePurchase']
export type VehicleUpdateDto = Schemas['VehicleUpdate']
export type ShipmentDto = Schemas['Shipment']
export type ShipmentDispatchDto = Schemas['ShipmentDispatch']

// ——— Filtros de query, derivados de `operations` (nunca a mano) ———
export type VehicleTypeListQuery = NonNullable<
  operations['listVehicleTypes']['parameters']['query']
>
export type VehicleListQuery = NonNullable<operations['listVehicles']['parameters']['query']>
export type ShipmentListQuery = NonNullable<operations['listShipments']['parameters']['query']>

/** Puerto de flota y cargamentos: catálogo, compra, comando y despacho físico. */
export interface FleetApi {
  /** GET /world/vehicle-types — catálogo (camión/tren/barco). */
  listVehicleTypes(query?: VehicleTypeListQuery): Promise<Page<VehicleTypeDto>>
  /** GET /world/vehicles — flota propia. */
  listVehicles(query?: VehicleListQuery): Promise<Page<VehicleDto>>
  /** POST /world/vehicles — compra a precio de catálogo con entrega en nodo propio. */
  purchaseVehicle(purchase: VehiclePurchaseDto): Promise<VehicleDto>
  /** GET /world/vehicles/{vehicleId} — incluye posición derivada al consultar. */
  getVehicle(vehicleId: Schemas['VehicleId']): Promise<VehicleDto>
  /** PATCH /world/vehicles/{vehicleId} — asigna/retira ruta o mantenimiento (403 `VEHICLE_SEALED`). */
  updateVehicle(vehicleId: Schemas['VehicleId'], update: VehicleUpdateDto): Promise<VehicleDto>

  /** GET /world/shipments — cargamentos propios. */
  listShipments(query?: ShipmentListQuery): Promise<Page<ShipmentDto>>
  /** GET /world/shipments/{shipmentId} */
  getShipment(shipmentId: Schemas['ShipmentId']): Promise<ShipmentDto>
  /**
   * POST /world/shipments/{shipmentId}/dispatch — carga un cargamento
   * `in_warehouse` en un vehículo propio `idle` del mismo nodo y lo pone en
   * ruta hasta el destino del contrato. Valida capacidad y autonomía de una vez.
   */
  dispatchShipment(
    shipmentId: Schemas['ShipmentId'],
    vehicleId: Schemas['VehicleId'],
    routeId: Schemas['RouteId'],
  ): Promise<ShipmentDto>
}

export function createFleetApi(rest: RestClient): FleetApi {
  return {
    listVehicleTypes(query) {
      return requestPage<VehicleTypeDto>(rest, {
        method: 'GET',
        path: '/world/vehicle-types',
        query: query ?? {},
      })
    },

    listVehicles(query) {
      return requestPage<VehicleDto>(rest, {
        method: 'GET',
        path: '/world/vehicles',
        query: query ?? {},
      })
    },

    async purchaseVehicle(purchase) {
      const { data } = await rest.request<VehicleDto>({
        method: 'POST',
        path: '/world/vehicles',
        body: purchase,
      })
      return data
    },

    async getVehicle(vehicleId) {
      const { data } = await rest.request<VehicleDto>({
        method: 'GET',
        path: `/world/vehicles/${encodeURIComponent(vehicleId)}`,
      })
      return data
    },

    async updateVehicle(vehicleId, update) {
      const { data } = await rest.request<VehicleDto>({
        method: 'PATCH',
        path: `/world/vehicles/${encodeURIComponent(vehicleId)}`,
        body: update,
      })
      return data
    },

    listShipments(query) {
      return requestPage<ShipmentDto>(rest, {
        method: 'GET',
        path: '/world/shipments',
        query: query ?? {},
      })
    },

    async getShipment(shipmentId) {
      const { data } = await rest.request<ShipmentDto>({
        method: 'GET',
        path: `/world/shipments/${encodeURIComponent(shipmentId)}`,
      })
      return data
    },

    async dispatchShipment(shipmentId, vehicleId, routeId) {
      const body: ShipmentDispatchDto = { vehicle_id: vehicleId, route_id: routeId }
      const { data } = await rest.request<ShipmentDto>({
        method: 'POST',
        path: `/world/shipments/${encodeURIComponent(shipmentId)}/dispatch`,
        body,
      })
      return data
    },
  }
}
