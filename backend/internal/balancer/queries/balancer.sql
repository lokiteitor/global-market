-- =============================================================================
-- Imperio Industrial — queries sqlc del Economy Balancer Service (Incremento 6b,
-- GDD 5.5/5.6/18.1, ADR-020). El Balancer es el agente decisor de las ciudades:
-- recalcula sus curvas de demanda (world.city_demand), publica sus solicitudes
-- de compra por la API estándar del Contract Service (sin canal privilegiado) y
-- CONSUME lo entregado (city stock_free → world_source, ADR-022), cerrando el
-- bucle de faucets/sinks.
--
-- Frontera de servicio de CÓDIGO Go, no de esquema: estas queries leen/escriben
-- world.cities/city_demand, leen world.products/network_nodes/building_inventories
-- y asientan en ledger.* (cash/emission/world_source/stock_free) — el paquete Go
-- nunca importa internal/contracts (salvo el PORT en el composition root).
--
-- Dinero y stock son int64 de punto fijo. Los NUMERIC del dominio de oferta
-- (supply_index, supply_ema, saturation_factor) se proyectan/leen como float8:
-- son magnitudes continuas de la curva, no dinero.
-- =============================================================================

-- ─── Ciudades ────────────────────────────────────────────────────────────────

-- ListCities devuelve todas las ciudades para el barrido del DemandWorker y las
-- métricas macro. supply_index es NUMERIC → float8.
-- name: ListCities :many
SELECT id, region_id, account_id, name, level, population,
       supply_index::float8 AS supply_index, base_salary, updated_at_sim
FROM world.cities
ORDER BY id;

-- LockCity bloquea una ciudad (FOR UPDATE) para su recálculo en una transacción
-- serializable; pgx.ErrNoRows si desapareció.
-- name: LockCity :one
SELECT id, region_id, account_id, name, level, population,
       supply_index::float8 AS supply_index, base_salary, updated_at_sim
FROM world.cities
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- UpdateCityGrowth escribe el estado de crecimiento de una ciudad tras el
-- recálculo/máquina de niveles: nivel, población, supply_index y updated_at_sim.
-- name: UpdateCityGrowth :exec
UPDATE world.cities
   SET level = sqlc.arg(level),
       population = sqlc.arg(population),
       supply_index = sqlc.arg(supply_index)::float8,
       updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE id = sqlc.arg(id);

-- AddCitySupplyIndex incrementa el índice de suministro histórico de una ciudad
-- (lo llama el consumer al consumir una entrega, ponderado por variedad).
-- Devuelve el índice resultante (float8). NO toca updated_at_sim: ese sello es
-- el marcador del último RECÁLCULO (lo escribe solo UpdateCityGrowth), del que
-- el DemandWorker deriva la ventana de decaimiento — el consumo no lo corrompe.
-- name: AddCitySupplyIndex :one
UPDATE world.cities
   SET supply_index = supply_index + sqlc.arg(delta)::float8
 WHERE id = sqlc.arg(id)
RETURNING supply_index::float8 AS supply_index;

-- ─── Curva de demanda ─────────────────────────────────────────────────────────

-- ListCityDemand devuelve las filas de la curva de demanda de una ciudad para el
-- recálculo (todas; el DemandWorker filtra las activas por unlocked_at_level).
-- supply_ema y saturation_factor son NUMERIC → float8.
-- name: ListCityDemand :many
SELECT product_id, d0_per_sim_day,
       supply_ema::float8 AS supply_ema,
       saturation_factor::float8 AS saturation_factor,
       current_price, unlocked_at_level, recent_supply, updated_at_sim
FROM world.city_demand
WHERE city_id = sqlc.arg(city_id)
ORDER BY product_id;

-- UpdateCityDemandCurve escribe el resultado del recálculo de una (ciudad,
-- producto): supply_ema (con suelo > 0), saturation_factor (acotado), el precio
-- efectivo (ya clampado en [price_floor, price_ceiling]) y el sello de recálculo;
-- RESETEA recent_supply a 0 (la ventana de oferta reciente arranca de nuevo).
-- name: UpdateCityDemandCurve :exec
UPDATE world.city_demand
   SET supply_ema = sqlc.arg(supply_ema)::float8,
       saturation_factor = sqlc.arg(saturation_factor)::float8,
       current_price = sqlc.arg(current_price),
       recent_supply = 0,
       updated_at_sim = sqlc.arg(updated_at_sim)
 WHERE city_id = sqlc.arg(city_id) AND product_id = sqlc.arg(product_id);

-- GrowCityDemandD0 escala la demanda base D0 de TODAS las filas de una ciudad al
-- subir de nivel (p. ej. +20% con factor_bp=12000): d0 = d0 * factor_bp / 10000.
-- name: GrowCityDemandD0 :exec
UPDATE world.city_demand
   SET d0_per_sim_day = (d0_per_sim_day * sqlc.arg(factor_bp)) / 10000
 WHERE city_id = sqlc.arg(city_id);

-- AddRecentSupply acumula la oferta entregada a una (ciudad, producto) desde el
-- último recálculo (lo llama el consumer). Devuelve el acumulado RESULTANTE: el
-- llamante deriva el previo (resultante − qty) para el bono de variedad (era 0 =
-- primer suministro de la ventana = producto "nuevo"). Cero filas afectadas si
-- no hay curva para ese producto (el consumer lo trata como sin variedad).
-- name: AddRecentSupply :one
UPDATE world.city_demand
   SET recent_supply = recent_supply + sqlc.arg(qty)
 WHERE city_id = sqlc.arg(city_id) AND product_id = sqlc.arg(product_id)
RETURNING recent_supply;

-- ─── Productos (ancla de precio, GDD 5.1) ─────────────────────────────────────

-- GetProduct devuelve el ancla de precio y la clase de elasticidad de un producto
-- (base_price, price_floor, price_ceiling, class); pgx.ErrNoRows si no existe.
-- name: GetProduct :one
SELECT base_price, price_floor, price_ceiling, class
FROM world.products
WHERE id = sqlc.arg(id);

-- ─── Tablón: deduplicación de la buy viva por (ciudad, producto) ──────────────

-- CountLiveCityBuys cuenta las publicaciones buy VIVAS del tablón (draw_window,
-- open, micro_window) de una ciudad para un producto: mantener UNA sola solicitud
-- de compra viva por (ciudad, producto) evita duplicar demanda en el tablón.
-- name: CountLiveCityBuys :one
SELECT count(*)
FROM ledger.publications
WHERE publisher_account_id = sqlc.arg(publisher_account_id)
  AND product_id = sqlc.arg(product_id)
  AND kind = 'buy'
  AND status IN ('draw_window', 'open', 'micro_window');

-- GetCityDistributionNode devuelve el nodo del centro de distribución de una
-- ciudad (destino de sus buys: la entrega estándar deja ahí el stock_free de la
-- ciudad). pgx.ErrNoRows si la ciudad no tiene centro de distribución sembrado.
-- name: GetCityDistributionNode :one
SELECT id FROM world.network_nodes
WHERE city_id = sqlc.arg(city_id) AND kind = 'distribution_center'
ORDER BY id
LIMIT 1;

-- ─── auth: identidad de comprador ciudad ──────────────────────────────────────

-- IsCityAccount indica si una cuenta es una ciudad (el consumer solo consume las
-- entregas cuyo comprador es una ciudad).
-- name: IsCityAccount :one
SELECT EXISTS (
    SELECT 1 FROM auth.accounts WHERE id = sqlc.arg(id) AND kind = 'city'
);

-- ─── Contratos: entrega urbana a consumir ─────────────────────────────────────

-- GetContractForConsume devuelve los datos del contrato liquidado que el consumer
-- necesita para consumir la entrega urbana: comprador, producto, nodo de destino,
-- cantidad entregada y estado. pgx.ErrNoRows si no existe.
-- name: GetContractForConsume :one
SELECT buyer_account_id, product_id, destination_node_id, quantity_delivered, status
FROM ledger.contracts
WHERE id = sqlc.arg(id);

-- ─── Nodos → edificio (centro de distribución de la ciudad) ───────────────────

-- GetNodeBuilding devuelve el edificio y la ciudad ligados a un nodo del grafo
-- (el destino de una buy de ciudad es su centro de distribución). pgx.ErrNoRows
-- si el nodo no existe.
-- name: GetNodeBuilding :one
SELECT building_id, city_id, region_id
FROM world.network_nodes
WHERE id = sqlc.arg(id);

-- ─── Inventario físico del centro de distribución ─────────────────────────────

-- ConsumeBuildingInventory descuenta stock físico consumido por la ciudad de su
-- centro de distribución (mantiene físico↔contable en sincronía, ADR-004). La
-- fila debe existir y cubrir la cantidad (la entrega la creó al llegar); el CHECK
-- quantity >= 0 respalda la invariante.
-- name: ConsumeBuildingInventory :exec
UPDATE world.building_inventories
   SET quantity = quantity - sqlc.arg(amount), updated_at_sim = sqlc.arg(sim_now)
 WHERE building_id = sqlc.arg(building_id) AND product_id = sqlc.arg(product_id);

-- ─── Ledger: cuentas del faucet y del consumo ─────────────────────────────────

-- GetCashAccount devuelve la caja de una cuenta (la de la ciudad); pgx.ErrNoRows
-- si aún no existe.
-- name: GetCashAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'cash' AND owner_account_id = sqlc.arg(owner_account_id);

-- CreateCashAccount crea la caja de una cuenta on-demand (unicidad ux_accounts_cash).
-- name: CreateCashAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id)
VALUES (sqlc.arg(id), 'cash', sqlc.arg(owner_account_id))
RETURNING id, balance;

-- GetEmissionAccount devuelve la cuenta de emisión del banco central (única
-- cuenta monetaria que puede quedar negativa: la contrapartida del faucet).
-- pgx.ErrNoRows si el seed no la creó.
-- name: GetEmissionAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'emission'
ORDER BY id
LIMIT 1;

-- GetStockFreeAccount devuelve la cuenta stock_free de (dueño, producto, almacén);
-- pgx.ErrNoRows si no existe.
-- name: GetStockFreeAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'stock_free'
  AND owner_account_id = sqlc.arg(owner_account_id)
  AND product_id = sqlc.arg(product_id)
  AND warehouse_building_id = sqlc.arg(warehouse_building_id);

-- GetWorldSourceAccount devuelve la cuenta world_source (contrapartida física del
-- banco central, ADR-022) de un producto; pgx.ErrNoRows si no existe.
-- name: GetWorldSourceAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'world_source' AND product_id = sqlc.arg(product_id)
LIMIT 1;

-- GetWorldSourceOwner devuelve el titular (banco central) de cualquier cuenta
-- world_source existente, para crear las de productos nuevos con el mismo
-- titular; pgx.ErrNoRows si aún no hay ninguna.
-- name: GetWorldSourceOwner :one
SELECT owner_account_id FROM ledger.accounts
WHERE kind = 'world_source' AND owner_account_id IS NOT NULL
LIMIT 1;

-- CreateWorldSourceAccount crea la cuenta world_source de un producto (titular:
-- banco central; NULL admitido como cuenta pura de sistema si aún no hay banco).
-- name: CreateWorldSourceAccount :one
INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id)
VALUES (sqlc.arg(id), 'world_source', sqlc.narg(owner_account_id), sqlc.arg(product_id))
RETURNING id, balance;

-- InsertLedgerTransaction inserta la cabecera de un asiento (los IDs UUIDv7 los
-- genera la aplicación, ADR-018).
-- name: InsertLedgerTransaction :exec
INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(sim_time_at), sqlc.arg(reference_id), sqlc.arg(description));

-- InsertLedgerEntry inserta una partida de doble entrada (los triggers de 0004
-- aplican saldo, no-negatividad y balance por activo diferido).
-- name: InsertLedgerEntry :exec
INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
VALUES (sqlc.arg(id), sqlc.arg(transaction_id), sqlc.arg(account_id), sqlc.arg(amount));

-- ─── Macro: masa monetaria (faucet vs. sinks, GDD 5.5) ────────────────────────

-- MoneySupply devuelve la masa monetaria total (suma de cuentas cash + escrow +
-- guarantee): el gauge macro del bucle faucet/sink que el Balancer observa.
--
-- DECISIÓN VINCULANTE (coherencia macro): la masa monetaria es EXACTAMENTE
-- cash+escrow+guarantee — el dinero en circulación (cash) más el bloqueado
-- (escrow del comprador, guarantee del vendedor). Se EXCLUYE 'custody': por el
-- esquema 0004 una cuenta custody lleva product_id NOT NULL, es decir contiene
-- STOCK (la mercancía de un CCRI-Flete), no dinero — su lado monetario ya está
-- contado como el escrow del flete. Sumar su saldo mezclaría unidades de stock
-- con dinero y ROMPERÍA la invariante emisión−absorción = Δmasa monetaria: el
-- activo "dinero" (product_id IS NULL) balancea a cero por asiento sobre
-- cash+escrow+guarantee+sink+emission, de modo que
-- Δ(cash+escrow+guarantee) = −Δ(sink) − Δ(emission) = emisión − absorción.
-- name: MoneySupply :one
SELECT COALESCE(SUM(balance), 0)::bigint AS total
FROM ledger.accounts
WHERE kind IN ('cash', 'escrow', 'guarantee');

-- =============================================================================
-- MACRO (Incremento 6b): job de analítica, fórmula laboral (GDD 5.7) y ajuste
-- fiscal algorítmico (banco central algorítmico, GDD 5.5). Escribe los agregados
-- de analytics.* (bucketizados por sim-time) y ajusta world.cities.base_salary y
-- world.regions.tax_rate_bp/canon_base dentro de rangos. Todo lo que sigue son
-- LECTURAS de ledger/world/auth y ESCRITURAS de analytics/world (nunca dinero:
-- el macro observa y regula parámetros, no mueve valor del ledger).
-- =============================================================================

-- ─── Analítica macro: indicadores del bucket (analytics.economy_indicators) ───

-- BucketGdp calcula el PIB simulado del bucket: valor de los contratos LIQUIDADOS
-- (fill > 0) cuyo settled_at_sim cae en [bucket_start, bucket_end). Es el valor
-- efectivamente entregado (quantity_delivered × unit_price), no el pactado.
-- name: BucketGdp :one
SELECT COALESCE(SUM(quantity_delivered * unit_price), 0)::bigint AS gdp
FROM ledger.contracts
WHERE status = 'settled'
  AND settled_at_sim >= sqlc.arg(bucket_start)::bigint
  AND settled_at_sim <  sqlc.arg(bucket_end)::bigint;

-- BucketEmission calcula la emisión (faucet) del bucket: dinero NUEVO puesto en
-- circulación, medido como −Σ de las partidas sobre la cuenta de emisión del
-- banco central en asientos con sim_time_at en el bucket (la emisión se abona en
-- negativo al crear dinero, así que −Σ es el neto emitido). Coincide con los
-- kinds faucet (seed_capital, bot_capitalization, fondeo de ciudad) pero se mide
-- sobre el movimiento monetario autoritativo (la cuenta emission), garantizando
-- la coherencia emisión−absorción = Δmasa monetaria por doble entrada.
-- name: BucketEmission :one
SELECT COALESCE(-SUM(e.amount), 0)::bigint AS emission_total
FROM ledger.entries e
JOIN ledger.accounts a     ON a.id = e.account_id
JOIN ledger.transactions t ON t.id = e.transaction_id
WHERE a.kind = 'emission'
  AND t.sim_time_at >= sqlc.arg(bucket_start)
  AND t.sim_time_at <  sqlc.arg(bucket_end);

-- BucketAbsorption calcula la absorción (sinks) del bucket: dinero RETIRADO de
-- circulación, medido como Σ de las partidas sobre las cuentas sink en asientos
-- con sim_time_at en el bucket (el sink se abona en positivo al destruir valor).
-- Coincide con los kinds sink (tax, canon, maintenance, sanciones de
-- liquidación, bot_retirement) pero se mide sobre el movimiento monetario
-- autoritativo (las cuentas sink), garantizando la coherencia macro.
-- name: BucketAbsorption :one
SELECT COALESCE(SUM(e.amount), 0)::bigint AS absorption_total
FROM ledger.entries e
JOIN ledger.accounts a     ON a.id = e.account_id
JOIN ledger.transactions t ON t.id = e.transaction_id
WHERE a.kind = 'sink'
  AND t.sim_time_at >= sqlc.arg(bucket_start)
  AND t.sim_time_at <  sqlc.arg(bucket_end);

-- CountActiveAccounts cuenta las cuentas activas por rol (bots y humanos) para
-- los indicadores de población económica del bucket.
-- name: CountActiveAccounts :one
SELECT
    COALESCE(SUM(CASE WHEN kind = 'bot'   THEN 1 ELSE 0 END), 0)::int AS bot_count,
    COALESCE(SUM(CASE WHEN kind = 'human' THEN 1 ELSE 0 END), 0)::int AS human_count
FROM auth.accounts
WHERE status = 'active';

-- FiniteDepletionByProduct desglosa el agotamiento de los yacimientos FINITOS
-- (no renovables) por producto (código legible): stock restante y extraído
-- (initial − remaining). Alimenta la proyección JSONB de agotamiento por recurso
-- (analytics, GDD 10/20) y el ritmo global de agotamiento, que se deriva en Go
-- como Σ extraído_producto / tiempo transcurrido desde el génesis
-- (delta_remaining / tiempo).
-- name: FiniteDepletionByProduct :many
SELECT p.code AS product_code,
       COALESCE(SUM(d.remaining_amount), 0)::bigint            AS remaining,
       COALESCE(SUM(d.initial_amount - d.remaining_amount), 0)::bigint AS extracted
FROM world.resource_deposits d
JOIN world.products p ON p.id = d.product_id
WHERE d.renewable = false
GROUP BY p.code
ORDER BY p.code;

-- UpsertEconomyIndicators asienta (o reescribe) la fila del bucket de
-- analytics.economy_indicators. Idempotente: cada barrido dentro del bucket
-- RECALCULA el bucket completo y sobrescribe (el bucle de analítica corre muchas
-- veces por bucket; el resultado converge conforme se acumulan transacciones).
-- name: UpsertEconomyIndicators :exec
INSERT INTO analytics.economy_indicators (
    bucket_start_sim, money_supply, simulated_gdp, emission_total, absorption_total,
    active_bot_count, active_human_count, global_depletion_rate, depletion_projection)
VALUES (
    sqlc.arg(bucket_start_sim), sqlc.arg(money_supply), sqlc.arg(simulated_gdp),
    sqlc.arg(emission_total), sqlc.arg(absorption_total),
    sqlc.arg(active_bot_count), sqlc.arg(active_human_count),
    sqlc.arg(global_depletion_rate)::float8, sqlc.arg(depletion_projection))
ON CONFLICT (bucket_start_sim) DO UPDATE SET
    money_supply          = EXCLUDED.money_supply,
    simulated_gdp         = EXCLUDED.simulated_gdp,
    emission_total        = EXCLUDED.emission_total,
    absorption_total      = EXCLUDED.absorption_total,
    active_bot_count      = EXCLUDED.active_bot_count,
    active_human_count    = EXCLUDED.active_human_count,
    global_depletion_rate = EXCLUDED.global_depletion_rate,
    depletion_projection  = EXCLUDED.depletion_projection;

-- ─── Analítica macro: estadísticas regionales (analytics.region_stats) ────────

-- ListRegions devuelve las regiones con su fiscalidad vigente (para el ajuste
-- fiscal) y es la lista maestra sobre la que el job itera region_stats.
-- name: ListRegions :many
SELECT id, tax_rate_bp, canon_base
FROM world.regions
ORDER BY id;

-- RegionActiveBuildings cuenta los edificios OPERATIVOS por región (ocupación
-- industrial: numerador del factor de saturación laboral, GDD 5.7).
-- name: RegionActiveBuildings :many
SELECT region_id, COUNT(*)::int AS active_buildings
FROM world.buildings
WHERE status = 'operational'
GROUP BY region_id;

-- RegionSettledStats agrega los contratos LIQUIDADOS del bucket por región,
-- atribuidos por su NODO DE DESTINO (lado de consumo/entrega; atribución única
-- que evita el doble conteo cuando origen y destino están en regiones distintas).
-- name: RegionSettledStats :many
SELECT n.region_id,
       COUNT(*)::int AS contracts_settled,
       COALESCE(SUM(c.quantity_delivered * c.unit_price), 0)::bigint AS trade_volume
FROM ledger.contracts c
JOIN world.network_nodes n ON n.id = c.destination_node_id
WHERE c.status = 'settled'
  AND c.settled_at_sim >= sqlc.arg(bucket_start)::bigint
  AND c.settled_at_sim <  sqlc.arg(bucket_end)::bigint
GROUP BY n.region_id;

-- UpsertRegionStats asienta (o reescribe) la fila (región, bucket) de
-- analytics.region_stats. industrial_occupation es el factor de saturación
-- laboral normalizado que calcula el job (edificios activos / capacidad de
-- referencia de la región, acotado).
-- name: UpsertRegionStats :exec
INSERT INTO analytics.region_stats (
    region_id, bucket_start_sim, industrial_occupation, active_buildings,
    contracts_settled, trade_volume)
VALUES (
    sqlc.arg(region_id), sqlc.arg(bucket_start_sim), sqlc.arg(industrial_occupation)::float8,
    sqlc.arg(active_buildings), sqlc.arg(contracts_settled), sqlc.arg(trade_volume))
ON CONFLICT (region_id, bucket_start_sim) DO UPDATE SET
    industrial_occupation = EXCLUDED.industrial_occupation,
    active_buildings      = EXCLUDED.active_buildings,
    contracts_settled     = EXCLUDED.contracts_settled,
    trade_volume          = EXCLUDED.trade_volume;

-- ─── Analítica macro: fotos de ciudad (analytics.city_snapshots) ──────────────

-- UpsertCitySnapshot asienta (o reescribe) la foto (ciudad, bucket): nivel,
-- población y supply_index actuales de la ciudad.
-- name: UpsertCitySnapshot :exec
INSERT INTO analytics.city_snapshots (
    city_id, bucket_start_sim, level, population, supply_index)
VALUES (
    sqlc.arg(city_id), sqlc.arg(bucket_start_sim), sqlc.arg(level),
    sqlc.arg(population), sqlc.arg(supply_index)::float8)
ON CONFLICT (city_id, bucket_start_sim) DO UPDATE SET
    level        = EXCLUDED.level,
    population   = EXCLUDED.population,
    supply_index = EXCLUDED.supply_index;

-- ─── Fórmula laboral (GDD 5.7): salario efectivo por ciudad ───────────────────

-- LatestRegionOccupation devuelve la ocupación industrial MÁS RECIENTE por región
-- (última foto de region_stats): la entrada de la fórmula laboral. El job de
-- analítica la escribe antes en el mismo barrido, de modo que la fórmula laboral
-- consume el factor de saturación recién calculado.
-- name: LatestRegionOccupation :many
SELECT DISTINCT ON (region_id)
       region_id, industrial_occupation::float8 AS industrial_occupation
FROM analytics.region_stats
ORDER BY region_id, bucket_start_sim DESC;

-- UpdateCityBaseSalary escribe el salario EFECTIVO recalculado por el Balancer en
-- world.cities.base_salary (DECISIÓN: base_salary almacenado ES el salario
-- efectivo = salario_base(nivel) × factor_saturación(ocupación regional); el
-- módulo de producción del 6a lo lee tal cual para el sink de salario, GDD 5.7).
-- name: UpdateCityBaseSalary :exec
UPDATE world.cities
   SET base_salary = sqlc.arg(base_salary)
 WHERE id = sqlc.arg(id);

-- ─── Ajuste fiscal algorítmico (banco central algorítmico, GDD 5.5) ───────────

-- RecentEconomyIndicators devuelve los últimos indicadores macro (masa monetaria
-- y PIB simulado) para medir la tendencia inflación/deflación del lazo fiscal.
-- name: RecentEconomyIndicators :many
SELECT bucket_start_sim, money_supply, simulated_gdp
FROM analytics.economy_indicators
ORDER BY bucket_start_sim DESC
LIMIT sqlc.arg(row_limit);

-- UpdateRegionFiscal escribe la fiscalidad ajustada de una región (tax_rate_bp y
-- canon_base) dentro de sus rangos. Cambios pequeños y acotados: lazo suave.
-- name: UpdateRegionFiscal :exec
UPDATE world.regions
   SET tax_rate_bp = sqlc.arg(tax_rate_bp),
       canon_base  = sqlc.arg(canon_base),
       updated_at  = now()
 WHERE id = sqlc.arg(id);
