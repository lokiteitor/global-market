import { describe, expect, it } from 'vitest'

import type { Result } from './index'
import { err, isErr, isOk, map, ok, unwrapOr } from './index'

describe('shared/result', () => {
  it('ok construye la variante de éxito', () => {
    expect(ok(42)).toEqual({ ok: true, value: 42 })
  })

  it('err construye la variante de error', () => {
    expect(err('boom')).toEqual({ ok: false, error: 'boom' })
  })

  it('isOk / isErr discriminan y narrean el tipo', () => {
    const success: Result<number, string> = ok(1)
    const failure: Result<number, string> = err('e')

    expect(isOk(success)).toBe(true)
    expect(isErr(success)).toBe(false)
    expect(isOk(failure)).toBe(false)
    expect(isErr(failure)).toBe(true)

    if (isOk(success)) expect(success.value).toBe(1)
    if (isErr(failure)) expect(failure.error).toBe('e')
  })

  it('map transforma el valor de éxito', () => {
    const result = map(ok(2), (n) => n * 10)
    expect(result).toEqual({ ok: true, value: 20 })
  })

  it('map propaga el error sin invocar la función', () => {
    let called = false
    const failure: Result<number, string> = err('e')
    const result = map(failure, (n: number) => {
      called = true
      return n * 10
    })
    expect(result).toEqual({ ok: false, error: 'e' })
    expect(called).toBe(false)
  })

  it('map permite cambiar el tipo del valor', () => {
    const result = map(ok(5), (n) => `#${n}`)
    expect(unwrapOr(result, '')).toBe('#5')
  })

  it('unwrapOr devuelve el valor en éxito', () => {
    expect(unwrapOr(ok(7), 0)).toBe(7)
  })

  it('unwrapOr devuelve el fallback en error', () => {
    const failure: Result<number, string> = err('e')
    expect(unwrapOr(failure, 0)).toBe(0)
  })
})
