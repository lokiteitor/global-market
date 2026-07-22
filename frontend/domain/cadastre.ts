/**
 * domain/cadastre — concesiones de suelo (bounded context Cadastre, FAD §9.1).
 *
 * El edificio pertenece a la corporación; el suelo es SIEMPRE concesión del
 * sistema con canon periódico (sink estructural). Mismas convenciones que
 * domain/world (brands, Money, SimTime, `| null`).
 */

import type { EntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { WorldPolygonM } from './geo'
import type { RegionId } from './world'

export type ConcessionId = EntityId<'Concession'>
export type ConcessionTransferId = EntityId<'ConcessionTransfer'>

export const CONCESSION_STATUSES = ['active', 'delinquent', 'grace', 'reverted'] as const
export type ConcessionStatus = (typeof CONCESSION_STATUSES)[number]

export interface Concession {
  readonly id: ConcessionId
  readonly regionId: RegionId
  /** Titular de la concesión (equivale al "owner" a efectos de OwnershipPolicy). */
  readonly holderAccountId: AccountId
  readonly parcelM: WorldPolygonM
  readonly canonAmount: Money
  readonly periodSimDays: number
  readonly expiresAtSim: SimTime
  readonly status: ConcessionStatus
  readonly grantedAtSim: SimTime
}

export function isConcessionStatus(value: string): value is ConcessionStatus {
  return (CONCESSION_STATUSES as readonly string[]).includes(value)
}

/**
 * Antelación del AVISO de vencimiento de canon (presentación): 7 días-sim.
 * El servidor no publica un umbral normativo; su gracia post-impago
 * (II_SEIZE_GRACE_SIM_SECONDS) es de 14 días-sim, así que avisar con media
 * gracia de antelación da margen real de reacción. Decisión de cliente.
 */
export const CONCESSION_EXPIRY_WARNING_SIM_SECONDS = 7 * 86_400
