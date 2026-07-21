/**
 * game/entities/textures-extra — texturas runtime adicionales del mundo vivo.
 *
 * Mismo régimen que game/textures.ts (ARTE PLACEHOLDER CONSCIENTE, ADR-019):
 * formas geométricas generadas con Graphics + generateTexture, paleta de los
 * tokens Sass (hex equivalentes anotados). Idempotente: se invoca al crear el
 * mundo vivo y salta las texturas ya existentes.
 */

import type Phaser from 'phaser'

import { TILE_PX } from '~shared/geometry/grid'

/** $color-accent-500 — borde de edificio PROPIO. */
const COLOR_OWN = 0xd29224
/** $color-danger-500 — ghost de emplazamiento inválido. */
const COLOR_GHOST_INVALID = 0xc4504a
/** $color-info-500 — círculo de influencia de ciudad. */
const COLOR_INFLUENCE = 0x3f7fae

export const EXTRA_TEXTURES = {
  /** Anillo cuadrado ámbar: marca los edificios propios (se escala al bbox). */
  ownBorder: 'tx-own-border',
  /** Cuadrado rojo translúcido: ghost de build fuera del mundo. */
  ghostInvalid: 'tx-ghost-invalid',
  /** Círculo translúcido: radio de influencia de ciudad (se escala al radio). */
  influence: 'tx-influence',
} as const

/** Lado de la textura del borde propio (se escala al bbox del edificio). */
export const OWN_BORDER_PX = TILE_PX + 2
/** Diámetro de la textura de influencia (se escala a influence_radius_m). */
export const INFLUENCE_TEXTURE_PX = 128

function generateIfMissing(
  scene: Phaser.Scene,
  key: string,
  width: number,
  height: number,
  draw: (g: Phaser.GameObjects.Graphics) => void,
): void {
  if (scene.textures.exists(key)) {
    return
  }
  const g = scene.make.graphics()
  draw(g)
  g.generateTexture(key, width, height)
  g.destroy()
}

/** Genera las texturas extra (idempotente); se llama desde createWorldLive. */
export function makeExtraTextures(scene: Phaser.Scene): void {
  generateIfMissing(scene, EXTRA_TEXTURES.ownBorder, OWN_BORDER_PX, OWN_BORDER_PX, (g) => {
    g.lineStyle(2, COLOR_OWN, 1)
    g.strokeRect(1, 1, OWN_BORDER_PX - 2, OWN_BORDER_PX - 2)
  })

  generateIfMissing(scene, EXTRA_TEXTURES.ghostInvalid, TILE_PX, TILE_PX, (g) => {
    g.fillStyle(COLOR_GHOST_INVALID, 0.35)
    g.fillRect(1, 1, TILE_PX - 2, TILE_PX - 2)
    g.lineStyle(2, COLOR_GHOST_INVALID, 0.8)
    g.strokeRect(1, 1, TILE_PX - 2, TILE_PX - 2)
  })

  generateIfMissing(
    scene,
    EXTRA_TEXTURES.influence,
    INFLUENCE_TEXTURE_PX,
    INFLUENCE_TEXTURE_PX,
    (g) => {
      const r = INFLUENCE_TEXTURE_PX / 2
      g.fillStyle(COLOR_INFLUENCE, 0.12)
      g.fillCircle(r, r, r - 1)
      g.lineStyle(1, COLOR_INFLUENCE, 0.5)
      g.strokeCircle(r, r, r - 1)
    },
  )
}
