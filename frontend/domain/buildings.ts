/**
 * domain/buildings — edificios, inventarios y producción (bounded context Buildings, FAD §9.1).
 *
 * Mismas convenciones que domain/world (brands, Money/Quantity, SimTime,
 * `| null`). El progreso de un lote NO se calcula aquí: el servidor lo deriva
 * al consultarlo (`progressPctObserved`) y la UI lo re-deriva de
 * `startedAtSim` + duración de la receta con el SimClock (presentación, P1).
 */

import type { EntityId } from '~shared/ids'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { ConcessionId } from './cadastre'
import type { WorldPolygonM } from './geo'
import type { Quantity } from './quantity'
import type { BuildingTypeId, ProductId, RecipeId, RegionId } from './world'

export type BuildingId = EntityId<'Building'>
export type BatchId = EntityId<'Batch'>

export const BUILDING_STATUSES = [
  'under_construction',
  'operational',
  'damaged',
  'in_maintenance',
  'abandoned',
  'seized',
] as const
export type BuildingStatus = (typeof BUILDING_STATUSES)[number]

export const BATCH_STATUSES = [
  'queued',
  'running',
  'paused_no_fuel',
  'paused_no_workers',
  'completed',
  'cancelled',
] as const
export type BatchStatus = (typeof BATCH_STATUSES)[number]

export interface Building {
  readonly id: BuildingId
  readonly ownerAccountId: AccountId
  readonly regionId: RegionId
  /** El suelo es siempre concesión del sistema (domain/cadastre). */
  readonly concessionId: ConcessionId
  readonly buildingTypeId: BuildingTypeId
  readonly footprintM: WorldPolygonM
  readonly level: number
  readonly status: BuildingStatus
  readonly activeRecipeId: RecipeId | null
  /** Estado de conservación 0–100 (degrada con el impago de mantenimiento). */
  readonly conditionPct: number
  /** Almacén de combustible local — sin combustible, la producción pausa. */
  readonly fuelStock: Quantity
  readonly updatedAtSim: SimTime | null
}

/** Stock físico de un producto en un edificio (la partición libre/reservado vive en el ledger). */
export interface InventoryItem {
  readonly buildingId: BuildingId
  readonly productId: ProductId
  readonly quantity: Quantity
  readonly updatedAtSim: SimTime
}

export interface ProductionBatch {
  readonly id: BatchId
  readonly buildingId: BuildingId
  readonly recipeId: RecipeId
  readonly batchesQueued: number
  readonly batchesDone: number
  readonly status: BatchStatus
  readonly queuePosition: number
  /** Arranque del lote en curso — base del progreso derivado en cliente. */
  readonly startedAtSim: SimTime | null
  /** Progreso observado en la última respuesta del servidor (no autoritativo en adelante). */
  readonly progressPctObserved: number | null
  readonly etaSim: SimTime | null
}

export function isBuildingStatus(value: string): value is BuildingStatus {
  return (BUILDING_STATUSES as readonly string[]).includes(value)
}

export function isBatchStatus(value: string): value is BatchStatus {
  return (BATCH_STATUSES as readonly string[]).includes(value)
}
