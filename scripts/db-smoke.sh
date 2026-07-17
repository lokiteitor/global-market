#!/usr/bin/env bash
# =============================================================================
# Imperio Industrial — db-smoke.sh
# Smoke test del esquema contra una base ya migrada (make migrate-up).
# Verifica las invariantes del ledger (DB doc "Comportamiento verificado"),
# uuidv7() nativo (ADR-018), PostGIS (ADR-019) y el reloj world.sim_clock.
#
# Usa psql local si esta en PATH; si no, psql dentro del contenedor
# imperio-postgres. Conexion: II_DATABASE_URL (por defecto, el entorno dev).
# Los casos de fallo esperado se ejecutan en transacciones abortadas y no
# ensucian datos; los datos del caso 1 se limpian al final.
# =============================================================================
set -euo pipefail

DB_URL="${II_DATABASE_URL:-postgres://imperio:imperio@localhost:5432/imperio?sslmode=disable}"

if command -v psql >/dev/null 2>&1; then
    PSQL=(psql)
else
    PSQL=(docker exec -i imperio-postgres psql)
fi

# Ejecuta SQL leido de stdin; propaga el codigo de salida de psql
run_sql() {
    "${PSQL[@]}" "$DB_URL" -X -q -v ON_ERROR_STOP=1 -At
}

# Consulta escalar
query() {
    "${PSQL[@]}" "$DB_URL" -X -q -v ON_ERROR_STOP=1 -Atc "$1"
}

FAILURES=0
pass() { echo "PASS  $1"; }
fail() { echo "FAIL  $1"; FAILURES=$((FAILURES + 1)); }

expect_ok() { # $1=descripcion, $2=sql — debe ejecutarse sin error
    if printf '%s\n' "$2" | run_sql >/dev/null 2>&1; then
        pass "$1"
    else
        fail "$1"
    fi
}

expect_fail() { # $1=descripcion, $2=sql — DEBE fallar (transaccion abortada)
    if printf '%s\n' "$2" | run_sql >/dev/null 2>&1; then
        fail "$1 (la operacion NO fallo y debia fallar)"
    else
        pass "$1"
    fi
}

# UUIDs fijos de prueba (solo hex; se limpian al inicio y al final)
ACC='0f000000-0000-4000-8000-0000000000a0'   # auth.accounts (system, de prueba)
LAC_CASH='0f000000-0000-4000-8000-0000000000a1'
LAC_EMIS='0f000000-0000-4000-8000-0000000000a2'
TX1='0f000000-0000-4000-8000-0000000000b1'
E1='0f000000-0000-4000-8000-0000000000c1'
E2='0f000000-0000-4000-8000-0000000000c2'
TX2='0f000000-0000-4000-8000-0000000000b2'
E3='0f000000-0000-4000-8000-0000000000c3'
TX3='0f000000-0000-4000-8000-0000000000b3'
E4='0f000000-0000-4000-8000-0000000000c4'
E5='0f000000-0000-4000-8000-0000000000c5'

# Limpieza de los datos de prueba del caso 1. Las partidas son append-only por
# diseño: se deshabilitan sus triggers de inmutabilidad SOLO para la limpieza
# (requiere ser owner de las tablas, i.e. el usuario que migra).
cleanup() {
    run_sql <<SQL
BEGIN;
ALTER TABLE ledger.entries DISABLE TRIGGER trg_entries_immutable;
ALTER TABLE ledger.transactions DISABLE TRIGGER trg_transactions_immutable;
DELETE FROM ledger.entries WHERE transaction_id = '$TX1';
DELETE FROM ledger.transactions WHERE id = '$TX1';
ALTER TABLE ledger.entries ENABLE TRIGGER trg_entries_immutable;
ALTER TABLE ledger.transactions ENABLE TRIGGER trg_transactions_immutable;
DELETE FROM ledger.accounts WHERE id IN ('$LAC_CASH', '$LAC_EMIS');
DELETE FROM auth.accounts WHERE id = '$ACC';
COMMIT;
SQL
}

echo "== db-smoke: $DB_URL (cliente: ${PSQL[0]}) =="

# Limpieza previa por si una ejecucion anterior quedo a medias
cleanup

# ── Caso 1: asiento balanceado de emision — COMMIT OK y saldos correctos ────
CASE1_SQL="
BEGIN;
INSERT INTO auth.accounts (id, kind, name) VALUES ('$ACC', 'system', 'zz_smoke_test_corp');
INSERT INTO ledger.accounts (id, kind, owner_account_id) VALUES ('$LAC_CASH', 'cash', '$ACC');
INSERT INTO ledger.accounts (id, kind) VALUES ('$LAC_EMIS', 'emission');
INSERT INTO ledger.transactions (id, kind, sim_time_at, description)
    VALUES ('$TX1', 'seed_capital', 0, 'smoke: emision balanceada');
INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES
    ('$E1', '$TX1', '$LAC_CASH',  1000),
    ('$E2', '$TX1', '$LAC_EMIS', -1000);
COMMIT;
"
if printf '%s\n' "$CASE1_SQL" | run_sql >/dev/null 2>&1; then
    BAL_CASH=$(query "SELECT balance FROM ledger.accounts WHERE id = '$LAC_CASH'")
    BAL_EMIS=$(query "SELECT balance FROM ledger.accounts WHERE id = '$LAC_EMIS'")
    if [ "$BAL_CASH" = "1000" ] && [ "$BAL_EMIS" = "-1000" ]; then
        pass "caso 1: emision balanceada asentada; saldos cash=$BAL_CASH emission=$BAL_EMIS"
    else
        fail "caso 1: saldos incorrectos (cash=$BAL_CASH emission=$BAL_EMIS, esperado 1000/-1000)"
    fi
else
    fail "caso 1: el asiento balanceado de emision no se pudo asentar"
fi

# ── Caso 2: asiento desbalanceado (una sola partida) — debe fallar en COMMIT ─
expect_fail "caso 2: asiento desbalanceado rechazado en el COMMIT" "
BEGIN;
INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ('$TX2', 'transfer', 0);
INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
    VALUES ('$E3', '$TX2', '$LAC_CASH', 500);
COMMIT;
"

# ── Caso 3: partida que dejaria cash negativo — debe fallar (sin deuda) ──────
expect_fail "caso 3: partida que dejaria cash negativo rechazada" "
BEGIN;
INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ('$TX3', 'transfer', 0);
INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES
    ('$E4', '$TX3', '$LAC_CASH', -5000),
    ('$E5', '$TX3', '$LAC_EMIS',  5000);
COMMIT;
"

# El saldo del caso 1 debe seguir intacto tras los fallos esperados
BAL_CASH=$(query "SELECT balance FROM ledger.accounts WHERE id = '$LAC_CASH'")
if [ "$BAL_CASH" != "1000" ]; then
    fail "casos 2/3: el saldo cash cambio tras transacciones abortadas (cash=$BAL_CASH)"
fi

# ── Caso 4: UPDATE y DELETE sobre ledger.entries — append-only ───────────────
expect_fail "caso 4a: UPDATE sobre ledger.entries rechazado (append-only)" "
UPDATE ledger.entries SET amount = 999 WHERE id = '$E1';
"
expect_fail "caso 4b: DELETE sobre ledger.entries rechazado (append-only)" "
DELETE FROM ledger.entries WHERE id = '$E1';
"

# ── Caso 5: uuidv7() nativo (PG18, ADR-018) ──────────────────────────────────
if UUID=$(query "SELECT uuidv7()") && [ -n "$UUID" ]; then
    pass "caso 5: SELECT uuidv7() -> $UUID"
else
    fail "caso 5: SELECT uuidv7() no funciona"
fi

# ── Caso 6: PostGIS instalado ────────────────────────────────────────────────
if PGIS=$(query "SELECT postgis_full_version()") && [ -n "$PGIS" ]; then
    pass "caso 6: postgis_full_version() responde"
else
    fail "caso 6: postgis_full_version() no responde"
fi

# ── Caso 7: world.sim_clock solo acepta la fila id = 1 ───────────────────────
expect_ok "caso 7a: world.sim_clock acepta la fila id=1 (tx revertida)" "
BEGIN;
DELETE FROM world.sim_clock;
INSERT INTO world.sim_clock (id) VALUES (1);
ROLLBACK;
"
expect_fail "caso 7b: world.sim_clock rechaza una fila con id<>1" "
BEGIN;
INSERT INTO world.sim_clock (id) VALUES (2);
ROLLBACK;
"

# ── Limpieza final de los datos de prueba ────────────────────────────────────
cleanup
echo "== limpieza de datos de prueba completada =="

if [ "$FAILURES" -gt 0 ]; then
    echo "== db-smoke: $FAILURES caso(s) FALLARON =="
    exit 1
fi
echo "== db-smoke: todos los casos PASS =="
