/**
 * DTOs mínimos de la API pública (specs/openapi.yaml v1.1.0) que los bots
 * consumen. Dinero y stock viajan SIEMPRE como strings decimales.
 */

// ── Geometrías (GeoJSON RFC 7946, SRID 4326) ──
export interface GeoPoint {
  type: "Point";
  coordinates: [number, number]; // [lon, lat]
}

export interface GeoPolygon {
  type: "Polygon";
  coordinates: number[][][]; // anillo exterior cerrado
}

// ── Envoltorios ──
export interface Meta {
  sim_time: string;
  sim_time_seconds?: number;
  server_time: string;
  next_cursor?: string;
}

export interface ApiResponse<T> {
  data: T;
  meta: Meta;
}

export interface ApiErrorBody {
  error: { code: string; message: string; details?: Record<string, unknown> };
}

// ── Auth ──
export interface Account {
  id: string;
  kind: "human" | "bot" | "city" | "system";
  name: string;
  status: string;
  bot_archetype?: string;
  created_at: string;
}

export interface SessionCreated {
  session_id: string;
  token: string;
  expires_at: string;
  account: Account;
}

// ── Ledger ──
export type LedgerAccountKind =
  | "cash" | "escrow" | "guarantee" | "stock_free"
  | "stock_reserved" | "custody" | "sink" | "emission";

export interface LedgerAccount {
  id: string;
  kind: LedgerAccountKind;
  owner_account_id?: string;
  product_id?: string;
  warehouse_building_id?: string;
  reference_id?: string;
  balance: string;
  created_at: string;
}

// ── Contratos ──
export type PublicationKind = "sell" | "buy" | "freight";
export type PublicationStatus =
  | "draw_window" | "open" | "micro_window" | "exhausted" | "cancelled" | "expired";

export interface Publication {
  id: string;
  kind: PublicationKind;
  publisher_account_id: string;
  channel: "board" | "private";
  product_id?: string;
  quantity_total: string;
  quantity_remaining: string;
  unit_price: string;
  min_lot: string;
  origin_node_id?: string;
  destination_node_id?: string;
  delivery_sim_seconds: number;
  status: PublicationStatus;
  published_at_sim: number;
}

export interface Acceptance {
  id: string;
  publication_id: string;
  acceptor_account_id: string;
  quantity: string;
  quantity_served: string;
  status: "pending_draw" | "served" | "released";
  contract_id?: string;
  accepted_at: string;
}

// ── World: catálogos ──
export interface Product {
  id: string;
  code: string;
  name: string;
  class: "basic" | "luxury";
  unit_volume: number;
  base_price: string;
  price_floor: string;
  price_ceiling: string;
  is_fuel: boolean;
}

export interface BuildingType {
  id: string;
  code: string;
  name: string;
  footprint_cells: number;
  max_level: number;
  base_storage: string;
  build_cost: string;
  maintenance_cost: string;
}

export interface RecipeIngredient {
  product_id: string;
  role: "input" | "output";
  quantity: string;
}

export interface Recipe {
  id: string;
  building_type_id: string;
  code: string;
  name: string;
  batch_sim_seconds: number;
  fuel_product_id?: string;
  fuel_per_batch: string;
  workers_required: number;
  min_city_level: number;
  changeover_seconds: number;
  ingredients: RecipeIngredient[];
}

export interface Region {
  id: string;
  name: string;
  grid_x: number;
  grid_y: number;
  biome: string;
  tax_rate_bp: number;
  customs_rate_bp: number;
  canon_base: string;
  opened_at_sim: number;
}

export interface ResourceDeposit {
  id: string;
  region_id: string;
  product_id: string;
  location: GeoPoint;
  initial_amount: string;
  remaining_amount: string;
  renewable: boolean;
  regen_per_sim_day: string;
}

export interface City {
  id: string;
  region_id: string;
  account_id: string;
  name: string;
  location: GeoPoint;
  level: number;
  population: number;
  supply_index: number;
  influence_radius_m: number;
  base_salary: string;
}

export interface CityDemand {
  city_id: string;
  product_id: string;
  d0_per_sim_day: string;
  saturation_factor: number;
  current_price: string;
  unlocked_at_level: number;
  updated_at_sim: number;
}

// ── World: suelo, edificios, producción ──
export interface Concession {
  id: string;
  region_id: string;
  holder_account_id: string;
  parcel: GeoPolygon;
  canon_amount: string;
  period_sim_days: number;
  expires_at_sim: number;
  status: "active" | "delinquent" | "grace" | "reverted";
  granted_at_sim: number;
}

export type BuildingStatus =
  | "under_construction" | "operational" | "damaged"
  | "in_maintenance" | "abandoned" | "seized";

export interface Building {
  id: string;
  owner_account_id: string;
  region_id: string;
  concession_id: string;
  building_type_id: string;
  footprint: GeoPolygon;
  level: number;
  status: BuildingStatus;
  active_recipe_id?: string;
  condition_pct: number;
  fuel_stock: string;
}

export interface InventoryItem {
  building_id: string;
  product_id: string;
  quantity: string;
  updated_at_sim: number;
}

export type BatchStatus =
  | "queued" | "running" | "paused_no_fuel"
  | "paused_no_workers" | "completed" | "cancelled";

export interface ProductionBatch {
  id: string;
  building_id: string;
  recipe_id: string;
  batches_queued: number;
  batches_done: number;
  status: BatchStatus;
  queue_position: number;
}

// ── Logistics ──
export interface NetworkNode {
  id: string;
  kind: string;
  region_id: string;
  building_id?: string;
  city_id?: string;
  location: GeoPoint;
}
