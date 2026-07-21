/**
 * network/mappers/geometry — geometrías del contrato (ADR-019: SRID 0 planar).
 *
 * Alias tipados de los schemas GeoJSON-like del contrato generado. Las
 * coordenadas son SIEMPRE planas de mundo en METROS `[x_m, y_m]` (SRID 0,
 * cartesiano) — desviación consciente de RFC 7946, que presupone WGS84.
 *
 * Toda conversión mundo↔pantalla pasa por la GridProjection ortogonal de
 * `shared/geometry` (WORLD_M_PER_TILE=250, TILE_PX=32); nunca math suelta
 * sobre estos arrays.
 */

import type { components } from '../../types/api'

/** Punto `[x_m, y_m]` en metros de mundo (SRID 0 planar). */
export type GeoPoint = components['schemas']['GeoPoint']

/** Polilínea con vértices `[x_m, y_m]` en metros de mundo (p. ej. `path` de un enlace). */
export type GeoLineString = components['schemas']['GeoLineString']

/** Polígono (anillo exterior cerrado) `[x_m, y_m]` en metros (parcelas, footprints, bounds). */
export type GeoPolygon = components['schemas']['GeoPolygon']
