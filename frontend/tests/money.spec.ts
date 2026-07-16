import { describe, expect, it } from 'vitest'
import {
  addMoney,
  cmpMoney,
  formatMoney,
  formatQuantity,
  moneyOf,
  mulByQty,
  negMoney,
  parseMoney,
  parseQuantity,
  quantityOf,
  subMoney,
  ZERO_MONEY
} from '~/lib/kernel/money'

describe('kernel/money', () => {
  it('parseMoney valida enteros de punto fijo (con signo) y normaliza', () => {
    expect(parseMoney('125000')).toEqual({ ok: true, value: '125000' })
    expect(parseMoney('-42')).toEqual({ ok: true, value: '-42' })
    expect(parseMoney('000500')).toEqual({ ok: true, value: '500' })
    expect(parseMoney('12.5').ok).toBe(false)
    expect(parseMoney('12,5').ok).toBe(false)
    expect(parseMoney('1e6').ok).toBe(false)
    expect(parseMoney('').ok).toBe(false)
    expect(parseMoney('abc').ok).toBe(false)
  })

  it('parseQuantity rechaza negativos y no enteros', () => {
    expect(parseQuantity('500')).toEqual({ ok: true, value: '500' })
    expect(parseQuantity('-1').ok).toBe(false)
    expect(parseQuantity('1.0').ok).toBe(false)
  })

  it('moneyOf/quantityOf lanzan ante entrada inválida', () => {
    expect(() => moneyOf('1.23')).toThrow(TypeError)
    expect(() => quantityOf('-5')).toThrow(TypeError)
  })

  it('aritmética exacta con BigInt más allá de Number.MAX_SAFE_INTEGER', () => {
    const a = moneyOf('9007199254740993') // MAX_SAFE_INTEGER + 2: irrepresentable en float
    const b = moneyOf('1')
    expect(addMoney(a, b)).toBe('9007199254740994')
    expect(subMoney(a, b)).toBe('9007199254740992')
  })

  it('addMoney/subMoney/negMoney con signos', () => {
    expect(addMoney(moneyOf('100'), moneyOf('-40'))).toBe('60')
    expect(subMoney(moneyOf('40'), moneyOf('100'))).toBe('-60')
    expect(negMoney(moneyOf('-7'))).toBe('7')
    expect(addMoney(ZERO_MONEY, moneyOf('0'))).toBe('0')
  })

  it('cmpMoney ordena por valor numérico, no lexicográfico', () => {
    expect(cmpMoney(moneyOf('9'), moneyOf('10'))).toBe(-1)
    expect(cmpMoney(moneyOf('10'), moneyOf('9'))).toBe(1)
    expect(cmpMoney(moneyOf('10'), moneyOf('10'))).toBe(0)
    expect(cmpMoney(moneyOf('-2'), moneyOf('1'))).toBe(-1)
  })

  it('mulByQty: precio unitario × cantidad', () => {
    expect(mulByQty(moneyOf('120'), quantityOf('500'))).toBe('60000')
    expect(mulByQty(moneyOf('9007199254740993'), quantityOf('1000'))).toBe('9007199254740993000')
  })

  it('formatMoney agrupa miles y respeta el signo', () => {
    expect(formatMoney(moneyOf('0'))).toBe('0')
    expect(formatMoney(moneyOf('999'))).toBe('999')
    expect(formatMoney(moneyOf('1000'))).toBe('1.000')
    expect(formatMoney(moneyOf('1234567'))).toBe('1.234.567')
    expect(formatMoney(moneyOf('-1234567'))).toBe('-1.234.567')
    expect(formatMoney(moneyOf('1234567'), ' ')).toBe('1 234 567')
  })

  it('formatQuantity agrupa miles', () => {
    expect(formatQuantity(quantityOf('2500000'))).toBe('2.500.000')
  })
})
