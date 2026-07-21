-- Revierte 0014_freight (CCRI-Flete, Incremento 8).
DROP FUNCTION IF EXISTS ledger.settle_freight_prorata(uuid, uuid, sim_time, stock_qty, uuid, uuid, uuid, uuid, INT, uuid[]);
DROP FUNCTION IF EXISTS ledger.confirm_freight(uuid, uuid, sim_time, stock_qty, money_amount, money_amount, uuid, uuid, uuid, uuid, uuid, uuid, uuid[]);
DROP INDEX IF EXISTS ledger.ix_freight_due;
DROP TABLE IF EXISTS ledger.freight_deliveries;
ALTER TABLE ledger.publications DROP CONSTRAINT IF EXISTS ck_publications_freight;
ALTER TABLE ledger.publications DROP COLUMN IF EXISTS declared_value;
