-- =============================================================================
-- Imperio Industrial — seed_world.sql
-- Mundo inicial determinista e IDEMPOTENTE.
--
-- Se aplica con `make db-seed` (psql --single-transaction). Todos los INSERT
-- llevan ON CONFLICT DO NOTHING sobre UUIDs fijos, de modo que re-ejecutar el
-- fichero no duplica filas. Los triggers del ledger (apply_entry_balance) solo
-- disparan en inserciones reales, así que los saldos tampoco se duplican.
--
-- El dinero SOLO entra por asientos balanceados (ledger.transactions +
-- ledger.entries) contra la cuenta 'emission' del banco central — la única
-- que puede quedar negativa. Nunca se actualizan saldos directamente.
--
-- MAPA DE BLOQUES DE UUIDs — esquema '00000000-0000-7000-8000-XXXXXXXXXXXX'
-- (los 12 hex finales se numeran por bloques):
--   0000 0000 00xx  auth: cuentas (0001-0007) y bot_profiles (0021-0023)
--   0000 0000 01xx  world.products          (0101-0106)
--   0000 0000 02xx  world.building_types    (0201-0207)
--   0000 0000 03xx  world.recipes           (0301-0306)
--   0000 0000 04xx  world.regions           (0401-0404)
--   0000 0000 05xx  world.cities            (0501-0502)
--   0000 0000 06xx  world.resource_deposits (0601-0604)
--   0000 0000 07xx  world.network_nodes     (0701-0707)
--   0000 0000 08xx  world.network_links (0801-0806) y link_segments (0851-0856)
--   0000 0000 09xx  world.vehicle_types     (0901-0902)
--   0000 0000 0Axx  ledger.accounts         (0A01-0A09)
--   0000 0000 0Bxx  ledger.transactions (0B01-0B06) y entries (0B11-0B1C)
-- =============================================================================

-- -----------------------------------------------------------------------------
-- 1. auth.accounts — banco central, ciudades, humano demo y bots
-- -----------------------------------------------------------------------------

INSERT INTO auth.accounts (id, kind, name) VALUES
    ('00000000-0000-7000-8000-000000000001', 'system', 'Banco Central'),
    ('00000000-0000-7000-8000-000000000002', 'city',   'Ferrópolis'),
    ('00000000-0000-7000-8000-000000000003', 'city',   'Costaverde'),
    ('00000000-0000-7000-8000-000000000004', 'human',  'Aurora Corp'),
    ('00000000-0000-7000-8000-000000000005', 'bot',    'Bot Minero Norte'),
    ('00000000-0000-7000-8000-000000000006', 'bot',    'Bot Fundición Este'),
    ('00000000-0000-7000-8000-000000000007', 'bot',    'Bot Arbitraje Sur')
ON CONFLICT DO NOTHING;

INSERT INTO auth.bot_profiles (id, account_id, archetype, behavior, density_weight, active) VALUES
    ('00000000-0000-7000-8000-000000000021', '00000000-0000-7000-8000-000000000005',
     'primary_producer',       '{"focus_region":"Norte"}', 1.0, true),
    ('00000000-0000-7000-8000-000000000022', '00000000-0000-7000-8000-000000000006',
     'industrial_transformer', '{"focus_region":"Este"}',  1.0, true),
    ('00000000-0000-7000-8000-000000000023', '00000000-0000-7000-8000-000000000007',
     'arbitrageur',            '{"focus_region":"Sur"}',   1.0, true)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 2. auth.account_credentials — sha256 hex del secreto (nivel dev)
--    'aurora' / 'botmineronorte' / 'botfundicioneste' / 'botarbitrajesur'
-- -----------------------------------------------------------------------------

INSERT INTO auth.account_credentials (account_id, secret_hash) VALUES
    ('00000000-0000-7000-8000-000000000004',
     '9b89025ce7a6d932b28f6e15132a70d402f723874a425e9b4c7cc3b179fa66ce'),  -- 'aurora'
    ('00000000-0000-7000-8000-000000000005',
     '24a0ebefa43a5f024de32372bcc38f2c912e1e1d5e4ae12c56535a0610e9b295'),  -- 'botmineronorte'
    ('00000000-0000-7000-8000-000000000006',
     '63191abd86db2b61f4b8bf89839e73835e3d06331198fa393a5abd866b87191a'),  -- 'botfundicioneste'
    ('00000000-0000-7000-8000-000000000007',
     'b04b616bf4e498935f57b7cbfa3df0a4f7f36207c617fb97534377f85e79ac50')   -- 'botarbitrajesur'
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 3. world.products
-- -----------------------------------------------------------------------------

INSERT INTO world.products (id, code, name, class, unit_volume, base_price, price_floor, price_ceiling, is_fuel) VALUES
    ('00000000-0000-7000-8000-000000000101', 'iron_ore',    'Mineral de hierro',  'basic',  2,  100,   50,   300, false),
    ('00000000-0000-7000-8000-000000000102', 'coal',        'Carbón',             'basic',  2,   80,   40,   240, true),
    ('00000000-0000-7000-8000-000000000103', 'food',        'Alimentos',          'basic',  1,   60,   30,   180, false),
    ('00000000-0000-7000-8000-000000000104', 'steel_ingot', 'Lingote de acero',   'basic',  3,  400,  200,  1200, false),
    ('00000000-0000-7000-8000-000000000105', 'components',  'Componentes',        'luxury', 2,  900,  450,  2700, false),
    ('00000000-0000-7000-8000-000000000106', 'machinery',   'Maquinaria',         'luxury', 10, 5000, 2500, 15000, false)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 4. world.building_types
-- -----------------------------------------------------------------------------

INSERT INTO world.building_types
    (id, code, name, footprint_cells, max_level, base_storage, placement_rules, level_curve, build_cost, maintenance_cost)
VALUES
    ('00000000-0000-7000-8000-000000000201', 'iron_mine',         'Mina de hierro',        4,  4,  2000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}',  80000, 400),
    ('00000000-0000-7000-8000-000000000202', 'coal_mine',         'Mina de carbón',        4,  4,  2000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}',  70000, 350),
    ('00000000-0000-7000-8000-000000000203', 'farm',              'Granja',                6,  4,  3000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}',  40000, 200),
    ('00000000-0000-7000-8000-000000000204', 'blast_furnace',     'Alto horno',            9,  4,  4000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}', 150000, 800),
    ('00000000-0000-7000-8000-000000000205', 'component_plant',   'Planta de componentes', 6,  4,  3000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}', 200000, 1000),
    ('00000000-0000-7000-8000-000000000206', 'machinery_factory', 'Fábrica de maquinaria', 12, 4,  5000, '{}',
     '{"2":{"lines":2,"speed":1.25},"3":{"lines":3,"speed":1.5},"4":{"lines":4,"speed":2.0}}', 350000, 1500),
    ('00000000-0000-7000-8000-000000000207', 'warehouse',         'Almacén',               4,  4, 20000, '{}',
     '{"2":{"lines":1,"speed":1.0},"3":{"lines":1,"speed":1.0},"4":{"lines":1,"speed":1.0}}',   60000, 300)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 5. world.recipes + world.recipe_ingredients
-- -----------------------------------------------------------------------------

INSERT INTO world.recipes
    (id, building_type_id, code, name, batch_sim_seconds, fuel_product_id, fuel_per_batch, workers_required, min_city_level)
VALUES
    ('00000000-0000-7000-8000-000000000301', '00000000-0000-7000-8000-000000000201',
     'mine_iron',       'Extracción de hierro',      3600, '00000000-0000-7000-8000-000000000102', 1,  5, 1),
    ('00000000-0000-7000-8000-000000000302', '00000000-0000-7000-8000-000000000202',
     'mine_coal',       'Extracción de carbón',      3600, NULL,                                   0,  5, 1),
    ('00000000-0000-7000-8000-000000000303', '00000000-0000-7000-8000-000000000203',
     'grow_food',       'Cultivo de alimentos',      7200, NULL,                                   0,  3, 1),
    ('00000000-0000-7000-8000-000000000304', '00000000-0000-7000-8000-000000000204',
     'smelt_steel',     'Fundición de acero',        5400, '00000000-0000-7000-8000-000000000102', 2, 10, 1),
    ('00000000-0000-7000-8000-000000000305', '00000000-0000-7000-8000-000000000205',
     'make_components', 'Fabricación de componentes', 5400, '00000000-0000-7000-8000-000000000102', 1,  8, 2),
    ('00000000-0000-7000-8000-000000000306', '00000000-0000-7000-8000-000000000206',
     'make_machinery',  'Fabricación de maquinaria', 9000, '00000000-0000-7000-8000-000000000102', 2, 12, 2)
ON CONFLICT DO NOTHING;

INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES
    -- mine_iron: output 10 iron_ore
    ('00000000-0000-7000-8000-000000000301', '00000000-0000-7000-8000-000000000101', 'output', 10),
    -- mine_coal: output 10 coal
    ('00000000-0000-7000-8000-000000000302', '00000000-0000-7000-8000-000000000102', 'output', 10),
    -- grow_food: output 20 food
    ('00000000-0000-7000-8000-000000000303', '00000000-0000-7000-8000-000000000103', 'output', 20),
    -- smelt_steel: 8 iron_ore + 4 coal -> 4 steel_ingot
    ('00000000-0000-7000-8000-000000000304', '00000000-0000-7000-8000-000000000101', 'input',   8),
    ('00000000-0000-7000-8000-000000000304', '00000000-0000-7000-8000-000000000102', 'input',   4),
    ('00000000-0000-7000-8000-000000000304', '00000000-0000-7000-8000-000000000104', 'output',  4),
    -- make_components: 4 steel_ingot -> 8 components
    ('00000000-0000-7000-8000-000000000305', '00000000-0000-7000-8000-000000000104', 'input',   4),
    ('00000000-0000-7000-8000-000000000305', '00000000-0000-7000-8000-000000000105', 'output',  8),
    -- make_machinery: 6 components + 2 steel_ingot -> 1 machinery
    ('00000000-0000-7000-8000-000000000306', '00000000-0000-7000-8000-000000000105', 'input',   6),
    ('00000000-0000-7000-8000-000000000306', '00000000-0000-7000-8000-000000000104', 'input',   2),
    ('00000000-0000-7000-8000-000000000306', '00000000-0000-7000-8000-000000000106', 'output',  1)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 6. world.regions — grilla 2x2, polígonos 1x1 grados (SRID 4326)
-- -----------------------------------------------------------------------------

INSERT INTO world.regions
    (id, name, grid_x, grid_y, bounds, biome, shard_key, tax_rate_bp, customs_rate_bp, canon_base)
VALUES
    ('00000000-0000-7000-8000-000000000401', 'Norte', 0, 0,
     ST_GeomFromText('POLYGON((0 0, 1 0, 1 1, 0 1, 0 0))', 4326),      'mountain', 'shard-0', 200, 100, 5000),
    ('00000000-0000-7000-8000-000000000402', 'Este',  1, 0,
     ST_GeomFromText('POLYGON((1 0, 2 0, 2 1, 1 1, 1 0))', 4326),      'coast',    'shard-0', 200, 100, 5000),
    ('00000000-0000-7000-8000-000000000403', 'Sur',   0, -1,
     ST_GeomFromText('POLYGON((0 -1, 1 -1, 1 0, 0 0, 0 -1))', 4326),   'plains',   'shard-0', 200, 100, 5000),
    ('00000000-0000-7000-8000-000000000404', 'Oeste', 1, -1,
     ST_GeomFromText('POLYGON((1 -1, 2 -1, 2 0, 1 0, 1 -1))', 4326),   'forest',   'shard-0', 200, 100, 5000)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 7. world.cities — vinculadas a su cuenta auth
-- -----------------------------------------------------------------------------

INSERT INTO world.cities
    (id, region_id, account_id, name, location, level, population, influence_radius_m, base_salary)
VALUES
    ('00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000401',
     '00000000-0000-7000-8000-000000000002', 'Ferrópolis',
     ST_GeomFromText('POINT(0.5 0.5)', 4326), 2, 50000, 30000, 120),
    ('00000000-0000-7000-8000-000000000502', '00000000-0000-7000-8000-000000000402',
     '00000000-0000-7000-8000-000000000003', 'Costaverde',
     ST_GeomFromText('POINT(1.5 0.5)', 4326), 1, 20000, 20000, 100)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 8. world.city_demand — food, coal, machinery, components (esta última nivel 2)
--    current_price = base_price del producto
-- -----------------------------------------------------------------------------

INSERT INTO world.city_demand
    (city_id, product_id, d0_per_sim_day, supply_ema, saturation_factor, current_price, unlocked_at_level)
VALUES
    -- Ferrópolis (50.000 hab, nivel 2)
    ('00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000103', 500, 1, 1,   60, 1),
    ('00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000102', 200, 1, 1,   80, 1),
    ('00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000106',   5, 1, 1, 5000, 1),
    ('00000000-0000-7000-8000-000000000501', '00000000-0000-7000-8000-000000000105',  50, 1, 1,  900, 2),
    -- Costaverde (20.000 hab, nivel 1)
    ('00000000-0000-7000-8000-000000000502', '00000000-0000-7000-8000-000000000103', 200, 1, 1,   60, 1),
    ('00000000-0000-7000-8000-000000000502', '00000000-0000-7000-8000-000000000102',  80, 1, 1,   80, 1),
    ('00000000-0000-7000-8000-000000000502', '00000000-0000-7000-8000-000000000106',   2, 1, 1, 5000, 1),
    ('00000000-0000-7000-8000-000000000502', '00000000-0000-7000-8000-000000000105',  20, 1, 1,  900, 2)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 9. world.resource_deposits — 2 de hierro en Norte, 2 de carbón en Sur
-- -----------------------------------------------------------------------------

INSERT INTO world.resource_deposits
    (id, region_id, product_id, location, initial_amount, remaining_amount, renewable, regen_per_sim_day)
VALUES
    ('00000000-0000-7000-8000-000000000601', '00000000-0000-7000-8000-000000000401',
     '00000000-0000-7000-8000-000000000101', ST_GeomFromText('POINT(0.3 0.7)', 4326),  1000000, 1000000, false, 0),
    ('00000000-0000-7000-8000-000000000602', '00000000-0000-7000-8000-000000000401',
     '00000000-0000-7000-8000-000000000101', ST_GeomFromText('POINT(0.7 0.8)', 4326),  1000000, 1000000, false, 0),
    ('00000000-0000-7000-8000-000000000603', '00000000-0000-7000-8000-000000000403',
     '00000000-0000-7000-8000-000000000102', ST_GeomFromText('POINT(0.3 -0.5)', 4326), 1000000, 1000000, false, 0),
    ('00000000-0000-7000-8000-000000000604', '00000000-0000-7000-8000-000000000403',
     '00000000-0000-7000-8000-000000000102', ST_GeomFromText('POINT(0.7 -0.4)', 4326), 1000000, 1000000, false, 0)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 10. Red logística — nodos, enlaces (una fila por enlace; grafo no dirigido
--     para la app) y un segmento por enlace
-- -----------------------------------------------------------------------------

INSERT INTO world.network_nodes (id, kind, region_id, city_id, location) VALUES
    ('00000000-0000-7000-8000-000000000701', 'city_gate', '00000000-0000-7000-8000-000000000401',
     '00000000-0000-7000-8000-000000000501', ST_GeomFromText('POINT(0.5 0.5)', 4326)),
    ('00000000-0000-7000-8000-000000000702', 'city_gate', '00000000-0000-7000-8000-000000000402',
     '00000000-0000-7000-8000-000000000502', ST_GeomFromText('POINT(1.5 0.5)', 4326)),
    ('00000000-0000-7000-8000-000000000703', 'mine',      '00000000-0000-7000-8000-000000000401',
     NULL, ST_GeomFromText('POINT(0.3 0.7)', 4326)),
    ('00000000-0000-7000-8000-000000000704', 'mine',      '00000000-0000-7000-8000-000000000401',
     NULL, ST_GeomFromText('POINT(0.7 0.8)', 4326)),
    ('00000000-0000-7000-8000-000000000705', 'mine',      '00000000-0000-7000-8000-000000000403',
     NULL, ST_GeomFromText('POINT(0.3 -0.5)', 4326)),
    ('00000000-0000-7000-8000-000000000706', 'mine',      '00000000-0000-7000-8000-000000000403',
     NULL, ST_GeomFromText('POINT(0.7 -0.4)', 4326)),
    ('00000000-0000-7000-8000-000000000707', 'junction',  '00000000-0000-7000-8000-000000000401',
     NULL, ST_GeomFromText('POINT(0.9 0.05)', 4326))
ON CONFLICT DO NOTHING;

-- Enlaces road: minas -> junction -> ciudades. Una sola fila por enlace; la
-- aplicación trata el grafo como no dirigido.
INSERT INTO world.network_links
    (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
VALUES
    ('00000000-0000-7000-8000-000000000801', 'road',
     '00000000-0000-7000-8000-000000000703', '00000000-0000-7000-8000-000000000707',
     ST_GeomFromText('LINESTRING(0.3 0.7, 0.9 0.05)', 4326), 45000, 60, 80),
    ('00000000-0000-7000-8000-000000000802', 'road',
     '00000000-0000-7000-8000-000000000704', '00000000-0000-7000-8000-000000000707',
     ST_GeomFromText('LINESTRING(0.7 0.8, 0.9 0.05)', 4326), 40000, 60, 80),
    ('00000000-0000-7000-8000-000000000803', 'road',
     '00000000-0000-7000-8000-000000000705', '00000000-0000-7000-8000-000000000707',
     ST_GeomFromText('LINESTRING(0.3 -0.5, 0.9 0.05)', 4326), 50000, 60, 80),
    ('00000000-0000-7000-8000-000000000804', 'road',
     '00000000-0000-7000-8000-000000000706', '00000000-0000-7000-8000-000000000707',
     ST_GeomFromText('LINESTRING(0.7 -0.4, 0.9 0.05)', 4326), 35000, 60, 80),
    ('00000000-0000-7000-8000-000000000805', 'road',
     '00000000-0000-7000-8000-000000000707', '00000000-0000-7000-8000-000000000701',
     ST_GeomFromText('LINESTRING(0.9 0.05, 0.5 0.5)', 4326), 30000, 60, 80),
    ('00000000-0000-7000-8000-000000000806', 'road',
     '00000000-0000-7000-8000-000000000707', '00000000-0000-7000-8000-000000000702',
     ST_GeomFromText('LINESTRING(0.9 0.05, 1.5 0.5)', 4326), 55000, 60, 80)
ON CONFLICT DO NOTHING;

-- Un segmento por enlace (seq 0), región del tramo, congestion_ema 1
INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema) VALUES
    ('00000000-0000-7000-8000-000000000851', '00000000-0000-7000-8000-000000000801',
     '00000000-0000-7000-8000-000000000401', 0,
     ST_GeomFromText('LINESTRING(0.3 0.7, 0.9 0.05)', 4326), 45000, 1),
    ('00000000-0000-7000-8000-000000000852', '00000000-0000-7000-8000-000000000802',
     '00000000-0000-7000-8000-000000000401', 0,
     ST_GeomFromText('LINESTRING(0.7 0.8, 0.9 0.05)', 4326), 40000, 1),
    ('00000000-0000-7000-8000-000000000853', '00000000-0000-7000-8000-000000000803',
     '00000000-0000-7000-8000-000000000403', 0,
     ST_GeomFromText('LINESTRING(0.3 -0.5, 0.9 0.05)', 4326), 50000, 1),
    ('00000000-0000-7000-8000-000000000854', '00000000-0000-7000-8000-000000000804',
     '00000000-0000-7000-8000-000000000403', 0,
     ST_GeomFromText('LINESTRING(0.7 -0.4, 0.9 0.05)', 4326), 35000, 1),
    ('00000000-0000-7000-8000-000000000855', '00000000-0000-7000-8000-000000000805',
     '00000000-0000-7000-8000-000000000401', 0,
     ST_GeomFromText('LINESTRING(0.9 0.05, 0.5 0.5)', 4326), 30000, 1),
    ('00000000-0000-7000-8000-000000000856', '00000000-0000-7000-8000-000000000806',
     '00000000-0000-7000-8000-000000000402', 0,
     ST_GeomFromText('LINESTRING(0.9 0.05, 1.5 0.5)', 4326), 55000, 1)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 11. world.vehicle_types — camiones road; combustible: coal (simplificación)
-- -----------------------------------------------------------------------------

INSERT INTO world.vehicle_types
    (id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id, fuel_per_100km, autonomy_km,
     purchase_price, operating_cost_per_day)
VALUES
    ('00000000-0000-7000-8000-000000000901', 'truck_s', 'Camión pequeño', 'road', 100, 80,
     '00000000-0000-7000-8000-000000000102', 2, 800,  50000, 500),
    ('00000000-0000-7000-8000-000000000902', 'truck_m', 'Camión mediano', 'road', 250, 70,
     '00000000-0000-7000-8000-000000000102', 4, 800, 120000, 900)
ON CONFLICT DO NOTHING;

-- -----------------------------------------------------------------------------
-- 12. Ledger — cuentas y emisión inicial por asientos balanceados
-- -----------------------------------------------------------------------------

-- Cuentas de sistema del banco central: emisión (única que puede ser negativa) y sink
INSERT INTO ledger.accounts (id, kind, owner_account_id) VALUES
    ('00000000-0000-7000-8000-000000000a01', 'emission', '00000000-0000-7000-8000-000000000001'),
    ('00000000-0000-7000-8000-000000000a02', 'sink',     '00000000-0000-7000-8000-000000000001'),
    -- Cajas ('cash') de cada corporación
    ('00000000-0000-7000-8000-000000000a03', 'cash',     '00000000-0000-7000-8000-000000000001'), -- Banco Central
    ('00000000-0000-7000-8000-000000000a04', 'cash',     '00000000-0000-7000-8000-000000000002'), -- Ferrópolis
    ('00000000-0000-7000-8000-000000000a05', 'cash',     '00000000-0000-7000-8000-000000000003'), -- Costaverde
    ('00000000-0000-7000-8000-000000000a06', 'cash',     '00000000-0000-7000-8000-000000000004'), -- Aurora Corp
    ('00000000-0000-7000-8000-000000000a07', 'cash',     '00000000-0000-7000-8000-000000000005'), -- Bot Minero Norte
    ('00000000-0000-7000-8000-000000000a08', 'cash',     '00000000-0000-7000-8000-000000000006'), -- Bot Fundición Este
    ('00000000-0000-7000-8000-000000000a09', 'cash',     '00000000-0000-7000-8000-000000000007')  -- Bot Arbitraje Sur
ON CONFLICT DO NOTHING;

-- Asientos de emisión inicial. TODO el dinero entra por doble entrada contra
-- 'emission'; los IDs fijos de transactions Y entries hacen el bloque
-- idempotente (re-ejecutar no inserta partidas y el trigger de saldo no
-- vuelve a disparar).
INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description) VALUES
    ('00000000-0000-7000-8000-000000000b01', 'seed_capital',       0,
     '00000000-0000-7000-8000-000000000004', 'Capital semilla de Aurora Corp'),
    ('00000000-0000-7000-8000-000000000b02', 'bot_capitalization', 0,
     '00000000-0000-7000-8000-000000000005', 'Capitalización de Bot Minero Norte'),
    ('00000000-0000-7000-8000-000000000b03', 'bot_capitalization', 0,
     '00000000-0000-7000-8000-000000000006', 'Capitalización de Bot Fundición Este'),
    ('00000000-0000-7000-8000-000000000b04', 'bot_capitalization', 0,
     '00000000-0000-7000-8000-000000000007', 'Capitalización de Bot Arbitraje Sur'),
    ('00000000-0000-7000-8000-000000000b05', 'seed_capital',       0,
     '00000000-0000-7000-8000-000000000002', 'Pre-fondeo de la ciudad Ferrópolis'),
    ('00000000-0000-7000-8000-000000000b06', 'seed_capital',       0,
     '00000000-0000-7000-8000-000000000003', 'Pre-fondeo de la ciudad Costaverde')
ON CONFLICT DO NOTHING;

INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES
    -- Aurora Corp: 1.000.000
    ('00000000-0000-7000-8000-000000000b11', '00000000-0000-7000-8000-000000000b01',
     '00000000-0000-7000-8000-000000000a01',  -1000000),
    ('00000000-0000-7000-8000-000000000b12', '00000000-0000-7000-8000-000000000b01',
     '00000000-0000-7000-8000-000000000a06',   1000000),
    -- Bot Minero Norte: 500.000
    ('00000000-0000-7000-8000-000000000b13', '00000000-0000-7000-8000-000000000b02',
     '00000000-0000-7000-8000-000000000a01',   -500000),
    ('00000000-0000-7000-8000-000000000b14', '00000000-0000-7000-8000-000000000b02',
     '00000000-0000-7000-8000-000000000a07',    500000),
    -- Bot Fundición Este: 500.000
    ('00000000-0000-7000-8000-000000000b15', '00000000-0000-7000-8000-000000000b03',
     '00000000-0000-7000-8000-000000000a01',   -500000),
    ('00000000-0000-7000-8000-000000000b16', '00000000-0000-7000-8000-000000000b03',
     '00000000-0000-7000-8000-000000000a08',    500000),
    -- Bot Arbitraje Sur: 500.000
    ('00000000-0000-7000-8000-000000000b17', '00000000-0000-7000-8000-000000000b04',
     '00000000-0000-7000-8000-000000000a01',   -500000),
    ('00000000-0000-7000-8000-000000000b18', '00000000-0000-7000-8000-000000000b04',
     '00000000-0000-7000-8000-000000000a09',    500000),
    -- Ferrópolis: 10.000.000
    ('00000000-0000-7000-8000-000000000b19', '00000000-0000-7000-8000-000000000b05',
     '00000000-0000-7000-8000-000000000a01', -10000000),
    ('00000000-0000-7000-8000-000000000b1a', '00000000-0000-7000-8000-000000000b05',
     '00000000-0000-7000-8000-000000000a04',  10000000),
    -- Costaverde: 10.000.000
    ('00000000-0000-7000-8000-000000000b1b', '00000000-0000-7000-8000-000000000b06',
     '00000000-0000-7000-8000-000000000a01', -10000000),
    ('00000000-0000-7000-8000-000000000b1c', '00000000-0000-7000-8000-000000000b06',
     '00000000-0000-7000-8000-000000000a05',  10000000)
ON CONFLICT DO NOTHING;

-- world.sim_clock (fila id=1) ya la creó la migración 0003; no se toca aquí.
