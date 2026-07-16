-- =============================================================================
-- Imperio Industrial — 0007_idempotency.sql
-- Idempotencia de comandos del gateway (ADR-IMPL-09): ante una cabecera
-- Idempotency-Key repetida por la misma cuenta, el gateway reproduce la
-- respuesta almacenada en lugar de re-ejecutar el comando.
-- =============================================================================

CREATE TABLE auth.idempotency_keys (
    key             uuid PRIMARY KEY,
    account_id      uuid NOT NULL REFERENCES auth.accounts(id),
    endpoint        text NOT NULL,
    response_status int NOT NULL,
    response_body   jsonb NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);
