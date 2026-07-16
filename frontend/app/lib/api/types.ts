/**
 * lib/api/types.ts — DTOs del contrato REST (specs/openapi.yaml v1.1.0).
 *
 * Transcripción fiel de los schemas que consume el cliente, refinada con los
 * branded types del kernel (P9/C11/C12): dinero = Money, stock = Quantity,
 * plazos = SimTime, ids = Id<'…'>. El WS reutiliza estas mismas formas de
 * entidad (specs/ws-protocol.md §4.1: "las formas de entidad son las mismas
 * DTO que en REST").
 *
 * Estos tipos NO se editan a mano ante una discrepancia con el backend: se
 * corrige contra specs/openapi.yaml (regla de precedencia del FAD).
 */
import type { Id } from '../kernel/ids'
import type { Money, Quantity } from '../kernel/money'
import type { SimTime } from '../kernel/simtime'

// ─── Identificadores ─────────────────────────────────────────────────────────

export type AccountId = Id<'account'>
export type SessionId = Id<'session'>
export type RegionId = Id<'region'>
export type ProductId = Id<'product'>
export type BuildingTypeId = Id<'building_type'>
export type RecipeId = Id<'recipe'>
export type DepositId = Id<'deposit'>
export type CityId = Id<'city'>
export type ConcessionId = Id<'concession'>
export type BuildingId = Id<'building'>
export type BatchId = Id<'batch'>
export type NodeId = Id<'node'>
export type LinkId = Id<'link'>
export type SegmentId = Id<'segment'>
export type TerminalId = Id<'terminal'>
export type VehicleTypeId = Id<'vehicle_type'>
export type RouteId = Id<'route'>
export type VehicleId = Id<'vehicle'>
export type ShipmentId = Id<'shipment'>
export type LedgerAccountId = Id<'ledger_account'>
export type LedgerEntryId = Id<'ledger_entry'>
export type LedgerTransactionId = Id<'ledger_transaction'>
export type PublicationId = Id<'publication'>
export type AcceptanceId = Id<'acceptance'>
export type ContractId = Id<'contract'>
export type DeliveryId = Id<'delivery'>
export type FreightContractId = Id<'freight_contract'>

// ─── Enums de dominio ────────────────────────────────────────────────────────

export type AccountKind = 'human' | 'bot' | 'city' | 'system'
export type AccountStatus = 'active' | 'suspended' | 'retired'
export type BotArchetype = 'primary_producer' | 'industrial_transformer' | 'arbitrageur' | 'freighter'
export type Biome = 'plains' | 'forest' | 'desert' | 'mountain' | 'ocean' | 'coast'
export type ProductClass = 'basic' | 'luxury'
export type ConcessionStatus = 'active' | 'delinquent' | 'grace' | 'reverted'
export type BuildingStatus = 'under_construction' | 'operational' | 'damaged' | 'in_maintenance' | 'abandoned' | 'seized'
export type BatchStatus = 'queued' | 'running' | 'paused_no_fuel' | 'paused_no_workers' | 'completed' | 'cancelled'
export type NodeKind = 'mine' | 'factory' | 'warehouse' | 'port' | 'station' | 'distribution_center' | 'junction' | 'city_gate'
export type LinkMode = 'road' | 'rail' | 'sea'
export type RouteKind = 'fixed_line' | 'on_demand'
export type VehicleStatus = 'idle' | 'loading' | 'in_transit' | 'unloading' | 'broken' | 'in_maintenance' | 'sealed'
export type ShipmentStatus = 'in_warehouse' | 'in_transit' | 'at_terminal' | 'delivered' | 'released_in_situ'
export type LedgerAccountKind = 'cash' | 'escrow' | 'guarantee' | 'stock_free' | 'stock_reserved' | 'custody' | 'sink' | 'emission'
export type PublicationKind = 'sell' | 'buy' | 'freight'
export type PublicationStatus = 'draw_window' | 'open' | 'micro_window' | 'exhausted' | 'cancelled' | 'expired'
export type AcceptanceStatus = 'pending_draw' | 'served' | 'released'
export type ContractStatus = 'active' | 'settled' | 'failed'
export type ContractChannel = 'board' | 'private'

export type LedgerTransactionKind =
  | 'seed_capital'
  | 'bot_capitalization'
  | 'bot_retirement'
  | 'publication_lock'
  | 'publication_release'
  | 'acceptance_lock'
  | 'contract_confirmation'
  | 'delivery_settlement'
  | 'custody_load'
  | 'custody_release'
  | 'production_output'
  | 'consumption'
  | 'wage'
  | 'maintenance'
  | 'tax'
  | 'canon'
  | 'transfer'
  | 'auction'
  | 'reconciliation'

// ─── Geometrías (GeoJSON RFC 7946, SRID 4326) ────────────────────────────────

export interface GeoPoint {
  type: 'Point'
  /** [lon, lat] */
  coordinates: [number, number]
}

export interface GeoLineString {
  type: 'LineString'
  coordinates: [number, number][]
}

export interface GeoPolygon {
  type: 'Polygon'
  coordinates: [number, number][][]
}

// ─── Envoltorios ─────────────────────────────────────────────────────────────

export interface Meta {
  /** Sim-time legible `AÑO-DDD-HH:MM`. */
  sim_time: string
  /** Forma canónica para clientes; alimenta el SimClock. */
  sim_time_seconds?: SimTime
  /** Wall-clock del servidor (solo informativo). */
  server_time: string
  /** Cursor de la página siguiente; ausente si no hay más resultados. */
  next_cursor?: string
}

/** Toda respuesta exitosa: `{ data, meta }`. */
export interface DataEnvelope<T> {
  data: T
  meta: Meta
}

export interface ApiError {
  /** Código estable (INSUFFICIENT_COLLATERAL, PUBLICATION_EXHAUSTED, …). */
  code: string
  message: string
  /** Contexto estructurado (importes como strings de punto fijo). */
  details?: Record<string, unknown>
}

export interface ErrorEnvelope {
  error: ApiError
}

// ─── Auth ────────────────────────────────────────────────────────────────────

export interface Account {
  id: AccountId
  kind: AccountKind
  name: string
  status: AccountStatus
  /** Solo presente cuando kind = 'bot'. */
  bot_archetype?: BotArchetype
  created_at: string
}

export interface SessionCreated {
  session_id: SessionId
  /** Token bearer; se devuelve una única vez. */
  token: string
  expires_at: string
  account: Account
}

// ─── Ledger ──────────────────────────────────────────────────────────────────

export interface LedgerAccount {
  id: LedgerAccountId
  kind: LedgerAccountKind
  owner_account_id?: AccountId
  /** Presente en cuentas de stock (stock_free, stock_reserved, custody). */
  product_id?: ProductId
  /** Almacén de la partida de stock (presente en stock_free). */
  warehouse_building_id?: BuildingId
  /** Publicación/contrato al que sirve de cuenta espejo. */
  reference_id?: string
  /** Saldo: dinero en unidades menores o stock en unidad mínima según kind. */
  balance: Money
  updated_at?: string
  created_at: string
}

export interface LedgerEntry {
  id: LedgerEntryId
  transaction_id: LedgerTransactionId
  account_id: LedgerAccountId
  /** Importe de la partida (positivo o negativo, nunca cero). */
  amount: Money
  transaction_kind: LedgerTransactionKind
  reference_id?: string
  description?: string
  sim_time_at: SimTime
  created_at: string
}

// ─── Contratos (tablón CCRI) ─────────────────────────────────────────────────

export interface Publication {
  id: PublicationId
  kind: PublicationKind
  publisher_account_id: AccountId
  channel: ContractChannel
  /** Contraparte fija en negociación privada. */
  counterparty_account_id?: AccountId
  /** Requerido en sell/buy; ausente en freight. */
  product_id?: ProductId
  quantity_total: Quantity
  quantity_remaining: Quantity
  unit_price: Money
  min_lot: Quantity
  /** Requerido en sell (almacén con el stock congelado) y freight. */
  origin_node_id?: NodeId
  /** Requerido en buy y freight. */
  destination_node_id?: NodeId
  /** Plazo de entrega pactado desde la confirmación, en sim-time. */
  delivery_sim_seconds: SimTime
  status: PublicationStatus
  /** Cierre de ventana de sorteo/micro-ventana (wall-clock, mecánica en tiempo real). */
  window_closes_at?: string
  /** Fin del cooldown anti-parpadeo (wall-clock). */
  cancel_cooldown_until?: string
  /** Solo freight: valor declarado de la carga. */
  declared_value?: Money
  published_at_sim: SimTime
  created_at?: string
}

export interface Acceptance {
  id: AcceptanceId
  publication_id: PublicationId
  acceptor_account_id: AccountId
  quantity: Quantity
  /** Cantidad servida tras el sorteo (0 si no resultó servido). */
  quantity_served: Quantity
  status: AcceptanceStatus
  /** Orden aleatorio asignado en el sorteo (presente tras resolverse). */
  draw_order?: number
  /** Contrato resultante si fue servida. */
  contract_id?: ContractId
  freight_contract_id?: FreightContractId
  accepted_at: string
  resolved_at?: string
}

export interface Contract {
  id: ContractId
  publication_id?: PublicationId
  channel: ContractChannel
  buyer_account_id: AccountId
  seller_account_id: AccountId
  product_id: ProductId
  quantity_agreed: Quantity
  /** Acumulado de entregas parciales confirmadas dentro del plazo. */
  quantity_delivered: Quantity
  unit_price: Money
  origin_node_id: NodeId
  destination_node_id: NodeId
  /** Vencimiento del plazo de entrega, en sim-time. */
  deadline_sim: SimTime
  status: ContractStatus
  /** Porcentaje entregado a tiempo en puntos básicos (10000 = 100%). */
  fill_bp?: number
  stock_reserve_account_id?: LedgerAccountId
  seller_guarantee_account_id?: LedgerAccountId
  escrow_account_id?: LedgerAccountId
  confirmed_at_sim: SimTime
  settled_at_sim?: SimTime
  created_at?: string
}

export interface ContractDelivery {
  id: DeliveryId
  contract_id: ContractId
  shipment_id: ShipmentId
  quantity: Quantity
  delivered_at_sim: SimTime
  /** Solo lo entregado a tiempo se paga en la liquidación pro-rata. */
  on_time: boolean
}

export interface OhlcCandle {
  product_id: ProductId
  region_id: RegionId
  bucket_start_sim: SimTime
  bucket_sim_secs: number
  open_price: Money
  high_price: Money
  low_price: Money
  close_price: Money
  volume: Quantity
  contract_count: number
}

// ─── World: estático y catálogos ─────────────────────────────────────────────

export interface Region {
  id: RegionId
  name: string
  grid_x: number
  grid_y: number
  bounds?: GeoPolygon
  biome: Biome
  tax_rate_bp: number
  customs_rate_bp: number
  canon_base: Money
  opened_at_sim: SimTime
}

export interface Product {
  id: ProductId
  code: string
  name: string
  class: ProductClass
  /** Volumen por unidad (capacidad de carga). */
  unit_volume: number
  base_price: Money
  price_floor: Money
  price_ceiling: Money
  is_fuel: boolean
}

export interface BuildingType {
  id: BuildingTypeId
  code: string
  name: string
  footprint_cells: number
  max_level: number
  base_storage: Quantity
  /** Requisitos de emplazamiento — los valida SOLO el servidor. */
  placement_rules?: Record<string, unknown>
  level_curve?: Record<string, unknown>
  build_cost: Money
  maintenance_cost: Money
}

export interface RecipeIngredient {
  product_id: ProductId
  role: 'input' | 'output'
  quantity: Quantity
}

export interface Recipe {
  id: RecipeId
  building_type_id: BuildingTypeId
  code: string
  name: string
  /** Duración de un lote, en sim-time. */
  batch_sim_seconds: SimTime
  fuel_product_id?: ProductId
  fuel_per_batch: Quantity
  workers_required: number
  min_city_level: number
  changeover_seconds: SimTime
  ingredients: RecipeIngredient[]
}

export interface ResourceDeposit {
  id: DepositId
  region_id: RegionId
  product_id: ProductId
  location: GeoPoint
  initial_amount: Quantity
  /** Los minerales son estrictamente finitos y se agotan a cero. */
  remaining_amount: Quantity
  renewable: boolean
  regen_per_sim_day: Quantity
}

// ─── World: ciudades ─────────────────────────────────────────────────────────

export interface City {
  id: CityId
  region_id: RegionId
  /** La ciudad como cuenta de mercado (agente: Economy Balancer). */
  account_id: AccountId
  name: string
  location: GeoPoint
  level: number
  population: number
  supply_index: number
  influence_radius_m: number
  base_salary: Money
}

export interface CityDemand {
  city_id: CityId
  product_id: ProductId
  /** Demanda base diaria D0(producto, nivel_ciudad). */
  d0_per_sim_day: Quantity
  saturation_factor: number
  /** Precio que la ciudad paga actualmente (acotado por los clamps del producto). */
  current_price: Money
  unlocked_at_level: number
  updated_at_sim: SimTime
}

// ─── World: suelo ────────────────────────────────────────────────────────────

export interface Concession {
  id: ConcessionId
  region_id: RegionId
  holder_account_id: AccountId
  parcel: GeoPolygon
  canon_amount: Money
  period_sim_days: number
  expires_at_sim: SimTime
  status: ConcessionStatus
  granted_at_sim: SimTime
}

// ─── World: edificios y producción ───────────────────────────────────────────

export interface Building {
  id: BuildingId
  owner_account_id: AccountId
  region_id: RegionId
  /** El edificio es de la corporación; el suelo es siempre concesión del sistema. */
  concession_id: ConcessionId
  building_type_id: BuildingTypeId
  footprint: GeoPolygon
  level: number
  status: BuildingStatus
  active_recipe_id?: RecipeId
  condition_pct: number
  fuel_stock: Quantity
  updated_at_sim?: SimTime
  created_at?: string
}

export interface InventoryItem {
  building_id: BuildingId
  product_id: ProductId
  /** Stock físico total (la partición libre/reservado vive en el ledger). */
  quantity: Quantity
  updated_at_sim: SimTime
}

export interface ProductionBatch {
  id: BatchId
  building_id: BuildingId
  recipe_id: RecipeId
  batches_queued: number
  batches_done: number
  status: BatchStatus
  queue_position: number
  started_at_sim?: SimTime
  /** Derivado analíticamente por el servidor al consultarlo (no persiste). */
  progress_pct?: number
  eta_sim?: SimTime
}

// ─── World: flota y cargamentos ──────────────────────────────────────────────

export interface VehicleType {
  id: VehicleTypeId
  code: string
  name: string
  mode: LinkMode
  cargo_capacity: Quantity
  speed_kmh: number
  fuel_product_id: ProductId
  fuel_per_100km: Quantity
  autonomy_km: number
  purchase_price: Money
  operating_cost_per_day: Money
}

/** Posición física derivada analíticamente al observarla (solo hitos escriben). */
export interface VehiclePosition {
  /** Presente si está detenido en un nodo (XOR con on_segment_id). */
  at_node_id?: NodeId
  /** Presente si circula por un segmento. */
  on_segment_id?: SegmentId
  segment_progress_pct?: number
  /** Coordenadas derivadas para renderizado. */
  location?: GeoPoint
}

export interface Vehicle {
  id: VehicleId
  vehicle_type_id: VehicleTypeId
  owner_account_id: AccountId
  status: VehicleStatus
  wear_pct: number
  fuel: Quantity
  route_id?: RouteId
  route_leg_index?: number
  position: VehiclePosition
  /** Fin de la reparación si está broken (avería = tiempo, no carga perdida). */
  repair_until_sim?: SimTime
  updated_at_sim?: SimTime
}

export interface Shipment {
  id: ShipmentId
  owner_account_id: AccountId
  product_id: ProductId
  quantity: Quantity
  /** CCRI cuyo stock reservado transporta. */
  contract_id?: ContractId
  freight_contract_id?: FreightContractId
  /** Vehículo a bordo del cual viaja (XOR con at_node_id). */
  vehicle_id?: VehicleId
  at_node_id?: NodeId
  status: ShipmentStatus
  updated_at_sim?: SimTime
}

// ─── Logistics ───────────────────────────────────────────────────────────────

export interface NetworkNode {
  id: NodeId
  kind: NodeKind
  region_id: RegionId
  building_id?: BuildingId
  city_id?: CityId
  location: GeoPoint
}

export interface LinkSegment {
  id: SegmentId
  region_id: RegionId
  seq: number
  length_m: number
  /** Congestión EMA (1 = fluido; mayor = más lento). Peso del pathfinding. */
  congestion_ema: number
  updated_at_sim: SimTime
}

export interface NetworkLink {
  id: LinkId
  mode: LinkMode
  from_node_id: NodeId
  to_node_id: NodeId
  path?: GeoLineString
  length_m: number
  capacity_per_hour: number
  base_speed_kmh: number
  segments: LinkSegment[]
}

export interface RoutePlanLeg {
  seq: number
  link_id: LinkId
  mode: LinkMode
  /** Duración estimada del tramo con la congestión EMA vigente. */
  eta_sim_seconds: SimTime
  transshipment_terminal_id?: TerminalId
}

/** Plan sugerido por el asistente; ETAs informativas, no garantías (P1). */
export interface RoutePlan {
  origin_node_id: NodeId
  destination_node_id: NodeId
  legs: RoutePlanLeg[]
  total_eta_sim_seconds: SimTime
  estimated_cost?: Money
}

export interface RouteLeg {
  leg_index: number
  link_id: LinkId
}

export interface Route {
  id: RouteId
  owner_account_id: AccountId
  name: string
  kind: RouteKind
  active: boolean
  legs: RouteLeg[]
  created_at?: string
}

// ─── Protocolo WS (specs/ws-protocol.md §4.2) ────────────────────────────────
// Las ops de patch que las stores aplican vía applyPatch(ops). `upsert` trae
// la entidad COMPLETA (no parcial); la aplicación es idempotente.

export type PatchEntity = 'building' | 'vehicle' | 'shipment' | 'publication' | 'contract' | 'ledger_account' | 'city'

export type PatchOp =
  | { op: 'upsert'; entity: PatchEntity; id: string; data: unknown }
  | { op: 'remove'; entity: PatchEntity; id: string }
