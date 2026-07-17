-- =============================================================================
-- Imperio Industrial — 0010_delivery_idempotency (down)
-- Revierte 0010: elimina el índice único de idempotencia de entregas. No hay
-- datos estructurales que perder (el índice solo impone unicidad; la tabla
-- ledger.contract_deliveries y su columna shipment_id son de 0004).
-- =============================================================================

DROP INDEX IF EXISTS ledger.ux_contract_deliveries_shipment;
