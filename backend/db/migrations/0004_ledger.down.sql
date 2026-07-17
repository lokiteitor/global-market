-- =============================================================================
-- Imperio Industrial — 0004_ledger (down)
-- Revierte el esquema ledger en orden inverso a su creación, empezando por
-- las funciones y las FKs cross-schema hacia/desde world.
-- =============================================================================

DROP FUNCTION IF EXISTS ledger.settle_contract_prorata(
    uuid, uuid, sim_time, uuid, uuid, uuid, uuid, uuid, INT, uuid[]);
DROP FUNCTION IF EXISTS ledger.confirm_contract(
    uuid, uuid, sim_time, stock_qty, money_amount,
    uuid, uuid, uuid, uuid, uuid, uuid, uuid[]);

ALTER TABLE ledger.contract_deliveries
    DROP CONSTRAINT IF EXISTS fk_deliveries_shipment;
ALTER TABLE world.shipments
    DROP CONSTRAINT IF EXISTS fk_shipments_freight,
    DROP CONSTRAINT IF EXISTS fk_shipments_contract;

DROP TABLE IF EXISTS ledger.freight_contracts;
DROP TABLE IF EXISTS ledger.contract_deliveries;
DROP TABLE IF EXISTS ledger.contracts;
DROP TABLE IF EXISTS ledger.publication_acceptances;
DROP TABLE IF EXISTS ledger.publications;

-- Los triggers de entries/transactions caen con sus tablas; después, sus funciones.
DROP TABLE IF EXISTS ledger.entries;
DROP TABLE IF EXISTS ledger.transactions;
DROP TABLE IF EXISTS ledger.accounts;

DROP FUNCTION IF EXISTS ledger.forbid_mutation();
DROP FUNCTION IF EXISTS ledger.assert_transaction_balanced();
DROP FUNCTION IF EXISTS ledger.apply_entry_balance();

DROP TYPE IF EXISTS ledger.contract_channel;
DROP TYPE IF EXISTS ledger.contract_status;
DROP TYPE IF EXISTS ledger.acceptance_status;
DROP TYPE IF EXISTS ledger.publication_status;
DROP TYPE IF EXISTS ledger.publication_kind;
DROP TYPE IF EXISTS ledger.transaction_kind;
DROP TYPE IF EXISTS ledger.account_kind;
