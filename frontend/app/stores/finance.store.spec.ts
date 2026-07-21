import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { ZERO } from '~shared/money'
import { useFinanceStore } from './finance.store'
import { ledgerAccount, ledgerEntry, mon, uid } from './testing/fixtures'

const PRODUCT_IRON = uid<'Product'>(10)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/finance.store — cuentas del ledger', () => {
  it('cash es el getter destacado: ZERO sin bootstrap, saldo de la cuenta cash después', () => {
    const store = useFinanceStore()
    expect(store.cash).toBe(ZERO)
    expect(store.cashAccount).toBeNull()

    const cash = ledgerAccount({
      id: uid<'LedgerAccount'>(190),
      kind: 'cash',
      balance: mon('50000'),
    })
    store.applyLedgerAccountsSnapshot([cash])
    expect(store.cash).toBe('50000')
    expect(store.cashAccount).toEqual(cash)

    // Nueva observación del saldo (solo respuesta del servidor escribe).
    store.applyLedgerAccount(ledgerAccount({ id: cash.id, kind: 'cash', balance: mon('47500') }))
    expect(store.cash).toBe('47500')
  })

  it('totales por tipo suman con BigInt (sin floats): garantías y escrow', () => {
    const store = useFinanceStore()
    store.applyLedgerAccountsSnapshot([
      ledgerAccount({ id: uid<'LedgerAccount'>(190), kind: 'cash', balance: mon('1000') }),
      ledgerAccount({ id: uid<'LedgerAccount'>(191), kind: 'guarantee', balance: mon('300') }),
      ledgerAccount({
        id: uid<'LedgerAccount'>(192),
        kind: 'guarantee',
        balance: mon('900719925474099299999'),
      }),
      ledgerAccount({ id: uid<'LedgerAccount'>(193), kind: 'escrow', balance: mon('250') }),
    ])

    expect(store.guaranteeLocked).toBe('900719925474099300299')
    expect(store.escrowLocked).toBe('250')
    expect(store.totalOfKind('sink')).toBe(ZERO)
  })

  it('stockAccountsFor indexa por producto', () => {
    const store = useFinanceStore()
    const ironFree = ledgerAccount({
      id: uid<'LedgerAccount'>(194),
      kind: 'stock_free',
      productId: PRODUCT_IRON,
      balance: mon('500'),
    })
    const otherProduct = ledgerAccount({
      id: uid<'LedgerAccount'>(195),
      kind: 'stock_free',
      productId: uid<'Product'>(11),
      balance: mon('80'),
    })
    store.applyLedgerAccountsSnapshot([ironFree, otherProduct])

    expect(store.stockAccountsFor(PRODUCT_IRON)).toEqual([ironFree])
    expect(store.accountIdsByKind['stock_free']).toEqual([ironFree.id, otherProduct.id])
  })
})

describe('app/stores/finance.store — partidas paginadas', () => {
  it('applyEntriesPage añade en orden del servidor y conserva el cursor', () => {
    const store = useFinanceStore()
    expect(store.entriesLoaded).toBe(false)
    expect(store.hasMoreEntries).toBe(false)

    const e1 = ledgerEntry({ id: uid<'LedgerEntry'>(200) })
    const e2 = ledgerEntry({ id: uid<'LedgerEntry'>(201) })
    store.applyEntriesPage([e1, e2], 'cursor-2')

    expect(store.entriesLoaded).toBe(true)
    expect(store.hasMoreEntries).toBe(true)
    expect(store.entriesList).toEqual([e1, e2])

    const e3 = ledgerEntry({ id: uid<'LedgerEntry'>(202) })
    store.applyEntriesPage([e3], null)
    expect(store.entriesList).toEqual([e1, e2, e3])
    expect(store.hasMoreEntries).toBe(false)
  })

  it('reaplicar la misma página NO duplica (idempotencia ante reintentos)', () => {
    const store = useFinanceStore()
    const e1 = ledgerEntry({ id: uid<'LedgerEntry'>(200) })
    const e2 = ledgerEntry({ id: uid<'LedgerEntry'>(201) })
    store.applyEntriesPage([e1, e2], 'cursor-2')
    store.applyEntriesPage([e1, e2], 'cursor-2')

    expect(store.entriesList).toEqual([e1, e2])
    expect(store.entryOrder).toHaveLength(2)
  })

  it('resetEntries descarta lo cargado; clear purga cuentas y partidas', () => {
    const store = useFinanceStore()
    store.applyLedgerAccountsSnapshot([ledgerAccount()])
    store.applyEntriesPage([ledgerEntry()], null)

    store.resetEntries()
    expect(store.entriesList).toEqual([])
    expect(store.entriesLoaded).toBe(false)
    expect(store.ledgerAccountList).toHaveLength(1)

    store.applyEntriesPage([ledgerEntry()], null)
    store.clear()
    expect(store.ledgerAccountList).toHaveLength(0)
    expect(store.entriesList).toEqual([])
  })
})
