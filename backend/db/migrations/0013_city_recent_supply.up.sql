-- =============================================================================
-- Imperio Industrial — 0013_city_recent_supply (up)
-- Incremento 6b: ECONOMY BALANCER (ciudades como consumidor final, GDD 5.5/5.6).
-- Añade a world.city_demand el acumulador `recent_supply`: la oferta entregada
-- a la ciudad para ese producto DESDE EL ÚLTIMO RECÁLCULO del Balancer.
--
-- Alimenta la media móvil exponencial de la curva de demanda (supply_ema, GDD
-- 5.6): el CONSUMER del Balancer lo INCREMENTA al consumir cada entrega urbana
-- (contract.settled cuyo comprador es una ciudad → city stock_free consumido a
-- world_source, ADR-022) y el DemandWorker lo PLIEGA en supply_ema y lo RESETEA
-- a 0 en cada recálculo. Su valor a la hora del recálculo distingue además:
--   - variedad: era 0 antes de la primera entrega de la ventana → bono de
--     supply_index por producto nuevo (crecimiento de ciudad, GDD 5.6);
--   - abandono logístico: suma 0 en la ventana → decae supply_index (posible
--     bajada de nivel).
--
-- Es un tracker interno del Balancer (world es propiedad del motor Go): no forma
-- parte del contrato de lectura de curva de demanda (world/catalog no lo
-- proyecta). ii_engine ya tiene ALL sobre world.* (0007): sin GRANTs nuevos.
-- =============================================================================

ALTER TABLE world.city_demand
    ADD COLUMN recent_supply stock_qty NOT NULL DEFAULT 0 CHECK (recent_supply >= 0);

COMMENT ON COLUMN world.city_demand.recent_supply IS
    'Oferta entregada a la ciudad para este producto desde el último recálculo del Balancer (alimenta supply_ema; el consumer la incrementa, el DemandWorker la pliega y la resetea a 0). Incremento 6b, GDD 5.6.';
