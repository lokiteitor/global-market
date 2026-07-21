/**
 * domain/world — catálogos y entidades del mundo (bounded context World, FAD §9.1).
 *
 * Modelo de dominio del cliente para regiones, productos, tipos de edificio,
 * recetas, yacimientos y ciudades. Los DTO crudos del contrato NUNCA salen de
 * network/ (O5): los mappers producen ESTAS formas. Convenciones:
 *
 * - Ids con brand por entidad (§20.6) derivados de los schemas nominales.
 * - Dinero como `Money` (shared/money) y stock como `Quantity` (domain/quantity):
 *   strings de punto fijo, jamás floats (C11).
 * - INSTANTES de sim-time como `SimTime`; DURACIONES sim como `number` en
 *   segundos (el contrato tipa ambos con el schema SimTime; el dominio los
 *   distingue semánticamente).
 * - Campos opcionales del contrato → `| null` (nunca `undefined`, por
 *   exactOptionalPropertyTypes).
 */

import type { EntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { WorldPointM, WorldPolygonM } from './geo'
import type { Quantity } from './quantity'

export type RegionId = EntityId<'Region'>
export type ProductId = EntityId<'Product'>
export type BuildingTypeId = EntityId<'BuildingType'>
export type RecipeId = EntityId<'Recipe'>
export type DepositId = EntityId<'Deposit'>
export type CityId = EntityId<'City'>

export const BIOMES = ['plains', 'forest', 'desert', 'mountain', 'ocean', 'coast'] as const
export type Biome = (typeof BIOMES)[number]

/** `basic`: demanda urbana inelástica; `luxury`: elástica y sensible a saturación. */
export const PRODUCT_CLASSES = ['basic', 'luxury'] as const
export type ProductClass = (typeof PRODUCT_CLASSES)[number]

export const INGREDIENT_ROLES = ['input', 'output'] as const
export type IngredientRole = (typeof INGREDIENT_ROLES)[number]

/** Macro-región: jurisdicción de juego y unidad indivisible de sharding. */
export interface Region {
  readonly id: RegionId
  readonly name: string
  readonly gridX: number
  readonly gridY: number
  /** Límites de la región en metros de mundo; `null` si el contrato no los envió. */
  readonly boundsM: WorldPolygonM | null
  readonly biome: Biome
  readonly taxRateBp: number
  readonly customsRateBp: number
  readonly canonBase: Money
  readonly openedAtSim: SimTime
}

export interface Product {
  readonly id: ProductId
  readonly code: string
  readonly name: string
  readonly productClass: ProductClass
  readonly unitVolume: number
  readonly basePrice: Money
  readonly priceFloor: Money
  readonly priceCeiling: Money
  readonly isFuel: boolean
}

export interface BuildingType {
  readonly id: BuildingTypeId
  readonly code: string
  readonly name: string
  readonly footprintCells: number
  readonly maxLevel: number
  readonly baseStorage: Quantity
  /** Reglas de emplazamiento OPACAS para el cliente (las valida el servidor). */
  readonly placementRules: Readonly<Record<string, unknown>> | null
  /** Curva de niveles OPACA (presentación informativa, nunca cálculo autoritativo). */
  readonly levelCurve: Readonly<Record<string, unknown>> | null
  readonly buildCost: Money
  readonly maintenanceCost: Money
}

export interface RecipeIngredient {
  readonly productId: ProductId
  readonly role: IngredientRole
  readonly quantity: Quantity
}

export interface Recipe {
  readonly id: RecipeId
  readonly buildingTypeId: BuildingTypeId
  readonly code: string
  readonly name: string
  /** Duración de un lote (DURACIÓN sim, en segundos). */
  readonly batchSimSeconds: number
  readonly fuelProductId: ProductId | null
  readonly fuelPerBatch: Quantity
  readonly workersRequired: number
  readonly minCityLevel: number
  /** Cambio de línea al activar la receta (DURACIÓN sim, en segundos). */
  readonly changeoverSeconds: number
  readonly ingredients: readonly RecipeIngredient[]
}

export interface ResourceDeposit {
  readonly id: DepositId
  readonly regionId: RegionId
  readonly productId: ProductId
  readonly locationM: WorldPointM
  readonly initialAmount: Quantity
  /** Los minerales son estrictamente finitos y se agotan a cero. */
  readonly remainingAmount: Quantity
  readonly renewable: boolean
  readonly regenPerSimDay: Quantity
}

/** Único consumidor final de la economía; compra por el mecanismo CCRI estándar. */
export interface City {
  readonly id: CityId
  readonly regionId: RegionId
  /** La ciudad como cuenta de mercado (sin canal privilegiado). */
  readonly accountId: AccountId
  readonly name: string
  readonly locationM: WorldPointM
  readonly level: number
  readonly population: number
  readonly supplyIndex: number
  /** Radio de influencia logística y laboral, en metros de mundo. */
  readonly influenceRadiusM: number
  readonly baseSalary: Money
}

/**
 * Fila de la curva de demanda vigente de una ciudad (pull bajo demanda del
 * inspector, C10 — no se replica por WS ni se almacena en store).
 */
export interface CityDemand {
  readonly cityId: CityId
  readonly productId: ProductId
  /** Demanda base diaria `D0(producto, nivel_ciudad)`. */
  readonly d0PerSimDay: Quantity
  /** Factor de saturación por oferta reciente (EMA con suelo, nunca cero). */
  readonly saturationFactor: number
  /** Precio que la ciudad paga actualmente (acotado por los clamps del producto). */
  readonly currentPrice: Money
  readonly unlockedAtLevel: number
  readonly updatedAtSim: SimTime
}

export function isBiome(value: string): value is Biome {
  return (BIOMES as readonly string[]).includes(value)
}

export function isProductClass(value: string): value is ProductClass {
  return (PRODUCT_CLASSES as readonly string[]).includes(value)
}

export function isIngredientRole(value: string): value is IngredientRole {
  return (INGREDIENT_ROLES as readonly string[]).includes(value)
}
