/**
 * kernel/projection.ts — proyección del mundo a pantalla.
 *
 * FE-6: proyección ISOMÉTRICA 2:1 estilo Simutrans (rombo de 128×64 px).
 * Este módulo es el ÚNICO punto que conoce la fórmula — nada fuera de aquí
 * hace math de proyección ni de tiles.
 *
 * Dos pasos afines:
 *   lon/lat → (u,v) coordenadas de tile continuas (u crece al ESTE, v al SUR)
 *   (u,v)   → px de pantalla-mundo: x=(u-v)·tw/2, y=(u+v)·th/2
 *
 * Convención resultante (Simutrans): el norte queda arriba-derecha y el este
 * abajo-derecha. La transformación es afín e invertible de forma exacta.
 */

export interface ProjectionConfig {
  /** Tiles por grado de lon/lat (escala del mundo en celdas). */
  readonly tilesPerDegree: number
  /** Ancho del rombo base en px (pak128: 128). */
  readonly tileWidth: number
  /** Alto del rombo base en px (pak128: 64, relación 2:1). */
  readonly tileHeight: number
  /** Longitud del origen de pantalla (x = 0). */
  readonly originLon: number
  /** Latitud del origen de pantalla (y = 0). */
  readonly originLat: number
}

export interface ScreenPoint {
  readonly x: number
  readonly y: number
}

export interface WorldPoint {
  readonly lon: number
  readonly lat: number
}

/** Coordenadas de tile CONTINUAS; floor() da el índice de celda entera. */
export interface TilePoint {
  readonly u: number
  readonly v: number
}

export const DEFAULT_PROJECTION: ProjectionConfig = {
  tilesPerDegree: 32,
  tileWidth: 128,
  tileHeight: 64,
  originLon: 0,
  originLat: 0
}

export interface WorldProjection {
  readonly config: ProjectionConfig
  worldToScreen(lon: number, lat: number): ScreenPoint
  screenToWorld(x: number, y: number): WorldPoint
}

/** Proyección isométrica: expone además la capa intermedia de tiles. */
export interface IsoProjection extends WorldProjection {
  /** lon/lat → coordenadas de tile continuas. */
  worldToTile(lon: number, lat: number): TilePoint
  /** Coordenadas de tile (continuas) → px de pantalla-mundo. */
  tileToScreen(u: number, v: number): ScreenPoint
}

export function createProjection(config: Partial<ProjectionConfig> = {}): IsoProjection {
  const cfg: ProjectionConfig = { ...DEFAULT_PROJECTION, ...config }
  const halfW = cfg.tileWidth / 2
  const halfH = cfg.tileHeight / 2

  const worldToTile = (lon: number, lat: number): TilePoint => ({
    u: (lon - cfg.originLon) * cfg.tilesPerDegree,
    v: (cfg.originLat - lat) * cfg.tilesPerDegree
  })

  const tileToScreen = (u: number, v: number): ScreenPoint => ({
    x: (u - v) * halfW,
    y: (u + v) * halfH
  })

  return {
    config: cfg,
    worldToTile,
    tileToScreen,

    worldToScreen(lon, lat) {
      const t = worldToTile(lon, lat)
      return tileToScreen(t.u, t.v)
    },

    screenToWorld(x, y) {
      const u = (x / halfW + y / halfH) / 2
      const v = (y / halfH - x / halfW) / 2
      return {
        lon: cfg.originLon + u / cfg.tilesPerDegree,
        lat: cfg.originLat - v / cfg.tilesPerDegree
      }
    }
  }
}
