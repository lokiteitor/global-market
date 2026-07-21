/**
 * domain/geo — tipos geométricos del mundo en METROS (ADR-019, SRID 0 planar).
 *
 * El contrato transporta la geometría GeoJSON-like con coordenadas PLANAS de
 * mundo `[x_m, y_m]` (desviación consciente de RFC 7946). El dominio del
 * cliente la modela con tuplas inmutables; la conversión a píxeles es asunto
 * EXCLUSIVO de `GridProjection` (shared/geometry) en la capa de render —
 * aquí no hay ninguna matemática de pantalla.
 */

/** Punto del mundo en metros `[x_m, y_m]` (SRID 0, cartesiano). */
export type WorldPointM = readonly [number, number]

/** Polilínea del mundo en metros (p. ej. `path` de un enlace logístico). */
export type WorldPathM = readonly WorldPointM[]

/**
 * Polígono del mundo en metros: lista de anillos; `[0]` es el anillo exterior
 * (cerrado, como en GeoJSON). Los anillos interiores (huecos) son legales por
 * contrato aunque el juego aún no los produzca.
 */
export type WorldPolygonM = readonly WorldPathM[]

/** ¿Es una tupla `[x, y]` con ambas coordenadas finitas? (guarda para mappers). */
export function isWorldPointM(value: unknown): value is WorldPointM {
  return (
    Array.isArray(value) &&
    value.length === 2 &&
    typeof value[0] === 'number' &&
    typeof value[1] === 'number' &&
    Number.isFinite(value[0]) &&
    Number.isFinite(value[1])
  )
}

/**
 * ¿Contiene el polígono el punto? Ray-casting sobre el ANILLO EXTERIOR
 * (`polygon[0]`); los anillos interiores (huecos) se ignoran deliberadamente
 * — el juego aún no los produce (ver `WorldPolygonM`). Puntos exactamente
 * sobre el borde pueden caer a cualquiera de los dos lados (suficiente para
 * detección de parcela/región en UI; la validación real es del servidor).
 */
export function polygonContainsPointM(polygon: WorldPolygonM, xM: number, yM: number): boolean {
  const ring = polygon[0]
  if (ring === undefined || ring.length < 3) {
    return false
  }
  let inside = false
  for (let i = 0, j = ring.length - 1; i < ring.length; j = i, i++) {
    const a = ring[i]
    const b = ring[j]
    if (a === undefined || b === undefined) {
      continue
    }
    const [ax, ay] = a
    const [bx, by] = b
    const crosses = ay > yM !== by > yM && xM < ((bx - ax) * (yM - ay)) / (by - ay) + ax
    if (crosses) {
      inside = !inside
    }
  }
  return inside
}

/**
 * Anillo exterior CERRADO (primer vértice repetido al final, como GeoJSON) de
 * un rectángulo alineado a ejes en metros de mundo. Para construir parcelas
 * (concesiones) y footprints cuadrados de edificio desde la UI.
 */
export function rectRingM(xM: number, yM: number, widthM: number, heightM: number): WorldPathM {
  return [
    [xM, yM],
    [xM + widthM, yM],
    [xM + widthM, yM + heightM],
    [xM, yM + heightM],
    [xM, yM],
  ]
}
