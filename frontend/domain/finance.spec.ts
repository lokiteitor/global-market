import { describe, expect, it } from 'vitest'

import type { SignedAmount } from './finance'
import { isSignedAmount, signedAmountParts } from './finance'

function sa(input: string): SignedAmount {
  if (!isSignedAmount(input)) {
    throw new Error(`fixture inválida: ${input}`)
  }
  return input
}

describe('domain/finance — SignedAmount', () => {
  it('acepta enteros con signo opcional y rechaza el resto', () => {
    expect(isSignedAmount('1000')).toBe(true)
    expect(isSignedAmount('-1000')).toBe(true)
    expect(isSignedAmount('0')).toBe(true)
    for (const bad of ['+5', '1.5', '--3', '-', '', 'abc', '1e3']) {
      expect(isSignedAmount(bad), `debería rechazar "${bad}"`).toBe(false)
    }
  })

  it('descompone en signo + magnitud Money canónica (sin pasar por number)', () => {
    expect(signedAmountParts(sa('1500'))).toEqual({ negative: false, magnitude: '1500' })
    expect(signedAmountParts(sa('-1500'))).toEqual({ negative: true, magnitude: '1500' })
    expect(signedAmountParts(sa('-007'))).toEqual({ negative: true, magnitude: '7' })
  })

  it('"-0" colapsa a cero positivo', () => {
    expect(signedAmountParts(sa('-0'))).toEqual({ negative: false, magnitude: '0' })
  })

  it('magnitudes mayores que MAX_SAFE_INTEGER se conservan exactas', () => {
    expect(signedAmountParts(sa('-900719925474099299999'))).toEqual({
      negative: true,
      magnitude: '900719925474099299999',
    })
  })
})
