/**
 * app/composables/useMyNodes — nodos logísticos "míos" para formularios.
 *
 * El grafo es de uso común; "mis nodos" son los ligados a MIS edificios
 * (buildings.store solo replica edificios propios, por contrato). Son los
 * candidatos válidos de origen/destino en publicaciones, entrega de vehículos
 * y despacho. Incluye un descriptor legible para selects.
 */

import { computed } from 'vue'
import type { ComputedRef } from 'vue'
import { t } from '~shared/i18n'
import type { NetworkNode } from '~domain/logistics'
import { NODE_KIND_LABEL } from '~/components/play/labels'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useLogisticsStore } from '~/stores/logistics.store'
import { useWorldStore } from '~/stores/world.store'

export interface MyNodesApi {
  readonly myNodes: ComputedRef<readonly NetworkNode[]>
  describeNode(node: NetworkNode): string
  /**
   * Descriptor de CUALQUIER nodo del grafo (no solo propios): clase + región
   * (+ ciudad si la tiene). Para selects de destino de flete/reposicionamiento,
   * donde el destino puede ser ajeno.
   */
  describeAnyNode(node: NetworkNode): string
}

export function useMyNodes(): MyNodesApi {
  const logistics = useLogisticsStore()
  const buildings = useBuildingsStore()
  const world = useWorldStore()

  const myNodes = computed(() =>
    logistics.nodeList.filter(
      (node) => node.buildingId !== null && buildings.getBuilding(node.buildingId) !== null,
    ),
  )

  function describeNode(node: NetworkNode): string {
    const kindLabel = t(NODE_KIND_LABEL[node.kind])
    const building = buildings.getBuilding(node.buildingId)
    const typeName = world.getBuildingType(building?.buildingTypeId ?? null)?.name
    return typeName === undefined ? kindLabel : `${kindLabel} — ${typeName}`
  }

  function describeAnyNode(node: NetworkNode): string {
    const kindLabel = t(NODE_KIND_LABEL[node.kind])
    const cityName = node.cityId === null ? undefined : world.getCity(node.cityId)?.name
    const regionName = world.getRegion(node.regionId)?.name
    const place = cityName ?? regionName
    return place === undefined ? kindLabel : `${kindLabel} — ${place}`
  }

  return { myNodes, describeNode, describeAnyNode }
}
