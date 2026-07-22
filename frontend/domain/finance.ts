/**
 * domain/finance — ledger de doble entrada (bounded context Finance, FAD §9.1).
 *
 * El cliente REFLEJA el ledger, jamás lo calcula (P1). Notas de tipos:
 *
 * - `LedgerAccount.balance` se tipa `Money` aunque las cuentas de stock
 *   expresen unidades de producto: el contrato serializa ambos con el MISMO
 *   patrón de punto fijo sin signo y así se reutilizan los helpers BigInt de
 *   shared/money (la cuenta `emission` — la única negativa posible — es del
 *   banco central y nunca llega a las cuentas propias de este cliente).
 * - `LedgerEntry.amount` SÍ es con signo ("positivo o negativo, nunca cero"):
 *   brand propio `SignedAmount` + descomposición para formatear sin floats.
 */

import type { EntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { BuildingId } from './buildings'
import type { ProductId } from './world'

export type LedgerAccountId = EntityId<'LedgerAccount'>
export type LedgerEntryId = EntityId<'LedgerEntry'>
export type LedgerTransactionId = EntityId<'LedgerTransaction'>

export const LEDGER_ACCOUNT_KINDS = [
  'cash',
  'escrow',
  'guarantee',
  'stock_free',
  'stock_reserved',
  'custody',
  'sink',
  'emission',
] as const
export type LedgerAccountKind = (typeof LEDGER_ACCOUNT_KINDS)[number]

export const LEDGER_TRANSACTION_KINDS = [
  'seed_capital',
  'bot_capitalization',
  'bot_retirement',
  'publication_lock',
  'publication_release',
  'acceptance_lock',
  'contract_confirmation',
  'delivery_settlement',
  'custody_load',
  'custody_release',
  'production_output',
  'consumption',
  'wage',
  'maintenance',
  'tax',
  'canon',
  'transfer',
  'auction',
  'reconciliation',
  'power_spot',
] as const
export type LedgerTransactionKind = (typeof LEDGER_TRANSACTION_KINDS)[number]

/** Importe con signo de una partida del ledger: entero de punto fijo, `-` opcional. */
export type SignedAmount = string & { readonly __brand: 'SignedAmount' }

const SIGNED_AMOUNT_PATTERN = /^-?[0-9]+$/

/** Cuenta del ledger. Cada cuenta contiene UN activo: dinero o stock de un producto. */
export interface LedgerAccount {
  readonly id: LedgerAccountId
  readonly kind: LedgerAccountKind
  readonly ownerAccountId: AccountId | null
  /** Presente en cuentas de stock (`stock_free`, `stock_reserved`, `custody`). */
  readonly productId: ProductId | null
  /** Almacén de la partida de stock (presente en `stock_free`). */
  readonly warehouseBuildingId: BuildingId | null
  /** UUID de la publicación/contrato al que sirve de cuenta espejo. */
  readonly referenceId: string | null
  readonly balance: Money
  readonly updatedAtMs: number | null
  readonly createdAtMs: number
}

/** Partida append-only del ledger — nunca se edita ni borra. */
export interface LedgerEntry {
  readonly id: LedgerEntryId
  readonly transactionId: LedgerTransactionId
  readonly accountId: LedgerAccountId
  /** Importe de la partida (positivo o negativo, nunca cero). */
  readonly amount: SignedAmount
  readonly transactionKind: LedgerTransactionKind
  /** UUID de la entidad de dominio que originó el asiento. */
  readonly referenceId: string | null
  readonly description: string | null
  readonly simTimeAt: SimTime
  readonly createdAtMs: number
}

export function isLedgerAccountKind(value: string): value is LedgerAccountKind {
  return (LEDGER_ACCOUNT_KINDS as readonly string[]).includes(value)
}

export function isLedgerTransactionKind(value: string): value is LedgerTransactionKind {
  return (LEDGER_TRANSACTION_KINDS as readonly string[]).includes(value)
}

/** Guarda de tipo del patrón de importe con signo. */
export function isSignedAmount(value: string): value is SignedAmount {
  return SIGNED_AMOUNT_PATTERN.test(value)
}

/**
 * Descompone un importe con signo en signo + magnitud `Money`, para formatear
 * con shared/money#format sin pasar jamás por `number` (C11).
 */
export function signedAmountParts(amount: SignedAmount): {
  readonly negative: boolean
  readonly magnitude: Money
} {
  const negative = amount.startsWith('-')
  const digits = negative ? amount.slice(1) : amount
  // Canonicaliza ceros a la izquierda con BigInt; "-0" colapsa a "0" positivo.
  const magnitude = BigInt(digits).toString(10) as Money
  return { negative: negative && magnitude !== '0', magnitude }
}
