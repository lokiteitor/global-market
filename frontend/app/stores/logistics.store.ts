/**
 * app/stores/logistics.store — bounded context Logistics (FAD §9.1, §20).
 *
 * Grafo logístico observado (nodos y enlaces con congestión por segmento) y
 * rutas PROPIAS. Estado replicado con la tríada idempotente. El grafo se pulla
 * completo o por región (`applyRegion*Snapshot` reemplaza solo ese subárbol;
 * `mergeLinks` upserta sin bajas para refrescos suaves de congestión).
 *
 * `segmentContext` resuelve el insumo de la extrapolación cinemática
 * (domain/kinematics): segmento + enlace dueño (velocidad base + path).
 */

import { computed } from 'vue'
import { defineStore } from 'pinia'
import type {
  LinkId,
  LinkSegment,
  NetworkLink,
  NetworkNode,
  NodeId,
  Route,
  RouteId,
  SegmentId,
} from '~domain/logistics'
import type { RegionId } from '~domain/world'
import { createEntityCollection, indexBy, uniqueIndexBy } from './entity-collection'

/** Segmento resuelto junto a su enlace dueño (insumo de domain/kinematics). */
export interface SegmentContext {
  readonly link: NetworkLink
  readonly segment: LinkSegment
}

export const useLogisticsStore = defineStore('logistics', () => {
  const nodes = createEntityCollection<NodeId, NetworkNode>((n) => n.id)
  const links = createEntityCollection<LinkId, NetworkLink>((l) => l.id)
  const routes = createEntityCollection<RouteId, Route>((r) => r.id)

  // Índices derivados.
  const nodeIdsByRegion = indexBy(nodes, (n) => n.regionId)
  const nodeIdsByKind = indexBy(nodes, (n) => n.kind)
  const nodeIdByBuilding = uniqueIndexBy(nodes, (n) => n.buildingId)
  const nodeIdByCity = uniqueIndexBy(nodes, (n) => n.cityId)

  /** Adyacencia: enlaces que tocan cada nodo (ambos extremos). */
  const linkIdsByNode = computed(() => {
    const index: Record<NodeId, LinkId[]> = {}
    for (const link of links.list.value) {
      ;(index[link.fromNodeId] ??= []).push(link.id)
      if (link.toNodeId !== link.fromNodeId) {
        ;(index[link.toNodeId] ??= []).push(link.id)
      }
    }
    return index as Readonly<Record<NodeId, readonly LinkId[]>>
  })

  /** Índice segmento → id del enlace dueño. */
  const linkIdBySegment = computed(() => {
    const index: Record<SegmentId, LinkId> = {}
    for (const link of links.list.value) {
      for (const segment of link.segments) {
        index[segment.id] = link.id
      }
    }
    return index as Readonly<Record<SegmentId, LinkId>>
  })

  function linksAtNode(nodeId: NodeId): readonly NetworkLink[] {
    return (linkIdsByNode.value[nodeId] ?? []).flatMap((id) => {
      const link = links.get(id)
      return link === null ? [] : [link]
    })
  }

  /** Segmento + enlace dueño, o `null` si el grafo local aún no lo tiene. */
  function segmentContext(segmentId: SegmentId): SegmentContext | null {
    const link = links.get(linkIdBySegment.value[segmentId] ?? null)
    if (link === null) {
      return null
    }
    const segment = link.segments.find((s) => s.id === segmentId)
    return segment === undefined ? null : { link, segment }
  }

  /** Reemplaza los nodos de UNA región conservando el resto (pull por región). */
  function applyRegionNodesSnapshot(regionId: RegionId, items: readonly NetworkNode[]): void {
    nodes.applyScopedSnapshot((n) => n.regionId === regionId, items)
  }

  /** Rutas propias activas (asignables a vehículos). */
  const activeRoutes = computed(() => routes.list.value.filter((r) => r.active))

  function clear(): void {
    nodes.clear()
    links.clear()
    routes.clear()
  }

  return {
    // Nodos
    nodeById: nodes.byId,
    nodeList: nodes.list,
    getNode: nodes.get,
    applyNodesSnapshot: nodes.applySnapshot,
    applyRegionNodesSnapshot,
    applyNode: nodes.applyOne,
    removeNode: nodes.remove,
    nodeIdsByRegion,
    nodeIdsByKind,
    nodeIdByBuilding,
    nodeIdByCity,
    // Enlaces
    linkById: links.byId,
    linkList: links.list,
    getLink: links.get,
    applyLinksSnapshot: links.applySnapshot,
    mergeLinks: links.applyMany,
    applyLink: links.applyOne,
    removeLink: links.remove,
    linkIdsByNode,
    linkIdBySegment,
    linksAtNode,
    segmentContext,
    // Rutas propias
    routeById: routes.byId,
    routeList: routes.list,
    getRoute: routes.get,
    applyRoutesSnapshot: routes.applySnapshot,
    applyRoute: routes.applyOne,
    removeRoute: routes.remove,
    activeRoutes,
    // Global
    clear,
  }
})
