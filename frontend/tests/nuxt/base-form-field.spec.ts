import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'

import BaseFormField from '~/components/base/BaseFormField.vue'
import BaseInput from '~/components/base/BaseInput.vue'

/** Host que cablea BaseInput con las slot props, como lo hacen las páginas. */
const Host = defineComponent({
  components: { BaseFormField, BaseInput },
  props: {
    error: { type: String, default: null },
    hint: { type: String, default: undefined },
  },
  template: `
    <BaseFormField label="Nombre" :error="error" :hint="hint" required>
      <template #default="{ id, describedBy, invalid }">
        <BaseInput :id="id" :aria-describedby="describedBy" :invalid="invalid" />
      </template>
    </BaseFormField>
  `,
})

describe('components/base/BaseFormField', () => {
  it('asocia el label al control del slot por id', () => {
    const wrapper = mount(Host)

    const label = wrapper.get('label')
    const input = wrapper.get('input')
    expect(label.text()).toContain('Nombre')
    expect(label.attributes('for')).toBeDefined()
    expect(input.attributes('id')).toBe(label.attributes('for'))
    expect(input.attributes('aria-invalid')).toBeUndefined()
  })

  it('sin error muestra el hint y lo enlaza por aria-describedby', () => {
    const wrapper = mount(Host, { props: { hint: 'Como en el registro' } })

    expect(wrapper.text()).toContain('Como en el registro')
    const input = wrapper.get('input')
    const hint = wrapper.get(`#${input.attributes('aria-describedby')}`)
    expect(hint.text()).toBe('Como en el registro')
  })

  it('con error lo anuncia (role=alert), marca el control y oculta el hint', () => {
    const wrapper = mount(Host, {
      props: { error: 'Este campo es obligatorio', hint: 'Como en el registro' },
    })

    const alert = wrapper.get('[role="alert"]')
    expect(alert.text()).toBe('Este campo es obligatorio')
    expect(wrapper.text()).not.toContain('Como en el registro')

    const input = wrapper.get('input')
    expect(input.attributes('aria-invalid')).toBe('true')
    expect(input.attributes('aria-describedby')).toBe(alert.attributes('id'))
    expect(input.classes()).toContain('invalid')
  })
})
