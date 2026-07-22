/**
 * domain/market — tablón, publicaciones, aceptaciones, contratos y velas
 * (bounded context Market/CCRI, FAD §9.1).
 *
 * Mismas convenciones que domain/world. Los únicos instantes wall-clock del
 * dominio de mercado son las DOS mecánicas en tiempo real del contrato
 * (ventana de sorteo/micro-ventana y cooldown anti-parpadeo): se modelan en
 * ms de epoch (`…AtMs`), como en domain/auth.
 */

import type { EntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { LedgerAccountId } from './finance'
import type { NodeId } from './logistics'
import type { Quantity } from './quantity'
import type { ProductId, RegionId } from './world'

export type PublicationId = EntityId<'Publication'>
export type AcceptanceId = EntityId<'Acceptance'>
export type ContractId = EntityId<'Contract'>
export type FreightContractId = EntityId<'FreightContract'>

export const PUBLICATION_KINDS = ['sell', 'buy', 'freight'] as const
export type PublicationKind = (typeof PUBLICATION_KINDS)[number]

export const PUBLICATION_STATUSES = [
  'draw_window',
  'open',
  'micro_window',
  'exhausted',
  'cancelled',
  'expired',
] as const
export type PublicationStatus = (typeof PUBLICATION_STATUSES)[number]

/** Estados "vivos" del tablón: la publicación aún puede terminar en contrato. */
export const LIVE_PUBLICATION_STATUSES = ['draw_window', 'open', 'micro_window'] as const

export const ACCEPTANCE_STATUSES = ['pending_draw', 'served', 'released'] as const
export type AcceptanceStatus = (typeof ACCEPTANCE_STATUSES)[number]

export const CONTRACT_STATUSES = ['active', 'settled', 'failed'] as const
export type ContractStatus = (typeof CONTRACT_STATUSES)[number]

/** Mismo mecanismo de garantías; solo cambia el canal de descubrimiento. */
export const CONTRACT_CHANNELS = ['board', 'private'] as const
export type ContractChannel = (typeof CONTRACT_CHANNELS)[number]

/** Publicación del tablón: toda publicación visible es ejecutable al 100%. */
export interface Publication {
  readonly id: PublicationId
  readonly kind: PublicationKind
  /** Autor (equivale al "owner" a efectos de OwnershipPolicy: solo él cancela). */
  readonly publisherAccountId: AccountId
  readonly channel: ContractChannel
  readonly counterpartyAccountId: AccountId | null
  /**
   * Producto de la publicación. Requerido en los tres tipos desde v1.4.0 (en
   * `freight` es el producto de la carga: la custodia del ledger es por
   * producto); se conserva `| null` por tolerancia con datos previos.
   */
  readonly productId: ProductId | null
  readonly quantityTotal: Quantity
  readonly quantityRemaining: Quantity
  readonly unitPrice: Money
  readonly minLot: Quantity
  readonly originNodeId: NodeId | null
  readonly destinationNodeId: NodeId | null
  /** Plazo de entrega pactado (DURACIÓN sim, en segundos). */
  readonly deliverySimSeconds: number
  readonly status: PublicationStatus
  /** Cierre de la ventana de sorteo/micro-ventana (mecánica real, wall-clock ms). */
  readonly windowClosesAtMs: number | null
  /** Fin del cooldown anti-parpadeo (mecánica real, wall-clock ms). */
  readonly cancelCooldownUntilMs: number | null
  /** Solo `freight`: valor declarado de la carga. */
  readonly declaredValue: Money | null
  readonly publishedAtSim: SimTime
}

export interface Acceptance {
  readonly id: AcceptanceId
  readonly publicationId: PublicationId
  readonly acceptorAccountId: AccountId
  readonly quantity: Quantity
  /** Cantidad servida tras el sorteo (0 si no resultó servido). */
  readonly quantityServed: Quantity
  readonly status: AcceptanceStatus
  readonly drawOrder: number | null
  readonly contractId: ContractId | null
  readonly freightContractId: FreightContractId | null
  readonly acceptedAtMs: number
  readonly resolvedAtMs: number | null
}

/**
 * CCRI de bienes — la unidad económica atómica. Si `destinationNodeId ===
 * originNodeId` la entrega fue in situ; si difieren exige transporte físico
 * antes de `deadlineSim` (liquidación pro-rata por lo entregado a tiempo).
 */
export interface Contract {
  readonly id: ContractId
  readonly publicationId: PublicationId | null
  readonly channel: ContractChannel
  readonly buyerAccountId: AccountId
  readonly sellerAccountId: AccountId
  readonly productId: ProductId
  readonly quantityAgreed: Quantity
  readonly quantityDelivered: Quantity
  readonly unitPrice: Money
  readonly originNodeId: NodeId
  readonly destinationNodeId: NodeId
  readonly deadlineSim: SimTime
  readonly status: ContractStatus
  /** Porcentaje entregado a tiempo, en puntos básicos (presente al liquidar). */
  readonly fillBp: number | null
  readonly confirmedAtSim: SimTime
  readonly settledAtSim: SimTime | null
}

/**
 * CCRI-Flete — contrato de transporte (GDD §5.3.2). El cargador bloqueó el
 * flete en escrow; el transportista depositó su garantía sobre el valor
 * declarado y lleva la carga en custodia (no puede venderla). El schema NO
 * lleva producto ni cantidad: se leen del Shipment ligado
 * (`Shipment.freightContractId`) o de la publicación de origen.
 */
export interface FreightContract {
  readonly id: FreightContractId
  readonly publicationId: PublicationId | null
  readonly channel: ContractChannel
  /** Cargador: dueño de la mercancía, paga el flete (escrow). */
  readonly shipperAccountId: AccountId
  /** Transportista: deposita la garantía y ejecuta el transporte. */
  readonly carrierAccountId: AccountId
  readonly originNodeId: NodeId
  readonly destinationNodeId: NodeId
  /** Importe TOTAL del flete servido (tarifa unitaria × cantidad), no tarifa. */
  readonly freightPrice: Money
  /** Valor declarado prorrateado a lo servido: base de la garantía del carrier. */
  readonly declaredValue: Money
  readonly deadlineSim: SimTime
  readonly status: ContractStatus
  /** Porcentaje entregado a tiempo, en puntos básicos (presente al liquidar). */
  readonly fillBp: number | null
  readonly escrowAccountId: LedgerAccountId | null
  readonly carrierGuaranteeAccountId: LedgerAccountId | null
  readonly custodyAccountId: LedgerAccountId | null
  readonly confirmedAtSim: SimTime
  readonly settledAtSim: SimTime | null
}

/**
 * Garantía del transportista por defecto, en puntos básicos del valor
 * declarado prorrateado (II_FREIGHT_GUARANTEE_BP, default del servidor). Es
 * configuración de servidor no expuesta por la API: toda previsualización
 * cliente construida con ella es una ESTIMACIÓN y debe rotularse como tal.
 */
export const DEFAULT_FREIGHT_GUARANTEE_BP = 1000

export interface OhlcCandle {
  readonly productId: ProductId
  readonly regionId: RegionId
  readonly bucketStartSim: SimTime
  /** Tamaño del bucket (DURACIÓN sim, en segundos). */
  readonly bucketSimSecs: number
  readonly openPrice: Money
  readonly highPrice: Money
  readonly lowPrice: Money
  readonly closePrice: Money
  readonly volume: Quantity
  readonly contractCount: number
}

export function isPublicationKind(value: string): value is PublicationKind {
  return (PUBLICATION_KINDS as readonly string[]).includes(value)
}

export function isPublicationStatus(value: string): value is PublicationStatus {
  return (PUBLICATION_STATUSES as readonly string[]).includes(value)
}

export function isAcceptanceStatus(value: string): value is AcceptanceStatus {
  return (ACCEPTANCE_STATUSES as readonly string[]).includes(value)
}

export function isContractStatus(value: string): value is ContractStatus {
  return (CONTRACT_STATUSES as readonly string[]).includes(value)
}

export function isContractChannel(value: string): value is ContractChannel {
  return (CONTRACT_CHANNELS as readonly string[]).includes(value)
}

/** ¿La publicación sigue "viva" en el tablón (puede terminar en contrato)? */
export function isLivePublicationStatus(status: PublicationStatus): boolean {
  return (LIVE_PUBLICATION_STATUSES as readonly string[]).includes(status)
}
