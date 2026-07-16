-- =============================================================================
-- Imperio Industrial — 02_world.sql
-- Esquema world: estado físico del mundo (PostGIS).
-- Propiedad: motor Go (sqlc), un shard lógico por región (GDD 15.1).
-- El shard es la fuente de verdad de la FÍSICA (posiciones, progresos,
-- ocupación); el valor económico vive en el esquema ledger (Arquitectura 11.1).
-- Durabilidad del estado físico: snapshots periódicos (RPO de minutos
-- aceptado, GDD 1.1); este esquema persiste el estado base y los snapshots.
-- Identificadores: uuid con DEFAULT uuidv7(), UUIDv7 nativo de PostgreSQL 18.
-- Fuente ejecutable: backend/migrations/0003_world.sql (aplicación manual vía make db-migrate).
-- =============================================================================

-- -----------------------------------------------------------------------------
-- Enums
-- -----------------------------------------------------------------------------

CREATE TYPE world.biome AS ENUM ('plains','forest','desert','mountain','ocean','coast');

CREATE TYPE world.product_class AS ENUM (
    'basic',    -- demanda inelástica de ciudades (alimentos, combustible)
    'luxury'    -- demanda elástica, sensible a saturación (GDD 5.6, decisión #31)
);

CREATE TYPE world.ingredient_role AS ENUM ('input','output');

CREATE TYPE world.building_status AS ENUM (
    'under_construction',
    'operational',
    'damaged',
    'in_maintenance',
    'abandoned',
    'seized'          -- en embargo (GDD 11.2)
);

CREATE TYPE world.batch_status AS ENUM (
    'queued',
    'running',
    'paused_no_fuel',     -- sin combustible, la producción pausa (GDD 5.8)
    'paused_no_workers',  -- salarios impagados (GDD 5.9)
    'completed',
    'cancelled'
);

CREATE TYPE world.concession_status AS ENUM (
    'active',      -- vigente
    'delinquent',  -- morosa (canon impagado)
    'grace',       -- periodo de gracia previo al embargo
    'reverted'     -- revertida al sistema
);

CREATE TYPE world.node_kind AS ENUM (
    'mine','factory','warehouse','port','station',
    'distribution_center','junction','city_gate'
);

CREATE TYPE world.link_mode AS ENUM ('road','rail','sea');  -- aéreo: expansión futura (#35)

CREATE TYPE world.vehicle_status AS ENUM (
    'idle',
    'loading',
    'in_transit',
    'unloading',
    'broken',          -- avería = espera + reparación, sin rescate en v1 (#36)
    'in_maintenance',
    'sealed'           -- SELLADO durante handoff multi-proceso (GDD 15.2; 403 a comandos)
);

CREATE TYPE world.route_kind AS ENUM ('fixed_line','on_demand');

CREATE TYPE world.shipment_status AS ENUM (
    'in_warehouse',
    'in_transit',
    'at_terminal',
    'delivered',
    'released_in_situ'  -- stock de contrato fallido liberado donde esté (GDD 5.3 paso 6c)
);

-- -----------------------------------------------------------------------------
-- Mundo estático / catálogos
-- -----------------------------------------------------------------------------

-- 1. regions — macro-región: jurisdicción de juego Y unidad de sharding (ADR-007)
CREATE TABLE world.regions (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    name             TEXT NOT NULL UNIQUE,
    grid_x           INT NOT NULL,
    grid_y           INT NOT NULL,
    bounds           geometry(Polygon, 4326) NOT NULL,
    biome            world.biome NOT NULL,
    shard_key        TEXT NOT NULL,               -- asignación región→shard lógico (config versionada)
    tax_rate_bp      INT NOT NULL DEFAULT 0 CHECK (tax_rate_bp BETWEEN 0 AND 10000),
    customs_rate_bp  INT NOT NULL DEFAULT 0 CHECK (customs_rate_bp BETWEEN 0 AND 10000),
    canon_base       money_amount NOT NULL,       -- base del canon de concesión (sink, GDD 11.1)
    opened_at_sim    sim_time NOT NULL DEFAULT 0, -- expansiones territoriales (GDD 10)
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (grid_x, grid_y)
);

CREATE INDEX ix_regions_bounds ON world.regions USING GIST (bounds);

-- 2. products — catálogo de bienes (materias primas, intermedios, finales, combustible)
CREATE TABLE world.products (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    code             TEXT NOT NULL UNIQUE,        -- 'iron_ore', 'steel_ingot', 'coal', ...
    name             TEXT NOT NULL,
    class            world.product_class NOT NULL,
    unit_volume      INT NOT NULL CHECK (unit_volume > 0),   -- volumen logístico por unidad
    base_price       money_amount NOT NULL CHECK (base_price > 0), -- ancla administrada (GDD 5.1/5.6)
    price_floor      money_amount NOT NULL,       -- clamps obligatorios de la curva de demanda
    price_ceiling    money_amount NOT NULL,
    is_fuel          BOOLEAN NOT NULL DEFAULT false,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (price_floor > 0 AND price_ceiling >= price_floor)
);

-- 3. building_types — catálogo de instalaciones construibles (GDD 11)
CREATE TABLE world.building_types (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    code               TEXT NOT NULL UNIQUE,      -- 'iron_mine', 'blast_furnace', ...
    name               TEXT NOT NULL,
    footprint_cells    INT NOT NULL CHECK (footprint_cells > 0),
    max_level          INT NOT NULL DEFAULT 4 CHECK (max_level BETWEEN 1 AND 8),
    base_storage       stock_qty NOT NULL CHECK (base_storage >= 0),
    placement_rules    JSONB NOT NULL DEFAULT '{}',  -- cercanía a recursos/agua/acceso vial (GDD 11)
    level_curve        JSONB NOT NULL DEFAULT '{}',  -- líneas/velocidad/eficiencia por nivel (GDD 6.3)
    build_cost         money_amount NOT NULL,
    maintenance_cost   money_amount NOT NULL,        -- por día de sim-time (sink)
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 4. recipes — recetas de producción (estructura fija, GDD 6.1/6.2)
CREATE TABLE world.recipes (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    building_type_id   uuid NOT NULL REFERENCES world.building_types(id),
    code               TEXT NOT NULL UNIQUE,
    name               TEXT NOT NULL,
    batch_sim_seconds  sim_time NOT NULL CHECK (batch_sim_seconds > 0),
    fuel_product_id    uuid REFERENCES world.products(id),
    fuel_per_batch     stock_qty NOT NULL DEFAULT 0 CHECK (fuel_per_batch >= 0),
    workers_required   INT NOT NULL DEFAULT 0 CHECK (workers_required >= 0),
    min_city_level     INT NOT NULL DEFAULT 1,       -- cualificación ligada al nivel urbano (GDD 5.7)
    changeover_seconds sim_time NOT NULL DEFAULT 0,  -- plantas multi-receta (GDD 6.2)
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_recipes_building_type ON world.recipes (building_type_id);

-- 5. recipe_ingredients — insumos y productos de cada receta
CREATE TABLE world.recipe_ingredients (
    recipe_id    uuid NOT NULL REFERENCES world.recipes(id) ON DELETE CASCADE,
    product_id   uuid NOT NULL REFERENCES world.products(id),
    role         world.ingredient_role NOT NULL,
    quantity     stock_qty NOT NULL CHECK (quantity > 0),
    PRIMARY KEY (recipe_id, product_id, role)
);

-- 6. resource_deposits — yacimientos: minerales finitos estrictos, renovables
--    con tasa de regeneración (GDD 10)
CREATE TABLE world.resource_deposits (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    region_id           uuid NOT NULL REFERENCES world.regions(id),
    product_id          uuid NOT NULL REFERENCES world.products(id),
    location            geometry(Point, 4326) NOT NULL,
    initial_amount      stock_qty NOT NULL CHECK (initial_amount > 0),
    remaining_amount    stock_qty NOT NULL CHECK (remaining_amount >= 0),
    renewable           BOOLEAN NOT NULL DEFAULT false,
    regen_per_sim_day   stock_qty NOT NULL DEFAULT 0 CHECK (regen_per_sim_day >= 0),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim      sim_time NOT NULL DEFAULT 0,
    CHECK (renewable OR regen_per_sim_day = 0),
    CHECK (remaining_amount <= initial_amount OR renewable)
);

CREATE INDEX ix_deposits_region ON world.resource_deposits (region_id);
CREATE INDEX ix_deposits_location ON world.resource_deposits USING GIST (location);
CREATE INDEX ix_deposits_product ON world.resource_deposits (product_id)
    WHERE remaining_amount > 0;

-- -----------------------------------------------------------------------------
-- Ciudades y demanda
-- -----------------------------------------------------------------------------

-- 7. cities — consumidor final único de la economía (GDD 5.6, decisión #34)
CREATE TABLE world.cities (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    region_id            uuid NOT NULL REFERENCES world.regions(id),
    account_id           uuid NOT NULL UNIQUE REFERENCES auth.accounts(id), -- la ciudad es una cuenta más
    name                 TEXT NOT NULL UNIQUE,
    location             geometry(Point, 4326) NOT NULL,
    level                INT NOT NULL DEFAULT 1 CHECK (level >= 1),
    population           BIGINT NOT NULL CHECK (population >= 0),
    supply_index         NUMERIC NOT NULL DEFAULT 0 CHECK (supply_index >= 0), -- índice de suministro histórico
    influence_radius_m   INT NOT NULL CHECK (influence_radius_m > 0),
    base_salary          money_amount NOT NULL,   -- salario_base(nivel_ciudad), recalculado por el Balancer (GDD 5.7)
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim       sim_time NOT NULL DEFAULT 0
);

CREATE INDEX ix_cities_region ON world.cities (region_id);
CREATE INDEX ix_cities_location ON world.cities USING GIST (location);

-- 8. city_demand — curva de demanda dinámica por (ciudad, producto) (GDD 5.6)
--    Escrita por el Economy Balancer; leída por él mismo para publicar
--    solicitudes de compra vía la API estándar del Contract Service.
CREATE TABLE world.city_demand (
    city_id             uuid NOT NULL REFERENCES world.cities(id),
    product_id          uuid NOT NULL REFERENCES world.products(id),
    d0_per_sim_day      stock_qty NOT NULL CHECK (d0_per_sim_day >= 0), -- demanda base D0(producto, nivel)
    supply_ema          NUMERIC NOT NULL CHECK (supply_ema > 0),        -- oferta reciente (EMA con suelo > 0)
    saturation_factor   NUMERIC NOT NULL DEFAULT 1
                        CHECK (saturation_factor BETWEEN 0 AND 10),     -- acotado por clamps (GDD 5.6)
    current_price       money_amount NOT NULL,   -- precio efectivo, acotado por clamps del producto
    unlocked_at_level   INT NOT NULL DEFAULT 1,  -- categorías de consumo por nivel urbano
    updated_at_sim      sim_time NOT NULL DEFAULT 0,
    PRIMARY KEY (city_id, product_id)
);

-- -----------------------------------------------------------------------------
-- Suelo: concesiones del sistema (GDD 11.1)
-- -----------------------------------------------------------------------------

-- 9. land_concessions — todo el suelo es arrendamiento renovable del sistema
CREATE TABLE world.land_concessions (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    region_id         uuid NOT NULL REFERENCES world.regions(id),
    holder_account_id uuid NOT NULL REFERENCES auth.accounts(id),
    parcel            geometry(Polygon, 4326) NOT NULL,
    canon_amount      money_amount NOT NULL CHECK (canon_amount > 0), -- por periodo; sink estructural
    period_sim_days   INT NOT NULL DEFAULT 90,       -- plazo de referencia: 90 días de juego
    expires_at_sim    sim_time NOT NULL,
    status            world.concession_status NOT NULL DEFAULT 'active',
    granted_at_sim    sim_time NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_concessions_holder ON world.land_concessions (holder_account_id)
    WHERE status <> 'reverted';
CREATE INDEX ix_concessions_parcel ON world.land_concessions USING GIST (parcel);
CREATE INDEX ix_concessions_expiry ON world.land_concessions (expires_at_sim)
    WHERE status = 'active';

-- 10. concession_transfers — mercado secundario de traspasos (con tasa del sistema)
CREATE TABLE world.concession_transfers (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    concession_id    uuid NOT NULL REFERENCES world.land_concessions(id),
    from_account_id  uuid NOT NULL REFERENCES auth.accounts(id),
    to_account_id    uuid NOT NULL REFERENCES auth.accounts(id),
    price            money_amount NOT NULL CHECK (price >= 0),
    system_fee       money_amount NOT NULL CHECK (system_fee >= 0),
    occurred_at_sim  sim_time NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_concession_transfers_concession ON world.concession_transfers (concession_id);

-- -----------------------------------------------------------------------------
-- Edificios y producción
-- -----------------------------------------------------------------------------

-- 11. buildings — instalaciones de los jugadores (GDD 11)
CREATE TABLE world.buildings (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_account_id  uuid NOT NULL REFERENCES auth.accounts(id),
    region_id         uuid NOT NULL REFERENCES world.regions(id),
    concession_id     uuid NOT NULL REFERENCES world.land_concessions(id),
    building_type_id  uuid NOT NULL REFERENCES world.building_types(id),
    footprint         geometry(Polygon, 4326) NOT NULL,
    level             INT NOT NULL DEFAULT 1 CHECK (level >= 1),
    status            world.building_status NOT NULL DEFAULT 'under_construction',
    active_recipe_id  uuid REFERENCES world.recipes(id),
    condition_pct     INT NOT NULL DEFAULT 100 CHECK (condition_pct BETWEEN 0 AND 100), -- degradación (GDD 11.2)
    fuel_stock        stock_qty NOT NULL DEFAULT 0 CHECK (fuel_stock >= 0),  -- combustible in situ (GDD 5.8)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim    sim_time NOT NULL DEFAULT 0
);

CREATE INDEX ix_buildings_owner ON world.buildings (owner_account_id);
CREATE INDEX ix_buildings_region_status ON world.buildings (region_id, status);
CREATE INDEX ix_buildings_footprint ON world.buildings USING GIST (footprint);

-- 12. building_inventories — inventario FÍSICO por edificio y producto.
--     Vista física del stock; el stock comprometible se contabiliza como
--     cuentas del ledger, con reconciliación periódica física↔contable
--     (GDD 15.3, ADR-004).
CREATE TABLE world.building_inventories (
    building_id     uuid NOT NULL REFERENCES world.buildings(id),
    product_id      uuid NOT NULL REFERENCES world.products(id),
    quantity        stock_qty NOT NULL DEFAULT 0 CHECK (quantity >= 0),
    updated_at_sim  sim_time NOT NULL DEFAULT 0,
    PRIMARY KEY (building_id, product_id)
);

CREATE INDEX ix_building_inventories_product ON world.building_inventories (product_id)
    WHERE quantity > 0;

-- 13. production_batches — cola de producción; progreso ANALÍTICO, no por tick:
--     se persiste (estado_inicial, t_inicio) y el avance se deriva bajo demanda
--     (GDD 1.1). Solo el hito de fin de lote genera evento y escritura.
CREATE TABLE world.production_batches (
    id               uuid PRIMARY KEY DEFAULT uuidv7(),
    building_id      uuid NOT NULL REFERENCES world.buildings(id),
    recipe_id        uuid NOT NULL REFERENCES world.recipes(id),
    batches_queued   INT NOT NULL CHECK (batches_queued > 0),
    batches_done     INT NOT NULL DEFAULT 0 CHECK (batches_done >= 0),
    status           world.batch_status NOT NULL DEFAULT 'queued',
    queue_position   INT NOT NULL DEFAULT 0,
    started_at_sim   sim_time,                    -- t_inicio del lote en curso
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim   sim_time NOT NULL DEFAULT 0,
    CHECK (batches_done <= batches_queued),
    CHECK (status <> 'running' OR started_at_sim IS NOT NULL)
);

CREATE INDEX ix_batches_building ON world.production_batches (building_id, queue_position)
    WHERE status IN ('queued','running','paused_no_fuel','paused_no_workers');

-- -----------------------------------------------------------------------------
-- Red logística: grafo, terminales, vehículos, rutas, cargamentos (GDD 7-8)
-- -----------------------------------------------------------------------------

-- 14. network_nodes — nodos del grafo logístico
CREATE TABLE world.network_nodes (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    kind         world.node_kind NOT NULL,
    region_id    uuid NOT NULL REFERENCES world.regions(id),
    building_id  uuid REFERENCES world.buildings(id),  -- si el nodo es una instalación
    city_id      uuid REFERENCES world.cities(id),     -- si es puerta urbana
    location     geometry(Point, 4326) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_nodes_region ON world.network_nodes (region_id);
CREATE INDEX ix_nodes_location ON world.network_nodes USING GIST (location);
CREATE INDEX ix_nodes_building ON world.network_nodes (building_id) WHERE building_id IS NOT NULL;

-- 15. network_links — enlaces de USO COMÚN (sin reservas exclusivas de vía,
--     decisión #12); capacidad y velocidad base por enlace
CREATE TABLE world.network_links (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    mode               world.link_mode NOT NULL,
    from_node_id       uuid NOT NULL REFERENCES world.network_nodes(id),
    to_node_id         uuid NOT NULL REFERENCES world.network_nodes(id),
    path               geometry(LineString, 4326) NOT NULL,
    length_m           INT NOT NULL CHECK (length_m > 0),
    capacity_per_hour  INT NOT NULL CHECK (capacity_per_hour > 0),
    base_speed_kmh     INT NOT NULL CHECK (base_speed_kmh > 0),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_node_id <> to_node_id)
);

CREATE INDEX ix_links_from ON world.network_links (from_node_id);
CREATE INDEX ix_links_to ON world.network_links (to_node_id);
CREATE INDEX ix_links_path ON world.network_links USING GIST (path);

-- 16. link_segments — un enlace fronterizo se divide en el punto de cruce;
--     cada shard simula la congestión de SU segmento (GDD 7.3 / 15.1).
--     congestion_ema: media móvil exponencial publicada al Logistics Service.
CREATE TABLE world.link_segments (
    id              uuid PRIMARY KEY DEFAULT uuidv7(),
    link_id         uuid NOT NULL REFERENCES world.network_links(id),
    region_id       uuid NOT NULL REFERENCES world.regions(id),
    seq             INT NOT NULL,                 -- orden del segmento dentro del enlace
    portion         geometry(LineString, 4326) NOT NULL,
    length_m        INT NOT NULL CHECK (length_m > 0),
    congestion_ema  NUMERIC NOT NULL DEFAULT 1 CHECK (congestion_ema > 0), -- factor sobre velocidad base
    updated_at_sim  sim_time NOT NULL DEFAULT 0,
    UNIQUE (link_id, seq)
);

CREATE INDEX ix_segments_region ON world.link_segments (region_id);

-- 17. terminals — las terminales TIENEN dueño y venden slots de prioridad
--     (el gameplay de "infraestructura como servicio" vive en los nodos, GDD 7.3)
CREATE TABLE world.terminals (
    id                       uuid PRIMARY KEY DEFAULT uuidv7(),
    node_id                  uuid NOT NULL UNIQUE REFERENCES world.network_nodes(id),
    owner_account_id         uuid NOT NULL REFERENCES auth.accounts(id),
    transshipment_per_hour   INT NOT NULL CHECK (transshipment_per_hour > 0),
    queue_length             INT NOT NULL DEFAULT 0 CHECK (queue_length >= 0),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim           sim_time NOT NULL DEFAULT 0
);

CREATE INDEX ix_terminals_owner ON world.terminals (owner_account_id);

-- 18. terminal_slots — slots de prioridad vendibles (Fase 2, junto al CCRI-Flete)
CREATE TABLE world.terminal_slots (
    id                 uuid PRIMARY KEY DEFAULT uuidv7(),
    terminal_id        uuid NOT NULL REFERENCES world.terminals(id),
    priority_tier      INT NOT NULL CHECK (priority_tier > 0),
    price              money_amount NOT NULL CHECK (price >= 0),
    holder_account_id  uuid REFERENCES auth.accounts(id),   -- NULL = a la venta
    valid_until_sim    sim_time,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_terminal_slots_terminal ON world.terminal_slots (terminal_id);

-- 19. vehicle_types — catálogo de vehículos (GDD 7.2 / 8)
CREATE TABLE world.vehicle_types (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    code                  TEXT NOT NULL UNIQUE,   -- 'truck_s', 'freight_train', 'cargo_ship'
    name                  TEXT NOT NULL,
    mode                  world.link_mode NOT NULL,
    cargo_capacity        stock_qty NOT NULL CHECK (cargo_capacity > 0),  -- en unidades de volumen
    speed_kmh             INT NOT NULL CHECK (speed_kmh > 0),
    fuel_product_id       uuid NOT NULL REFERENCES world.products(id),
    fuel_per_100km        stock_qty NOT NULL CHECK (fuel_per_100km >= 0),
    autonomy_km           INT NOT NULL CHECK (autonomy_km > 0),
    purchase_price        money_amount NOT NULL,
    operating_cost_per_day money_amount NOT NULL, -- sink de mantenimiento
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 20. routes — rutas definidas por el jugador (líneas fijas u órdenes bajo demanda)
CREATE TABLE world.routes (
    id                uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_account_id  uuid NOT NULL REFERENCES auth.accounts(id),
    name              TEXT NOT NULL,
    kind              world.route_kind NOT NULL,
    active            BOOLEAN NOT NULL DEFAULT true,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_routes_owner ON world.routes (owner_account_id) WHERE active;

-- 21. route_legs — secuencia de enlaces de una ruta (potencialmente multimodal)
CREATE TABLE world.route_legs (
    route_id   uuid NOT NULL REFERENCES world.routes(id) ON DELETE CASCADE,
    leg_index  INT NOT NULL,
    link_id    uuid NOT NULL REFERENCES world.network_links(id),
    PRIMARY KEY (route_id, leg_index)
);

-- 22. vehicles — posición ANALÍTICA: (segmento, t_entrada, función de avance);
--     la posición exacta se deriva bajo demanda; solo los hitos escriben (GDD 1.1/7.3)
CREATE TABLE world.vehicles (
    id                    uuid PRIMARY KEY DEFAULT uuidv7(),
    vehicle_type_id       uuid NOT NULL REFERENCES world.vehicle_types(id),
    owner_account_id      uuid NOT NULL REFERENCES auth.accounts(id),
    status                world.vehicle_status NOT NULL DEFAULT 'idle',
    wear_pct              INT NOT NULL DEFAULT 0 CHECK (wear_pct BETWEEN 0 AND 100),
    fuel                  stock_qty NOT NULL DEFAULT 0 CHECK (fuel >= 0),
    route_id              uuid REFERENCES world.routes(id),
    route_leg_index       INT,
    at_node_id            uuid REFERENCES world.network_nodes(id),   -- si está detenido en un nodo
    on_segment_id         uuid REFERENCES world.link_segments(id),   -- si está en tránsito
    segment_entered_sim   sim_time,                                  -- t_entrada al segmento
    advance_fn            JSONB,                     -- parámetros de la función de avance
    repair_until_sim      sim_time,                  -- fin de reparación si status = 'broken'
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim        sim_time NOT NULL DEFAULT 0,
    -- exactamente una ubicación física: en nodo XOR en segmento
    CHECK ((at_node_id IS NULL) <> (on_segment_id IS NULL)),
    CHECK (on_segment_id IS NULL OR (segment_entered_sim IS NOT NULL AND advance_fn IS NOT NULL))
);

CREATE INDEX ix_vehicles_owner ON world.vehicles (owner_account_id);
CREATE INDEX ix_vehicles_segment ON world.vehicles (on_segment_id) WHERE on_segment_id IS NOT NULL;
CREATE INDEX ix_vehicles_node ON world.vehicles (at_node_id) WHERE at_node_id IS NOT NULL;

-- 23. shipments — cargamentos: el stock reservado viaja etiquetado con su
--     contrato; nada se teletransporta, tampoco en los fallos (GDD 5.3, decisión #9)
CREATE TABLE world.shipments (
    id                   uuid PRIMARY KEY DEFAULT uuidv7(),
    owner_account_id     uuid NOT NULL REFERENCES auth.accounts(id),
    product_id           uuid NOT NULL REFERENCES world.products(id),
    quantity             stock_qty NOT NULL CHECK (quantity > 0),
    contract_id          uuid,   -- FK cross-schema a ledger.contracts: se añade en 03_ledger.sql (migración 0004)
    freight_contract_id  uuid,   -- FK cross-schema a ledger.freight_contracts: se añade en 03_ledger.sql (migración 0004)
    vehicle_id           uuid REFERENCES world.vehicles(id),
    at_node_id           uuid REFERENCES world.network_nodes(id),
    status               world.shipment_status NOT NULL DEFAULT 'in_warehouse',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at_sim       sim_time NOT NULL DEFAULT 0,
    -- ubicación física coherente: a bordo de un vehículo XOR en un nodo/almacén
    CHECK ((vehicle_id IS NULL) <> (at_node_id IS NULL))
);

CREATE INDEX ix_shipments_contract ON world.shipments (contract_id) WHERE contract_id IS NOT NULL;
CREATE INDEX ix_shipments_freight ON world.shipments (freight_contract_id) WHERE freight_contract_id IS NOT NULL;
CREATE INDEX ix_shipments_vehicle ON world.shipments (vehicle_id) WHERE vehicle_id IS NOT NULL;
CREATE INDEX ix_shipments_node ON world.shipments (at_node_id) WHERE at_node_id IS NOT NULL;

-- 24. shard_snapshots — metadatos de snapshots periódicos por shard
--     (Job World Persistence; RPO de minutos para estado físico, GDD 1.1)
CREATE TABLE world.shard_snapshots (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    shard_key      TEXT NOT NULL,
    sim_time_at    sim_time NOT NULL,
    storage_ref    TEXT NOT NULL,       -- ubicación del blob del snapshot
    is_global      BOOLEAN NOT NULL DEFAULT false,  -- snapshot global de la ventana de mantenimiento
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX ix_snapshots_shard ON world.shard_snapshots (shard_key, created_at DESC);

-- 25. sim_clock — reloj sim-time persistido del mundo.
CREATE TABLE world.sim_clock (
    id           smallint PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    sim_seconds  sim_time NOT NULL DEFAULT 0,
    frozen       boolean  NOT NULL DEFAULT false,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
-- fila única: reloj sim-time persistido del mundo (ratio 24x); lo avanza el motor Go;
-- el gateway lo lee para meta.sim_time. frozen = ventana de mantenimiento.
INSERT INTO world.sim_clock (id, sim_seconds) VALUES (1, 0) ON CONFLICT DO NOTHING;
