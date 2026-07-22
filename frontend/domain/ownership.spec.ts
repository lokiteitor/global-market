import { describe, expect, it } from 'vitest'

import type { EntityId } from '~shared/ids'
import { asEntityId } from '~shared/ids'
import type { AccountId } from './auth'
import type { FreightContractId } from './market'
import { canDispatchShipment, isCommandable, isMine, isVehicleCommandable } from './ownership'

function accountId(n: number): AccountId {
  return asEntityId<'Account'>(`00000000-0000-7000-8000-${String(n).padStart(12, '0')}`)
}

const ME = accountId(1)
const OTHER = accountId(2)
const FREIGHT_ID = asEntityId<'FreightContract'>(
  '00000000-0000-7000-8000-000000000185',
) as FreightContractId

describe('domain/ownership — isMine', () => {
  it('true solo si el owner coincide con mi cuenta', () => {
    expect(isMine(ME, ME)).toBe(true)
    expect(isMine(OTHER, ME)).toBe(false)
  })

  it('sin sesión nada es mío', () => {
    expect(isMine(ME, null)).toBe(false)
  })

  it('entidades sin dueño (mundo/sistema) jamás son mías', () => {
    expect(isMine(null, ME)).toBe(false)
    expect(isMine(undefined, ME)).toBe(false)
  })

  it('sirve para cualquier campo de titularidad (holder, publisher, acceptor…)', () => {
    const holderAccountId: EntityId<'Account'> = ME
    expect(isMine(holderAccountId, ME)).toBe(true)
  })
})

describe('domain/ownership — isCommandable (Observable vs Comandable, FAD §5.3)', () => {
  it('lo propio es comandable; lo ajeno solo observable', () => {
    expect(isCommandable({ ownerAccountId: ME }, ME)).toBe(true)
    expect(isCommandable({ ownerAccountId: OTHER }, ME)).toBe(false)
  })

  it('entidad sin campo owner o con owner null → no comandable', () => {
    expect(isCommandable({}, ME)).toBe(false)
    expect(isCommandable({ ownerAccountId: null }, ME)).toBe(false)
  })

  it('sin sesión, nada es comandable (ni siquiera lo "propio")', () => {
    expect(isCommandable({ ownerAccountId: ME }, null)).toBe(false)
  })
})

describe('domain/ownership — isVehicleCommandable', () => {
  it('vehículo propio y no sellado → comandable', () => {
    expect(isVehicleCommandable({ ownerAccountId: ME, status: 'idle' }, ME)).toBe(true)
    expect(isVehicleCommandable({ ownerAccountId: ME, status: 'in_transit' }, ME)).toBe(true)
  })

  it('sealed: visible pero NO comandable aunque sea propio (VEHICLE_SEALED)', () => {
    expect(isVehicleCommandable({ ownerAccountId: ME, status: 'sealed' }, ME)).toBe(false)
  })

  it('vehículo ajeno nunca es comandable, sellado o no', () => {
    expect(isVehicleCommandable({ ownerAccountId: OTHER, status: 'idle' }, ME)).toBe(false)
    expect(isVehicleCommandable({ ownerAccountId: OTHER, status: 'sealed' }, ME)).toBe(false)
  })
})

describe('domain/ownership — canDispatchShipment (regla de despacho del servidor)', () => {
  it('cargamento de bienes: lo despacha su dueño y solo su dueño', () => {
    const goods = { ownerAccountId: ME, freightContractId: null }
    expect(canDispatchShipment(goods, null, ME)).toBe(true)
    expect(canDispatchShipment(goods, null, OTHER)).toBe(false)
  })

  it('cargamento de flete: lo despacha el TRANSPORTISTA, no el cargador/dueño', () => {
    const freightShipment = { ownerAccountId: ME, freightContractId: FREIGHT_ID }
    const freight = { carrierAccountId: OTHER }
    expect(canDispatchShipment(freightShipment, freight, OTHER)).toBe(true)
    expect(canDispatchShipment(freightShipment, freight, ME)).toBe(false)
  })

  it('flete sin contrato replicado aún (null): sin affordance para nadie', () => {
    const freightShipment = { ownerAccountId: ME, freightContractId: FREIGHT_ID }
    expect(canDispatchShipment(freightShipment, null, ME)).toBe(false)
    expect(canDispatchShipment(freightShipment, null, OTHER)).toBe(false)
  })

  it('sin sesión, nada se despacha', () => {
    expect(canDispatchShipment({ ownerAccountId: ME, freightContractId: null }, null, null)).toBe(
      false,
    )
    expect(
      canDispatchShipment(
        { ownerAccountId: ME, freightContractId: FREIGHT_ID },
        { carrierAccountId: OTHER },
        null,
      ),
    ).toBe(false)
  })
})
