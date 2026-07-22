/**
 * game/textures — fábrica de texturas RUNTIME (Rendering Layer, FAD §14/§16 + ADR-019).
 *
 * ARTE PLACEHOLDER CONSCIENTE: en esta fase no hay binarios de arte; todas las
 * texturas se generan en runtime con `Phaser.GameObjects.Graphics` +
 * `generateTexture` (formas geométricas planas). Cuando exista un pipeline de
 * atlases (FAD §14.2), este módulo se sustituye por el Loader sin tocar a los
 * consumidores: todo el mundo referencia las texturas por las claves de
 * `TEXTURES` / `biomeTextureKey` / `buildingTextureKey`, nunca por literales.
 *
 * Paleta: hex equivalentes de los tokens Sass de
 * `app/assets/styles/settings/_colors.scss` (game/ no puede importar Sass;
 * cada constante anota su token de origen). Los biomas no tienen token propio:
 * se derivan de la misma familia (grafito frío + ámbar industrial) con
 * saturación contenida para que la información de juego (entidades, overlays)
 * destaque sobre el terreno (mandato: claridad > realismo).
 *
 * Framework-agnostic respecto a Vue/Nuxt/Pinia; Phaser solo como TIPOS (las
 * texturas se generan con métodos de la instancia `scene` inyectada).
 */

import type Phaser from 'phaser'

import { TILE_PX } from '~shared/geometry/grid'

/**
 * Biomas del contrato (schema `Biome` de openapi.yaml v1.3.0). Vocabulario de
 * RENDER duplicado a propósito: game/ no importa `types/api.d.ts` (los DTO no
 * salen de network/, FAD O5); el bridge de la fase UI mapea contrato → render.
 */
export type BiomeName = 'plains' | 'forest' | 'desert' | 'mountain' | 'ocean' | 'coast'

/**
 * Estados de edificio del contrato (schema `BuildingStatus` v1.3.0). Misma
 * duplicación consciente que `BiomeName`.
 */
export type BuildingStatusName =
  | 'under_construction'
  | 'operational'
  | 'damaged'
  | 'in_maintenance'
  | 'abandoned'
  | 'seized'

// ── Paleta (hex ← tokens Sass de settings/_colors.scss) ──────────────────────

/** $color-gray-950 — fondo del mundo fuera de mapa. */
export const COLOR_WORLD_BG = 0x0d1117
/** $color-gray-950 — bordes/trazos oscuros. */
const COLOR_STROKE = 0x0d1117
/** $color-gray-700 — cuerpo de la mina. */
const COLOR_MINE_BODY = 0x2e3a4f
/** $color-gray-600 — edificio abandonado. */
const COLOR_ABANDONED = 0x45536b
/** $color-gray-300 — relleno base de edificio. */
const COLOR_BUILDING = 0xaab7c9
/** $color-gray-200 — nodo logístico. */
const COLOR_NODE = 0xcdd6e2
/** $color-gray-100 — cuerpo de ciudad. */
const COLOR_CITY = 0xe7ecf2
/** $color-accent-600 — yacimiento (rombo). */
const COLOR_DEPOSIT = 0xb0761a
/** $color-accent-500 — edificio en construcción / detalle de mina y ciudad. */
const COLOR_ACCENT = 0xd29224
/** $color-accent-400 — marcador de selección. */
const COLOR_SELECTION = 0xe3ab45
/** $color-info-500 — edificio en mantenimiento (pausa). */
const COLOR_MAINTENANCE = 0x3f7fae
/** $color-info-400 — vehículo. */
const COLOR_VEHICLE = 0x5f9bc4
/** $color-danger-500 — edificio dañado / franja de embargo. */
const COLOR_DANGER = 0xc4504a
/** $color-danger-400 — relleno de edificio embargado (seized). */
const COLOR_SEIZED = 0xd96f68
/** $color-success-400 — ghost de emplazamiento válido. */
const COLOR_GHOST = 0x57b877

/**
 * Color de suelo por bioma (sin token propio; derivados de la paleta):
 * verdes desde $color-success-500 desaturado hacia el grafito, arena desde la
 * familia ámbar, montaña = $color-gray-500 literal, aguas desde $color-info-500
 * oscurecido. Lo consume el ChunkManager para el rectángulo de terreno.
 */
// NOTA: MINIMAP_BIOME_COLORS (app/components/play/minimap/MinimapPanel.vue)
// es un espejo consciente de esta paleta en hex CSS (ADR-026). Cambiar un
// color aquí exige cambiarlo también allí.
export const BIOME_COLORS: Readonly<Record<BiomeName, number>> = {
  plains: 0x3f5a3c,
  forest: 0x2c4430,
  desert: 0x7d6f45,
  mountain: 0x5f6f88, // $color-gray-500
  ocean: 0x1d3a54,
  coast: 0x35597a,
}

// ── Claves de textura ────────────────────────────────────────────────────────

export const TEXTURES = {
  mine: 'tx-mine',
  city: 'tx-city',
  deposit: 'tx-deposit',
  node: 'tx-node',
  vehicle: 'tx-vehicle',
  selection: 'tx-selection',
  ghost: 'tx-ghost',
} as const

/** Clave de textura del tile de un bioma. */
export function biomeTextureKey(biome: BiomeName): string {
  return `tx-tile-${biome}`
}

/** Clave de textura del edificio según su estado visual. */
export function buildingTextureKey(status: BuildingStatusName): string {
  return `tx-building-${status}`
}

const BUILDING_FILL: Readonly<Record<BuildingStatusName, number>> = {
  operational: COLOR_BUILDING,
  under_construction: COLOR_ACCENT,
  damaged: COLOR_DANGER,
  in_maintenance: COLOR_MAINTENANCE,
  abandoned: COLOR_ABANDONED,
  seized: COLOR_SEIZED,
}

/** Diámetro base de la ciudad (px); el sprite se escala por nivel. */
export const CITY_BASE_PX = 64
/** Lado base de edificio/mina/yacimiento (px) = 1 tile. */
export const ENTITY_BASE_PX = TILE_PX
/** Vehículo: rectángulo orientable apuntando a +X (rotación = ángulo del tramo). */
export const VEHICLE_W_PX = 14
export const VEHICLE_H_PX = 8
/** Lado del marcador de selección (anillo cuadrado alrededor de 1 tile). */
export const SELECTION_PX = TILE_PX + 6
/** Diámetro del nodo logístico. */
export const NODE_PX = 8

type Gfx = Phaser.GameObjects.Graphics

function withGraphics(scene: Phaser.Scene, draw: (g: Gfx) => void): void {
  const g = scene.make.graphics()
  draw(g)
  g.destroy()
}

function generateIfMissing(
  scene: Phaser.Scene,
  key: string,
  width: number,
  height: number,
  draw: (g: Gfx) => void,
): void {
  if (scene.textures.exists(key)) {
    return
  }
  withGraphics(scene, (g) => {
    draw(g)
    g.generateTexture(key, width, height)
  })
}

/** Cuadrado con borde (edificios y variantes). */
function drawSquare(g: Gfx, size: number, fill: number): void {
  g.fillStyle(fill, 1)
  g.fillRect(1, 1, size - 2, size - 2)
  g.lineStyle(2, COLOR_STROKE, 1)
  g.strokeRect(1, 1, size - 2, size - 2)
}

/**
 * Genera TODAS las texturas del mundo (idempotente: las existentes se saltan).
 * Se invoca una única vez desde BootScene, antes de arrancar WorldScene.
 */
export function makeWorldTextures(scene: Phaser.Scene): void {
  // Tiles de bioma: color plano, legible, sin ruido (el terreno es fondo).
  // Hoy el ChunkManager pinta 1 rectángulo por chunk (el backend aún no expone
  // terreno por tile); estas texturas quedan listas para datos por tile.
  for (const [biome, color] of Object.entries(BIOME_COLORS) as [BiomeName, number][]) {
    generateIfMissing(scene, biomeTextureKey(biome), TILE_PX, TILE_PX, (g) => {
      g.fillStyle(color, 1)
      g.fillRect(0, 0, TILE_PX, TILE_PX)
    })
  }

  // Edificio: cuadrado con borde, relleno tintado por estado del contrato.
  for (const [status, fill] of Object.entries(BUILDING_FILL) as [BuildingStatusName, number][]) {
    generateIfMissing(scene, buildingTextureKey(status), ENTITY_BASE_PX, ENTITY_BASE_PX, (g) => {
      drawSquare(g, ENTITY_BASE_PX, fill)
      if (status === 'seized') {
        // Franja diagonal: el embargo se distingue de "dañado" también por forma.
        g.lineStyle(3, COLOR_DANGER, 1)
        g.lineBetween(2, ENTITY_BASE_PX - 2, ENTITY_BASE_PX - 2, 2)
      }
    })
  }

  // Mina: cuadrado grafito con triángulo ámbar invertido (bocamina).
  generateIfMissing(scene, TEXTURES.mine, ENTITY_BASE_PX, ENTITY_BASE_PX, (g) => {
    drawSquare(g, ENTITY_BASE_PX, COLOR_MINE_BODY)
    const s = ENTITY_BASE_PX
    g.fillStyle(COLOR_ACCENT, 1)
    g.fillTriangle(s * 0.25, s * 0.3, s * 0.75, s * 0.3, s * 0.5, s * 0.75)
  })

  // Ciudad: círculo claro con borde y núcleo ámbar; el sprite escala por nivel.
  generateIfMissing(scene, TEXTURES.city, CITY_BASE_PX, CITY_BASE_PX, (g) => {
    const r = CITY_BASE_PX / 2
    g.fillStyle(COLOR_CITY, 1)
    g.fillCircle(r, r, r - 2)
    g.lineStyle(2, COLOR_STROKE, 1)
    g.strokeCircle(r, r, r - 2)
    g.fillStyle(COLOR_ACCENT, 1)
    g.fillCircle(r, r, r * 0.28)
  })

  // Yacimiento: rombo ámbar con borde.
  generateIfMissing(scene, TEXTURES.deposit, ENTITY_BASE_PX, ENTITY_BASE_PX, (g) => {
    const s = ENTITY_BASE_PX
    g.fillStyle(COLOR_DEPOSIT, 1)
    g.beginPath()
    g.moveTo(s / 2, 1)
    g.lineTo(s - 1, s / 2)
    g.lineTo(s / 2, s - 1)
    g.lineTo(1, s / 2)
    g.closePath()
    g.fillPath()
    g.lineStyle(2, COLOR_STROKE, 1)
    g.strokePath()
  })

  // Nodo logístico: punto neutro con borde.
  generateIfMissing(scene, TEXTURES.node, NODE_PX, NODE_PX, (g) => {
    const r = NODE_PX / 2
    g.fillStyle(COLOR_NODE, 1)
    g.fillCircle(r, r, r - 1)
    g.lineStyle(1, COLOR_STROKE, 1)
    g.strokeCircle(r, r, r - 1)
  })

  // Vehículo: rectángulo orientable con morro claro apuntando a +X.
  generateIfMissing(scene, TEXTURES.vehicle, VEHICLE_W_PX, VEHICLE_H_PX, (g) => {
    g.fillStyle(COLOR_VEHICLE, 1)
    g.fillRect(0, 0, VEHICLE_W_PX - 4, VEHICLE_H_PX)
    g.fillStyle(COLOR_CITY, 1) // morro: mismo claro que la ciudad ($color-gray-100)
    g.fillRect(VEHICLE_W_PX - 4, 1, 4, VEHICLE_H_PX - 2)
    g.lineStyle(1, COLOR_STROKE, 1)
    g.strokeRect(0, 0, VEHICLE_W_PX, VEHICLE_H_PX)
  })

  // Selección: anillo cuadrado ámbar (solo trazo) alrededor de la entidad.
  generateIfMissing(scene, TEXTURES.selection, SELECTION_PX, SELECTION_PX, (g) => {
    g.lineStyle(2, COLOR_SELECTION, 1)
    g.strokeRect(1, 1, SELECTION_PX - 2, SELECTION_PX - 2)
  })

  // Ghost de emplazamiento: cuadrado verde translúcido (el alpha se hornea).
  generateIfMissing(scene, TEXTURES.ghost, ENTITY_BASE_PX, ENTITY_BASE_PX, (g) => {
    g.fillStyle(COLOR_GHOST, 0.35)
    g.fillRect(1, 1, ENTITY_BASE_PX - 2, ENTITY_BASE_PX - 2)
    g.lineStyle(2, COLOR_GHOST, 0.8)
    g.strokeRect(1, 1, ENTITY_BASE_PX - 2, ENTITY_BASE_PX - 2)
  })
}
