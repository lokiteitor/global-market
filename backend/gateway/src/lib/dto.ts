// Serializadores fila → DTO REST (specs/openapi.yaml). Los mismos DTO se usan
// en el WebSocket (los payloads del WS son las formas REST, ADR-IMPL-08).
// Convenciones pg: uuid/bigint/numeric → string, int → number, timestamptz → Date.
/* eslint-disable @typescript-eslint/no-explicit-any */
import { clean, iso } from './envelope.js';

type Row = Record<string, any>;

function num(v: unknown): number | undefined {
  if (v === null || v === undefined) return undefined;
  return Number(v);
}

function str(v: unknown): string | undefined {
  if (v === null || v === undefined) return undefined;
  return String(v);
}

function geo(v: unknown): unknown {
  if (v === null || v === undefined) return undefined;
  if (typeof v === 'string') return JSON.parse(v);
  return v;
}

export function accountDto(r: Row): Row {
  return clean({
    id: r.id,
    kind: r.kind,
    name: r.name,
    status: r.status,
    bot_archetype: r.kind === 'bot' ? (r.bot_archetype ?? undefined) : undefined,
    created_at: iso(r.created_at),
  });
}

export function ledgerAccountDto(r: Row): Row {
  return clean({
    id: r.id,
    kind: r.kind,
    owner_account_id: r.owner_account_id ?? undefined,
    product_id: r.product_id ?? undefined,
    warehouse_building_id: r.warehouse_building_id ?? undefined,
    reference_id: r.reference_id ?? undefined,
    balance: String(r.balance),
    updated_at: iso(r.updated_at),
    created_at: iso(r.created_at),
  });
}

export function ledgerEntryDto(r: Row): Row {
  return clean({
    id: r.id,
    transaction_id: r.transaction_id,
    account_id: r.account_id,
    amount: String(r.amount),
    transaction_kind: r.transaction_kind,
    reference_id: r.reference_id ?? undefined,
    description: r.description ?? undefined,
    sim_time_at: num(r.sim_time_at),
    created_at: iso(r.created_at),
  });
}

export function publicationDto(r: Row): Row {
  return clean({
    id: r.id,
    kind: r.kind,
    publisher_account_id: r.publisher_account_id,
    channel: r.channel,
    counterparty_account_id: r.counterparty_account_id ?? undefined,
    product_id: r.product_id ?? undefined,
    quantity_total: String(r.quantity_total),
    quantity_remaining: String(r.quantity_remaining),
    unit_price: String(r.unit_price),
    min_lot: String(r.min_lot),
    origin_node_id: r.origin_node_id ?? undefined,
    destination_node_id: r.destination_node_id ?? undefined,
    delivery_sim_seconds: num(r.delivery_sim_seconds),
    status: r.status,
    window_closes_at: iso(r.window_closes_at),
    cancel_cooldown_until: iso(r.cancel_cooldown_until),
    published_at_sim: num(r.published_at_sim),
    created_at: iso(r.created_at),
  });
}

export function acceptanceDto(r: Row): Row {
  return clean({
    id: r.id,
    publication_id: r.publication_id,
    acceptor_account_id: r.acceptor_account_id,
    quantity: String(r.quantity),
    quantity_served: String(r.quantity_served),
    status: r.status,
    draw_order: r.draw_order ?? undefined,
    contract_id: r.contract_id ?? undefined,
    freight_contract_id: r.freight_contract_id ?? undefined,
    accepted_at: iso(r.accepted_at),
    resolved_at: iso(r.resolved_at),
  });
}

export function contractDto(r: Row): Row {
  return clean({
    id: r.id,
    publication_id: r.publication_id ?? undefined,
    channel: r.channel,
    buyer_account_id: r.buyer_account_id,
    seller_account_id: r.seller_account_id,
    product_id: r.product_id,
    quantity_agreed: String(r.quantity_agreed),
    quantity_delivered: String(r.quantity_delivered),
    unit_price: String(r.unit_price),
    origin_node_id: r.origin_node_id,
    destination_node_id: r.destination_node_id,
    deadline_sim: num(r.deadline_sim),
    status: r.status,
    fill_bp: r.fill_bp ?? undefined,
    stock_reserve_account_id: r.stock_reserve_account_id ?? undefined,
    seller_guarantee_account_id: r.seller_guarantee_account_id ?? undefined,
    escrow_account_id: r.escrow_account_id ?? undefined,
    confirmed_at_sim: num(r.confirmed_at_sim),
    settled_at_sim: num(r.settled_at_sim),
    created_at: iso(r.created_at),
  });
}

export function contractDeliveryDto(r: Row): Row {
  return clean({
    id: r.id,
    contract_id: r.contract_id,
    shipment_id: r.shipment_id,
    quantity: String(r.quantity),
    delivered_at_sim: num(r.delivered_at_sim),
    on_time: r.on_time,
  });
}

export function regionDto(r: Row): Row {
  return clean({
    id: r.id,
    name: r.name,
    grid_x: r.grid_x,
    grid_y: r.grid_y,
    bounds: geo(r.bounds_geojson),
    biome: r.biome,
    tax_rate_bp: r.tax_rate_bp,
    customs_rate_bp: r.customs_rate_bp,
    canon_base: String(r.canon_base),
    opened_at_sim: num(r.opened_at_sim),
  });
}

export function productDto(r: Row): Row {
  return clean({
    id: r.id,
    code: r.code,
    name: r.name,
    class: r.class,
    unit_volume: r.unit_volume,
    base_price: String(r.base_price),
    price_floor: String(r.price_floor),
    price_ceiling: String(r.price_ceiling),
    is_fuel: r.is_fuel,
  });
}

export function buildingTypeDto(r: Row): Row {
  return clean({
    id: r.id,
    code: r.code,
    name: r.name,
    footprint_cells: r.footprint_cells,
    max_level: r.max_level,
    base_storage: String(r.base_storage),
    placement_rules: r.placement_rules,
    level_curve: r.level_curve,
    build_cost: String(r.build_cost),
    maintenance_cost: String(r.maintenance_cost),
  });
}

export function recipeDto(r: Row): Row {
  const ingredients = Array.isArray(r.ingredients) ? r.ingredients : [];
  return clean({
    id: r.id,
    building_type_id: r.building_type_id,
    code: r.code,
    name: r.name,
    batch_sim_seconds: num(r.batch_sim_seconds),
    fuel_product_id: r.fuel_product_id ?? undefined,
    fuel_per_batch: String(r.fuel_per_batch),
    workers_required: r.workers_required,
    min_city_level: r.min_city_level,
    changeover_seconds: num(r.changeover_seconds),
    ingredients: ingredients.map((i: Row) =>
      clean({ product_id: i.product_id, role: i.role, quantity: String(i.quantity) }),
    ),
  });
}

export function depositDto(r: Row): Row {
  return clean({
    id: r.id,
    region_id: r.region_id,
    product_id: r.product_id,
    location: geo(r.location_geojson),
    initial_amount: String(r.initial_amount),
    remaining_amount: String(r.remaining_amount),
    renewable: r.renewable,
    regen_per_sim_day: String(r.regen_per_sim_day),
  });
}

export function cityDto(r: Row): Row {
  return clean({
    id: r.id,
    region_id: r.region_id,
    account_id: r.account_id,
    name: r.name,
    location: geo(r.location_geojson),
    level: r.level,
    population: num(r.population),
    supply_index: Number(r.supply_index),
    influence_radius_m: r.influence_radius_m,
    base_salary: String(r.base_salary),
  });
}

export function cityDemandDto(r: Row): Row {
  return clean({
    city_id: r.city_id,
    product_id: r.product_id,
    d0_per_sim_day: String(r.d0_per_sim_day),
    saturation_factor: Number(r.saturation_factor),
    current_price: String(r.current_price),
    unlocked_at_level: r.unlocked_at_level,
    updated_at_sim: num(r.updated_at_sim),
  });
}

export function concessionDto(r: Row): Row {
  return clean({
    id: r.id,
    region_id: r.region_id,
    holder_account_id: r.holder_account_id,
    parcel: geo(r.parcel_geojson),
    canon_amount: String(r.canon_amount),
    period_sim_days: r.period_sim_days,
    expires_at_sim: num(r.expires_at_sim),
    status: r.status,
    granted_at_sim: num(r.granted_at_sim),
  });
}

export function concessionTransferDto(r: Row): Row {
  return clean({
    id: r.id,
    concession_id: r.concession_id,
    from_account_id: r.from_account_id,
    to_account_id: r.to_account_id,
    price: String(r.price),
    system_fee: String(r.system_fee),
    occurred_at_sim: num(r.occurred_at_sim),
  });
}

export function buildingDto(r: Row): Row {
  return clean({
    id: r.id,
    owner_account_id: r.owner_account_id,
    region_id: r.region_id,
    concession_id: r.concession_id,
    building_type_id: r.building_type_id,
    footprint: geo(r.footprint_geojson),
    level: r.level,
    status: r.status,
    active_recipe_id: r.active_recipe_id ?? undefined,
    condition_pct: r.condition_pct,
    fuel_stock: String(r.fuel_stock),
    updated_at_sim: num(r.updated_at_sim),
    created_at: iso(r.created_at),
  });
}

export function inventoryItemDto(r: Row): Row {
  return clean({
    building_id: r.building_id,
    product_id: r.product_id,
    quantity: String(r.quantity),
    updated_at_sim: num(r.updated_at_sim),
  });
}

/** El progreso del lote es analítico: derivado en el momento de la consulta. */
export function productionBatchDto(r: Row, simSeconds: number, batchSimSeconds?: number): Row {
  let progress: number | undefined;
  let eta: number | undefined;
  if (r.status === 'running' && r.started_at_sim !== null && batchSimSeconds && batchSimSeconds > 0) {
    const elapsed = simSeconds - Number(r.started_at_sim);
    progress = Math.min(100, Math.max(0, (100 * elapsed) / batchSimSeconds));
    eta = Number(r.started_at_sim) + batchSimSeconds;
  }
  return clean({
    id: r.id,
    building_id: r.building_id,
    recipe_id: r.recipe_id,
    batches_queued: r.batches_queued,
    batches_done: r.batches_done,
    status: r.status,
    queue_position: r.queue_position,
    started_at_sim: num(r.started_at_sim),
    progress_pct: progress,
    eta_sim: eta,
  });
}

export function vehicleTypeDto(r: Row): Row {
  return clean({
    id: r.id,
    code: r.code,
    name: r.name,
    mode: r.mode,
    cargo_capacity: String(r.cargo_capacity),
    speed_kmh: r.speed_kmh,
    fuel_product_id: r.fuel_product_id,
    fuel_per_100km: String(r.fuel_per_100km),
    autonomy_km: r.autonomy_km,
    purchase_price: String(r.purchase_price),
    operating_cost_per_day: String(r.operating_cost_per_day),
  });
}

/**
 * SELECT de vehículos con posición derivada. Requiere $N = sim_seconds como
 * ÚLTIMO parámetro del query (índice `simParamIdx`).
 * Posición analítica: progreso = (sim_now − segment_entered_sim) / duration.
 */
export function vehicleSelectSql(simParam: string): string {
  return `
    SELECT v.id, v.vehicle_type_id, v.owner_account_id, v.status, v.wear_pct, v.fuel,
           v.route_id, v.route_leg_index, v.repair_until_sim, v.updated_at_sim,
           v.at_node_id, v.on_segment_id, v.segment_entered_sim,
           (v.advance_fn->>'duration_sim_seconds')::float8 AS seg_duration,
           CASE WHEN v.at_node_id IS NOT NULL THEN ST_AsGeoJSON(n.location) END AS node_loc,
           CASE WHEN v.on_segment_id IS NOT NULL THEN ST_AsGeoJSON(
             ST_LineInterpolatePoint(s.portion, LEAST(1.0, GREATEST(0.0,
               (${simParam}::float8 - v.segment_entered_sim::float8)
                 / NULLIF((v.advance_fn->>'duration_sim_seconds')::float8, 0)
             )))) END AS seg_loc
      FROM world.vehicles v
      LEFT JOIN world.network_nodes n ON n.id = v.at_node_id
      LEFT JOIN world.link_segments s ON s.id = v.on_segment_id`;
}

export function vehicleDto(r: Row, simSeconds: number): Row {
  const position: Row = {};
  if (r.at_node_id) {
    position.at_node_id = r.at_node_id;
    const loc = geo(r.node_loc);
    if (loc) position.location = loc;
  } else if (r.on_segment_id) {
    position.on_segment_id = r.on_segment_id;
    const dur = Number(r.seg_duration);
    if (Number.isFinite(dur) && dur > 0 && r.segment_entered_sim !== null) {
      const pct = (100 * (simSeconds - Number(r.segment_entered_sim))) / dur;
      position.segment_progress_pct = Math.min(100, Math.max(0, pct));
    }
    const loc = geo(r.seg_loc);
    if (loc) position.location = loc;
  }
  return clean({
    id: r.id,
    vehicle_type_id: r.vehicle_type_id,
    owner_account_id: r.owner_account_id,
    status: r.status,
    wear_pct: r.wear_pct,
    fuel: String(r.fuel),
    route_id: r.route_id ?? undefined,
    route_leg_index: r.route_leg_index ?? undefined,
    position,
    repair_until_sim: num(r.repair_until_sim),
    updated_at_sim: num(r.updated_at_sim),
  });
}

export function shipmentDto(r: Row): Row {
  return clean({
    id: r.id,
    owner_account_id: r.owner_account_id,
    product_id: r.product_id,
    quantity: String(r.quantity),
    contract_id: r.contract_id ?? undefined,
    freight_contract_id: r.freight_contract_id ?? undefined,
    vehicle_id: r.vehicle_id ?? undefined,
    at_node_id: r.at_node_id ?? undefined,
    status: r.status,
    updated_at_sim: num(r.updated_at_sim),
  });
}

export function terminalDto(r: Row): Row {
  return clean({
    id: r.id,
    node_id: r.node_id,
    owner_account_id: r.owner_account_id,
    transshipment_per_hour: r.transshipment_per_hour,
    queue_length: r.queue_length,
    updated_at_sim: num(r.updated_at_sim),
  });
}

export function terminalSlotDto(r: Row): Row {
  return clean({
    id: r.id,
    terminal_id: r.terminal_id,
    priority_tier: r.priority_tier,
    price: String(r.price),
    holder_account_id: r.holder_account_id ?? undefined,
    valid_until_sim: num(r.valid_until_sim),
  });
}

export function networkNodeDto(r: Row): Row {
  return clean({
    id: r.id,
    kind: r.kind,
    region_id: r.region_id,
    building_id: r.building_id ?? undefined,
    city_id: r.city_id ?? undefined,
    location: geo(r.location_geojson),
  });
}

export function networkLinkDto(r: Row): Row {
  const segments = Array.isArray(r.segments) ? r.segments : [];
  return clean({
    id: r.id,
    mode: r.mode,
    from_node_id: r.from_node_id,
    to_node_id: r.to_node_id,
    path: geo(r.path_geojson),
    length_m: r.length_m,
    capacity_per_hour: r.capacity_per_hour,
    base_speed_kmh: r.base_speed_kmh,
    segments: segments.map((s: Row) =>
      clean({
        id: s.id,
        region_id: s.region_id,
        seq: s.seq,
        length_m: s.length_m,
        congestion_ema: Number(s.congestion_ema),
        updated_at_sim: num(s.updated_at_sim),
      }),
    ),
  });
}

export function routeDto(r: Row): Row {
  const legs = Array.isArray(r.legs) ? r.legs : [];
  return clean({
    id: r.id,
    owner_account_id: r.owner_account_id,
    name: r.name,
    kind: r.kind,
    active: r.active,
    legs: legs.map((l: Row) => ({ leg_index: l.leg_index, link_id: l.link_id })),
    created_at: iso(r.created_at),
  });
}

export function ohlcDto(r: Row, productId: string, bucketSimSecs: number): Row {
  return clean({
    product_id: productId,
    region_id: r.region_id,
    bucket_start_sim: num(r.bucket_start_sim),
    bucket_sim_secs: bucketSimSecs,
    open_price: String(r.open_price),
    high_price: String(r.high_price),
    low_price: String(r.low_price),
    close_price: String(r.close_price),
    volume: String(r.volume),
    contract_count: Number(r.contract_count),
  });
}
