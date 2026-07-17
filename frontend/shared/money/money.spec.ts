import { describe, expect, it } from 'vitest'

import type { Money } from './index'
import { ZERO, add, compare, format, isMoney, multiplyByInt, parseMoney, subtract } from './index'

const m = (value: string): Money => {
  const result = parseMoney(value)
  if (!result.ok) throw new Error(`fixture inválida: ${value}`)
  return result.value
}

describe('shared/money — isMoney (patrón MoneyAmount del contrato)', () => {
  it.each(['0', '7', '42', '125000', '9007199254740993', '000', '007'])('acepta "%s"', (value) => {
    expect(isMoney(value)).toBe(true)
  })

  it.each([
    '',
    '-1',
    '+1',
    '1.5',
    '1,5',
    '1e3',
    ' 1',
    '1 ',
    '0x10',
    '١٢', // dígitos árabes: el patrón exige [0-9] ASCII
    '12_000',
    'abc',
    '12.0',
  ])('rechaza "%s"', (value) => {
    expect(isMoney(value)).toBe(false)
  })
})

describe('shared/money — parseMoney', () => {
  it('acepta un importe canónico del servidor', () => {
    const result = parseMoney('125000')
    expect(result.ok).toBe(true)
    if (result.ok) expect(result.value).toBe('125000')
  })

  it('canonicaliza ceros a la izquierda', () => {
    const result = parseMoney('007')
    expect(result.ok && result.value).toBe('7')
  })

  it('canonicaliza "000" a "0"', () => {
    const result = parseMoney('000')
    expect(result.ok && result.value).toBe('0')
  })

  it('devuelve error tipado ante formato inválido', () => {
    const result = parseMoney('-125000')
    expect(result.ok).toBe(false)
    if (!result.ok) expect(result.error).toEqual({ kind: 'invalid-format', input: '-125000' })
  })

  it('rechaza floats serializados', () => {
    expect(parseMoney('12.50').ok).toBe(false)
  })
})

describe('shared/money — add', () => {
  it('suma importes pequeños', () => {
    expect(add(m('1'), m('2'))).toBe('3')
  })

  it('el cero es neutro', () => {
    expect(add(m('125000'), ZERO)).toBe('125000')
  })

  it('suma con acarreo de magnitud', () => {
    expect(add(m('999999999'), m('1'))).toBe('1000000000')
  })

  it('no pierde precisión más allá de Number.MAX_SAFE_INTEGER (BigInt interno)', () => {
    // 2^53 + 1: con float, (2^53 + 1) + 1 === 2^53 + 2 fallaría por redondeo.
    expect(add(m('9007199254740993'), m('1'))).toBe('9007199254740994')
    expect(add(m('123456789012345678901234567890'), m('1'))).toBe('123456789012345678901234567891')
  })
})

describe('shared/money — subtract', () => {
  it('resta importes', () => {
    expect(subtract(m('5'), m('3'))).toBe('2')
  })

  it('a - a = 0', () => {
    expect(subtract(m('125000'), m('125000'))).toBe('0')
  })

  it('resta sin pérdida de precisión en magnitudes grandes', () => {
    expect(subtract(m('9007199254740994'), m('1'))).toBe('9007199254740993')
  })

  it('lanza RangeError si el resultado sería negativo (Money no admite negativos)', () => {
    expect(() => subtract(m('3'), m('5'))).toThrow(RangeError)
  })

  it('restar de cero cualquier importe positivo falla', () => {
    expect(() => subtract(ZERO, m('1'))).toThrow(RangeError)
  })
})

describe('shared/money — compare', () => {
  it('ordena numéricamente, no lexicográficamente', () => {
    // Lexicográficamente "9" > "10"; numéricamente 9 < 10.
    expect(compare(m('9'), m('10'))).toBe(-1)
    expect(compare(m('10'), m('9'))).toBe(1)
  })

  it('detecta igualdad', () => {
    expect(compare(m('125000'), m('125000'))).toBe(0)
  })

  it('compara magnitudes grandes correctamente', () => {
    expect(compare(m('9007199254740993'), m('9007199254740994'))).toBe(-1)
  })

  it('cero es menor que cualquier positivo', () => {
    expect(compare(ZERO, m('1'))).toBe(-1)
  })
})

describe('shared/money — multiplyByInt', () => {
  it('multiplica por un entero positivo', () => {
    expect(multiplyByInt(m('125'), 4)).toBe('500')
  })

  it('por cero da "0"', () => {
    expect(multiplyByInt(m('125000'), 0)).toBe('0')
  })

  it('por uno es identidad', () => {
    expect(multiplyByInt(m('125000'), 1)).toBe('125000')
  })

  it('no pierde precisión con magnitudes grandes', () => {
    expect(multiplyByInt(m('9007199254740991'), 3)).toBe('27021597764222973')
  })

  it.each([-1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, 2 ** 53])(
    'lanza RangeError ante factor inválido (%s)',
    (factor) => {
      expect(() => multiplyByInt(m('10'), factor)).toThrow(RangeError)
    },
  )
})

describe('shared/money — format (es-ES, sin decimales)', () => {
  it.each([
    ['0', '0'],
    ['7', '7'],
    ['999', '999'],
    ['1000', '1.000'],
    ['12500', '12.500'],
    ['125000', '125.000'],
    ['1234567', '1.234.567'],
    ['1000000000', '1.000.000.000'],
    ['9007199254740993', '9.007.199.254.740.993'],
  ])('formatea %s como "%s"', (input, expected) => {
    expect(format(m(input))).toBe(expected)
  })

  it('formatea el cero exportado', () => {
    expect(format(ZERO)).toBe('0')
  })
})
