/**
 * game/iso-dirs.ts — orientación de sprites de 8 direcciones (pak128).
 *
 * Los sprites iso NO se rotan libremente: la escena mapea el ángulo de pantalla
 * que devuelve `interpolateOnPath` (atan2 sobre px ya proyectados) al frame
 * direccional más cercano. Los 8 vectores canónicos son los pasos de tile
 * proyectados a pantalla con la iso 2:1 (tile 128×64):
 *   E=(+64,+32)  NE=(+128,0)  N=(+64,−32)  NW=(0,−64)
 *   W=(−64,−32)  SW=(−128,0)  S=(−64,+32)  SE=(0,+64)
 */

export type IsoDir = 'w' | 'nw' | 'n' | 'ne' | 'e' | 'se' | 's' | 'sw'

const DIRS: readonly [IsoDir, number][] = [
  ['e', Math.atan2(32, 64)],
  ['ne', Math.atan2(0, 128)],
  ['n', Math.atan2(-32, 64)],
  ['nw', Math.atan2(-64, 0)],
  ['w', Math.atan2(-32, -64)],
  ['sw', Math.atan2(0, -128)],
  ['s', Math.atan2(32, -64)],
  ['se', Math.atan2(64, 0)]
]

const TWO_PI = Math.PI * 2

/** Distancia angular mínima |a−b| con wrap a (−π, π]. */
function angularDistance(a: number, b: number): number {
  const d = (((a - b) % TWO_PI) + TWO_PI) % TWO_PI
  return d > Math.PI ? TWO_PI - d : d
}

/** Dirección de 8 puntos más cercana a un ángulo de pantalla (radianes atan2). */
export function dirFromAngle(angle: number): IsoDir {
  let best: IsoDir = 'e'
  let bestDist = Infinity
  for (const [dir, canonical] of DIRS) {
    const dist = angularDistance(angle, canonical)
    if (dist < bestDist) {
      bestDist = dist
      best = dir
    }
  }
  return best
}
