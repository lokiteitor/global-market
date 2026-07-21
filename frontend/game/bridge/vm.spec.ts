import { describe, expect, it } from 'vitest'

import {
  LABEL_MIN_ZOOM,
  cityRadiusM,
  cityScale,
  congestionTier,
  labelsVisibleAtZoom,
  vehicleTint,
} from './vm'

describe('game/bridge/vm — congestión → tier (mandato: verde<1.2, ámbar<2, rojo≥2)', () => {
  it('mapea los rangos del mandato', () => {
    expect(congestionTier(1)).toBe('fluid')
    expect(congestionTier(1.19)).toBe('fluid')
    expect(congestionTier(1.2)).toBe('busy')
    expect(congestionTier(1.99)).toBe('busy')
    expect(congestionTier(2)).toBe('jammed')
    expect(congestionTier(5)).toBe('jammed')
  })

  it('valores no finitos degradan a fluido (sin dato ⇒ sin alarma)', () => {
    expect(congestionTier(Number.NaN)).toBe('fluid')
    expect(congestionTier(Number.POSITIVE_INFINITY)).toBe('jammed')
  })
})

describe('game/bridge/vm — culling de etiquetas por zoom (mandato: zoom ≥ 0.6)', () => {
  it('oculta por debajo del umbral y muestra desde él', () => {
    expect(labelsVisibleAtZoom(0.15)).toBe(false)
    expect(labelsVisibleAtZoom(0.59)).toBe(false)
    expect(labelsVisibleAtZoom(LABEL_MIN_ZOOM)).toBe(true)
    expect(labelsVisibleAtZoom(1)).toBe(true)
    expect(labelsVisibleAtZoom(3)).toBe(true)
  })
})

describe('game/bridge/vm — tinte de vehículo por estado', () => {
  it('broken en rojo, sealed apagado, resto sin tinte', () => {
    expect(vehicleTint('broken')).toBe(0xc4504a)
    expect(vehicleTint('sealed')).not.toBeNull()
    expect(vehicleTint('idle')).toBeNull()
    expect(vehicleTint('in_transit')).toBeNull()
  })
})

describe('game/bridge/vm — escala visual de ciudad', () => {
  it('crece con el nivel y clampa niveles absurdos', () => {
    expect(cityScale(1)).toBeCloseTo(0.8)
    expect(cityScale(2)).toBeCloseTo(1)
    expect(cityScale(3)).toBeGreaterThan(cityScale(2))
    expect(cityScale(0)).toBe(cityScale(1)) // clamp inferior
    expect(cityScale(99)).toBe(cityScale(10)) // clamp superior
  })

  it('cityRadiusM es coherente con la proyección (nivel 2 = textura 64px ⇒ 250 m)', () => {
    // 32 px de radio / 0.128 px/m = 250 m
    expect(cityRadiusM(2)).toBeCloseTo(250)
  })
})
