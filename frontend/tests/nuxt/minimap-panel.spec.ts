import { createPinia, setActivePinia } from 'pinia'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it } from 'vitest'

import MinimapPanel from '~/components/play/minimap/MinimapPanel.vue'
import { useMapUiStore } from '~/stores/mapui.store'
import { useWorldStore } from '~/stores/world.store'
import { region } from '~/stores/testing/fixtures'
import { stubNuxtApp } from './game-fakes'

/** Región Askadia con bounds reales [0, 50 000)². */
function askadia() {
  return region({
    boundsM: [
      [
        [0, 0],
        [50_000, 0],
        [50_000, 50_000],
        [0, 50_000],
        [0, 0],
      ],
    ],
  })
}

async function mountMinimap() {
  const pinia = createPinia()
  setActivePinia(pinia)

  const world = useWorldStore()
  world.applyRegionsSnapshot([askadia()])

  const wrapper = mount(MinimapPanel, { global: { plugins: [pinia] } })
  await flushPromises()
  return wrapper
}

describe('components/play/minimap/MinimapPanel', () => {
  beforeEach(() => {
    stubNuxtApp(1_000)
  })

  it('renderiza el marco con el canvas visible por defecto', async () => {
    const wrapper = await mountMinimap()
    expect(wrapper.find('[data-testid="minimap-panel"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="minimap-canvas"]').attributes('style') ?? '').not.toContain(
      'display: none',
    )
  })

  it('el toggle oculta y muestra el canvas (estado en mapui.store)', async () => {
    const wrapper = await mountMinimap()
    const mapui = useMapUiStore()

    await wrapper.get('[data-testid="minimap-toggle"]').trigger('click')
    expect(mapui.minimapVisible).toBe(false)
    // v-show: assert sobre el estilo inline (isVisible() de VTU no computa
    // estilos bajo happy-dom).
    expect(wrapper.get('[data-testid="minimap-canvas"]').attributes('style')).toContain(
      'display: none',
    )

    await wrapper.get('[data-testid="minimap-toggle"]').trigger('click')
    expect(mapui.minimapVisible).toBe(true)
    expect(wrapper.get('[data-testid="minimap-canvas"]').attributes('style') ?? '').not.toContain(
      'display: none',
    )
  })

  it('un clic en el canvas emite un comando de cámara center (thin client: solo pide)', async () => {
    const wrapper = await mountMinimap()
    const mapui = useMapUiStore()

    await wrapper.get('[data-testid="minimap-canvas"]').trigger('pointerdown', {
      clientX: 110,
      clientY: 110,
      pointerId: 1,
    })

    expect(mapui.cameraCommand).not.toBeNull()
    expect(mapui.cameraCommand?.kind).toBe('center')
  })
})
