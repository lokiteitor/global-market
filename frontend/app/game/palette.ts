/**
 * game/palette.ts — paleta del mundo como CONFIG (FAD C5/C6, O2).
 *
 * game/ no puede importar Sass: estos valores son un ESPEJO documentado de
 * app/assets/styles/settings/_tokens.scss (+ derivados por bioma/status).
 * Si los tokens cambian, se actualiza este espejo — la fuente de verdad
 * visual sigue siendo _tokens.scss.
 *
 * FE-6: los biomas mapean a FRAMES de suelo del atlas pak128 (el terreno
 * tileado sustituye a los fills por color) y los status de edificio son
 * TINTES sobre el sprite (blanco = sin alterar).
 */
import type { WorldPalette } from './types'

export const DEFAULT_PALETTE: WorldPalette = {
  // Fuera de las regiones no hay tiles: el fondo hace de océano.
  background: '#152a45', // = regionFillByBiome.ocean de la v1 ($color-bg-deep marino)

  groundFrameByBiome: {
    plains: 'ground.grass',
    forest: 'ground.tropic',
    desert: 'ground.desert',
    mountain: 'ground.rocky',
    ocean: 'ground.water',
    coast: 'ground.desert'
  },
  groundFrameDefault: 'ground.grass',

  node: 0x6e7681, // $color-text-faint

  // Rampa de congestión EMA (tints de los tiles de carretera):
  // fluido (sin tinte) → medio → congestionado.
  linkCongestion: [0xffffff, 0xd29922, 0xf85149],

  buildingTintByStatus: {
    operational: 0xffffff, // sin alterar
    under_construction: 0xd29922, // ámbar  ($color-accent)
    damaged: 0xdb6d28, // naranja
    seized: 0xf85149, // rojo    ($color-error)
    in_maintenance: 0x58a6ff, // azul   ($color-info)
    abandoned: 0x6e7681 // gris
  },
  buildingTintDefault: 0xffffff,
  ownedOutline: 0xe3b341, // $color-accent-strong: marca de lo propio

  vehicle: 0xffffff, // sprite sin tinte
  vehicleOwned: 0xe3b341,

  selection: 0xe3b341,
  hover: 0x58a6ff,
  label: '#e6edf3'
}
