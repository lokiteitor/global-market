import { describe, expect, it } from 'vitest'

import type { WorldRectM } from './bridge/vm'
import { CAMERA_EVENT_MIN_INTERVAL_MS, createCameraEventGate } from './camera-throttle'

const view = (xM: number): WorldRectM => ({ xM, yM: 0, widthM: 1_000, heightM: 800 })

describe('game/camera-throttle — createCameraEventGate', () => {
  it('emite la primera vista y luego solo si cambió Y pasó el intervalo', () => {
    const gate = createCameraEventGate(200)
    expect(gate(0, view(0), 1)).toBe(true)
    // Cambió pero no pasó el intervalo.
    expect(gate(100, view(50), 1)).toBe(false)
    // Pasó el intervalo y cambió.
    expect(gate(250, view(50), 1)).toBe(true)
  })

  it('sin cambio no emite aunque pase el tiempo', () => {
    const gate = createCameraEventGate(200)
    expect(gate(0, view(0), 1)).toBe(true)
    expect(gate(5_000, view(0), 1)).toBe(false)
  })

  it('un cambio SOLO de zoom también emite', () => {
    const gate = createCameraEventGate(200)
    expect(gate(0, view(0), 1)).toBe(true)
    expect(gate(300, view(0), 2)).toBe(true)
  })

  it('el intervalo por defecto es ~5 Hz', () => {
    expect(CAMERA_EVENT_MIN_INTERVAL_MS).toBe(200)
  })
})
