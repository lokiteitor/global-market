/**
 * domain/status — máquinas de estado ESPEJO para presentación (FAD §20.9).
 *
 * Los ciclos de vida del dominio los dicta el SERVIDOR; estos mapas solo
 * REFLEJAN cada estado del contrato como presentación: clave i18n (el texto
 * vive en shared/i18n, jamás aquí) + severidad visual para StatusBadge y
 * tintes de render. No hay transiciones ni decisiones autoritativas (P1).
 *
 * Las claves i18n se tipan `MessageKey`: un estado sin texto en es.json es un
 * error de COMPILACIÓN, no un fallo en runtime.
 */

import type { MessageKey } from '~shared/i18n'
import type { BatchStatus, BuildingStatus } from './buildings'
import type { ConcessionStatus } from './cadastre'
import type { ShipmentStatus, VehicleStatus } from './fleet'
import type { AcceptanceStatus, ContractStatus, PublicationStatus } from './market'

/**
 * Severidad visual de un estado:
 * - `ok`: normal/estable (verde-neutro).
 * - `busy`: transitorio en curso (actividad, esperas normales).
 * - `warn`: requiere atención (degradación, pausas, finales no deseados).
 * - `danger`: crítico (averías, embargos, fallos).
 */
export type StatusSeverity = 'ok' | 'busy' | 'warn' | 'danger'

export interface StatusPresentation {
  readonly labelKey: MessageKey
  readonly severity: StatusSeverity
}

export const BUILDING_STATUS_PRESENTATION: Readonly<Record<BuildingStatus, StatusPresentation>> = {
  under_construction: { labelKey: 'status.building.under_construction', severity: 'busy' },
  operational: { labelKey: 'status.building.operational', severity: 'ok' },
  damaged: { labelKey: 'status.building.damaged', severity: 'warn' },
  in_maintenance: { labelKey: 'status.building.in_maintenance', severity: 'busy' },
  abandoned: { labelKey: 'status.building.abandoned', severity: 'warn' },
  seized: { labelKey: 'status.building.seized', severity: 'danger' },
}

export const BATCH_STATUS_PRESENTATION: Readonly<Record<BatchStatus, StatusPresentation>> = {
  queued: { labelKey: 'status.batch.queued', severity: 'busy' },
  running: { labelKey: 'status.batch.running', severity: 'busy' },
  paused_no_fuel: { labelKey: 'status.batch.paused_no_fuel', severity: 'warn' },
  paused_no_workers: { labelKey: 'status.batch.paused_no_workers', severity: 'warn' },
  paused_no_power: { labelKey: 'status.batch.paused_no_power', severity: 'warn' },
  completed: { labelKey: 'status.batch.completed', severity: 'ok' },
  cancelled: { labelKey: 'status.batch.cancelled', severity: 'warn' },
}

export const CONTRACT_STATUS_PRESENTATION: Readonly<Record<ContractStatus, StatusPresentation>> = {
  active: { labelKey: 'status.contract.active', severity: 'busy' },
  settled: { labelKey: 'status.contract.settled', severity: 'ok' },
  failed: { labelKey: 'status.contract.failed', severity: 'danger' },
}

export const PUBLICATION_STATUS_PRESENTATION: Readonly<
  Record<PublicationStatus, StatusPresentation>
> = {
  draw_window: { labelKey: 'status.publication.draw_window', severity: 'busy' },
  open: { labelKey: 'status.publication.open', severity: 'ok' },
  micro_window: { labelKey: 'status.publication.micro_window', severity: 'busy' },
  exhausted: { labelKey: 'status.publication.exhausted', severity: 'ok' },
  cancelled: { labelKey: 'status.publication.cancelled', severity: 'warn' },
  expired: { labelKey: 'status.publication.expired', severity: 'warn' },
}

export const VEHICLE_STATUS_PRESENTATION: Readonly<Record<VehicleStatus, StatusPresentation>> = {
  idle: { labelKey: 'status.vehicle.idle', severity: 'ok' },
  loading: { labelKey: 'status.vehicle.loading', severity: 'busy' },
  in_transit: { labelKey: 'status.vehicle.in_transit', severity: 'busy' },
  unloading: { labelKey: 'status.vehicle.unloading', severity: 'busy' },
  broken: { labelKey: 'status.vehicle.broken', severity: 'danger' },
  in_maintenance: { labelKey: 'status.vehicle.in_maintenance', severity: 'warn' },
  sealed: { labelKey: 'status.vehicle.sealed', severity: 'warn' },
}

export const SHIPMENT_STATUS_PRESENTATION: Readonly<Record<ShipmentStatus, StatusPresentation>> = {
  in_warehouse: { labelKey: 'status.shipment.in_warehouse', severity: 'ok' },
  in_transit: { labelKey: 'status.shipment.in_transit', severity: 'busy' },
  at_terminal: { labelKey: 'status.shipment.at_terminal', severity: 'busy' },
  delivered: { labelKey: 'status.shipment.delivered', severity: 'ok' },
  released_in_situ: { labelKey: 'status.shipment.released_in_situ', severity: 'ok' },
}

export const CONCESSION_STATUS_PRESENTATION: Readonly<
  Record<ConcessionStatus, StatusPresentation>
> = {
  active: { labelKey: 'status.concession.active', severity: 'ok' },
  delinquent: { labelKey: 'status.concession.delinquent', severity: 'danger' },
  grace: { labelKey: 'status.concession.grace', severity: 'warn' },
  reverted: { labelKey: 'status.concession.reverted', severity: 'danger' },
}

export const ACCEPTANCE_STATUS_PRESENTATION: Readonly<
  Record<AcceptanceStatus, StatusPresentation>
> = {
  pending_draw: { labelKey: 'status.acceptance.pending_draw', severity: 'busy' },
  served: { labelKey: 'status.acceptance.served', severity: 'ok' },
  released: { labelKey: 'status.acceptance.released', severity: 'warn' },
}

/**
 * Severidad visual del estado físico (`conditionPct`, 0–100) de un edificio:
 * el deterioro por mantenimiento impagado (GDD §5.9, 3er escalón) que antecede
 * al abandono. Umbrales de presentación del cliente (el servidor no publica
 * los suyos): < 25 crítico, < 50 atención, resto normal.
 */
export function conditionSeverity(pct: number): StatusSeverity {
  if (pct < 25) return 'danger'
  if (pct < 50) return 'warn'
  return 'ok'
}

export function buildingStatusPresentation(status: BuildingStatus): StatusPresentation {
  return BUILDING_STATUS_PRESENTATION[status]
}

export function batchStatusPresentation(status: BatchStatus): StatusPresentation {
  return BATCH_STATUS_PRESENTATION[status]
}

export function contractStatusPresentation(status: ContractStatus): StatusPresentation {
  return CONTRACT_STATUS_PRESENTATION[status]
}

export function publicationStatusPresentation(status: PublicationStatus): StatusPresentation {
  return PUBLICATION_STATUS_PRESENTATION[status]
}

export function vehicleStatusPresentation(status: VehicleStatus): StatusPresentation {
  return VEHICLE_STATUS_PRESENTATION[status]
}

export function shipmentStatusPresentation(status: ShipmentStatus): StatusPresentation {
  return SHIPMENT_STATUS_PRESENTATION[status]
}

export function concessionStatusPresentation(status: ConcessionStatus): StatusPresentation {
  return CONCESSION_STATUS_PRESENTATION[status]
}

export function acceptanceStatusPresentation(status: AcceptanceStatus): StatusPresentation {
  return ACCEPTANCE_STATUS_PRESENTATION[status]
}
