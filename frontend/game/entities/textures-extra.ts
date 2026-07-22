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
import type { NodeKind } from '~domain/logistics'

import { TEXTURES } from '../textures'

/** $color-accent-500 — borde de edificio PROPIO y anillo de terminal. */
const COLOR_OWN = 0xd29224
/** $color-danger-500 — ghost de emplazamiento inválido. */
const COLOR_GHOST_INVALID = 0xc4504a
/** $color-info-500 — círculo de influencia de ciudad y nodos portuarios. */
const COLOR_INFLUENCE = 0x3f7fae
/** $color-gray-300 aprox — relleno de estación/CD (más claro que el nodo base). */
const COLOR_NODE_SPECIAL = 0xa7b2c4
/** Borde de las formas de nodo (espejo de COLOR_STROKE de textures.ts). */
const COLOR_NODE_STROKE = 0x151a23

export const EXTRA_TEXTURES = {
  /** Anillo cuadrado ámbar: marca los edificios propios (se escala al bbox). */
  ownBorder: 'tx-own-border',
  /** Cuadrado rojo translúcido: ghost de build fuera del mundo. */
  ghostInvalid: 'tx-ghost-invalid',
  /** Círculo translúcido: radio de influencia de ciudad (se escala al radio). */
  influence: 'tx-influence',
  /** Nodos con forma distintiva por clase (§16.4): puerto/estación/CD. */
  nodePort: 'tx-node-port',
  nodeStation: 'tx-node-station',
  nodeDc: 'tx-node-dc',
} as const

/** Sufijo de la variante con anillo de terminal horneado en la textura. */
const TERMINAL_SUFFIX = '-terminal'

/** Lado de las texturas de nodo distintivo (el nodo base mide NODE_PX = 8). */
export const SPECIAL_NODE_PX = 12
/** Lado de las variantes con anillo de terminal (deja aire para el anillo). */
export const TERMINAL_NODE_PX = 18

/**
 * Textura de un nodo por clase y presencia de terminal. Los nodos con
 * terminal intermodal llevan un anillo ámbar HORNEADO en la textura (variante
 * `-terminal`): una sola textura por combinación, pool-friendly (sin sprites
 * hijos). Pura y testeable.
 */
export function nodeTextureKey(kind: NodeKind, intermodal: boolean): string {
  const base =
    kind === 'port'
      ? EXTRA_TEXTURES.nodePort
      : kind === 'station'
        ? EXTRA_TEXTURES.nodeStation
        : kind === 'distribution_center'
          ? EXTRA_TEXTURES.nodeDc
          : TEXTURES.node
  return intermodal ? `${base}${TERMINAL_SUFFIX}` : base
}

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

  // ── Nodos por clase (+ variante con anillo de terminal horneado) ──────────
  // Cada forma se genera dos veces: sola (SPECIAL_NODE_PX) y centrada dentro
  // de un anillo ámbar (TERMINAL_NODE_PX). El nodo base tx-node también recibe
  // su variante -terminal (junctions intermodales).
  const shapes: readonly {
    readonly key: string
    readonly draw: (g: Phaser.GameObjects.Graphics, cx: number, cy: number) => void
  }[] = [
    {
      // Puerto: círculo azul (agua).
      key: EXTRA_TEXTURES.nodePort,
      draw: (g, cx, cy) => {
        const r = SPECIAL_NODE_PX / 2 - 1
        g.fillStyle(COLOR_INFLUENCE, 1)
        g.fillCircle(cx, cy, r)
        g.lineStyle(1, COLOR_NODE_STROKE, 1)
        g.strokeCircle(cx, cy, r)
      },
    },
    {
      // Estación: cuadrado claro.
      key: EXTRA_TEXTURES.nodeStation,
      draw: (g, cx, cy) => {
        const half = SPECIAL_NODE_PX / 2 - 1
        g.fillStyle(COLOR_NODE_SPECIAL, 1)
        g.fillRect(cx - half, cy - half, half * 2, half * 2)
        g.lineStyle(1, COLOR_NODE_STROKE, 1)
        g.strokeRect(cx - half, cy - half, half * 2, half * 2)
      },
    },
    {
      // Centro de distribución: rombo claro.
      key: EXTRA_TEXTURES.nodeDc,
      draw: (g, cx, cy) => {
        const half = SPECIAL_NODE_PX / 2 - 1
        g.fillStyle(COLOR_NODE_SPECIAL, 1)
        g.beginPath()
        g.moveTo(cx, cy - half)
        g.lineTo(cx + half, cy)
        g.lineTo(cx, cy + half)
        g.lineTo(cx - half, cy)
        g.closePath()
        g.fillPath()
        g.lineStyle(1, COLOR_NODE_STROKE, 1)
        g.strokePath()
      },
    },
  ]

  for (const shape of shapes) {
    generateIfMissing(scene, shape.key, SPECIAL_NODE_PX, SPECIAL_NODE_PX, (g) => {
      shape.draw(g, SPECIAL_NODE_PX / 2, SPECIAL_NODE_PX / 2)
    })
    generateIfMissing(scene, `${shape.key}-terminal`, TERMINAL_NODE_PX, TERMINAL_NODE_PX, (g) => {
      const c = TERMINAL_NODE_PX / 2
      shape.draw(g, c, c)
      g.lineStyle(2, COLOR_OWN, 1)
      g.strokeCircle(c, c, c - 1)
    })
  }

  // Variante -terminal del nodo base (junction intermodal, forma de tx-node).
  generateIfMissing(scene, `${TEXTURES.node}-terminal`, TERMINAL_NODE_PX, TERMINAL_NODE_PX, (g) => {
    const c = TERMINAL_NODE_PX / 2
    const r = SPECIAL_NODE_PX / 2 - 1
    g.fillStyle(COLOR_NODE_SPECIAL, 1)
    g.fillCircle(c, c, r)
    g.lineStyle(1, COLOR_NODE_STROKE, 1)
    g.strokeCircle(c, c, r)
    g.lineStyle(2, COLOR_OWN, 1)
    g.strokeCircle(c, c, c - 1)
  })
}
