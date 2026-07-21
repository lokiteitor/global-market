import { beforeEach, describe, expect, it } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

import { useMapUiStore } from './mapui.store'

beforeEach(() => {
  setActivePinia(createPinia())
})

describe('app/stores/mapui.store — estado de UI del mapa', () => {
  it('arranca en modo select, sin overlays volcados, sin selección ni follow', () => {
    const store = useMapUiStore()
    expect(store.mode).toBe('select')
    expect(store.overlays).toEqual({})
    expect(store.selection).toBeNull()
    expect(store.followedVehicleId).toBeNull()
    expect(store.hasSelection).toBe(false)
  })

  it('setOverlay hace merge inmutable y es no-op si no cambia', () => {
    const store = useMapUiStore()
    store.setOverlay('congestion', true)
    store.setOverlay('regions', true)
    expect(store.overlays).toEqual({ congestion: true, regions: true })

    const before = store.overlays
    store.setOverlay('congestion', true)
    expect(store.overlays).toBe(before) // sin cambio ⇒ misma referencia
    store.setOverlay('congestion', false)
    expect(store.overlays).toEqual({ congestion: false, regions: true })
  })

  it('applyOverlays reemplaza el estado completo (volcado inicial del motor)', () => {
    const store = useMapUiStore()
    store.setOverlay('regions', true)
    store.applyOverlays({ logistics: true, resources: true, congestion: true })
    expect(store.overlays).toEqual({ logistics: true, resources: true, congestion: true })
  })

  it('selección y follow', () => {
    const store = useMapUiStore()
    store.setSelection({ type: 'vehicle', id: 'v1' })
    expect(store.hasSelection).toBe(true)
    store.setFollow('v1')
    expect(store.followedVehicleId).toBe('v1')
    store.setSelection(null)
    expect(store.hasSelection).toBe(false)
  })

  it('reset vuelve al estado inicial', () => {
    const store = useMapUiStore()
    store.setMode('build')
    store.setOverlay('influence', true)
    store.setSelection({ type: 'city', id: 'c1' })
    store.setFollow('v1')
    store.reset()
    expect(store.mode).toBe('select')
    expect(store.overlays).toEqual({})
    expect(store.selection).toBeNull()
    expect(store.followedVehicleId).toBeNull()
  })
})
