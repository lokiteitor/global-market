import { describe, expect, it } from 'vitest'

import { simTime } from '~shared/simtime'
import { AppError } from '../rest'
import type { ProductDto, RegionDto } from '../world.api'
import type { VehicleDto } from '../fleet.api'
import type { PublicationDto } from '../market.api'
import { mapProduct, mapPublication, mapRegion, mapVehicle } from './domain.mapper'

const UUID_A = '01981c5e-84b6-7c2a-8d3f-5b7a9c1e3f04'
const UUID_B = '01981c5e-91d8-7e4b-a2c6-4d8e0f6a2b93'
const UUID_C = '01981c5e-91d8-7e4b-a2c6-4d8e0f6a2b94'

function regionDto(): RegionDto {
  return {
    id: UUID_A,
    name: 'Askadia Norte',
    grid_x: 0,
    grid_y: 0,
    bounds: {
      type: 'Polygon',
      coordinates: [
        [
          [0, 0],
          [50_000, 0],
          [50_000, 50_000],
          [0, 50_000],
          [0, 0],
        ],
      ],
    },
    biome: 'plains',
    tax_rate_bp: 500,
    customs_rate_bp: 200,
    canon_base: '0001000',
    opened_at_sim: 0,
  }
}

describe('network/mappers/domain.mapper', () => {
  it('mapRegion: ids con brand, Money canónico, bounds a metros de mundo', () => {
    const region = mapRegion(regionDto())

    expect(region.id).toBe(UUID_A)
    // parseMoney canonicaliza ceros a la izquierda.
    expect(region.canonBase).toBe('1000')
    expect(region.boundsM).not.toBeNull()
    expect(region.boundsM?.[0]?.[1]).toEqual([50_000, 0])
    expect(region.openedAtSim).toBe(simTime(0))
  })

  it('mapProduct: importe fuera de contrato ⇒ AppError kind protocol', () => {
    const dto: ProductDto = {
      id: UUID_A,
      code: 'IRON',
      name: 'Hierro',
      class: 'basic',
      unit_volume: 1,
      base_price: '12.5', // decimal: viola el patrón de MoneyAmount
      price_floor: '1',
      price_ceiling: '100',
      is_fuel: false,
    }

    let caught: unknown = null
    try {
      mapProduct(dto)
    } catch (error) {
      caught = error
    }
    expect(caught).toBeInstanceOf(AppError)
    expect((caught as AppError).kind).toBe('protocol')
  })

  it('mapVehicle: posición on-segment y observedAtSim del fallback si no llegó', () => {
    const dto: VehicleDto = {
      id: UUID_A,
      vehicle_type_id: UUID_B,
      owner_account_id: UUID_C,
      status: 'in_transit',
      wear_pct: 10,
      fuel: '250',
      position: {
        on_segment_id: UUID_B,
        segment_progress_pct: 40,
        location: { type: 'Point', coordinates: [1_000, 2_000] },
      },
    }

    const vehicle = mapVehicle(dto, simTime(9_000))

    expect(vehicle.position.kind).toBe('on-segment')
    expect(vehicle.position.kind === 'on-segment' && vehicle.position.progressPct).toBe(40)
    expect(vehicle.position.locationM).toEqual([1_000, 2_000])
    expect(vehicle.observedAtSim).toBe(simTime(9_000))
    expect(vehicle.routeId).toBeNull()
  })

  it('mapPublication: instantes date-time a ms de epoch y opcionales a null', () => {
    const dto: PublicationDto = {
      id: UUID_A,
      kind: 'sell',
      publisher_account_id: UUID_B,
      channel: 'board',
      product_id: UUID_C,
      quantity_total: '500',
      quantity_remaining: '300',
      unit_price: '120',
      min_lot: '50',
      origin_node_id: UUID_B,
      delivery_sim_seconds: 172_800,
      status: 'draw_window',
      window_closes_at: '2026-07-20T10:00:00Z',
      published_at_sim: 1_000,
    }

    const publication = mapPublication(dto)

    expect(publication.windowClosesAtMs).toBe(Date.parse('2026-07-20T10:00:00Z'))
    expect(publication.cancelCooldownUntilMs).toBeNull()
    expect(publication.destinationNodeId).toBeNull()
    expect(publication.declaredValue).toBeNull()
    expect(publication.quantityRemaining).toBe('300')
  })
})
