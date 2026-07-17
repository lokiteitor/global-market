// Package buildings implementa el lado de ESCRITURA de las instalaciones del
// bounded context world (contrato world/buildings* del OpenAPI v1.2.0):
// construcción, configuración (receta / mantenimiento), mejora de nivel e
// inventario físico (GDD 6/11).
//
// La construcción inicia un edificio under_construction sobre una concesión
// propia, con validación de emplazamiento COMPLETA server-side (422
// PLACEMENT_INVALID): (a) la concesión es del solicitante y está activa; (b) el
// footprint cae dentro de la parcela (ST_Within); (c) no se solapa con edificios
// existentes (ST_Intersects); (d) las placement_rules del tipo (near_resource,
// requires_node_kind) se cumplen. El coste de construcción se asienta como sink
// (transacción maintenance) y se crea el nodo del grafo logístico en el centroide
// del footprint (mina→mine, resto→factory). La construcción COMPLETA
// (under_construction→operational) la realiza el motor tras II_BUILD_SIM_SECONDS
// (simplificación consciente: tiempo fijo). La mejora sube el nivel con coste no
// lineal según la level_curve del tipo.
//
// Autorización por propiedad: listados, detalle, configuración, mejora e
// inventario filtran por dueño (403 sobre edificios ajenos).
//
// Fronteras (SAD §7): world no importa contracts/market/auth ni viceversa; solo
// platform y sim son plataforma compartida. Toda operación que mueve valor corre
// en una única transacción SERIALIZABLE (platform/db.RunSerializable) que asienta
// a la vez el estado del mundo (world.buildings, world.network_nodes), las
// partidas del ledger (build_cost/upgrade_cost como sink) y el evento del outbox.
// El substrato de valor (ledger) se consume EXACTAMENTE como en internal/contracts
// y world/land: queries sqlc propias del contexto (internal/world/sqlcgen; el
// soporte de ledger se comparte con world/land) contra las tablas ledger.*.
//
// Serialización del contrato: dinero/stock son int64 de punto fijo (string en el
// JSON, jamás float); el sim-time es int64; el footprint es un objeto GeoPolygon
// con coordenadas planas de mundo en metros [x_m, y_m] (SRID 0, ADR-019).
package buildings
