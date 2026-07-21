/**
 * app/stores/finance.store — bounded context Finance (FAD §9.1, §20).
 *
 * Cuentas del ledger PROPIAS (cash destacado) y partidas paginadas por
 * cursor. El cliente REFLEJA el ledger, jamás lo calcula (P1): los totales de
 * getters son sumas de saldos ya asentados, con los helpers BigInt de
 * shared/money (C11 — cero floats).
 */

import { computed, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import type { Money } from '~shared/money'
import { ZERO, add } from '~shared/money'
import type {
  LedgerAccount,
  LedgerAccountId,
  LedgerAccountKind,
  LedgerEntry,
  LedgerEntryId,
} from '~domain/finance'
import type { ProductId } from '~domain/world'
import { createEntityCollection, indexBy } from './entity-collection'

export const useFinanceStore = defineStore('finance', () => {
  // ——— Cuentas del ledger (replicadas) ———
  const accounts = createEntityCollection<LedgerAccountId, LedgerAccount>((a) => a.id)

  const accountIdsByKind = indexBy(accounts, (a) => a.kind)
  const accountIdsByProduct = indexBy(accounts, (a) => a.productId)

  /** La cuenta `cash` de la corporación (o `null` antes del bootstrap). */
  const cashAccount = computed(() => accounts.list.value.find((a) => a.kind === 'cash') ?? null)

  /** SALDO DISPONIBLE — el getter destacado del contexto. ZERO sin bootstrap. */
  const cash = computed<Money>(() => cashAccount.value?.balance ?? ZERO)

  function accountsOfKind(kind: LedgerAccountKind): readonly LedgerAccount[] {
    return (accountIdsByKind.value[kind] ?? []).flatMap((id) => {
      const account = accounts.get(id)
      return account === null ? [] : [account]
    })
  }

  /** Suma de saldos de un tipo de cuenta (BigInt vía shared/money). */
  function totalOfKind(kind: LedgerAccountKind): Money {
    return accountsOfKind(kind).reduce<Money>((total, account) => add(total, account.balance), ZERO)
  }

  /** Garantías monetarias bloqueadas en publicaciones/contratos vivos. */
  const guaranteeLocked = computed<Money>(() => totalOfKind('guarantee'))

  /** Escrow bloqueado como comprador (pago íntegro retenido). */
  const escrowLocked = computed<Money>(() => totalOfKind('escrow'))

  /** Cuentas de stock de un producto (libre/reservado/custodia). */
  function stockAccountsFor(productId: ProductId): readonly LedgerAccount[] {
    return (accountIdsByProduct.value[productId] ?? []).flatMap((id) => {
      const account = accounts.get(id)
      return account === null ? [] : [account]
    })
  }

  // ——— Partidas (paginadas por cursor, append-only) ———
  const entryById = shallowRef<Readonly<Record<LedgerEntryId, LedgerEntry>>>({})
  const entryOrder = shallowRef<readonly LedgerEntryId[]>([])
  /** Cursor de la página siguiente; `null` = no hay más (o aún sin cargar). */
  const entriesNextCursor = ref<string | null>(null)
  /** ¿Se cargó al menos una página? (distingue "vacío" de "sin cargar"). */
  const entriesLoaded = ref(false)

  /**
   * Aplica una página de partidas en el orden del servidor. Idempotente:
   * los ids ya vistos actualizan la entidad sin duplicar el orden.
   */
  function applyEntriesPage(entries: readonly LedgerEntry[], nextCursor: string | null): void {
    const nextById = { ...entryById.value } as Record<LedgerEntryId, LedgerEntry>
    const nextOrder = [...entryOrder.value]
    for (const entry of entries) {
      if (nextById[entry.id] === undefined) {
        nextOrder.push(entry.id)
      }
      nextById[entry.id] = entry
    }
    entryById.value = nextById
    entryOrder.value = nextOrder
    entriesNextCursor.value = nextCursor
    entriesLoaded.value = true
  }

  /** Descarta las partidas cargadas (re-consulta desde el principio). */
  function resetEntries(): void {
    entryById.value = {}
    entryOrder.value = []
    entriesNextCursor.value = null
    entriesLoaded.value = false
  }

  /** Partidas en el orden de llegada del servidor (paginación por cursor). */
  const entriesList = computed<readonly LedgerEntry[]>(() =>
    entryOrder.value.flatMap((id) => {
      const entry = entryById.value[id]
      return entry === undefined ? [] : [entry]
    }),
  )

  const hasMoreEntries = computed(() => entriesNextCursor.value !== null)

  function clear(): void {
    accounts.clear()
    resetEntries()
  }

  return {
    // Cuentas
    ledgerAccountById: accounts.byId,
    ledgerAccountList: accounts.list,
    getLedgerAccount: accounts.get,
    applyLedgerAccountsSnapshot: accounts.applySnapshot,
    applyLedgerAccount: accounts.applyOne,
    removeLedgerAccount: accounts.remove,
    accountIdsByKind,
    accountIdsByProduct,
    accountsOfKind,
    totalOfKind,
    cashAccount,
    cash,
    guaranteeLocked,
    escrowLocked,
    stockAccountsFor,
    // Partidas
    entryById,
    entryOrder,
    entriesList,
    entriesNextCursor,
    entriesLoaded,
    hasMoreEntries,
    applyEntriesPage,
    resetEntries,
    // Global
    clear,
  }
})
