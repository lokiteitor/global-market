import { describe, expect, it } from 'vitest'

import { createFleetApi } from '~network/fleet.api'
import type { Enveloped, RequestSpec, RestClient } from '~network/rest'
import { AppError } from '~network/rest'

const META = {
  simTimeSeconds: null,
  simTimeLabel: '360-045-12:30',
  serverTimeMs: Date.parse('2026-07-16T12:30:00Z'),
  nextCursor: null,
} as const

const VEHICLE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f030'
const VEHICLE_TYPE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f031'
const NODE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f032'
const SHIPMENT_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f033'
const ROUTE_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f034'

/** Doble del PUERTO RestClient: sirve data programada y registra specs. */
function fakeRest(data: unknown, nextCursor: string | null = null) {
  const requested: RequestSpec[] = []
  const rest: RestClient = {
    request<TData>(spec: RequestSpec): Promise<Enveloped<TData>> {
      requested.push(spec)
      return Promise.resolve({ data: data as TData, meta: { ...META, nextCursor } })
    },
    requestVoid(spec: RequestSpec): Promise<void> {
      requested.push(spec)
      return Promise.resolve()
    },
  }
  return { rest, requested }
}

describe('network/fleet.api — contratos de endpoint', () => {
  it('listVehicles hace GET /world/vehicles con filtros + cursor y devuelve Page', async () => {
    const { rest, requested } = fakeRest([{ id: VEHICLE_ID }], 'cursor-flota')
    const page = await createFleetApi(rest).listVehicles({
      status: 'idle',
      cursor: 'cursor-0',
      limit: 20,
    })

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: '/world/vehicles',
      query: { status: 'idle', cursor: 'cursor-0', limit: 20 },
    })
    expect(page.items).toEqual([{ id: VEHICLE_ID }])
    expect(page.nextCursor).toBe('cursor-flota')
  })

  it('purchaseVehicle hace POST /world/vehicles con el DTO snake_case del contrato', async () => {
    const { rest, requested } = fakeRest({ id: VEHICLE_ID })
    await createFleetApi(rest).purchaseVehicle({
      vehicle_type_id: VEHICLE_TYPE_ID,
      delivery_node_id: NODE_ID,
    })

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: '/world/vehicles',
      body: { vehicle_type_id: VEHICLE_TYPE_ID, delivery_node_id: NODE_ID },
    })
  })

  it('dispatchShipment hace POST al dispatch del cargamento con vehicle_id y route_id', async () => {
    const { rest, requested } = fakeRest({ id: SHIPMENT_ID, status: 'in_transit' })
    const shipment = await createFleetApi(rest).dispatchShipment(SHIPMENT_ID, VEHICLE_ID, ROUTE_ID)

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: `/world/shipments/${SHIPMENT_ID}/dispatch`,
      body: { vehicle_id: VEHICLE_ID, route_id: ROUTE_ID },
    })
    expect(shipment).toEqual({ id: SHIPMENT_ID, status: 'in_transit' })
  })

  it('repositionVehicle hace POST al reposition con route_id (viaje en vacío)', async () => {
    const { rest, requested } = fakeRest({ id: VEHICLE_ID, status: 'in_transit' })
    const vehicle = await createFleetApi(rest).repositionVehicle(VEHICLE_ID, ROUTE_ID)

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: `/world/vehicles/${VEHICLE_ID}/reposition`,
      body: { route_id: ROUTE_ID },
    })
    expect(vehicle).toEqual({ id: VEHICLE_ID, status: 'in_transit' })
  })

  it('getTerminal y listTerminalSlots consultan la terminal y sus slots', async () => {
    const TERMINAL_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f040'
    const { rest, requested } = fakeRest({ id: TERMINAL_ID })
    const api = createFleetApi(rest)

    await api.getTerminal(TERMINAL_ID)
    await api.listTerminalSlots(TERMINAL_ID, true)
    await api.listTerminalSlots(TERMINAL_ID)

    expect(requested[0]).toMatchObject({
      method: 'GET',
      path: `/world/terminals/${TERMINAL_ID}`,
    })
    expect(requested[1]).toMatchObject({
      method: 'GET',
      path: `/world/terminals/${TERMINAL_ID}/slots`,
      query: { only_available: true },
    })
    // Sin onlyAvailable, la clave se OMITE (no viaja como undefined).
    expect(requested[2]?.query).toEqual({})
  })

  it('purchaseTerminalSlot hace POST sin body (Idempotency-Key la pone el cliente REST)', async () => {
    const SLOT_ID = '01981c5e-7d2a-7f3b-9e41-a2c4d6e8f041'
    const { rest, requested } = fakeRest({ id: SLOT_ID })
    const slot = await createFleetApi(rest).purchaseTerminalSlot(SLOT_ID)

    expect(requested[0]).toMatchObject({
      method: 'POST',
      path: `/world/terminal-slots/${SLOT_ID}/purchase`,
    })
    expect(requested[0]?.body).toBeUndefined()
    expect(slot).toEqual({ id: SLOT_ID })
  })

  it('propaga el AppError tipado del cliente REST (p. ej. VEHICLE_SEALED)', async () => {
    const error = new AppError({
      kind: 'http',
      code: 'VEHICLE_SEALED',
      message: 'vehículo sellado durante handoff',
      status: 403,
    })
    const rest: RestClient = {
      request: () => Promise.reject(error),
      requestVoid: () => Promise.reject(error),
    }
    await expect(createFleetApi(rest).getShipment(SHIPMENT_ID)).rejects.toBe(error)
  })
})
