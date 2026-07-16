// @vitest-environment happy-dom
/**
 * MoneyText — formatea Money (string de punto fijo) con el helper BigInt del
 * kernel: nunca parseFloat/Number (C11).
 */
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import MoneyText from '~/components/base/MoneyText.vue'
import { moneyOf } from '~/lib/kernel/money'

describe('MoneyText', () => {
  it('formatea con separador de miles', () => {
    const wrapper = mount(MoneyText, { props: { amount: moneyOf('1234567') } })
    expect(wrapper.text()).toBe('1.234.567')
  })

  it('formatea importes negativos y los marca cuando signed', () => {
    const wrapper = mount(MoneyText, { props: { amount: moneyOf('-98765'), signed: true } })
    expect(wrapper.text()).toBe('-98.765')
    expect(wrapper.classes()).toContain('b-money--negative')
  })

  it('antepone + con showPlus en positivos', () => {
    const wrapper = mount(MoneyText, { props: { amount: moneyOf('500'), signed: true, showPlus: true } })
    expect(wrapper.text()).toBe('+500')
    expect(wrapper.classes()).toContain('b-money--positive')
  })

  it('maneja importes mayores que Number.MAX_SAFE_INTEGER sin perder precisión', () => {
    const wrapper = mount(MoneyText, { props: { amount: moneyOf('9007199254740993') } })
    expect(wrapper.text()).toBe('9.007.199.254.740.993')
  })
})
