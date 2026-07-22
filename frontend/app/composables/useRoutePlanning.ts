/**
 * app/composables/useRoutePlanning — planificación de rutas compartida.
 *
 * Flujo común de DispatchDialog, RepositionDialog y la estimación de trayecto
 * de AcceptDialog: `POST /logistics/route-plans` (solo cálculo, ETAs
 * informativas — no garantías) y, si el flujo lo confirma, creación de la
 * ruta `on_demand` con los legs del plan (aplicada a logistics.store). El
 * cliente nunca inventa rutas: presenta el plan del servidor tal cual.
 */

import { ref } from 'vue'
import type { Ref } from 'vue'
import { t } from '~shared/i18n'
import type { LinkMode, NodeId } from '~domain/logistics'
import type { Quantity } from '~domain/quantity'
import type { RoutePlanDto, RouteDto } from '~network/logistics.api'
import { mapRoute } from '~network/mappers/domain.mapper'
import { useGameApis } from '~/composables/useGameApis'
import { useLogisticsStore } from '~/stores/logistics.store'

export interface PlanBetweenOptions {
  /** Volumen de carga a mover (dimensiona el coste estimado del plan). */
  readonly cargoVolume?: Quantity
  /** Restringe los modos permitidos (p. ej. solo el modo del vehículo). */
  readonly modes?: readonly LinkMode[]
}

export interface RoutePlanning {
  readonly plan: Readonly<Ref<RoutePlanDto | null>>
  readonly planning: Readonly<Ref<boolean>>
  readonly planError: Readonly<Ref<unknown>>
  planBetween(origin: NodeId, destination: NodeId, options?: PlanBetweenOptions): Promise<void>
  /**
   * Crea la ruta `on_demand` con los legs del plan vigente y la aplica a
   * logistics.store. Lanza si no hay plan (guardas del caller).
   */
  createOnDemandRoute(name: string): Promise<RouteDto>
  reset(): void
}

export function useRoutePlanning(): RoutePlanning {
  const apis = useGameApis()
  const logistics = useLogisticsStore()

  const plan = ref<RoutePlanDto | null>(null)
  const planning = ref(false)
  const planError = ref<unknown>(null)

  async function planBetween(
    origin: NodeId,
    destination: NodeId,
    options: PlanBetweenOptions = {},
  ): Promise<void> {
    planning.value = true
    planError.value = null
    plan.value = null
    try {
      plan.value = await apis.logistics.planRoute({
        origin_node_id: origin,
        destination_node_id: destination,
        optimize: 'time',
        ...(options.cargoVolume === undefined ? {} : { cargo_volume: options.cargoVolume }),
        ...(options.modes === undefined ? {} : { modes: [...options.modes] }),
      })
    } catch (error) {
      planError.value = error
    } finally {
      planning.value = false
    }
  }

  async function createOnDemandRoute(name: string): Promise<RouteDto> {
    const currentPlan = plan.value
    if (currentPlan === null) {
      throw new Error('useRoutePlanning.createOnDemandRoute: sin plan vigente')
    }
    const routeDto = await apis.logistics.createRoute({
      name,
      kind: 'on_demand',
      legs: currentPlan.legs.map((leg) => leg.link_id),
    })
    logistics.applyRoute(mapRoute(routeDto))
    return routeDto
  }

  function reset(): void {
    plan.value = null
    planError.value = null
  }

  return { plan, planning, planError, planBetween, createOnDemandRoute, reset }
}

/** ETA total de un plan como duración sim legible (días/horas de juego). */
export function totalEtaText(plan: RoutePlanDto): string {
  const totalSeconds = plan.total_eta_sim_seconds
  const days = Math.floor(totalSeconds / 86_400)
  const hours = Math.floor((totalSeconds % 86_400) / 3_600)
  return t('simtime.remaining', { days, hours })
}

/** Duración estimada de un tramo, en horas de juego (presentación). */
export function legEtaText(seconds: number): string {
  return t('market.col.deliveryHours', { hours: Math.max(1, Math.round(seconds / 3_600)) })
}
