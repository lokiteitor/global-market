/**
 * shared/geometry — GridProjection ortogonal ÚNICA del cliente (ADR-019, FAD §16.2).
 *
 * ÚNICO punto de conversión entre coordenadas del mundo y coordenadas de
 * pantalla/render. Nadie más multiplica por tamaños de tile ni divide por
 * escalas: toda matemática mundo ↔ píxel ↔ tile ↔ chunk vive aquí.
 *
 * Sistemas de coordenadas (ADR-019, SRID 0 planar):
 * - **Metros de mundo** `[x_m, y_m]`: lo que transporta la API (GeoJSON-like
 *   plano). Origen (0,0) en la esquina superior izquierda de la región
 *   inicial (Askadia), eje Y hacia abajo (coherente con el plano de pantalla).
 *   Con el mundo multi-región (GDD §9) las regiones al oeste/norte tienen
 *   coordenadas NEGATIVAS: el mundo ya no es un cuadrado fijo sino un
 *   `WorldBoundsM` derivado del catálogo de regiones en runtime (FAD §17.6);
 *   `DEFAULT_WORLD_BOUNDS_M` (Askadia) es solo el fallback pre-catálogo.
 * - **Píxeles de mundo**: espacio de render de Phaser (antes del zoom de la
 *   cámara). Escala FIJA (esta sí es invariante): 1 tile = 250 m = 32 px
 *   ⇒ 0.128 px/m.
 * - **Tiles**: celdas de la rejilla ortogonal (índices negativos válidos al
 *   oeste/norte del origen).
 * - **Chunks**: bloques de 32 × 32 tiles (unidad de streaming/culling,
 *   FAD §16.3), con índices también negativos; los del borde del mundo son
 *   parciales (sus bounds se recortan a los bounds del mundo aparte).
 *
 * Kernel puro: sin Vue/Nuxt/Pinia/Phaser. Testeado exhaustivamente.
 */

/** Metros de mundo por tile (ADR-019). */
export const WORLD_M_PER_TILE = 250

/** Píxeles de render por tile. */
export const TILE_PX = 32

/** Escala fija píxel/metro: 32 px / 250 m = 0.128. */
export const PX_PER_M = TILE_PX / WORLD_M_PER_TILE

/**
 * Lado de la región inicial (Askadia) en metros (50 km). Desde el mundo
 * multi-región NO es "el lado del mundo": solo la base del fallback
 * `DEFAULT_WORLD_BOUNDS_M` mientras el catálogo de regiones no ha llegado.
 */
export const WORLD_SIZE_M = 50_000

/** Lado del fallback en tiles: 50 000 / 250 = 200. */
export const WORLD_SIZE_TILES = WORLD_SIZE_M / WORLD_M_PER_TILE

/** Lado del fallback en píxeles de render: 200 × 32 = 6 400. */
export const WORLD_SIZE_PX = WORLD_SIZE_TILES * TILE_PX

/** Lado de un chunk en tiles (FAD §16.3). */
export const CHUNK_TILES = 32

/** Lado de un chunk en píxeles: 32 × 32 = 1 024. */
export const CHUNK_PX = CHUNK_TILES * TILE_PX

/**
 * Límites del mundo en METROS (rectángulo semiabierto [min, max) por eje).
 * Se derivan en runtime de la unión de los bounds del catálogo de regiones
 * (`unionBoundsM`); admiten mínimos negativos (regiones al oeste/norte de
 * Askadia).
 */
export interface WorldBoundsM {
  readonly minXM: number
  readonly minYM: number
  readonly maxXM: number
  readonly maxYM: number
}

/** Límites del mundo en PÍXELES de render (proyección lineal de `WorldBoundsM`). */
export interface WorldBoundsPx {
  readonly minXPx: number
  readonly minYPx: number
  readonly maxXPx: number
  readonly maxYPx: number
}

/** Fallback pre-catálogo: la región inicial Askadia, [0, 50 000)². */
export const DEFAULT_WORLD_BOUNDS_M: WorldBoundsM = {
  minXM: 0,
  minYM: 0,
  maxXM: WORLD_SIZE_M,
  maxYM: WORLD_SIZE_M,
}

/** Bounds del mundo en metros → píxeles de render. */
export function boundsMToPx(bounds: WorldBoundsM): WorldBoundsPx {
  return {
    minXPx: bounds.minXM * PX_PER_M,
    minYPx: bounds.minYM * PX_PER_M,
    maxXPx: bounds.maxXM * PX_PER_M,
    maxYPx: bounds.maxYM * PX_PER_M,
  }
}

/**
 * Unión (bounding box) de una lista de bounds. `null` con lista vacía. Tolera
 * mundos no contiguos (configs exóticas del worldgen): la unión solo crece.
 */
export function unionBoundsM(list: readonly WorldBoundsM[]): WorldBoundsM | null {
  const head = list[0]
  if (head === undefined) {
    return null
  }
  let { minXM, minYM, maxXM, maxYM } = head
  for (const b of list.slice(1)) {
    minXM = Math.min(minXM, b.minXM)
    minYM = Math.min(minYM, b.minYM)
    maxXM = Math.max(maxXM, b.maxXM)
    maxYM = Math.max(maxYM, b.maxYM)
  }
  return { minXM, minYM, maxXM, maxYM }
}

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

/** Coordenada de chunk (enteros; negativos al oeste/norte del origen). */
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
 * Metros de mundo → celda (floor). NO recorta al mundo: es el llamante quien
 * decide qué hacer fuera de él (picking fuera de mapa, culling…);
 * `isInsideWorldM` responde la pregunta de pertenencia contra unos bounds.
 */
export function mToTile(xM: number, yM: number): TileCoord {
  return { tx: Math.floor(xM / WORLD_M_PER_TILE), ty: Math.floor(yM / WORLD_M_PER_TILE) }
}

/** Celda → chunk que la contiene (floor; tampoco recorta al mundo). */
export function tileToChunk(tx: number, ty: number): ChunkCoord {
  return { cx: Math.floor(tx / CHUNK_TILES), cy: Math.floor(ty / CHUNK_TILES) }
}

/** ¿El punto en metros cae dentro del mundo? (mínimos inclusivos, máximos exclusivos). */
export function isInsideWorldM(xM: number, yM: number, bounds: WorldBoundsM): boolean {
  return xM >= bounds.minXM && xM < bounds.maxXM && yM >= bounds.minYM && yM < bounds.maxYM
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
 * `null` si el chunk no interseca los bounds del mundo.
 */
export function chunkBoundsClamped(cx: number, cy: number, worldPx: WorldBoundsPx): RectPx | null {
  const nominal = chunkBounds(cx, cy)
  const x0 = Math.max(nominal.x, worldPx.minXPx)
  const y0 = Math.max(nominal.y, worldPx.minYPx)
  const x1 = Math.min(nominal.x + nominal.width, worldPx.maxXPx)
  const y1 = Math.min(nominal.y + nominal.height, worldPx.maxYPx)
  if (x1 <= x0 || y1 <= y0) {
    return null
  }
  return { x: x0, y: y0, width: x1 - x0, height: y1 - y0 }
}

/**
 * Chunks del mundo que interseca el rectángulo de cámara (en píxeles de
 * render) expandido en `marginChunks` chunks por cada lado (anillo de
 * histéresis del culling, FAD §16.5). Siempre recortado a la rejilla que
 * cubre `worldPx` (índices posiblemente negativos): un viewport totalmente
 * fuera del mundo produce lista vacía. Un borde del rect exactamente sobre
 * una frontera de chunk NO incluye el chunk tangente del otro lado
 * (intervalo semiabierto), y un rect de área cero no interseca nada.
 *
 * Orden de salida: filas (cy) y dentro de cada fila columnas (cx), ascendente
 * — determinista para diffing y tests.
 */
export function visibleChunks(
  cameraViewPxRect: RectPx,
  worldPx: WorldBoundsPx,
  marginChunks = 0,
): ChunkCoord[] {
  const { x, y, width, height } = cameraViewPxRect
  if (width <= 0 || height <= 0) {
    return []
  }
  const first = (start: number): number => Math.floor(start / CHUNK_PX)
  // Último chunk intersecado: intervalo semiabierto [start, start+size).
  const last = (start: number, size: number): number => Math.ceil((start + size) / CHUNK_PX) - 1

  // Rejilla válida: chunks que intersecan los bounds del mundo (semiabiertos).
  const worldCx0 = Math.floor(worldPx.minXPx / CHUNK_PX)
  const worldCy0 = Math.floor(worldPx.minYPx / CHUNK_PX)
  const worldCx1 = Math.ceil(worldPx.maxXPx / CHUNK_PX) - 1
  const worldCy1 = Math.ceil(worldPx.maxYPx / CHUNK_PX) - 1

  const cx0 = Math.max(first(x) - marginChunks, worldCx0)
  const cy0 = Math.max(first(y) - marginChunks, worldCy0)
  const cx1 = Math.min(last(x, width) + marginChunks, worldCx1)
  const cy1 = Math.min(last(y, height) + marginChunks, worldCy1)

  const out: ChunkCoord[] = []
  for (let cy = cy0; cy <= cy1; cy += 1) {
    for (let cx = cx0; cx <= cx1; cx += 1) {
      out.push({ cx, cy })
    }
  }
  return out
}
