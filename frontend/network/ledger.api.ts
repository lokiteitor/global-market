/**
 * network/ledger.api — puerto LedgerApi contra /ledger/* del contrato (FAD §12.8).
 *
 * Ledger de doble entrada de la corporación: cuentas de valor (`cash`,
 * `escrow`, `guarantee`, `stock_free`, `stock_reserved`, `custody`) y su
 * extracto append-only. API de SOLO lectura — el saldo es derivado y protegido
 * por triggers; el cliente jamás calcula ni corrige saldos (thin client).
 *
 * Patrón de network/auth.api: puerto + factoría sobre `RestClient` (unwrap del
 * envelope y `AppError` centralizados). Tipos ÍNTEGROS del contrato generado;
 * `balance`/`amount` son strings de punto fijo del ledger y se manipulan SOLO
 * con `~shared/money` (C11) — nunca parseFloat/Number.
 * ACL DTO→dominio pendiente de su store (FAD §9.5, decisión consciente).
 */

import type { components, operations } from '../types/api'
import type { Page } from './mappers/page.mapper'
import { requestPage } from './mappers/page.mapper'
import type { RestClient } from './rest'

type Schemas = components['schemas']

export type LedgerAccountDto = Schemas['LedgerAccount']
export type LedgerEntryDto = Schemas['LedgerEntry']

// ——— Filtros de query, derivados de `operations` (nunca a mano) ———
export type LedgerAccountListQuery = NonNullable<
  operations['listLedgerAccounts']['parameters']['query']
>
export type LedgerEntryListQuery = NonNullable<
  operations['listLedgerEntries']['parameters']['query']
>

/** Puerto del ledger propio (solo lectura). */
export interface LedgerApi {
  /** GET /ledger/accounts — cuentas de valor propias (filtro por kind/product). */
  listLedgerAccounts(query?: LedgerAccountListQuery): Promise<Page<LedgerAccountDto>>
  /** GET /ledger/accounts/{ledgerAccountId}/entries — extracto append-only (auditoría). */
  listLedgerEntries(
    ledgerAccountId: Schemas['LedgerAccountId'],
    query?: LedgerEntryListQuery,
  ): Promise<Page<LedgerEntryDto>>
}

export function createLedgerApi(rest: RestClient): LedgerApi {
  return {
    listLedgerAccounts(query) {
      return requestPage<LedgerAccountDto>(rest, {
        method: 'GET',
        path: '/ledger/accounts',
        query: query ?? {},
      })
    },

    listLedgerEntries(ledgerAccountId, query) {
      return requestPage<LedgerEntryDto>(rest, {
        method: 'GET',
        path: `/ledger/accounts/${encodeURIComponent(ledgerAccountId)}/entries`,
        query: query ?? {},
      })
    },
  }
}
