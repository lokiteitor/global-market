// Package power materializa los ACTIVOS FÍSICOS de la red eléctrica regional
// (GDD 5.8 Fase 3, ADR-025) dentro del bounded context world:
//
//   - Líneas de transmisión (world.power_lines): infraestructura lineal propia
//     SIN concesión de suelo, con huella LineString (SRID 0, metros), coste de
//     construcción por longitud (sink) y mantenimiento periódico que cobra el
//     motor world/enforcement (mismo patrón que edificios: se cobra solo lo
//     disponible, los días impagados degradan, abandoned = deja de conducir).
//   - Ofertas de generación (world.power_offers): el dueño de una central fija
//     su precio por unidad de energía; sin oferta la central no participa.
//   - Pujas de consumo (world.power_bids): puja máxima por edificio — la
//     prioridad inversa de precio del recorte y el techo personal del
//     consumidor; sin puja rige el default del tick.
//   - Lecturas del contrato: catálogo de líneas, histórico del spot por región
//     (world.power_spot_ticks) y despacho/consumo propio (power_dispatches).
//
// El TICK del mercado spot NO vive aquí: pertenece al Economy Balancer
// (GDD 18.1; internal/balancer, PowerWorker). Este subpaquete comparte el
// sqlcgen del contexto world (sus handlers los monta el gateway) y sigue las
// reglas del contexto: toda mutación de valor en db.RunSerializable con
// outbox.Emit en la misma tx, dinero/stock int64 (string en JSON) y ledger
// consumido con queries propias (la frontera es de código Go, no de esquema).
// La generación sqlc la dispara la directiva go:generate compartida del
// contexto world.
package power
