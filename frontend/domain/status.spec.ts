import { describe, expect, it } from 'vitest'

import { isMessageKey, t } from '~shared/i18n'
import { BATCH_STATUSES, BUILDING_STATUSES } from './buildings'
import { CONCESSION_STATUSES } from './cadastre'
import { SHIPMENT_STATUSES, VEHICLE_STATUSES } from './fleet'
import { ACCEPTANCE_STATUSES, CONTRACT_STATUSES, PUBLICATION_STATUSES } from './market'
import type { StatusPresentation } from './status'
import {
  ACCEPTANCE_STATUS_PRESENTATION,
  BATCH_STATUS_PRESENTATION,
  BUILDING_STATUS_PRESENTATION,
  CONCESSION_STATUS_PRESENTATION,
  CONTRACT_STATUS_PRESENTATION,
  PUBLICATION_STATUS_PRESENTATION,
  SHIPMENT_STATUS_PRESENTATION,
  VEHICLE_STATUS_PRESENTATION,
  batchStatusPresentation,
  buildingStatusPresentation,
  contractStatusPresentation,
  publicationStatusPresentation,
  shipmentStatusPresentation,
  vehicleStatusPresentation,
} from './status'

/** Exhaustividad runtime + claves i18n existentes y con texto no vacío. */
function expectCompleteMap<S extends string>(
  statuses: readonly S[],
  map: Readonly<Record<S, StatusPresentation>>,
): void {
  for (const status of statuses) {
    const presentation = map[status]
    expect(presentation, `estado sin presentación: ${status}`).toBeDefined()
    expect(
      isMessageKey(presentation.labelKey),
      `clave i18n inexistente: ${presentation.labelKey}`,
    ).toBe(true)
    expect(t(presentation.labelKey).length).toBeGreaterThan(0)
    expect(['ok', 'busy', 'warn', 'danger']).toContain(presentation.severity)
  }
  expect(Object.keys(map).length).toBe(statuses.length)
}

describe('domain/status — mapas espejo completos y con i18n', () => {
  it('building_status', () => {
    expectCompleteMap(BUILDING_STATUSES, BUILDING_STATUS_PRESENTATION)
  })
  it('batch_status', () => {
    expectCompleteMap(BATCH_STATUSES, BATCH_STATUS_PRESENTATION)
  })
  it('contract_status', () => {
    expectCompleteMap(CONTRACT_STATUSES, CONTRACT_STATUS_PRESENTATION)
  })
  it('publication_status', () => {
    expectCompleteMap(PUBLICATION_STATUSES, PUBLICATION_STATUS_PRESENTATION)
  })
  it('vehicle_status', () => {
    expectCompleteMap(VEHICLE_STATUSES, VEHICLE_STATUS_PRESENTATION)
  })
  it('shipment_status', () => {
    expectCompleteMap(SHIPMENT_STATUSES, SHIPMENT_STATUS_PRESENTATION)
  })
  it('concession_status', () => {
    expectCompleteMap(CONCESSION_STATUSES, CONCESSION_STATUS_PRESENTATION)
  })
  it('acceptance_status', () => {
    expectCompleteMap(ACCEPTANCE_STATUSES, ACCEPTANCE_STATUS_PRESENTATION)
  })
})

describe('domain/status — severidades clave para la UI', () => {
  it('estados críticos son danger', () => {
    expect(vehicleStatusPresentation('broken').severity).toBe('danger')
    expect(buildingStatusPresentation('seized').severity).toBe('danger')
    expect(contractStatusPresentation('failed').severity).toBe('danger')
  })

  it('estados normales/estables son ok', () => {
    expect(buildingStatusPresentation('operational').severity).toBe('ok')
    expect(vehicleStatusPresentation('idle').severity).toBe('ok')
    expect(contractStatusPresentation('settled').severity).toBe('ok')
    expect(publicationStatusPresentation('open').severity).toBe('ok')
    expect(shipmentStatusPresentation('delivered').severity).toBe('ok')
  })

  it('pausas de producción exigen atención (warn)', () => {
    expect(batchStatusPresentation('paused_no_fuel').severity).toBe('warn')
    expect(batchStatusPresentation('paused_no_workers').severity).toBe('warn')
  })

  it('sealed es warn: visible, no comandable, pero no es una avería', () => {
    expect(vehicleStatusPresentation('sealed').severity).toBe('warn')
  })

  it('transitorios en curso son busy', () => {
    expect(vehicleStatusPresentation('in_transit').severity).toBe('busy')
    expect(batchStatusPresentation('running').severity).toBe('busy')
    expect(publicationStatusPresentation('draw_window').severity).toBe('busy')
  })
})
