-- =============================================================================
-- Imperio Industrial — 0011_enforcement (up)
-- Incremento 6a: CASCADA DE INSOLVENCIA (GDD 5.9, 11.2). Materializa los dos
-- escalones que faltaban de la cascada "saldo = 0, nunca deuda": la degradación
-- por mantenimiento impagado (3º) y el ciclo canon → gracia → embargo → subasta
-- (4º). El motor vive en internal/world/enforcement; esta migración solo AÑADE
-- las columnas de estado que el motor barre y los índices de esos barridos.
--
-- El esquema base ya existe (0003_world): world.buildings.status/condition_pct,
-- world.land_concessions.status (enum active/delinquent/grace/reverted),
-- world.building_types.maintenance_cost y world.vehicle_types.operating_cost_per_day.
--
-- Columnas nuevas:
--   * world.buildings.maintenance_paid_until_sim  — sim-time hasta el que las
--     obligaciones de mantenimiento del edificio están LIQUIDADAS (pagadas en
--     efectivo o saldadas por degradación: "nunca deuda", GDD 5.9). El barrido
--     de mantenimiento cobra por día-sim desde este marcador. En un edificio
--     ABANDONADO el marcador pasa a ser el instante del abandono: arranca el
--     conteo del periodo de gracia previo al embargo (GDD 11.2).
--   * world.vehicles.maintenance_paid_until_sim   — idéntico para el opex del
--     vehículo (vehicle_types.operating_cost_per_day). El vehículo no tiene
--     condición: el impago solo drena caja (su avería/desgaste los maneja el
--     motor de tránsito), y los días que no puede pagar se condonan (sin deuda).
--   * world.land_concessions.grace_until_sim (NULLABLE) — vencimiento del
--     periodo de gracia del canon. Se fija al pasar a 'delinquent' (impago de
--     canon); al vencer, la concesión pasa a 'grace' (marcada para embargo) y
--     de ahí a 'reverted'. NULL mientras la concesión está al día.
--
-- GRANTs: esta migración NO crea tablas nuevas, solo AÑADE columnas e índices a
-- tablas de world que ii_engine ya posee con ALL (0007 + ALTER DEFAULT
-- PRIVILEGES). Los privilegios de columna se heredan del privilegio de tabla:
-- no hacen falta GRANTs adicionales.
-- =============================================================================

-- ── Columnas de estado del barrido ───────────────────────────────────────────

ALTER TABLE world.buildings
    ADD COLUMN maintenance_paid_until_sim sim_time NOT NULL DEFAULT 0;

ALTER TABLE world.vehicles
    ADD COLUMN maintenance_paid_until_sim sim_time NOT NULL DEFAULT 0;

ALTER TABLE world.land_concessions
    ADD COLUMN grace_until_sim sim_time;   -- NULL mientras la concesión está al día

-- ── Índices de barrido ───────────────────────────────────────────────────────

-- Mantenimiento: edificios operativos/dañados con día-sim de mantenimiento
-- vencido (candidatos del barrido de degradación, GDD 11.2).
CREATE INDEX ix_buildings_maintenance_due ON world.buildings (maintenance_paid_until_sim)
    WHERE status IN ('operational', 'damaged');

-- Embargo (rama mantenimiento): edificios abandonados cuyo periodo de gracia
-- (medido desde maintenance_paid_until_sim = instante del abandono) va venciendo.
CREATE INDEX ix_buildings_abandoned ON world.buildings (maintenance_paid_until_sim)
    WHERE status = 'abandoned';

-- Embargo: localizar por concesión los edificios a congelar (retirada in situ).
CREATE INDEX ix_buildings_concession ON world.buildings (concession_id);

-- Canon (rama concesión): concesiones morosas cuyo periodo de gracia va
-- venciendo (delinquent → grace). El barrido de canon vencido reutiliza el
-- índice existente ix_concessions_expiry (expires_at_sim) WHERE status='active'.
CREATE INDEX ix_concessions_grace ON world.land_concessions (grace_until_sim)
    WHERE status = 'delinquent';

-- Embargo (rama concesión): concesiones marcadas para embargo (grace → reverted).
CREATE INDEX ix_concessions_pending_seizure ON world.land_concessions (id)
    WHERE status = 'grace';

-- Mantenimiento de flota: vehículos con opex vencido (todos los estados; el
-- opex se cobra por día-sim con independencia de si el vehículo se mueve).
CREATE INDEX ix_vehicles_maintenance_due ON world.vehicles (maintenance_paid_until_sim);
