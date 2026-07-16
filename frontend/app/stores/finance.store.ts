/**
 * stores/finance.store.ts — bounded context: finanzas (ledger).
 *
 * El cliente REFLEJA asientos y saldos del ledger de doble entrada; ni un
 * centavo cambia por decisión del cliente (P1). Todos los agregados usan los
 * helpers BigInt del kernel — nunca aritmética de coma flotante (C11).
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { LedgerAccount, LedgerEntry, PatchOp } from '~/lib/api/types'
import { addMoney, ZERO_MONEY, type Money } from '~/lib/kernel/money'
import { applySnapshotTo, emptyCollection, removeFrom, upsertTo, type ReplicatedCollection } from './replication'

const MAX_RECENT_ENTRIES = 500

export const useFinanceStore = defineStore('finance', () => {
  // ── Estado ──
  const accounts = ref<ReplicatedCollection<LedgerAccount>>(emptyCollection<LedgerAccount>())
  /** Partidas recientes (pull REST GET /ledger/entries), más nuevas primero. */
  const recentEntries = ref<LedgerEntry[]>([])

  // ── Getters ──
  const accountsById = computed(() => accounts.value.byId)
  const accountList = computed(() => Object.values(accounts.value.byId))

  /** Saldo de caja: cuenta `cash` (dinero: sin product_id). */
  const saldoCash = computed<Money>(() => {
    const cash = accountList.value.find((a) => a.kind === 'cash' && a.product_id === undefined)
    return cash?.balance ?? ZERO_MONEY
  })

  /** Total de garantías monetarias bloqueadas (cuentas `guarantee`). */
  const garantiasBloqueadas = computed<Money>(() =>
    accountList.value
      .filter((a) => a.kind === 'guarantee' && a.product_id === undefined)
      .reduce<Money>((total, a) => addMoney(total, a.balance), ZERO_MONEY)
  )

  /** Escrow bloqueado como comprador (informativo). */
  const escrowBloqueado = computed<Money>(() =>
    accountList.value
      .filter((a) => a.kind === 'escrow' && a.product_id === undefined)
      .reduce<Money>((total, a) => addMoney(total, a.balance), ZERO_MONEY)
  )

  // ── Acciones apply* ──
  function applySnapshot(room: string, data: { ledger_accounts?: LedgerAccount[] }): void {
    applySnapshotTo(accounts.value, room, data.ledger_accounts ?? [])
  }

  function applyPatch(ops: readonly PatchOp[]): void {
    for (const op of ops) {
      if (op.entity !== 'ledger_account') continue
      if (op.op === 'upsert') upsertTo(accounts.value, op.data as LedgerAccount)
      else removeFrom(accounts.value, op.id)
    }
  }

  /** Prepend idempotente de partidas (el ledger es append-only; id única). */
  function addEntries(entries: readonly LedgerEntry[]): void {
    const known = new Set(recentEntries.value.map((e) => e.id))
    const fresh = entries.filter((e) => !known.has(e.id))
    recentEntries.value = [...fresh, ...recentEntries.value].slice(0, MAX_RECENT_ENTRIES)
  }

  return {
    accounts,
    recentEntries,
    accountsById,
    accountList,
    saldoCash,
    garantiasBloqueadas,
    escrowBloqueado,
    applySnapshot,
    applyPatch,
    addEntries
  }
})
