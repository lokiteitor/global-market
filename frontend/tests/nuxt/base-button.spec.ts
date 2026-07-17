import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import BaseButton from '~/components/base/BaseButton.vue'

describe('components/base/BaseButton', () => {
  it('por defecto es un botón primary de type="button" habilitado', () => {
    const wrapper = mount(BaseButton, { slots: { default: 'Entrar' } })
    const button = wrapper.get('button')

    expect(button.text()).toBe('Entrar')
    expect(button.attributes('type')).toBe('button')
    expect(button.classes()).toContain('primary')
    expect(button.attributes('disabled')).toBeUndefined()
    expect(button.attributes('aria-busy')).toBeUndefined()
  })

  it.each(['primary', 'ghost', 'danger'] as const)('aplica la variante %s', (variant) => {
    const wrapper = mount(BaseButton, { props: { variant }, slots: { default: 'x' } })
    expect(wrapper.get('button').classes()).toContain(variant)
  })

  it('el click nativo cae por atributos heredados cuando está habilitado', async () => {
    const onClick = vi.fn()
    const wrapper = mount(BaseButton, { slots: { default: 'x' }, attrs: { onClick } })

    await wrapper.get('button').trigger('click')
    expect(onClick).toHaveBeenCalledTimes(1)
  })

  it('disabled deshabilita el botón nativo y no dispara el click', async () => {
    const onClick = vi.fn()
    const wrapper = mount(BaseButton, {
      props: { disabled: true },
      slots: { default: 'x' },
      attrs: { onClick },
    })
    const button = wrapper.get('button')

    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(onClick).not.toHaveBeenCalled()
  })

  it('loading deshabilita, anuncia aria-busy y muestra el spinner decorativo', () => {
    const wrapper = mount(BaseButton, { props: { loading: true }, slots: { default: 'x' } })
    const button = wrapper.get('button')

    expect(button.attributes('disabled')).toBeDefined()
    expect(button.attributes('aria-busy')).toBe('true')
    expect(wrapper.find('span[aria-hidden="true"]').exists()).toBe(true)
  })
})
