/**
 * shared/geometry — GridProjection ortogonal ÚNICA del cliente (ADR-019, FAD §16.2).
 *
 * ÚNICO punto de conversión entre coordenadas del mundo y coordenadas de
 * pantalla/render. Nadie más multiplica por tamaños de tile ni divide por
 * escalas: toda matemática mundo ↔ píxel ↔ tile ↔ chunk vive aquí.
 *
 * Sistemas de coordenadas (ADR-019, SRID 0 planar):
 * - **Metros de mundo** `[x_m, y_m]`: lo que transporta la API (GeoJSON-like
 *   plano). Mundo Askadia: 50 000 × 50 000 m, origen (0,0) esquina superior
 *   izquierda, eje Y hacia abajo (coherente con el plano de pantalla).
 * - **Píxeles de mundo**: espacio de render de Phaser (antes del zoom de la
 *   cámara). Escala fija: 1 tile = 250 m = 32 px ⇒ 0.128 px/m.
 * - **Tiles**: celdas de la rejilla ortogonal (200 × 200 en Askadia).
 * - **Chunks**: bloques de 32 × 32 tiles (unidad de streaming/culling,
 *   FAD §16.3). 200/32 = 6.25 ⇒ rejilla de 7 × 7 chunks con el borde
 *   derecho/inferior parciales (sus bounds se recortan al mundo aparte).
 *
 * Kernel puro: sin Vue/Nuxt/Pinia/Phaser. Testeado exhaustivamente.
 */

/** Metros de mundo por tile (ADR-019). */
export const WORLD_M_PER_TILE = 250

/** Píxeles de render por tile. */
export const TILE_PX = 32

/** Escala fija píxel/metro: 32 px / 250 m = 0.128. */
export const PX_PER_M = TILE_PX / WORLD_M_PER_TILE

/** Lado del mundo Askadia en metros (50 km). */
export const WORLD_SIZE_M = 50_000

/** Lado del mundo en tiles: 50 000 / 250 = 200. */
export const WORLD_SIZE_TILES = WORLD_SIZE_M / WORLD_M_PER_TILE

/** Lado del mundo en píxeles de render: 200 × 32 = 6 400. */
export const WORLD_SIZE_PX = WORLD_SIZE_TILES * TILE_PX

/** Lado de un chunk en tiles (FAD §16.3). */
export const CHUNK_TILES = 32

/** Lado de un chunk en píxeles: 32 × 32 = 1 024. */
export const CHUNK_PX = CHUNK_TILES * TILE_PX

/** Chunks por eje que cubren el mundo: ceil(200/32) = 7 (el último, parcial). */
export const WORLD_CHUNKS = Math.ceil(WORLD_SIZE_TILES / CHUNK_TILES)

/** Punto en metros de mundo (coordenadas planas de la API, ADR-019). */
export interface PointM {
  readonly xM: number
  readonly yM: number
}

/** Punto en píxeles de render (espacio de mundo de Phaser, pre-zoom). */
export interface PointPx {
  readonly xPx: number
  readonly yPx: number
}

/** Celda de la rejilla ortogonal (enteros; fuera de mundo pueden ser negativos). */
export interface TileCoord {
  readonly tx: number
  readonly ty: number
}

/** Coordenada de chunk (enteros; el mundo válido es [0, WORLD_CHUNKS)²). */
export interface ChunkCoord {
  readonly cx: number
  readonly cy: number
}

/** Rectángulo en píxeles de render (x/y = esquina superior izquierda). */
export interface RectPx {
  readonly x: number
  readonly y: number
  readonly width: number
  readonly height: number
}

/** Metros de mundo → píxeles de render (lineal; conserva fracciones). */
export function mToPx(xM: number, yM: number): PointPx {
  return { xPx: xM * PX_PER_M, yPx: yM * PX_PER_M }
}

/** Píxeles de render → metros de mundo (inversa exacta de `mToPx`). */
export function pxToM(xPx: number, yPx: number): PointM {
  return { xM: xPx / PX_PER_M, yM: yPx / PX_PER_M }
}

/**
 * Metros de mundo → celda (floor). NO recorta al mundo: coordenadas negativas
 * o más allá del borde producen tiles fuera de [0, WORLD_SIZE_TILES) y es el
 * llamante quien decide (picking fuera de mapa, culling…). `isInsideWorldM`
 * responde la pregunta de pertenencia.
 */
export function mToTile(xM: number, yM: number): TileCoord {
  return { tx: Math.floor(xM / WORLD_M_PER_TILE), ty: Math.floor(yM / WORLD_M_PER_TILE) }
}

/** Celda → chunk que la contiene (floor; tampoco recorta al mundo). */
export function tileToChunk(tx: number, ty: number): ChunkCoord {
  return { cx: Math.floor(tx / CHUNK_TILES), cy: Math.floor(ty / CHUNK_TILES) }
}

/** ¿El punto en metros cae dentro del mundo? (bordes superior/izquierdo inclusivos). */
export function isInsideWorldM(xM: number, yM: number): boolean {
  return xM >= 0 && xM < WORLD_SIZE_M && yM >= 0 && yM < WORLD_SIZE_M
}

/** Clave estable de un chunk para mapas/sets (diffing del ChunkManager). */
export function chunkKey(cx: number, cy: number): string {
  return `${String(cx)}:${String(cy)}`
}

/** Inversa de `chunkKey`. Lanza `RangeError` ante una clave malformada. */
export function parseChunkKey(key: string): ChunkCoord {
  const match = /^(-?\d+):(-?\d+)$/.exec(key)
  if (!match) {
    throw new RangeError(`parseChunkKey: clave inválida "${key}"`)
  }
  return { cx: Number(match[1]), cy: Number(match[2]) }
}

/**
 * Bounds NOMINALES de un chunk en píxeles de render (rejilla completa, sin
 * recortar al mundo). Los chunks del borde derecho/inferior sobresalen del
 * mundo; usa `chunkBoundsClamped` para el rectángulo realmente pintable.
 */
export function chunkBounds(cx: number, cy: number): RectPx {
  return { x: cx * CHUNK_PX, y: cy * CHUNK_PX, width: CHUNK_PX, height: CHUNK_PX }
}

/**
 * Bounds de un chunk recortados al mundo (rectángulo pintable). Devuelve
 * `null` si el chunk no interseca el mundo (fuera de [0, WORLD_CHUNKS)²).
 */
export function chunkBoundsClamped(cx: number, cy: number): RectPx | null {
  const nominal = chunkBounds(cx, cy)
  const x0 = Math.max(nominal.x, 0)
  const y0 = Math.max(nominal.y, 0)
  const x1 = Math.min(nominal.x + nominal.width, WORLD_SIZE_PX)
  const y1 = Math.min(nominal.y + nominal.height, WORLD_SIZE_PX)
  if (x1 <= x0 || y1 <= y0) {
    return null
  }
  return { x: x0, y: y0, width: x1 - x0, height: y1 - y0 }
}

/**
 * Chunks del mundo que interseca el rectángulo de cámara (en píxeles de
 * render) expandido en `marginChunks` chunks por cada lado (anillo de
 * histéresis del culling, FAD §16.5). Siempre recortado a la rejilla válida
 * [0, WORLD_CHUNKS)²: un viewport totalmente fuera del mundo produce lista
 * vacía. Un borde del rect exactamente sobre una frontera de chunk NO incluye
 * el chunk tangente del otro lado (intervalo semiabierto), y un rect de área
 * cero no interseca nada.
 *
 * Orden de salida: filas (cy) y dentro de cada fila columnas (cx), ascendente
 * — determinista para diffing y tests.
 */
export function visibleChunks(cameraViewPxRect: RectPx, marginChunks = 0): ChunkCoord[] {
  const { x, y, width, height } = cameraViewPxRect
  if (width <= 0 || height <= 0) {
    return []
  }
  const first = (start: number): number => Math.floor(start / CHUNK_PX)
  // Último chunk intersecado: intervalo semiabierto [start, start+size).
  const last = (start: number, size: number): number => Math.ceil((start + size) / CHUNK_PX) - 1

  const cx0 = Math.max(first(x) - marginChunks, 0)
  const cy0 = Math.max(first(y) - marginChunks, 0)
  const cx1 = Math.min(last(x, width) + marginChunks, WORLD_CHUNKS - 1)
  const cy1 = Math.min(last(y, height) + marginChunks, WORLD_CHUNKS - 1)

  const out: ChunkCoord[] = []
  for (let cy = cy0; cy <= cy1; cy += 1) {
    for (let cx = cx0; cx <= cx1; cx += 1) {
      out.push({ cx, cy })
    }
  }
  return out
}
