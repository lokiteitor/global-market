// Package balancer implementa el Economy Balancer Service (GDD 18.1), acotado
// en el Incremento 6b a su NÚCLEO DE CIUDADES: las ciudades son el ÚNICO
// consumidor final de la economía (GDD 5.6) y el faucet principal frente a los
// sinks del 6a (mantenimiento/canon/sanciones/liquidaciones). El Balancer es el
// agente decisor de las ciudades: decide cuándo, cuánto y a qué precio compra
// cada ciudad, y lo publica por la API ESTÁNDAR del Contract Service —una ciudad
// es una cuenta de mercado más, sin canal privilegiado (GDD 18.1)—.
//
// Cierra el bucle económico en cuatro piezas:
//
//   - demand.go (DemandWorker): recalcula periódicamente las curvas de demanda
//     de world.city_demand por (ciudad, producto activo) — supply_ema como media
//     móvil exponencial con SUELO > 0, saturation_factor y current_price SIEMPRE
//     acotados por los clamps del producto (price_floor/ceiling), sin los cuales
//     una ciudad sin suministro produciría precios que tienden a infinito (GDD
//     5.6). Elasticidad en dos clases: basic (inelástica) y luxury (elástica).
//   - levels.go: la máquina de niveles de ciudad — supply_index histórico cruza
//     umbral → sube de nivel (población +10%, D0 +20%, desbloquea categorías);
//     sin suministro en la ventana el índice decae y puede bajar de nivel. Emite
//     city.level_up / city.level_down por el outbox.
//   - buys.go: mantiene UNA solicitud de compra viva por (ciudad, producto) en el
//     tablón (evita duplicar), PRE-FONDEA la caja de la ciudad por EMISIÓN del
//     banco central si no cubre el escrow (faucet principal, GDD 5.5: una ciudad
//     nunca incumple el pago) y publica la buy por el PORT PublicationCreator.
//   - consume.go (Consumer del outbox sobre contract.settled con comprador
//     ciudad): CONSUME la entrega urbana (city stock_free → world_source,
//     transacción consumption, ADR-022) manteniendo físico↔contable en sincronía,
//     y actualiza supply_ema (recent_supply) y supply_index (ponderado por
//     variedad). La ciudad es así sumidero final real y no acumula inventario.
//
// La parte MACRO (Incremento 6b) es un worker INDEPENDIENTE, el AnalyticsWorker
// (analytics.go), que corre en su propio bucle (II_BALANCER_ANALYTICS_INTERVAL) y
// en cada barrido, en tres pasos ordenados y cada uno en su transacción
// SERIALIZABLE:
//
//   - analytics.go: escribe, bucketizado por sim-time (1 día de juego por
//     defecto), los agregados de analytics.* — region_stats (ocupación
//     industrial, edificios operativos, contratos liquidados y volumen del
//     bucket), city_snapshots (nivel/población/supply_index) y economy_indicators
//     (masa monetaria, PIB simulado, emisión vs. absorción, poblaciones activas y
//     ritmo de agotamiento con proyección JSONB). Es MONITOREO, no mueve valor.
//   - labor.go: recalcula world.cities.base_salary con la fórmula GDD 5.7 —
//     salario_efectivo = salario_base(nivel) · factor_saturación(ocupación
//     regional). DECISIÓN VINCULANTE: cities.base_salary ALMACENA el salario
//     efectivo (el sink de salario del módulo de producción lo lee tal cual); el
//     Balancer es su única autoridad.
//   - fiscal.go: banco central algorítmico (GDD 5.5). Un LAZO SUAVE y ACOTADO
//     mueve tax_rate_bp (dentro de [II_TAX_MIN_BP, II_TAX_MAX_BP]) y canon_base un
//     paso pequeño según la tendencia inflación/deflación (crecimiento de masa
//     monetaria − crecimiento del PIB) de los economy_indicators recientes.
//     NUNCA saca los parámetros de su rango.
//
// COHERENCIA MACRO (invariante verificada): money_supply es EXACTAMENTE
// cash+escrow+guarantee (custody es stock por 0004, no dinero, y se EXCLUYE);
// emission_total y absorption_total se miden sobre las partidas de las cuentas
// emission/sink del bucket. Por la doble entrada del ledger (el activo dinero
// balancea a cero por asiento sobre cash+escrow+guarantee+sink+emission),
// emission_total − absorption_total = Δmasa monetaria del bucket, siempre.
//
// Toda mutación de valor corre en UNA transacción SERIALIZABLE
// (platform/db.RunSerializable, o la transacción del lote del outbox) que
// asienta a la vez el estado, las partidas del ledger y el evento del outbox.
// Las invariantes de dinero/stock viven en la base (0004_ledger/ADR-022): este
// servicio orquesta, la base garantiza. Dinero y stock son int64 de punto fijo;
// las magnitudes continuas de la curva (supply_ema, saturation_factor,
// supply_index) son float64. El acceso a datos es código generado por sqlc
// (ADR-020) en el subpaquete sqlcgen, a partir de queries/balancer.sql.
//
// FRONTERA: el paquete NO importa internal/contracts. Publica las buys de ciudad
// por el PORT PublicationCreator (port.go), que el composition root (cmd/engine)
// implementa con internal/contracts como CLIENTE del Contract Service — la buy
// pasa por la MISMA validación, garantías y sorteo que cualquier otra (GDD 18.1).
package balancer

// La versión de sqlc queda fijada aquí para que `make backend-generate` sea
// reproducible sin añadir dependencias a go.mod (se ejecuta con go run
// paquete@versión). El código generado en sqlcgen/ SE VERSIONA.
//
//go:generate go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate -f ../../sqlc.yaml
