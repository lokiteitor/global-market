import { describe, expect, it } from 'vitest'

import type { SegmentId } from '~domain/logistics'

import {
  building,
  city,
  deposit,
  link,
  node,
  region,
  segment,
  st,
  uid,
  vehicle,
  MY_ACCOUNT,
  OTHER_ACCOUNT,
} from '../testing/fixtures'
import type { StaticsInput } from './derive'
import {
  deriveStatics,
  deriveVehicles,
  linkCongestionTier,
  poseAlongPath,
  vehiclePose,
} from './derive'
import type { SegmentContextInfo } from './source'
import type { WorldRectM } from './vm'

const VIEW: WorldRectM = { xM: 0, yM: 0, widthM: 5_000, heightM: 5_000 }

const staticsInput = (over: Partial<StaticsInput> = {}): StaticsInput => ({
  regions: [],
  cities: [],
  deposits: [],
  nodes: [],
  links: [],
  buildings: [],
  buildingTypeCode: () => 'MINE',
  ownAccountId: MY_ACCOUNT,
  ...over,
})

describe('game/bridge/derive — culling por rect en metros', () => {
  it('edificio dentro del viewport ⇒ VM con bbox del footprint; fuera ⇒ excluido', () => {
    const inside = building({ id: uid<'Building'>(70) })
    const outside = building({
      id: uid<'Building'>(71),
      footprintM: [
        [
          [40_000, 40_000],
          [40_250, 40_000],
          [40_250, 40_250],
          [40_000, 40_250],
          [40_000, 40_000],
        ],
      ],
    })
    const vms = deriveStatics(staticsInput({ buildings: [inside, outside] }), VIEW)
    expect([...vms.buildings.keys()]).toEqual([inside.id])
    expect(vms.buildings.get(inside.id)).toEqual({
      id: inside.id,
      xM: 1_000,
      yM: 1_000,
      wM: 250,
      hM: 250,
      status: 'operational',
      typeCode: 'MINE',
      own: true,
    })
  })

  it('own=false para edificios ajenos', () => {
    const foreign = building({ ownerAccountId: OTHER_ACCOUNT })
    const vms = deriveStatics(staticsInput({ buildings: [foreign] }), VIEW)
    expect(vms.buildings.get(foreign.id)?.own).toBe(false)
  })

  it('el margen evita el pop: entidad justo fuera del rect pero dentro del margen entra', () => {
    const nearEdge = deposit({ locationM: [5_400, 100] }) // margen default 500 m
    const farAway = deposit({ id: uid<'Deposit'>(41), locationM: [6_000, 100] })
    const vms = deriveStatics(staticsInput({ deposits: [nearEdge, farAway] }), VIEW)
    expect([...vms.deposits.keys()]).toEqual([nearEdge.id])
  })

  it('la ciudad se culla por su alcance (influencia): centro fuera del view pero radio dentro ⇒ visible', () => {
    const reaching = city({ locationM: [9_000, 2_500], influenceRadiusM: 5_000 })
    const tiny = city({ id: uid<'City'>(51), locationM: [9_000, 2_500], influenceRadiusM: 100 })
    const vms = deriveStatics(staticsInput({ cities: [reaching, tiny] }), VIEW)
    expect(vms.cities.has(reaching.id)).toBe(true)
    expect(vms.cities.has(tiny.id)).toBe(false)
  })

  it('nodos y regiones se cullan; la región sin bounds no produce VM', () => {
    const visibleNode = node()
    const hiddenNode = node({ id: uid<'Node'>(102), locationM: [30_000, 30_000] })
    const boundless = region({ id: uid<'Region'>(2), boundsM: null })
    const vms = deriveStatics(
      staticsInput({ nodes: [visibleNode, hiddenNode], regions: [region(), boundless] }),
      VIEW,
    )
    expect([...vms.nodes.keys()]).toEqual([visibleNode.id])
    expect([...vms.regions.keys()]).toEqual([region().id])
    expect(vms.regions.get(region().id)).toMatchObject({ xM: 0, yM: 0, wM: 10_000, hM: 10_000 })
  })
})

describe('game/bridge/derive — enlaces', () => {
  it('tier del enlace = PEOR segmento', () => {
    const busy = link({
      segments: [segment(), segment({ id: uid<'Segment'>(111), seq: 1, congestionEma: 1.5 })],
    })
    expect(linkCongestionTier(busy)).toBe('busy')
    const jammed = link({
      segments: [segment({ congestionEma: 2.4 }), segment({ id: uid<'Segment'>(111), seq: 1 })],
    })
    expect(linkCongestionTier(jammed)).toBe('jammed')
  })

  it('enlace sin path usa la recta fromNode → toNode; sin nodos conocidos se omite', () => {
    const a = node({ id: uid<'Node'>(100), locationM: [100, 100] })
    const b = node({ id: uid<'Node'>(101), locationM: [2_000, 2_000] })
    const withNodes = deriveStatics(
      staticsInput({ nodes: [a, b], links: [link({ pathM: null })] }),
      VIEW,
    )
    expect(withNodes.links.get(link().id)?.points).toEqual([
      [100, 100],
      [2_000, 2_000],
    ])

    const withoutNodes = deriveStatics(staticsInput({ links: [link({ pathM: null })] }), VIEW)
    expect(withoutNodes.links.size).toBe(0)
  })
})

describe('game/bridge/derive — poseAlongPath', () => {
  it('interpola por longitud acumulada y devuelve el ángulo del tramo', () => {
    const path: (readonly [number, number])[] = [
      [0, 0],
      [1_000, 0],
      [1_000, 1_000],
    ]
    const mid = poseAlongPath(path, 0.5)
    expect(mid?.point).toEqual([1_000, 0])
    const inSecond = poseAlongPath(path, 0.75)
    expect(inSecond?.point[0]).toBeCloseTo(1_000)
    expect(inSecond?.point[1]).toBeCloseTo(500)
    expect(inSecond?.angleRad).toBeCloseTo(Math.PI / 2) // hacia +Y
  })

  it('fracción 1 cae en el último vértice con el ángulo del último tramo', () => {
    const pose = poseAlongPath(
      [
        [0, 0],
        [100, 0],
      ],
      1,
    )
    expect(pose?.point).toEqual([100, 0])
    expect(pose?.angleRad).toBeCloseTo(0)
  })

  it('camino vacío ⇒ null; un vértice ⇒ ese punto', () => {
    expect(poseAlongPath([], 0.5)).toBeNull()
    expect(poseAlongPath([[7, 9]], 0.5)?.point).toEqual([7, 9])
  })
})

describe('game/bridge/derive — vehículos (extrapolación analítica, FAD §11.7)', () => {
  const SEGMENT_ID = uid<'Segment'>(110)
  const theLink = link() // 10 000 m, 60 km/h, congestión 1 ⇒ 600 sim-s de recorrido
  const context: SegmentContextInfo = { link: theLink, segment: theLink.segments[0]! }
  const segmentContext = (id: SegmentId): SegmentContextInfo | null =>
    id === SEGMENT_ID ? context : null

  const onSegment = vehicle({
    status: 'in_transit',
    position: { kind: 'on-segment', segmentId: SEGMENT_ID, progressPct: 0, locationM: null },
    observedAtSim: st(1_000),
  })

  it('extrapola el progreso con simNow y lo interpola sobre el path del enlace', () => {
    // 300 sim-s después de la observación: 50% de 10 km ⇒ x = 5 000 m.
    const pose = vehiclePose(onSegment, segmentContext, () => null, st(1_300))
    expect(pose?.point[0]).toBeCloseTo(5_000)
    expect(pose?.point[1]).toBeCloseTo(0)
    expect(pose?.angleRad).toBeCloseTo(0)
  })

  it('el progreso es DENTRO del segmento: se desplaza por el prefijo de segmentos previos', () => {
    const seg0 = segment({ id: uid<'Segment'>(115), seq: 0, lengthM: 4_000 })
    const seg1 = segment({ id: uid<'Segment'>(116), seq: 1, lengthM: 6_000 })
    const twoSegs = link({ segments: [seg0, seg1] })
    const v = vehicle({
      position: { kind: 'on-segment', segmentId: seg1.id, progressPct: 50, locationM: null },
      observedAtSim: st(1_000),
    })
    const pose = vehiclePose(
      v,
      () => ({ link: twoSegs, segment: seg1 }),
      () => null,
      st(1_000), // sin transcurso: usa el progreso observado
    )
    // prefijo 4 000 + 50% de 6 000 = 7 000 de 10 000 ⇒ x = 7 000 m.
    expect(pose?.point[0]).toBeCloseTo(7_000)
  })

  it('sin sim-now (reloj sin anclar) no extrapola: posición observada', () => {
    const pose = vehiclePose(onSegment, segmentContext, () => null, null)
    expect(pose?.point[0]).toBeCloseTo(0)
  })

  it('sin contexto de segmento usa las coordenadas derivadas del servidor, o nada', () => {
    const withFallback = vehicle({
      position: {
        kind: 'on-segment',
        segmentId: uid<'Segment'>(999),
        progressPct: 10,
        locationM: [123, 456],
      },
    })
    expect(
      vehiclePose(
        withFallback,
        () => null,
        () => null,
        st(2_000),
      )?.point,
    ).toEqual([123, 456])

    const noData = vehicle({
      position: {
        kind: 'on-segment',
        segmentId: uid<'Segment'>(999),
        progressPct: 10,
        locationM: null,
      },
    })
    expect(
      vehiclePose(
        noData,
        () => null,
        () => null,
        st(2_000),
      ),
    ).toBeNull()
  })

  it('at-node: usa locationM del contrato o el nodo del grafo local', () => {
    const atNode = vehicle()
    expect(vehiclePose(atNode, segmentContext, () => null, st(2_000))?.point).toEqual([
      1_000, 1_000,
    ])

    const noLocation = vehicle({
      position: { kind: 'at-node', nodeId: node().id, locationM: null },
    })
    expect(
      vehiclePose(noLocation, segmentContext, () => node({ locationM: [42, 43] }), st(2_000))
        ?.point,
    ).toEqual([42, 43])
  })

  it('deriveVehicles culla por posición extrapolada y marca own', () => {
    const visible = vehicle({ id: uid<'Vehicle'>(140) })
    const outside = vehicle({
      id: uid<'Vehicle'>(141),
      ownerAccountId: OTHER_ACCOUNT,
      position: { kind: 'at-node', nodeId: uid<'Node'>(100), locationM: [40_000, 40_000] },
    })
    const vms = deriveVehicles(
      {
        vehicles: [visible, outside],
        segmentContext,
        nodeById: () => null,
        ownAccountId: MY_ACCOUNT,
        simNow: st(1_000),
      },
      VIEW,
    )
    expect([...vms.keys()]).toEqual([visible.id])
    expect(vms.get(visible.id)).toMatchObject({ xM: 1_000, yM: 1_000, own: true, status: 'idle' })
  })
})
