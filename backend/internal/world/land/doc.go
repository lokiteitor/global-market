// Package land implementa el lado de ESCRITURA del suelo del bounded context
// world (contrato world/concessions* y world/concession-transfers del OpenAPI
// v1.2.0): concesiones renovables del sistema y su mercado secundario de
// traspasos (GDD 11.1).
//
// Todo terreno es concesión renovable del sistema — no hay propiedad perpetua.
// El canon periódico es un sink estructural (destrucción de valor, GDD 5.5):
// el primer canon se cobra al conceder, y cada renovación paga el canon vigente
// al sink. Un traspaso mueve el precio comprador→vendedor (cash→cash) y una tasa
// del sistema comprador→sink. La autorización es por propiedad: los listados y
// el detalle filtran por titular (403 sobre concesiones ajenas).
//
// Fronteras (SAD §7): world no importa contracts/market/auth ni viceversa; solo
// platform y sim son plataforma compartida. Toda operación que mueve valor corre
// en una única transacción SERIALIZABLE (platform/db.RunSerializable) que asienta
// a la vez el estado del mundo, las partidas del ledger y el evento del outbox
// (misma tx). El substrato de valor (ledger) se consume EXACTAMENTE como lo hizo
// internal/contracts: queries sqlc propias del contexto (internal/world/sqlcgen)
// contra las tablas ledger.* — la frontera de módulo es de código Go, no de
// esquema. Las invariantes de dinero (no-negatividad, doble entrada, inmutabilidad)
// las garantizan los triggers de 0004_ledger.
//
// Serialización del contrato: dinero es int64 de punto fijo (string en el JSON,
// jamás float); el sim-time es int64; la parcela es un objeto GeoPolygon con
// coordenadas planas de mundo en metros [x_m, y_m] (SRID 0, ADR-019).
package land
