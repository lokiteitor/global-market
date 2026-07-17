# ADR-019 — Vista top-down cenital (90°) y geometría planar del mundo

| Campo | Valor |
|---|---|
| **ID** | ADR-019 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Deroga** | "Mundo isométrico" (GDD §1) y la proyección isométrica 2:1 del FAD (§16.2 `IsoProjection`, depth sorting por `y` isométrico) |

## Contexto

El GDD describía un mundo isométrico y el FAD diseñó el render alrededor de una proyección 2:1 (`IsoProjection`, depth sorting, footprints anclados a rejilla isométrica). El mandato de proyecto fija una **vista top-down completamente cenital (90°)**, priorizando claridad, legibilidad y visualización de grandes cantidades de información (redes industriales, logísticas y económicas) sobre el realismo visual.

## Decisión

1. El mundo se representa en **top-down cenital estricto**: tilemaps ortogonales de Phaser, cámara con pan y zoom fluidos, sin proyección isométrica.
2. `IsoProjection` se sustituye por una **`GridProjection` ortogonal** (celda ↔ píxel: multiplicación/división por tamaño de tile). Sigue siendo el único punto de conversión de coordenadas del cliente (principio del FAD intacto).
3. El **orden de dibujo se resuelve por capas**, no por depth sorting por entidad: terreno → agua → red logística (carreteras/vías/rutas marítimas) → recursos → edificios → vehículos → efectos → overlays → etiquetas. En cenital estricto no hay oclusión entre entidades que exija ordenación por `y`.
4. Se conservan íntegros del FAD: chunking/streaming del mapa, culling por viewport y por capa, LOD por zoom con clustering, pooling, overlays en escena paralela, minimapa por RenderTexture, y el presupuesto "coste de render ∝ entidades visibles".
5. **Geometría del mundo en la base y en la API: planar.** Las columnas PostGIS pasan de `geometry(…, 4326)` (lon/lat WGS84) a **SRID 0 (cartesiano)** con unidad = **metro de mundo**. El mundo de juego es una grilla plana; las funciones PostGIS (GIST, `ST_DWithin`, `ST_Intersects`, validación de emplazamiento, radios de influencia) operan de forma más simple y correcta sobre un plano.
6. En la API, las formas siguen siendo **GeoJSON-like** (`Point`/`LineString`/`Polygon` con `coordinates`), pero las coordenadas son `[x_m, y_m]` planas del mundo; el contrato lo documenta explícitamente (desviación consciente de RFC 7946, que presupone WGS84).

## Consecuencias

- (+) Render y matemática de picking más simples (sin transformación iso, sin depth sorting por entidad); mayor densidad de información legible; tilesets más baratos de producir.
- (+) PostGIS planar elimina distorsiones de tratar un mundo de juego como coordenadas geográficas.
- (−) Se pierde el atractivo visual isométrico; asumido: el objetivo del juego es gestión, no vistosidad.
- (−) FAD §16.2/§16.4 y GDD §1 reescritos (FAD v1.1, GDD v1.3); el contrato OpenAPI actualiza las descripciones de geometrías.
