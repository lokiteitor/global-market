-- =============================================================================
-- Imperio Industrial — 0018_electric_grid (up)
-- Red eléctrica regional (GDD 5.8, Fase 3; ADR-025), parte 2 de 2: tipos,
-- tablas y columnas. Migración TRANSACCIONAL (atómica): o entra todo o nada.
--
--   1. Tipos: world.power_line_status, world.power_role.
--   2. Tablas: power_lines (líneas de transmisión con huella LineString y
--      mantenimiento, SIN concesión de suelo — ADR-025 §4), power_plant_types
--      (parámetros de generación por tipo de edificio), power_offers /
--      power_bids (precio de oferta del generador y puja máxima del
--      consumidor), power_spot_ticks / power_dispatches (plano físico del
--      resultado de cada tick del spot).
--   3. Columnas: recipes.power_per_hour (consumo eléctrico de la receta),
--      buildings.powered_until_sim (suministro cubierto hasta) y
--      buildings.last_curtailed_at_sim (marcador de rotación del recorte).
--
-- La energía NO es un producto del ledger: no es almacenable ni transportable
-- (sin baterías, GDD 22); solo mueven valor el dinero (asiento power_spot,
-- enum en 0017) y el combustible de las térmicas (consumption contra
-- world_source, ADR-022). El plano físico del despacho vive en estas tablas.
-- =============================================================================

-- ── 1. Tipos ─────────────────────────────────────────────────────────────────

-- operational: conduce (participa del pool regional).
-- abandoned:   degradada por impago sostenido de mantenimiento; deja de
--              conducir y es terminal (sin embargo ni subasta: la fila se
--              conserva por auditoría — ADR-025 §4).
CREATE TYPE world.power_line_status AS ENUM ('operational', 'abandoned');

-- Rol de un edificio en un tick del spot (una central nunca es consumidora:
-- los tipos de central no tienen recetas).
CREATE TYPE world.power_role AS ENUM ('generator', 'consumer');

-- ── 2. Tablas ────────────────────────────────────────────────────────────────

-- Líneas de transmisión: infraestructura lineal PROPIA sin concesión de suelo
-- (una línea cruza muchas parcelas, como las carreteras; el papel
-- anti-acaparamiento del canon lo cumple su mantenimiento por longitud —
-- ADR-025 §4). El trazado debe caer íntegro dentro de los bounds de su región
-- (sin interconexiones interregionales: expansión futura, GDD 22); la
-- validación es server-side en el alta, no un CHECK espacial.
CREATE TABLE world.power_lines (
    id                          uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_account_id            uuid NOT NULL REFERENCES auth.accounts(id),
    region_id                   uuid NOT NULL REFERENCES world.regions(id),
    path                        geometry(LineString, 0) NOT NULL,  -- SRID 0, metros (ADR-019)
    length_m                    INT NOT NULL CHECK (length_m > 0),
    status                      world.power_line_status NOT NULL DEFAULT 'operational',
    condition_pct               INT NOT NULL DEFAULT 100 CHECK (condition_pct BETWEEN 0 AND 100),
    maintenance_paid_until_sim  sim_time NOT NULL DEFAULT 0,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim              sim_time NOT NULL DEFAULT 0
);

CREATE INDEX ix_power_lines_owner ON world.power_lines (owner_account_id);
-- El pool regional: el tick del spot y la conectividad (ST_DWithin) solo miran
-- líneas operativas de la región.
CREATE INDEX ix_power_lines_region
    ON world.power_lines (region_id) WHERE status = 'operational';
CREATE INDEX ix_power_lines_path ON world.power_lines USING GIST (path);
-- Barrido de mantenimiento (world/enforcement), patrón de 0011.
CREATE INDEX ix_power_lines_maintenance_due
    ON world.power_lines (maintenance_paid_until_sim) WHERE status = 'operational';

-- Parámetros de generación por tipo de edificio. Las centrales son
-- building_types normales (concesión, huella, build/maintenance_cost, cascada
-- de insolvencia estándar) y NO tienen recetas: su generación la gobierna el
-- despacho del mercado spot.
CREATE TABLE world.power_plant_types (
    building_type_id  uuid PRIMARY KEY REFERENCES world.building_types(id),
    -- Unidades de energía por hora-sim a nivel 1; el nivel multiplica por
    -- level_curve.capacity_mult (default = nivel).
    capacity          stock_qty NOT NULL CHECK (capacity > 0),
    -- Térmicas: combustible físico consumido por unidad de energía despachada
    -- (consumption contra world_source, ADR-022). Hidro: NULL/0.
    fuel_product_id   uuid REFERENCES world.products(id),
    fuel_per_unit     stock_qty NOT NULL DEFAULT 0 CHECK (fuel_per_unit >= 0),
    CHECK ((fuel_product_id IS NULL) = (fuel_per_unit = 0))
);

-- Oferta del generador: precio por unidad de energía fijado por el dueño de
-- la central. Sin fila, la central NO participa (ofertar es una decisión del
-- jugador, como publicar en el tablón).
CREATE TABLE world.power_offers (
    building_id     uuid PRIMARY KEY REFERENCES world.buildings(id),
    unit_price      money_amount NOT NULL CHECK (unit_price > 0),
    updated_at_sim  sim_time NOT NULL DEFAULT 0
);

-- Puja máxima del consumidor: materializa la "prioridad inversa de precio"
-- del recorte (GDD 5.8) — menor puja = primero en recortarse — y el techo
-- personal (el precio de cierre nunca supera la puja de un servido). Sin fila
-- rige II_POWER_DEFAULT_BID_PRICE.
CREATE TABLE world.power_bids (
    building_id     uuid PRIMARY KEY REFERENCES world.buildings(id),
    unit_price      money_amount NOT NULL CHECK (unit_price > 0),
    updated_at_sim  sim_time NOT NULL DEFAULT 0
);

-- Resultado agregado de cada tick del spot por región (plano físico; el valor
-- va en el asiento power_spot). Idempotencia del tick: PK (region_id,
-- tick_sim); los buckets perdidos NO se recuperan (la energía no es
-- almacenable: un tick perdido es energía no comerciada, sin efecto contable).
CREATE TABLE world.power_spot_ticks (
    region_id           uuid NOT NULL REFERENCES world.regions(id),
    tick_sim            sim_time NOT NULL,  -- bucket: floor(simNow/intervalo)×intervalo
    interval_sim        sim_time NOT NULL CHECK (interval_sim > 0),
    closing_price       money_amount NOT NULL CHECK (closing_price >= 0),  -- 0 si no hubo despacho
    demand_units        stock_qty NOT NULL CHECK (demand_units >= 0),
    supplied_units      stock_qty NOT NULL CHECK (supplied_units >= 0),
    curtailed_units     stock_qty NOT NULL CHECK (curtailed_units >= 0),
    curtailed_buildings INT NOT NULL DEFAULT 0 CHECK (curtailed_buildings >= 0),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (region_id, tick_sim)
);

-- Despacho/consumo por participante y tick ("mi consumo" del contrato y
-- auditoría física del asiento power_spot; unit_price = precio de cierre).
CREATE TABLE world.power_dispatches (
    region_id         uuid NOT NULL REFERENCES world.regions(id),
    tick_sim          sim_time NOT NULL,
    building_id       uuid NOT NULL REFERENCES world.buildings(id),
    owner_account_id  uuid NOT NULL REFERENCES auth.accounts(id),
    role              world.power_role NOT NULL,
    units             stock_qty NOT NULL CHECK (units > 0),
    unit_price        money_amount NOT NULL CHECK (unit_price > 0),
    amount            money_amount NOT NULL CHECK (amount > 0),  -- units × unit_price
    PRIMARY KEY (region_id, tick_sim, building_id)
);

CREATE INDEX ix_power_dispatches_building
    ON world.power_dispatches (building_id, tick_sim DESC);

-- ── 3. Columnas ──────────────────────────────────────────────────────────────

-- Consumo eléctrico de la receta: unidades de energía por HORA-SIM mientras su
-- lote está activo (análogo eléctrico de fuel_per_batch expresado como
-- potencia; el "consumo por ciclo" de GDD 6.2 es power_per_hour × duración del
-- lote). 0 = receta no eléctrica. Puede convivir con fuel_per_batch
-- (conjunción: ambos necesarios — ADR-025 §1).
ALTER TABLE world.recipes
    ADD COLUMN power_per_hour stock_qty NOT NULL DEFAULT 0 CHECK (power_per_hour >= 0);

-- Suministro eléctrico cubierto hasta (lo escribe el tick del spot: al servir,
-- tick + intervalo × 1.5 — el medio intervalo de gracia absorbe el desfase
-- wall-clock entre buckets —; al NO servir, el propio tick, cerrando la
-- cobertura residual). El motor de producción exige
-- powered_until_sim > simNow al cerrar un lote de receta eléctrica.
ALTER TABLE world.buildings
    ADD COLUMN powered_until_sim sim_time NOT NULL DEFAULT 0;

-- Tasa FACTURADA por el tick que otorgó la cobertura (power_per_hour del lote
-- activo en el momento del tick): el cierre de un lote eléctrico exige además
-- power_per_hour <= powered_rate — sin ella, un cambio de lote a mitad de
-- intervalo produciría con energía comprada para una carga arbitrariamente
-- menor (ADR-025 §6).
ALTER TABLE world.buildings
    ADD COLUMN powered_rate stock_qty NOT NULL DEFAULT 0 CHECK (powered_rate >= 0);

-- Marcador de rotación del recorte (GDD 5.8: "el recorte rota entre ciclos
-- para no castigar siempre a los mismos"): entre pujas iguales se recorta
-- primero al recortado MENOS recientemente.
ALTER TABLE world.buildings
    ADD COLUMN last_curtailed_at_sim sim_time NOT NULL DEFAULT 0;
