import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useLogisticsStore } from './logistics.store'
import { link, node, route, segment, uid } from './testing/fixtures'

const REGION_A = uid<'Region'>(1)
const REGION_B = uid<'Region'>(2)
const NODE_1 = uid<'Node'>(100)
const NODE_2 = uid<'Node'>(101)
const NODE_3 = uid<'Node'>(102)

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/logistics.store — nodos', () => {
  it('índices por región/tipo y únicos por edificio/ciudad', () => {
    const store = useLogisticsStore()
    const warehouse = node({ id: NODE_1, regionId: REGION_A, kind: 'warehouse' })
    const gate = node({
      id: NODE_2,
      regionId: REGION_A,
      kind: 'city_gate',
      cityId: uid<'City'>(50),
    })
    const factory = node({
      id: NODE_3,
      regionId: REGION_B,
      kind: 'factory',
      buildingId: uid<'Building'>(70),
    })
    store.applyNodesSnapshot([warehouse, gate, factory])

    expect(store.nodeIdsByRegion[REGION_A]).toEqual([NODE_1, NODE_2])
    expect(store.nodeIdsByKind['factory']).toEqual([NODE_3])
    expect(store.nodeIdByBuilding[uid<'Building'>(70)]).toBe(NODE_3)
    expect(store.nodeIdByCity[uid<'City'>(50)]).toBe(NODE_2)
  })

  it('applyRegionNodesSnapshot reemplaza SOLO la región indicada', () => {
    const store = useLogisticsStore()
    const a1 = node({ id: NODE_1, regionId: REGION_A })
    const a2 = node({ id: NODE_2, regionId: REGION_A })
    const b1 = node({ id: NODE_3, regionId: REGION_B })
    store.applyNodesSnapshot([a1, a2, b1])

    store.applyRegionNodesSnapshot(REGION_A, [a1])
    expect(store.getNode(NODE_2)).toBeNull()
    expect(store.getNode(NODE_3)).toEqual(b1)

    // Idempotente.
    store.applyRegionNodesSnapshot(REGION_A, [a1])
    expect(store.nodeList).toHaveLength(2)
  })
})

describe('app/stores/logistics.store — enlaces y segmentos', () => {
  it('linkIdsByNode indexa ambos extremos y linksAtNode resuelve', () => {
    const store = useLogisticsStore()
    const l1 = link({ id: uid<'Link'>(120), fromNodeId: NODE_1, toNodeId: NODE_2 })
    const l2 = link({ id: uid<'Link'>(121), fromNodeId: NODE_2, toNodeId: NODE_3 })
    store.applyLinksSnapshot([l1, l2])

    expect(store.linkIdsByNode[NODE_1]).toEqual([l1.id])
    expect(store.linkIdsByNode[NODE_2]).toEqual([l1.id, l2.id])
    expect(store.linksAtNode(NODE_2)).toEqual([l1, l2])
    expect(store.linksAtNode(uid<'Node'>(999))).toEqual([])
  })

  it('segmentContext resuelve segmento + enlace dueño (insumo de kinematics)', () => {
    const store = useLogisticsStore()
    const s1 = segment({ id: uid<'Segment'>(110), seq: 0, congestionEma: 1.4 })
    const s2 = segment({ id: uid<'Segment'>(111), seq: 1 })
    const l1 = link({ id: uid<'Link'>(120), baseSpeedKmh: 80, segments: [s1, s2] })
    store.applyLink(l1)

    const context = store.segmentContext(s2.id)
    expect(context?.link.baseSpeedKmh).toBe(80)
    expect(context?.segment).toEqual(s2)
    expect(store.segmentContext(uid<'Segment'>(999))).toBeNull()
  })

  it('mergeLinks upserta sin bajas (refresco suave de congestión)', () => {
    const store = useLogisticsStore()
    const l1 = link({ id: uid<'Link'>(120) })
    const l2 = link({
      id: uid<'Link'>(121),
      fromNodeId: NODE_2,
      toNodeId: NODE_3,
      segments: [segment({ id: uid<'Segment'>(115) })],
    })
    store.applyLinksSnapshot([l1, l2])

    const refreshed = link({
      id: uid<'Link'>(120),
      segments: [segment({ id: uid<'Segment'>(110), congestionEma: 3.5 })],
    })
    store.mergeLinks([refreshed])
    store.mergeLinks([refreshed])

    expect(store.linkList).toHaveLength(2)
    expect(store.segmentContext(uid<'Segment'>(110))?.segment.congestionEma).toBe(3.5)
  })
})

describe('app/stores/logistics.store — rutas propias', () => {
  it('activeRoutes filtra por bandera activa; tríada idempotente', () => {
    const store = useLogisticsStore()
    const active = route({ id: uid<'Route'>(130), active: true })
    const inactive = route({ id: uid<'Route'>(131), active: false })
    store.applyRoutesSnapshot([active, inactive])
    store.applyRoutesSnapshot([active, inactive])

    expect(store.routeList).toHaveLength(2)
    expect(store.activeRoutes).toEqual([active])

    store.removeRoute(active.id)
    store.removeRoute(active.id)
    expect(store.activeRoutes).toEqual([])
  })

  it('clear purga el grafo y las rutas', () => {
    const store = useLogisticsStore()
    store.applyNode(node())
    store.applyLink(link())
    store.applyRoute(route())
    store.clear()
    expect(store.nodeList).toHaveLength(0)
    expect(store.linkList).toHaveLength(0)
    expect(store.routeList).toHaveLength(0)
  })
})
