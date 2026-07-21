/**
 * game/map/chunks — ChunkManager: streaming/culling del terreno (FAD §16.3/§16.5/§16.8).
 *
 * Dado el viewport de la cámara calcula los chunks visibles + anillo de margen
 * (histéresis), materializa el terreno de cada chunk y cachea/desaloja con LRU.
 * La lógica pura (diffing, LRU) vive en chunk-logic.ts; aquí solo la cara
 * Phaser (crear/mostrar/ocultar/destruir GameObjects).
 *
 * TERRENO PLACEHOLDER CONSCIENTE: el backend aún no expone terreno por tile,
 * así que cada chunk se materializa como UN rectángulo del color del bioma de
 * la región que contiene su centro (lookup por bounds de región inyectado como
 * función). El chunking/streaming/culling se ejercita igual y queda listo para
 * sustituir el rectángulo por tiles reales (texturas de bioma ya generadas en
 * game/textures.ts) cuando lleguen datos por tile.
 */

import type Phaser from 'phaser'

import type { ChunkCoord, RectPx } from '~shared/geometry/grid'
import { chunkBoundsClamped, chunkKey, pxToM, visibleChunks } from '~shared/geometry/grid'

import type { BiomeName } from '../textures'
import { BIOME_COLORS, COLOR_WORLD_BG } from '../textures'
import { ChunkLru, diffChunks } from './chunk-logic'

/** Bioma en un punto del mundo (metros). `null` = fuera de toda región. */
export type BiomeLookup = (xM: number, yM: number) => BiomeName | null

export interface ChunkManagerOptions {
  readonly scene: Phaser.Scene
  /** Capa de terreno de WorldScene (el orden de capas es el z-order). */
  readonly terrainLayer: Phaser.GameObjects.Layer
  /** Lookup de bioma por bounds de región (inyectado; el manager no conoce stores). */
  readonly biomeAtM: BiomeLookup
  /** Anillo de chunks extra alrededor del viewport (histéresis, FAD §16.5). */
  readonly marginChunks?: number
  /** Máximo de chunks materializados (visibles + cached) antes de desalojar LRU. */
  readonly maxCachedChunks?: number
  readonly onChunkShown?: (coord: ChunkCoord) => void
  readonly onChunkHidden?: (key: string) => void
}

export interface ChunkStats {
  readonly visible: number
  readonly materialized: number
}

const DEFAULT_MARGIN = 1
const DEFAULT_MAX_CACHED = 128

export class ChunkManager {
  private readonly objects = new Map<string, Phaser.GameObjects.Rectangle>()
  private visible = new Set<string>()
  private readonly lru: ChunkLru
  private readonly marginChunks: number

  constructor(private readonly options: ChunkManagerOptions) {
    this.marginChunks = options.marginChunks ?? DEFAULT_MARGIN
    this.lru = new ChunkLru(options.maxCachedChunks ?? DEFAULT_MAX_CACHED)
  }

  /** Recalcula chunks visibles para el viewport (px de render) y reconcilia. */
  update(viewRectPx: RectPx): void {
    const next = visibleChunks(viewRectPx, this.marginChunks)
    const diff = diffChunks(this.visible, next)

    for (const coord of diff.shown) {
      this.show(coord)
    }
    for (const key of diff.hidden) {
      this.hide(key)
    }

    this.visible = new Set(next.map((c) => chunkKey(c.cx, c.cy)))
    this.evict()
  }

  stats(): ChunkStats {
    return { visible: this.visible.size, materialized: this.objects.size }
  }

  /** Destruye todos los chunks materializados (shutdown de escena). */
  destroy(): void {
    for (const [key, rect] of this.objects) {
      rect.destroy()
      this.lru.delete(key)
    }
    this.objects.clear()
    this.visible.clear()
  }

  private show(coord: ChunkCoord): void {
    const key = chunkKey(coord.cx, coord.cy)
    const cached = this.objects.get(key)
    if (cached) {
      cached.setVisible(true)
    } else {
      const rect = this.materialize(coord)
      if (!rect) {
        return
      }
      this.objects.set(key, rect)
    }
    this.lru.touch(key)
    this.options.onChunkShown?.(coord)
  }

  private hide(key: string): void {
    // Pasa a "cached": invisible pero materializado, hasta que el LRU lo desaloje.
    this.objects.get(key)?.setVisible(false)
    this.options.onChunkHidden?.(key)
  }

  private evict(): void {
    for (const key of this.lru.planEviction(this.visible)) {
      this.objects.get(key)?.destroy()
      this.objects.delete(key)
      this.lru.delete(key)
    }
  }

  private materialize(coord: ChunkCoord): Phaser.GameObjects.Rectangle | null {
    const bounds = chunkBoundsClamped(coord.cx, coord.cy)
    if (!bounds) {
      // visibleChunks ya recorta al mundo; guarda defensiva.
      return null
    }
    // Bioma de la región que contiene el CENTRO del chunk (simplificación
    // documentada: un chunk que cruce frontera de región toma un solo color).
    const center = pxToM(bounds.x + bounds.width / 2, bounds.y + bounds.height / 2)
    const biome = this.options.biomeAtM(center.xM, center.yM)
    const color = biome === null ? COLOR_WORLD_BG : BIOME_COLORS[biome]
    const rect = this.options.scene.add
      .rectangle(bounds.x, bounds.y, bounds.width, bounds.height, color)
      .setOrigin(0, 0)
    this.options.terrainLayer.add(rect)
    return rect
  }
}
