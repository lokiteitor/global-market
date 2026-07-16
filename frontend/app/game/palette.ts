/**
 * game/palette.ts — paleta del mundo como CONFIG (FAD C5/C6, O2).
 *
 * game/ no puede importar Sass: estos valores son un ESPEJO documentado de
 * app/assets/styles/settings/_tokens.scss (+ derivados por bioma/status).
 * Si los tokens cambian, se actualiza este espejo — la fuente de verdad
 * visual sigue siendo _tokens.scss.
 */
import type { WorldPalette } from './types'

export const DEFAULT_PALETTE: WorldPalette = {
  background: '#0d1117', // $color-bg-deep

  regionStroke: 0x30363d, // $color-border
  regionFillByBiome: {
    plains: 0x2d4a22,
    forest: 0x1e3a29,
    desert: 0x4a3b1f,
    mountain: 0x3a3f47,
    ocean: 0x152a45,
    coast: 0x1f4045
  },

  city: 0x58a6ff, // $color-info
  deposit: 0x8b949e, // $color-text-muted
  node: 0x6e7681, // $color-text-faint

  // Rampa de congestión EMA: fluido → medio → congestionado.
  linkCongestion: [0x3fb950, 0xd29922, 0xf85149],

  buildingByStatus: {
    operational: 0x3fb950, // verde  ($color-success)
    under_construction: 0xd29922, // ámbar  ($color-accent)
    damaged: 0xdb6d28, // naranja
    seized: 0xf85149, // rojo    ($color-error)
    in_maintenance: 0x58a6ff,
    abandoned: 0x6e7681
  },
  buildingDefault: 0x8b949e,
  ownedOutline: 0xe3b341, // $color-accent-strong: borde destacado de lo propio

  vehicle: 0xe6edf3, // $color-text
  vehicleOwned: 0xe3b341,

  selection: 0xe3b341,
  hover: 0x58a6ff,
  label: '#e6edf3'
}
