/**
 * game/bridge/source — puerto de ESTADO REPLICADO para el bridge (FAD §11.6).
 *
 * game/ jamás importa Pinia ni app/ (frontera en ESLint): el bridge consume
 * este puerto de SOLO LECTURA sobre el estado replicado, y la capa app lo
 * implementa sobre sus stores (buildings, fleet, logistics, world, cadastre,
 * session) + el SimClock (app/composables/useWorldLive.ts). Los getters
 * devuelven snapshots de dominio inmutables (las stores reemplazan objetos,
 * nunca mutan) — el bridge puede comparar por referencia con seguridad.
 */

import type { SimTime } from '~shared/simtime'
import type { AccountId } from '~domain/auth'
import type { Building } from '~domain/buildings'
import type { Concession } from '~domain/cadastre'
import type { Vehicle } from '~domain/fleet'
import type { LinkSegment, NetworkLink, NetworkNode, SegmentId } from '~domain/logistics'
import type { BuildingTypeId, City, Region, ResourceDeposit } from '~domain/world'

/** Segmento resuelto junto a su enlace dueño (insumo de domain/kinematics). */
export interface SegmentContextInfo {
  readonly link: NetworkLink
  readonly segment: LinkSegment
}

export interface WorldStateSource {
  readonly regions: () => readonly Region[]
  readonly cities: () => readonly City[]
  readonly deposits: () => readonly ResourceDeposit[]
  readonly nodes: () => readonly NetworkNode[]
  readonly links: () => readonly NetworkLink[]
  readonly buildings: () => readonly Building[]
  readonly vehicles: () => readonly Vehicle[]
  /**
   * Concesiones propias (cadastre). El bridge aún no deriva VMs de parcela
   * (llegarán con la UI de concesiones); el puerto ya las expone y el
   * `subscribe` de la app cubre la store de cadastre.
   */
  readonly concessions: () => readonly Concession[]
  /** Código de catálogo de un tipo de edificio (textura por tipo), o `null`. */
  readonly buildingTypeCode: (id: BuildingTypeId) => string | null
  /** Segmento + enlace dueño, o `null` si el grafo local aún no lo tiene. */
  readonly segmentContext: (segmentId: SegmentId) => SegmentContextInfo | null
  /** Cuenta de la sesión (decide `own`), o `null` sin sesión. */
  readonly ownAccountId: () => AccountId | null
  /** Sim-time actual del SimClock único, o `null` si aún no está anclado. */
  readonly simNow: () => SimTime | null
  /**
   * Notifica CUALQUIER cambio del estado replicado observado (las stores solo
   * cambian aplicando respuestas/eventos del servidor). Devuelve la baja.
   * El bridge NO recomputa aquí: marca dirty y coalesce a ≤1 recomputación
   * por frame (FAD §11.6).
   */
  readonly subscribe: (listener: () => void) => () => void
}
