/**
 * app/stores/market.store — bounded context Market/CCRI (FAD §9.1, §20).
 *
 * - TABLÓN: resultado EFÍMERO de la última consulta pull (C10 — el tablón
 *   global no se replica por WS), con su sim-time de consulta para el marcado
 *   de frescura. No se fusiona con las colecciones propias.
 * - Publicaciones, aceptaciones y contratos PROPIOS: replicados con la tríada
 *   idempotente (bootstrap REST + deltas WS de la room corp).
 * - Velas OHLC por producto: serie reemplazable por consulta, deduplicada por
 *   bucket y ordenada ascendente (idempotente).
 */

import { computed, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import type { Money } from '~shared/money'
import type { SimTime } from '~shared/simtime'
import type { AccountId } from '~domain/auth'
import type {
  Acceptance,
  AcceptanceId,
  Contract,
  ContractId,
  OhlcCandle,
  Publication,
  PublicationId,
} from '~domain/market'
import { isLivePublicationStatus } from '~domain/market'
import type { ProductId } from '~domain/world'
import { createEntityCollection, indexBy } from './entity-collection'

/** Resultado efímero de la última consulta del tablón. */
export interface BoardView {
  readonly publications: readonly Publication[]
  /** Sim-time de la consulta; `null` = nunca consultado. */
  readonly fetchedAtSim: SimTime | null
}

const EMPTY_BOARD: BoardView = { publications: [], fetchedAtSim: null }

export const useMarketStore = defineStore('market', () => {
  // ——— Tablón (efímero, pull bajo demanda) ———
  const board = shallowRef<BoardView>(EMPTY_BOARD)

  function applyBoardSnapshot(publications: readonly Publication[], fetchedAtSim: SimTime): void {
    board.value = { publications, fetchedAtSim }
  }

  function clearBoard(): void {
    board.value = EMPTY_BOARD
  }

  // ——— Propio (replicado) ———
  const publications = createEntityCollection<PublicationId, Publication>((p) => p.id)
  const acceptances = createEntityCollection<AcceptanceId, Acceptance>((a) => a.id)
  const contracts = createEntityCollection<ContractId, Contract>((c) => c.id)

  const publicationIdsByStatus = indexBy(publications, (p) => p.status)
  const publicationIdsByKind = indexBy(publications, (p) => p.kind)
  const acceptanceIdsByPublication = indexBy(acceptances, (a) => a.publicationId)
  const contractIdsByStatus = indexBy(contracts, (c) => c.status)
  const contractIdsByProduct = indexBy(contracts, (c) => c.productId)

  /** Publicaciones propias aún "vivas" en el tablón (sorteo, abierta o micro-ventana). */
  const livePublications = computed(() =>
    publications.list.value.filter((p) => isLivePublicationStatus(p.status)),
  )

  /** Contratos propios en ejecución (obligaciones pendientes). */
  const activeContracts = computed(() => contracts.list.value.filter((c) => c.status === 'active'))

  /** Aceptaciones propias pendientes de sorteo. */
  const pendingAcceptances = computed(() =>
    acceptances.list.value.filter((a) => a.status === 'pending_draw'),
  )

  function acceptancesForPublication(publicationId: PublicationId): readonly Acceptance[] {
    return (acceptanceIdsByPublication.value[publicationId] ?? []).flatMap((id) => {
      const acceptance = acceptances.get(id)
      return acceptance === null ? [] : [acceptance]
    })
  }

  function contractsAsBuyer(accountId: AccountId): readonly Contract[] {
    return contracts.list.value.filter((c) => c.buyerAccountId === accountId)
  }

  function contractsAsSeller(accountId: AccountId): readonly Contract[] {
    return contracts.list.value.filter((c) => c.sellerAccountId === accountId)
  }

  // ——— Velas OHLC por producto ———
  const ohlcByProduct = shallowRef<Readonly<Record<ProductId, readonly OhlcCandle[]>>>({})

  /**
   * Reemplaza la serie del producto: deduplica por `bucketStartSim` (gana la
   * última) y ordena ascendente. Reaplicar la misma respuesta es no-op
   * observable (idempotencia P6).
   */
  function applyOhlcSnapshot(productId: ProductId, candles: readonly OhlcCandle[]): void {
    const byBucket = new Map<SimTime, OhlcCandle>()
    for (const candle of candles) {
      byBucket.set(candle.bucketStartSim, candle)
    }
    const series = [...byBucket.values()].toSorted((a, b) => a.bucketStartSim - b.bucketStartSim)
    ohlcByProduct.value = { ...ohlcByProduct.value, [productId]: series }
  }

  function candlesFor(productId: ProductId): readonly OhlcCandle[] {
    return ohlcByProduct.value[productId] ?? []
  }

  /** Último cierre conocido del producto (o `null` sin serie). */
  function lastCloseOf(productId: ProductId): Money | null {
    const series = candlesFor(productId)
    const last = series[series.length - 1]
    return last === undefined ? null : last.closePrice
  }

  function clear(): void {
    board.value = EMPTY_BOARD
    publications.clear()
    acceptances.clear()
    contracts.clear()
    ohlcByProduct.value = {}
  }

  return {
    // Tablón efímero
    board,
    applyBoardSnapshot,
    clearBoard,
    // Publicaciones propias
    publicationById: publications.byId,
    publicationList: publications.list,
    getPublication: publications.get,
    applyPublicationsSnapshot: publications.applySnapshot,
    applyPublication: publications.applyOne,
    removePublication: publications.remove,
    publicationIdsByStatus,
    publicationIdsByKind,
    livePublications,
    // Aceptaciones propias
    acceptanceById: acceptances.byId,
    acceptanceList: acceptances.list,
    getAcceptance: acceptances.get,
    applyAcceptancesSnapshot: acceptances.applySnapshot,
    applyAcceptance: acceptances.applyOne,
    removeAcceptance: acceptances.remove,
    acceptanceIdsByPublication,
    acceptancesForPublication,
    pendingAcceptances,
    // Contratos propios
    contractById: contracts.byId,
    contractList: contracts.list,
    getContract: contracts.get,
    applyContractsSnapshot: contracts.applySnapshot,
    applyContract: contracts.applyOne,
    removeContract: contracts.remove,
    contractIdsByStatus,
    contractIdsByProduct,
    activeContracts,
    contractsAsBuyer,
    contractsAsSeller,
    // OHLC
    ohlcByProduct,
    applyOhlcSnapshot,
    candlesFor,
    lastCloseOf,
    // Global
    clear,
  }
})
