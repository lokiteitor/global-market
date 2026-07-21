import { describe, expect, it } from 'vitest'

import {
  ZERO_QUANTITY,
  addQuantity,
  compareQuantity,
  formatQuantity,
  isQuantity,
  parseQuantity,
  subtractQuantity,
} from './quantity'

function q(input: string) {
  const result = parseQuantity(input)
  if (!result.ok) {
    throw new Error(`fixture inválida: ${input}`)
  }
  return result.value
}

describe('domain/quantity — parseo', () => {
  it('acepta el patrón del contrato y canonicaliza ceros a la izquierda', () => {
    expect(parseQuantity('500')).toEqual({ ok: true, value: '500' })
    expect(parseQuantity('007')).toEqual({ ok: true, value: '7' })
    expect(parseQuantity('0')).toEqual({ ok: true, value: '0' })
  })

  it('rechaza signo, decimales, notación científica y vacío', () => {
    for (const bad of ['-5', '+5', '1.5', '1e3', '', ' 5', '5 ', 'abc']) {
      expect(parseQuantity(bad).ok, `debería rechazar "${bad}"`).toBe(false)
      expect(isQuantity(bad)).toBe(false)
    }
  })

  it('maneja cantidades mayores que Number.MAX_SAFE_INTEGER sin pérdida', () => {
    const huge = '900719925474099299999'
    expect(parseQuantity(huge)).toEqual({ ok: true, value: huge })
  })
})

describe('domain/quantity — aritmética BigInt', () => {
  it('suma sin pérdida de precisión', () => {
    expect(addQuantity(q('900719925474099299999'), q('1'))).toBe('900719925474099300000')
    expect(addQuantity(ZERO_QUANTITY, q('42'))).toBe('42')
  })

  it('resta y falla ante resultado negativo (el stock no es negativo)', () => {
    expect(subtractQuantity(q('500'), q('200'))).toBe('300')
    expect(subtractQuantity(q('5'), q('5'))).toBe('0')
    expect(() => subtractQuantity(q('5'), q('6'))).toThrow(RangeError)
  })

  it('compara con orden numérico, no lexicográfico', () => {
    expect(compareQuantity(q('9'), q('10'))).toBe(-1)
    expect(compareQuantity(q('10'), q('9'))).toBe(1)
    expect(compareQuantity(q('10'), q('10'))).toBe(0)
  })
})

describe('domain/quantity — formateo es-ES', () => {
  it('agrupa miles con punto sin pasar por number', () => {
    expect(formatQuantity(q('0'))).toBe('0')
    expect(formatQuantity(q('999'))).toBe('999')
    expect(formatQuantity(q('1000'))).toBe('1.000')
    expect(formatQuantity(q('1234567'))).toBe('1.234.567')
    expect(formatQuantity(q('900719925474099299999'))).toBe('900.719.925.474.099.299.999')
  })
})
