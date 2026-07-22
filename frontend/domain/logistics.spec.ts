import { describe, expect, it } from 'vitest'

import { asEntityId } from '~shared/ids'
import type { Money } from '~shared/money'
import { simTime } from '~shared/simtime'
import type { AccountId } from './auth'
import type { TerminalSlot } from './logistics'
import { isSlotHeld } from './logistics'

const HOLDER: AccountId = asEntityId<'Account'>('00000000-0000-7000-8000-000000000901')

function slot(over: Partial<TerminalSlot> = {}): TerminalSlot {
  return {
    id: asEntityId<'TerminalSlot'>('00000000-0000-7000-8000-000000000125'),
    terminalId: asEntityId<'Terminal'>('00000000-0000-7000-8000-000000000120'),
    priorityTier: 1,
    price: '30000' as Money,
    holderAccountId: null,
    validUntilSim: null,
    ...over,
  }
}

describe('domain/logistics — isSlotHeld (espejo de la regla de compra)', () => {
  it('sin titular el slot está a la venta', () => {
    expect(isSlotHeld(slot(), simTime(1_000))).toBe(false)
  })

  it('titular con vigencia futura ⇒ vigente (409 SLOT_HELD al comprar)', () => {
    const held = slot({ holderAccountId: HOLDER, validUntilSim: simTime(2_000) })
    expect(isSlotHeld(held, simTime(1_000))).toBe(true)
  })

  it('la vigencia incluye el instante exacto del vencimiento', () => {
    const held = slot({ holderAccountId: HOLDER, validUntilSim: simTime(1_000) })
    expect(isSlotHeld(held, simTime(1_000))).toBe(true)
  })

  it('titular con vigencia vencida ⇒ comprable de nuevo', () => {
    const expired = slot({ holderAccountId: HOLDER, validUntilSim: simTime(500) })
    expect(isSlotHeld(expired, simTime(1_000))).toBe(false)
  })

  it('titular sin vencimiento ⇒ vigente indefinidamente', () => {
    const held = slot({ holderAccountId: HOLDER, validUntilSim: null })
    expect(isSlotHeld(held, simTime(1_000))).toBe(true)
  })
})
