/**
 * kernel/projection.ts — proyección del mundo a pantalla.
 *
 * SIMPLIFICACIÓN v1 (aceptada): proyección TOP-DOWN equirectangular simple
 * (lon/lat → px con escala lineal). La proyección ISOMÉTRICA del FAD llega en
 * FE-6; este módulo es el único punto que se sustituirá entonces — nada fuera
 * de aquí conoce la fórmula de proyección.
 *
 * Convención de pantalla: x crece hacia el este, y crece hacia el SUR (norte
 * arriba), de ahí la inversión de latitud.
 */

export interface ProjectionConfig {
  /** Píxeles por grado de lon/lat (escala uniforme en v1). */
  readonly pxPerDegree: number
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

export const DEFAULT_PROJECTION: ProjectionConfig = {
  pxPerDegree: 900,
  originLon: 0,
  originLat: 0
}

export interface WorldProjection {
  readonly config: ProjectionConfig
  worldToScreen(lon: number, lat: number): ScreenPoint
  screenToWorld(x: number, y: number): WorldPoint
}

export function createProjection(config: Partial<ProjectionConfig> = {}): WorldProjection {
  const cfg: ProjectionConfig = { ...DEFAULT_PROJECTION, ...config }

  return {
    config: cfg,

    worldToScreen(lon, lat) {
      return {
        x: (lon - cfg.originLon) * cfg.pxPerDegree,
        y: (cfg.originLat - lat) * cfg.pxPerDegree
      }
    },

    screenToWorld(x, y) {
      return {
        lon: cfg.originLon + x / cfg.pxPerDegree,
        lat: cfg.originLat - y / cfg.pxPerDegree
      }
    }
  }
}
