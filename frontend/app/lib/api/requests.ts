/**
 * lib/api/requests.ts — DTOs de PETICIÓN del contrato REST
 * (specs/openapi.yaml v1.1.0: *Create/*Update/queries), complemento de los
 * DTOs de respuesta de types.ts. Misma regla: ante discrepancia se corrige
 * contra specs/openapi.yaml, nunca al revés (regla de precedencia del FAD).
 *
 * El cliente solo VALIDA FORMA en la UI (C7); estos tipos expresan intenciones
 * que el servidor revalida íntegramente.
 */
import type { Id } from '../kernel/ids'
import type { Money, Quantity } from '../kernel/money'
import type { SimTime } from '../kernel/simtime'
import type {
  AccountId,
  Biome,
  BatchStatus,
  BuildingStatus,
  BuildingTypeId,
  ConcessionId,
  ConcessionStatus,
  ContractChannel,
  ContractId,
  ContractStatus,
  GeoPolygon,
  LedgerAccountKind,
  LinkId,
  LinkMode,
  NodeId,
  NodeKind,
  ProductClass,
  ProductId,
  PublicationKind,
  RecipeId,
  RegionId,
  RouteId,
  RouteKind,
  ShipmentStatus,
  VehicleId,
  VehicleStatus,
  VehicleTypeId
} from './types'

// ─── Paginación común ────────────────────────────────────────────────────────

export interface PageQuery {
  /** Cursor opaco devuelto en meta.next_cursor. */
  cursor?: string
  /** Tamaño máximo de página (1..200, default 50 en el servidor). */
  limit?: number
}

// ─── Auth ────────────────────────────────────────────────────────────────────

export interface SessionCreateRequest {
  account_name: string
  secret: string
  client_info?: Record<string, unknown>
}

// ─── Ledger ──────────────────────────────────────────────────────────────────

export interface LedgerAccountsQuery extends PageQuery {
  kind?: LedgerAccountKind
  product_id?: ProductId
}

export interface LedgerEntriesQuery extends PageQuery {
  from_sim?: SimTime
  to_sim?: SimTime
}

// ─── Contracts (tablón CCRI) ─────────────────────────────────────────────────

export type BoardSort = 'unit_price_asc' | 'unit_price_desc' | 'published_at_desc' | 'deadline_asc'

export interface BoardQuery extends PageQuery {
  kind?: PublicationKind
  product_id?: ProductId
  origin_region_id?: RegionId
  destination_region_id?: RegionId
  max_unit_price?: Money
  min_unit_price?: Money
  min_quantity_remaining?: Quantity
  max_delivery_sim_seconds?: SimTime
  sort?: BoardSort
}

export interface PublicationCreate {
  kind: PublicationKind
  channel?: ContractChannel
  /** Requerido si channel = private. */
  counterparty_account_id?: AccountId
  /** Requerido en sell y buy. */
  product_id?: ProductId
  quantity_total: Quantity
  unit_price: Money
  min_lot?: Quantity
  /** Requerido en sell (almacén con el stock) y freight. */
  origin_node_id?: NodeId
  /** Requerido en buy y freight. */
  destination_node_id?: NodeId
  delivery_sim_seconds: SimTime
  /** Solo freight. */
  declared_value?: Money
}

export interface AcceptanceCreate {
  /** Cantidad aceptada (K de N; >= min_lot de la publicación). */
  quantity: Quantity
}

export interface ContractsQuery extends PageQuery {
  role?: 'buyer' | 'seller'
  status?: ContractStatus
  product_id?: ProductId
}

// ─── Market ──────────────────────────────────────────────────────────────────

export interface OhlcQuery {
  product_id: ProductId
  region_id?: RegionId
  /** Tamaño del bucket en segundos de sim-time (default 3600 en el servidor). */
  bucket_sim_secs?: number
  from_sim?: SimTime
  to_sim?: SimTime
  limit?: number
}

// ─── World: catálogos ────────────────────────────────────────────────────────

export interface RegionsQuery extends PageQuery {
  biome?: Biome
}

export interface ProductsQuery extends PageQuery {
  class?: ProductClass
  is_fuel?: boolean
}

export interface RecipesQuery extends PageQuery {
  building_type_id?: BuildingTypeId
  product_id?: ProductId
}

export interface DepositsQuery extends PageQuery {
  region_id?: RegionId
  product_id?: ProductId
  only_available?: boolean
}

export interface CitiesQuery extends PageQuery {
  region_id?: RegionId
  min_level?: number
}

export interface CityDemandQuery {
  product_id?: ProductId
}

// ─── World: suelo ────────────────────────────────────────────────────────────

export interface ConcessionsQuery extends PageQuery {
  status?: ConcessionStatus
  region_id?: RegionId
}

export interface ConcessionCreate {
  region_id: RegionId
  parcel: GeoPolygon
}

export interface ConcessionTransferCreate {
  concession_id: ConcessionId
  to_account_id: AccountId
  price: Money
}

export type ConcessionTransferId = Id<'concession_transfer'>

/** Respuesta de POST /world/concession-transfers (no está en types.ts: solo la usa este comando). */
export interface ConcessionTransfer {
  id: ConcessionTransferId
  concession_id: ConcessionId
  from_account_id: AccountId
  to_account_id: AccountId
  price: Money
  system_fee: Money
  occurred_at_sim: SimTime
}

// ─── World: edificios y producción ───────────────────────────────────────────

export interface BuildingsQuery extends PageQuery {
  region_id?: RegionId
  status?: BuildingStatus
  building_type_id?: BuildingTypeId
}

export interface BuildingCreate {
  building_type_id: BuildingTypeId
  /** Concesión propia sobre la que se asienta. */
  concession_id: ConcessionId
  /** Huella dentro de la parcela; la valida SOLO el servidor (placement_rules). */
  footprint: GeoPolygon
}

export interface BuildingUpdate {
  /** Receta activa; null para detener la línea. */
  active_recipe_id?: RecipeId | null
  /** Inicia mantenimiento programado (in_maintenance). */
  start_maintenance?: boolean
}

export interface ProductionBatchesQuery extends PageQuery {
  status?: BatchStatus
}

export interface ProductionBatchCreate {
  recipe_id: RecipeId
  batches_queued: number
  /** Posición deseada en la cola (por defecto, al final). */
  queue_position?: number
}

// ─── World: flota ────────────────────────────────────────────────────────────

export interface VehicleTypesQuery extends PageQuery {
  mode?: LinkMode
}

export interface VehiclesQuery extends PageQuery {
  status?: VehicleStatus
  route_id?: RouteId
}

export interface VehiclePurchase {
  vehicle_type_id: VehicleTypeId
  /** Nodo de entrega (compatible con el modo del vehículo). */
  delivery_node_id: NodeId
}

export interface VehicleUpdate {
  /** Asigna una ruta propia; null para retirarla. */
  route_id?: RouteId | null
  /** Programa mantenimiento (reduce wear_pct y la probabilidad de avería). */
  schedule_maintenance?: boolean
}

export interface ShipmentsQuery extends PageQuery {
  status?: ShipmentStatus
  contract_id?: ContractId
  vehicle_id?: VehicleId
}

// ─── Logistics ───────────────────────────────────────────────────────────────

export interface NodesQuery extends PageQuery {
  region_id?: RegionId
  kind?: NodeKind
}

export interface LinksQuery extends PageQuery {
  region_id?: RegionId
  mode?: LinkMode
  from_node_id?: NodeId
}

export interface RoutePlanRequest {
  origin_node_id: NodeId
  destination_node_id: NodeId
  /** Modos permitidos (por defecto, todos). */
  modes?: LinkMode[]
  optimize?: 'time' | 'cost'
  /** Volumen a transportar (informa coste estimado y viabilidad). */
  cargo_volume?: Quantity
}

export interface RoutesQuery extends PageQuery {
  kind?: RouteKind
  active?: boolean
}

export interface RouteCreate {
  name: string
  kind: RouteKind
  /** Secuencia contigua de enlaces (multimodal solo con terminal intermodal). */
  legs: LinkId[]
}

export interface RouteUpdate {
  name?: string
  active?: boolean
  legs?: LinkId[]
}
