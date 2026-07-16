import { describe, expect, it } from 'vitest'
import { dirFromAngle } from '~/game/iso-dirs'

/** Ángulo de pantalla del paso de tile (du, dv) con iso 2:1 (tile 128×64). */
function screenAngle(du: number, dv: number): number {
  return Math.atan2((du + dv) * 32, (du - dv) * 64)
}

describe('game/iso-dirs', () => {
  it('los 8 pasos de tile canónicos mapean a su dirección', () => {
    expect(dirFromAngle(screenAngle(1, 0))).toBe('e')
    expect(dirFromAngle(screenAngle(1, -1))).toBe('ne')
    expect(dirFromAngle(screenAngle(0, -1))).toBe('n')
    expect(dirFromAngle(screenAngle(-1, -1))).toBe('nw')
    expect(dirFromAngle(screenAngle(-1, 0))).toBe('w')
    expect(dirFromAngle(screenAngle(-1, 1))).toBe('sw')
    expect(dirFromAngle(screenAngle(0, 1))).toBe('s')
    expect(dirFromAngle(screenAngle(1, 1))).toBe('se')
  })

  it('pequeñas desviaciones se ajustan a la dirección más cercana', () => {
    const east = screenAngle(1, 0)
    expect(dirFromAngle(east + 0.1)).toBe('e')
    expect(dirFromAngle(east - 0.1)).toBe('e')
  })

  it('maneja el wrap alrededor de ±π (oeste-suroeste)', () => {
    // sw es exactamente π; ángulos a ambos lados del corte deben caer en sw.
    expect(dirFromAngle(Math.PI - 0.05)).toBe('sw')
    expect(dirFromAngle(-Math.PI + 0.05)).toBe('sw')
  })

  it('ángulos fuera de (−π, π] se normalizan', () => {
    const east = screenAngle(1, 0)
    expect(dirFromAngle(east + Math.PI * 2)).toBe('e')
    expect(dirFromAngle(east - Math.PI * 4)).toBe('e')
  })
})
