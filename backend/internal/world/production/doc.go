// Package production implementa la cola de producción, el MOTOR event-driven de
// la simulación industrial y la reconciliación física↔contable del bounded
// context world (contrato world/*production-batches del OpenAPI v1.2.0). Cierra
// el lazo construir→producir→vender del Incremento 2 (GDD 6).
//
// # Cola de producción (handlers)
//
// GET /world/buildings/{id}/production-batches lista los lotes de un edificio
// propio; el progreso del lote en curso (progress_pct, eta_sim) es ANALÍTICO
// (GDD 1.1): se DERIVA en el momento de la consulta a partir de (started_at_sim,
// duración efectiva del nivel, simNow) y NO se persiste. POST encola uno o
// varios lotes de una receta soportada por el tipo del edificio (422 si el
// edificio no está operativo o la receta no pertenece al tipo) y, si no hay lote
// activo, promueve la cabeza de la cola a running. DELETE cancela lo aún NO
// producido (409 si el lote ya está completed/cancelled); lo ya producido queda
// asentado.
//
// # Motor (Worker, para el engine)
//
//  1. Construcción diferida: los edificios under_construction pasan a operational
//     tras un tiempo fijo (II_BUILD_SIM_SECONDS) desde su alta; emite
//     building.constructed (simplificación consciente: tiempo fijo, no derivado
//     del coste).
//  2. Producción analítica: barrido periódico (FOR UPDATE SKIP LOCKED) que
//     completa los lotes running vencidos. En UNA transacción SERIALIZABLE con
//     outbox.Emit mueve a la vez el plano FÍSICO (building_inventories,
//     resource_deposits, buildings.fuel_stock) y el CONTABLE (production_output /
//     consumption / wage sobre ledger.*), respetando combustible, salarios,
//     insumos y capacidad de almacén. Sin combustible → paused_no_fuel; sin
//     fondos para el salario → paused_no_workers (cascada de insolvencia sin
//     deuda, GDD 5.9); sin insumos/yacimiento o almacén lleno el lote no avanza
//     (permanece running, se reintenta) sin inventar estados nuevos del enum
//     (ADR-020: sin cambios de esquema en esta fase).
//  3. Reconciliación (ADR-004): compara SUM(stock_free) por (almacén, producto)
//     contra world.building_inventories; publica ii_reconciliation_discrepancies
//     (esperado 0, porque producción mueve ambos planos juntos). Sin endpoint.
//
// # Combustible (decisión del incremento, GDD 5.8)
//
// El "almacén de combustible local" de un edificio ES el stock del propio dueño
// de ese producto en ese edificio: el combustible se consume del inventario
// físico (world.building_inventories) y del stock_free contable (owner,
// producto, almacén=edificio) a la vez, y buildings.fuel_stock se mantiene como
// COLUMNA ESPEJO del inventario físico del producto combustible (visibilidad).
// No hay endpoint de reabastecimiento en el contrato: el combustible llega como
// cualquier insumo (producción propia o compra CCRI entregada al edificio).
//
// # Fronteras (SAD §7)
//
// world no importa contracts/market/auth ni viceversa; solo platform y sim son
// plataforma compartida. El substrato de valor (ledger) se consume EXACTAMENTE
// como en internal/contracts y los demás subpaquetes world: queries sqlc propias
// del contexto (internal/world/sqlcgen; el soporte de ledger de land.sql se
// comparte) contra las tablas ledger.*. Dinero/stock son int64 de punto fijo
// (string en el JSON, jamás float); el sim-time es int64.
package production
