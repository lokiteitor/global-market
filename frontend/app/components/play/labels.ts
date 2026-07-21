/**
 * app/components/play/labels — diccionarios tipados enum de dominio → clave
 * i18n (mismo patrón que domain/status para estados). Un valor sin texto en
 * es.json es un error de COMPILACIÓN, nunca un fallo en runtime.
 */

import type { MessageKey } from '~shared/i18n'
import type { LedgerAccountKind, LedgerTransactionKind } from '~domain/finance'
import type { LinkMode, NodeKind } from '~domain/logistics'
import type { PublicationKind } from '~domain/market'

export const NODE_KIND_LABEL: Readonly<Record<NodeKind, MessageKey>> = {
  mine: 'node.kind.mine',
  factory: 'node.kind.factory',
  warehouse: 'node.kind.warehouse',
  port: 'node.kind.port',
  station: 'node.kind.station',
  distribution_center: 'node.kind.distribution_center',
  junction: 'node.kind.junction',
  city_gate: 'node.kind.city_gate',
}

export const LINK_MODE_LABEL: Readonly<Record<LinkMode, MessageKey>> = {
  road: 'link.mode.road',
  rail: 'link.mode.rail',
  sea: 'link.mode.sea',
}

export const PUBLICATION_KIND_LABEL: Readonly<Record<PublicationKind, MessageKey>> = {
  sell: 'market.kind.sell',
  buy: 'market.kind.buy',
  freight: 'market.kind.freight',
}

export const LEDGER_ACCOUNT_KIND_LABEL: Readonly<Record<LedgerAccountKind, MessageKey>> = {
  cash: 'ledger.kind.cash',
  escrow: 'ledger.kind.escrow',
  guarantee: 'ledger.kind.guarantee',
  stock_free: 'ledger.kind.stock_free',
  stock_reserved: 'ledger.kind.stock_reserved',
  custody: 'ledger.kind.custody',
  sink: 'ledger.kind.sink',
  emission: 'ledger.kind.emission',
}

export const LEDGER_TX_KIND_LABEL: Readonly<Record<LedgerTransactionKind, MessageKey>> = {
  seed_capital: 'ledger.tx.seed_capital',
  bot_capitalization: 'ledger.tx.bot_capitalization',
  bot_retirement: 'ledger.tx.bot_retirement',
  publication_lock: 'ledger.tx.publication_lock',
  publication_release: 'ledger.tx.publication_release',
  acceptance_lock: 'ledger.tx.acceptance_lock',
  contract_confirmation: 'ledger.tx.contract_confirmation',
  delivery_settlement: 'ledger.tx.delivery_settlement',
  custody_load: 'ledger.tx.custody_load',
  custody_release: 'ledger.tx.custody_release',
  production_output: 'ledger.tx.production_output',
  consumption: 'ledger.tx.consumption',
  wage: 'ledger.tx.wage',
  maintenance: 'ledger.tx.maintenance',
  tax: 'ledger.tx.tax',
  canon: 'ledger.tx.canon',
  transfer: 'ledger.tx.transfer',
  auction: 'ledger.tx.auction',
  reconciliation: 'ledger.tx.reconciliation',
}
