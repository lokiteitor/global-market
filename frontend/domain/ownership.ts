/**
 * domain/ownership — OwnershipPolicy pura (FAD §5.3, §15.9, §24.3).
 *
 * Distinción Observable vs Comandable: TODO lo del área de interés se ve;
 * solo lo PROPIO se comanda. La UI deshabilita preventivamente los controles
 * de mando sobre entidades ajenas (con tooltip explicativo) y el servidor
 * revalida con 403. Esto NO es seguridad (la da el servidor): es UX honesta —
 * no ofrecer lo que se va a rechazar.
 *
 * Política pura: sin Vue/Pinia/Phaser, sin estado. La UI le pasa el
 * `myAccountId` de la sesión.
 */

import type { AccountId } from './auth'
import type { VehicleStatus } from './fleet'

/**
 * Forma mínima de una entidad con propietario. El campo es opcional/null
 * porque las entidades del mundo sin dueño (nodos del sistema, ciudades,
 * yacimientos) son observables pero jamás comandables.
 */
export interface Ownable {
  readonly ownerAccountId?: AccountId | null
}

/**
 * ¿Pertenece este owner a mi corporación? Núcleo de la política, aplicable a
 * cualquier campo de titularidad del dominio (`ownerAccountId`,
 * `holderAccountId` de concesiones, `publisherAccountId` de publicaciones,
 * `acceptorAccountId` de aceptaciones…).
 *
 * Sin sesión (`myAccountId === null`) nada es comandable; sin dueño tampoco.
 */
export function isMine(
  ownerAccountId: AccountId | null | undefined,
  myAccountId: AccountId | null,
): boolean {
  return (
    myAccountId !== null &&
    ownerAccountId !== null &&
    ownerAccountId !== undefined &&
    ownerAccountId === myAccountId
  )
}

/**
 * ¿Puede la UI ofrecer affordances de mando sobre esta entidad?
 * `true` ⇔ la entidad tiene dueño y ese dueño soy yo.
 */
export function isCommandable(entity: Ownable, myAccountId: AccountId | null): boolean {
  return isMine(entity.ownerAccountId, myAccountId)
}

/**
 * Caso especial del contrato: un vehículo `sealed` (handoff entre shards) es
 * "visible pero no comandable" AUNQUE sea propio — el servidor respondería
 * VEHICLE_SEALED. La UI lo deshabilita preventivamente igual que lo ajeno.
 */
export function isVehicleCommandable(
  vehicle: Ownable & { readonly status: VehicleStatus },
  myAccountId: AccountId | null,
): boolean {
  return isCommandable(vehicle, myAccountId) && vehicle.status !== 'sealed'
}
