// Package logistics implementa el Logistics Service del backend: el bounded
// context de PLANIFICACIÓN logística del contrato OpenAPI (logistics/*). Su
// pilar (ADR-006, GDD 15.1) es no tener estado de tránsito — planifica sobre el
// grafo del mundo y define rutas propietarias, pero el movimiento físico de los
// vehículos y cargamentos lo simula internal/world (el shard). Aquí no viaja
// nada; aquí se decide por dónde.
//
// Superficie del contrato:
//
//   - GET /logistics/network/nodes|links: lectura pública del grafo (nodos,
//     enlaces de uso común y sus segmentos con la congestión suavizada EMA que
//     publican los shards). Cualquier corporación autenticada lo observa.
//   - POST /logistics/route-plans: asistente de ruta óptima. Pathfinding de
//     SOLO cálculo (no persiste) ponderado por la congestión EMA: Dijkstra con
//     min-heap sobre nodos, minimizando tiempo (ETA en sim-segundos) o coste
//     monetario aproximado (combustible + aduanas). Las ETAs son estimaciones
//     informativas, no garantías. A la escala de la Fase 1 (una región, pocos
//     nodos) el Dijkstra plano es correcto y suficiente; la jerarquía HPA*
//     (GDD 7.4) es una optimización por escala que la interface Planner deja
//     lista para insertar sin cambiar la arquitectura.
//   - GET/POST/PATCH/DELETE /logistics/routes: CRUD de rutas propias (líneas
//     fijas u órdenes bajo demanda) como secuencia CONTIGUA de enlaces,
//     multimodal solo con terminal intermodal en el cambio de modo. Es el ÚNICO
//     estado que este servicio escribe. La autorización es por propiedad (403).
//
// Fronteras (SAD §7): internal/logistics NO importa internal/world (ni
// viceversa); ambos se integran solo por el outbox de eventos. La frontera es
// de código Go, no de esquema: las queries sqlc de este paquete leen world.*
// (grafo, terminales, regiones) y escriben world.routes/route_legs, pero el
// paquete Go nunca alcanza el de world. platform y sim son plataforma
// compartida. El acceso a datos es código generado por sqlc (ADR-020) en el
// paquete propio internal/logistics/sqlcgen.
//
// Serialización del contrato: el sim-time (ETA, updated_at_sim) es int64; el
// coste estimado es dinero de punto fijo int64 serializado como string (jamás
// float); la congestión EMA es un número del contrato (float64, no dinero); las
// geometrías (location, path) son objetos GeoJSON con coordenadas planas de
// mundo en metros [x_m, y_m] (SRID 0, ADR-019), nunca lon/lat.
package logistics
