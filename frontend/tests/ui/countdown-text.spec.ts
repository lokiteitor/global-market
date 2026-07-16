// @vitest-environment happy-dom
/**
 * CountdownText — cuenta atrás hacia un wall-clock (ventanas de sorteo) con
 * reloj falso: el componente solo PRESENTA; el cierre lo decide el servidor.
 */
import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import CountdownText from '~/components/base/CountdownText.vue'

describe('CountdownText', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-07-15T10:00:00Z'))
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('muestra el tiempo restante y cuenta atrás con el tick', async () => {
    const wrapper = mount(CountdownText, {
      props: { until: '2026-07-15T10:00:45Z' }
    })
    expect(wrapper.text()).toBe('00:45')

    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper.text()).toBe('00:35')

    await vi.advanceTimersByTimeAsync(30_000)
    expect(wrapper.text()).toBe('00:05')
  })

  it('al llegar a cero muestra el texto de expiración y emite expired una vez', async () => {
    const wrapper = mount(CountdownText, {
      props: { until: '2026-07-15T10:00:03Z', expiredText: 'cerrada' }
    })
    await vi.advanceTimersByTimeAsync(5_000)
    expect(wrapper.text()).toBe('cerrada')
    expect(wrapper.emitted('expired')).toHaveLength(1)

    await vi.advanceTimersByTimeAsync(5_000)
    expect(wrapper.emitted('expired')).toHaveLength(1)
  })

  it('usa formato horas para plazos largos', () => {
    const wrapper = mount(CountdownText, {
      props: { until: '2026-07-15T11:30:10Z' }
    })
    expect(wrapper.text()).toBe('1h 30m')
  })
})
