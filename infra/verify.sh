#!/usr/bin/env bash
# =============================================================================
# infra/verify.sh — smoke end-to-end reproducible de Imperio Industrial.
#
# Ejecuta el ciclo CCRI completo contra el stack real (BD + engine + gateway +
# bots) usando SOLO la API pública y asserts psql sobre las invariantes del
# ledger. El motor corre con TIME_RATIO=240 (1 s wall = 240 s sim: construcción
# 60 s, lote de mina 15 s) — ese ratio es SOLO para tests/desarrollo.
#
# Uso: make verify   (o: bash infra/verify.sh desde la raíz del repo)
#
# Al terminar, la base de datos queda POBLADA por el verify (edificios,
# contratos, publicaciones de bots). Para dejarla limpia:
#   make db-reset && make db-migrate && make db-seed
# =============================================================================
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# ── Configuración ────────────────────────────────────────────────────────────
DB_URL="${DATABASE_URL:-postgres://imperio:imperio@localhost:5440/imperio}"
GATEWAY_PORT="${GATEWAY_PORT:-8080}"
API="http://localhost:${GATEWAY_PORT}/api/v1"
WS_URL="ws://localhost:${GATEWAY_PORT}/ws"
TIME_RATIO=240          # solo dev/tests: acelera el sim-clock del motor
DC="docker compose -f infra/docker-compose.yml"

# UUIDs fijos del seed (backend/seeds/seed_world.sql) — mundo determinista.
COAL='00000000-0000-7000-8000-000000000102'
REGION_SUR='00000000-0000-7000-8000-000000000403'
CITY_FERROPOLIS='00000000-0000-7000-8000-000000000501'
ACC_FERROPOLIS='00000000-0000-7000-8000-000000000002'
ACC_BOT_FUNDICION='00000000-0000-7000-8000-000000000006'
BT_COAL_MINE='00000000-0000-7000-8000-000000000202'
RECIPE_MINE_COAL='00000000-0000-7000-8000-000000000302'
VT_TRUCK_S='00000000-0000-7000-8000-000000000901'
AURORA_CASH='00000000-0000-7000-8000-000000000a06'
COAL_BASE_PRICE=80

TMP="$(mktemp -d "${TMPDIR:-/tmp}/imperio-verify.XXXXXX")"
ENGINE_LOG="$TMP/engine.log"
GATEWAY_LOG="$TMP/gateway.log"
BOTS_LOG="$TMP/bots.log"
RESP="$TMP/resp.json"

ENGINE_PID=""
GATEWAY_PID=""
BOTS_PID=""
VERIFY_T0=$(date +%s)

# ── Limpieza: SIEMPRE mata engine/gateway/bots que arrancó este script ──────
cleanup() {
  local code=$?
  set +e
  if [[ -n "$BOTS_PID" ]]; then kill -TERM -- "-$BOTS_PID" 2>/dev/null; fi
  [[ -n "$ENGINE_PID" ]] && kill "$ENGINE_PID" 2>/dev/null
  [[ -n "$GATEWAY_PID" ]] && kill "$GATEWAY_PID" 2>/dev/null
  sleep 1
  if [[ -n "$BOTS_PID" ]]; then kill -KILL -- "-$BOTS_PID" 2>/dev/null; fi
  [[ -n "$ENGINE_PID" ]] && kill -9 "$ENGINE_PID" 2>/dev/null
  [[ -n "$GATEWAY_PID" ]] && kill -9 "$GATEWAY_PID" 2>/dev/null
  wait 2>/dev/null
  if [[ $code -ne 0 ]]; then
    echo ""
    echo "── últimas líneas de engine.log ──"; tail -n 15 "$ENGINE_LOG" 2>/dev/null
    echo "── últimas líneas de gateway.log ──"; tail -n 15 "$GATEWAY_LOG" 2>/dev/null
    echo "── últimas líneas de bots.log ──"; tail -n 10 "$BOTS_LOG" 2>/dev/null
    echo "(logs completos en $TMP)"
  fi
  exit "$code"
}
trap cleanup EXIT

# ── Helpers de salida ────────────────────────────────────────────────────────
STEP=0
step() { STEP=$((STEP + 1)); echo ""; echo "[$STEP] $*"; }
pass() { echo "  PASS: $*"; }
fail() { echo "  FAIL: $*" >&2; exit 1; }

# ── psql contra la BD del compose ────────────────────────────────────────────
db() { $DC exec -T db psql -v ON_ERROR_STOP=1 -qtA -U imperio -d imperio -c "$1"; }

# ── JSON: jq si está disponible; si no, mini-evaluador python3 equivalente ──
if command -v jq >/dev/null 2>&1; then
  HAVE_JQ=1
else
  HAVE_JQ=0
  command -v python3 >/dev/null 2>&1 || fail "ni jq ni python3 disponibles para parsear JSON"
  cat > "$TMP/pyjq.py" <<'PYEOF'
# Mini-subconjunto de jq (solo lo que usa verify.sh): pipelines de
#   .a.b[0].c   |  .data[]  |  select(.k=="v")  |  length
import json, re, sys

def apply_path(vals, path):
    out = []
    for v in vals:
        ok = True
        for part in re.findall(r'\.([A-Za-z_][A-Za-z0-9_]*)|\[(\d*)\]', path):
            key, idx = part
            if key:
                if isinstance(v, dict) and key in v: v = v[key]
                else: ok = False; break
            elif idx == '':
                if isinstance(v, list): out.extend(v); ok = False; break
                ok = False; break
            else:
                i = int(idx)
                if isinstance(v, list) and i < len(v): v = v[i]
                else: ok = False; break
        if ok: out.append(v)
    return out

def run(expr, data):
    vals = [data]
    for stage in [s.strip() for s in expr.split('|')]:
        m = re.fullmatch(r'select\(\.([A-Za-z_][A-Za-z0-9_]*)\s*==\s*"([^"]*)"\)', stage)
        if m:
            vals = [v for v in vals if isinstance(v, dict) and str(v.get(m.group(1))) == m.group(2)]
        elif stage == 'length':
            vals = [len(v) if isinstance(v, (list, dict)) else 0 for v in vals]
        elif stage == '.':
            pass
        else:
            vals = apply_path(vals, stage)
    return vals

data = json.load(sys.stdin)
for v in run(sys.argv[1], data):
    if isinstance(v, str): print(v)
    else: print(json.dumps(v))
PYEOF
fi

# jval FICHERO FILTRO → primer resultado del filtro (misma semántica jq/python)
jval() {
  local file=$1 filter=$2
  if [[ $HAVE_JQ == 1 ]]; then
    jq -r "$filter" < "$file" | head -n 1
  else
    python3 "$TMP/pyjq.py" "$filter" < "$file" | head -n 1
  fi
}

# ── REST: curl con código de estado + cuerpo en $RESP ────────────────────────
api() { # method path token json_body → imprime http_code; cuerpo en $RESP
  local method=$1 path=$2 token=$3 data=${4:-}
  local args=(-sS -X "$method" -H 'Content-Type: application/json' -o "$RESP" -w '%{http_code}' --max-time 20)
  [[ -n "$token" ]] && args+=(-H "Authorization: Bearer $token")
  [[ -n "$data" ]] && args+=(-d "$data")
  local out
  out=$(curl "${args[@]}" "$API$path" 2>/dev/null) || true
  echo "${out:-000}"
}

api_expect() { # method path token json_body expected_code descripcion
  local code
  code=$(api "$1" "$2" "$3" "$4")
  [[ "$code" == "$5" ]] || { echo "  respuesta ($code): $(cat "$RESP" 2>/dev/null)"; fail "$6 (esperado HTTP $5, recibido $code)"; }
}

# poll SEGUNDOS INTERVALO DESCRIPCION 'condición shell (eval)'
poll() {
  local timeout=$1 interval=$2 desc=$3 cond=$4 t=0
  while true; do
    if eval "$cond"; then return 0; fi
    (( t >= timeout )) && fail "timeout (${timeout}s) esperando: $desc"
    sleep "$interval"; t=$((t + interval))
  done
}

# ═════════════════════════════════════════════════════════════════════════════
# 1. Infraestructura y datos
# ═════════════════════════════════════════════════════════════════════════════
step "Infra y datos: make up + db-reset + db-migrate + db-seed"
make up >/dev/null
pass "infraestructura levantada (PostgreSQL + Caddy)"
make db-reset >/dev/null
pass "base de datos recreada"
MIGRATE_OUT=$(make db-migrate)
echo "$MIGRATE_OUT" | grep -q "migraciones aplicadas: 7" \
  || fail "se esperaban 7 migraciones aplicadas — salida: $(echo "$MIGRATE_OUT" | tail -n 1)"
pass "7 migraciones aplicadas"
make db-seed >/dev/null
pass "seed del mundo cargado"

# ═════════════════════════════════════════════════════════════════════════════
# 2. Builds
# ═════════════════════════════════════════════════════════════════════════════
step "Builds: engine (go) y gateway (tsc)"
(cd backend/engine && go build -o "$TMP/engine" ./cmd/engine)
pass "engine compilado ($TMP/engine)"
[[ -d backend/gateway/node_modules ]] || (cd backend/gateway && npm install >/dev/null 2>&1)
(cd backend/gateway && npm run build >/dev/null)
pass "gateway compilado (dist/)"

# ═════════════════════════════════════════════════════════════════════════════
# 3. Arranque de engine (TIME_RATIO=240) y gateway; salud por login
# ═════════════════════════════════════════════════════════════════════════════
step "Arranque: engine con TIME_RATIO=$TIME_RATIO y gateway en :$GATEWAY_PORT"
TIME_RATIO=$TIME_RATIO DATABASE_URL="$DB_URL" "$TMP/engine" > "$ENGINE_LOG" 2>&1 &
ENGINE_PID=$!
(cd backend/gateway && DATABASE_URL="$DB_URL" PORT="$GATEWAY_PORT" exec node dist/server.js) > "$GATEWAY_LOG" 2>&1 &
GATEWAY_PID=$!

AURORA_TOKEN=""
try_login() {
  local code
  code=$(api POST /auth/sessions "" '{"account_name":"Aurora Corp","secret":"aurora"}')
  if [[ "$code" == "201" ]]; then
    AURORA_TOKEN=$(jval "$RESP" '.data.token')
    AURORA_ID=$(jval "$RESP" '.data.account.id')
    return 0
  fi
  return 1
}
poll 60 2 "login de Aurora Corp (salud del gateway)" try_login
kill -0 "$ENGINE_PID" 2>/dev/null || fail "el engine murió al arrancar (ver $ENGINE_LOG)"
pass "gateway sano (login 201) y engine vivo — cuenta Aurora: $AURORA_ID"

# ═════════════════════════════════════════════════════════════════════════════
# 4. Ciclo CCRI determinista vía curl
# ═════════════════════════════════════════════════════════════════════════════
step "CCRI (a): concesión + mina de carbón sobre un yacimiento del Sur"
api_expect GET "/world/resource-deposits?product_id=$COAL" "$AURORA_TOKEN" "" 200 "GET resource-deposits"
DEP_LON=$(jval "$RESP" '.data[0].location.coordinates[0]')
DEP_LAT=$(jval "$RESP" '.data[0].location.coordinates[1]')
[[ -n "$DEP_LON" && -n "$DEP_LAT" && "$DEP_LON" != "null" ]] || fail "sin yacimiento de coal en la respuesta"

# Polígonos GeoJSON cuadrados centrados en el yacimiento (floats via awk)
square() { # lon lat half → GeoJSON Polygon
  awk -v lon="$1" -v lat="$2" -v h="$3" 'BEGIN {
    w=lon-h; e=lon+h; s=lat-h; n=lat+h;
    printf "{\"type\":\"Polygon\",\"coordinates\":[[[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f],[%.6f,%.6f]]]}", w,s,e,s,e,n,w,n,w,s }'
}
PARCEL=$(square "$DEP_LON" "$DEP_LAT" 0.01)     # 0.02° de lado
FOOTPRINT=$(square "$DEP_LON" "$DEP_LAT" 0.002)

api_expect POST /world/concessions "$AURORA_TOKEN" \
  "{\"region_id\":\"$REGION_SUR\",\"parcel\":$PARCEL}" 201 "POST concesión"
CONCESSION_ID=$(jval "$RESP" '.data.id')
pass "concesión $CONCESSION_ID sobre el yacimiento ($DEP_LON, $DEP_LAT)"

api_expect POST /world/buildings "$AURORA_TOKEN" \
  "{\"building_type_id\":\"$BT_COAL_MINE\",\"concession_id\":\"$CONCESSION_ID\",\"footprint\":$FOOTPRINT}" \
  201 "POST edificio coal_mine"
BUILDING_ID=$(jval "$RESP" '.data.id')
pass "mina $BUILDING_ID en construcción (14400 sim-s ≈ 60 s wall a ratio $TIME_RATIO)"

building_operational() {
  [[ $(api GET "/world/buildings/$BUILDING_ID" "$AURORA_TOKEN") == 200 ]] \
    && [[ $(jval "$RESP" '.data.status') == operational ]]
}
poll 120 3 "mina operational" building_operational
pass "mina operational"

step "CCRI (b): 3 lotes de mine_coal → stock_free >= 20"
api_expect POST "/world/buildings/$BUILDING_ID/production-batches" "$AURORA_TOKEN" \
  "{\"recipe_id\":\"$RECIPE_MINE_COAL\",\"batches_queued\":3}" 201 "POST production-batches"
pass "orden de 3 lotes encolada"

coal_free() { # imprime el stock_free de coal de Aurora (0 si aún no existe)
  local code
  code=$(api GET "/ledger/accounts?kind=stock_free&product_id=$COAL" "$AURORA_TOKEN")
  [[ "$code" == 200 ]] || { echo 0; return; }
  local v
  v=$(jval "$RESP" '.data[0].balance')
  [[ -n "$v" && "$v" != "null" ]] && echo "$v" || echo 0
}
poll 120 3 "stock_free de coal >= 20" '[[ $(coal_free) -ge 20 ]]'
pass "stock_free de coal: $(coal_free)"

step "CCRI (c): compra de camión truck_s entregado en el nodo de la mina"
api_expect GET "/logistics/network/nodes?region_id=$REGION_SUR" "$AURORA_TOKEN" "" 200 "GET network nodes"
MINE_NODE=$(jval "$RESP" ".data[] | select(.building_id==\"$BUILDING_ID\") | .id")
[[ -n "$MINE_NODE" && "$MINE_NODE" != "null" ]] || fail "no existe nodo logístico para la mina $BUILDING_ID"
api_expect POST /world/vehicles "$AURORA_TOKEN" \
  "{\"vehicle_type_id\":\"$VT_TRUCK_S\",\"delivery_node_id\":\"$MINE_NODE\"}" 201 "POST vehículo"
VEHICLE_ID=$(jval "$RESP" '.data.id')
pass "camión $VEHICLE_ID idle en el nodo de la mina $MINE_NODE"

step "CCRI (d): venta in situ de 10 coal → sorteo → contrato settled (fill 10000)"
api_expect POST /contracts/publications "$AURORA_TOKEN" \
  "{\"kind\":\"sell\",\"product_id\":\"$COAL\",\"quantity_total\":\"10\",\"unit_price\":\"$COAL_BASE_PRICE\",\"min_lot\":\"1\",\"origin_node_id\":\"$MINE_NODE\",\"delivery_sim_seconds\":86400}" \
  201 "POST publicación sell"
SELL_PUB=$(jval "$RESP" '.data.id')
pass "publicación sell $SELL_PUB (10 coal @ $COAL_BASE_PRICE)"

api_expect POST /auth/sessions "" \
  '{"account_name":"Bot Fundición Este","secret":"botfundicioneste"}' 201 "login Bot Fundición Este"
BOT_TOKEN=$(jval "$RESP" '.data.token')

api_expect POST "/contracts/publications/$SELL_PUB/acceptances" "$BOT_TOKEN" \
  '{"quantity":"10"}' 201 "POST aceptación del bot"
ACC1=$(jval "$RESP" '.data.id')
pass "aceptación $ACC1 pendiente de sorteo (ventana 45 s wall)"

acc_served() {
  [[ $(api GET "/contracts/acceptances/$ACC1" "$BOT_TOKEN") == 200 ]] \
    && [[ $(jval "$RESP" '.data.status') == served ]]
}
poll 90 3 "aceptación served tras el sorteo" acc_served
CONTRACT1=$(jval "$RESP" '.data.contract_id')
[[ -n "$CONTRACT1" && "$CONTRACT1" != "null" ]] || fail "aceptación served sin contract_id"
pass "aceptación served → contrato $CONTRACT1"

api_expect GET "/contracts/contracts/$CONTRACT1" "$AURORA_TOKEN" "" 200 "GET contrato in situ"
[[ $(jval "$RESP" '.data.status') == settled ]] || fail "el contrato in situ no está settled: $(jval "$RESP" '.data.status')"
[[ $(jval "$RESP" '.data.fill_bp') == 10000 ]] || fail "fill_bp != 10000: $(jval "$RESP" '.data.fill_bp')"
pass "contrato in situ settled con fill_bp 10000"

# Asserts psql de la venta in situ
MIRROR_LEFT=$(db "SELECT count(*) FROM ledger.accounts WHERE reference_id = '$CONTRACT1' AND balance <> 0")
[[ "$MIRROR_LEFT" == 0 ]] || fail "cuentas espejo del contrato in situ con saldo != 0: $MIRROR_LEFT"
pass "cuentas espejo del contrato a saldo 0"

# El cash de Aurora sube en pago (10×precio) + garantía devuelta (10% del valor);
# se verifica por partidas del asiento 'delivery_settlement' (inmune a cargos
# diarios no relacionados que pudieran cruzar la ventana de medición).
EXPECTED1=$((10 * COAL_BASE_PRICE + (10 * COAL_BASE_PRICE * 10) / 100))
CREDIT1=$(db "SELECT COALESCE(SUM(e.amount),0) FROM ledger.entries e
              JOIN ledger.transactions t ON t.id = e.transaction_id
             WHERE t.kind = 'delivery_settlement' AND t.reference_id = '$CONTRACT1'
               AND e.account_id = '$AURORA_CASH'")
[[ "$CREDIT1" == "$EXPECTED1" ]] || fail "liquidación in situ a caja de Aurora: esperado $EXPECTED1, recibido $CREDIT1"
pass "caja de Aurora +$CREDIT1 (pago + garantía devuelta)"

BUYER_STOCK=$(db "SELECT COALESCE(SUM(balance),0) FROM ledger.accounts
                  WHERE kind = 'stock_free' AND owner_account_id = '$ACC_BOT_FUNDICION'
                    AND product_id = '$COAL' AND warehouse_building_id = '$BUILDING_ID'")
[[ "$BUYER_STOCK" == 10 ]] || fail "stock_free del comprador en el almacén de la mina: esperado 10, hay $BUYER_STOCK"
pass "stock_free del comprador en la mina = 10"

step "CCRI (e): entrega física a Ferrópolis (compra del balancer + auto-despacho)"
EMA_BEFORE=$(db "SELECT supply_ema FROM world.city_demand WHERE city_id = '$CITY_FERROPOLIS' AND product_id = '$COAL'")

CITY_PUB=""
find_city_buy() {
  [[ $(api GET "/contracts/board?kind=buy&product_id=$COAL&limit=50" "$AURORA_TOKEN") == 200 ]] || return 1
  CITY_PUB=$(jval "$RESP" ".data[] | select(.publisher_account_id==\"$ACC_FERROPOLIS\") | .id")
  [[ -n "$CITY_PUB" && "$CITY_PUB" != "null" ]]
}
poll 150 5 "publicación buy de coal de Ferrópolis (balancer, pasada cada 60 s)" find_city_buy
api_expect GET "/contracts/publications/$CITY_PUB" "$AURORA_TOKEN" "" 200 "GET publicación de ciudad"
CITY_MIN_LOT=$(jval "$RESP" '.data.min_lot')
CITY_REMAINING=$(jval "$RESP" '.data.quantity_remaining')
CITY_PRICE=$(jval "$RESP" '.data.unit_price')
pass "publicación de Ferrópolis $CITY_PUB (remaining $CITY_REMAINING, min_lot $CITY_MIN_LOT, precio $CITY_PRICE)"

# qty a aceptar: al menos min_lot y como mucho lo restante / el stock libre.
QTY2=$CITY_MIN_LOT
(( QTY2 < 10 )) && QTY2=10
(( QTY2 > CITY_REMAINING )) && QTY2=$CITY_REMAINING
poll 90 3 "stock_free de coal >= $QTY2 para aceptar" '[[ $(coal_free) -ge $QTY2 ]]'

api_expect POST "/contracts/publications/$CITY_PUB/acceptances" "$AURORA_TOKEN" \
  "{\"quantity\":\"$QTY2\"}" 201 "POST aceptación de Aurora (vendedora)"
ACC2=$(jval "$RESP" '.data.id')
pass "aceptación $ACC2 de $QTY2 coal"

acc2_served() {
  [[ $(api GET "/contracts/acceptances/$ACC2" "$AURORA_TOKEN") == 200 ]] \
    && [[ $(jval "$RESP" '.data.status') == served ]]
}
poll 120 3 "aceptación de Aurora served" acc2_served
CONTRACT2=$(jval "$RESP" '.data.contract_id')
[[ -n "$CONTRACT2" && "$CONTRACT2" != "null" ]] || fail "aceptación served sin contract_id"

api_expect GET "/contracts/contracts/$CONTRACT2" "$AURORA_TOKEN" "" 200 "GET contrato de entrega"
[[ $(jval "$RESP" '.data.status') == active ]] || fail "contrato de entrega no activo: $(jval "$RESP" '.data.status')"
[[ $(jval "$RESP" '.data.origin_node_id') == "$MINE_NODE" ]] \
  || fail "origin del contrato != nodo de la mina: $(jval "$RESP" '.data.origin_node_id')"
pass "contrato $CONTRACT2 activo con origin = nodo de la mina"

veh_status() {
  [[ $(api GET "/world/vehicles/$VEHICLE_ID" "$AURORA_TOKEN") == 200 ]] || { echo unknown; return; }
  jval "$RESP" '.data.status'
}
poll 90 2 "camión in_transit (auto-despacho del motor)" '[[ $(veh_status) == in_transit ]]'
pass "camión in_transit"
poll 150 3 "camión de vuelta a idle (llegada)" '[[ $(veh_status) == idle ]]'
pass "camión llegó (idle en destino)"

contract2_settled() {
  [[ $(api GET "/contracts/contracts/$CONTRACT2" "$AURORA_TOKEN") == 200 ]] \
    && [[ $(jval "$RESP" '.data.status') == settled ]]
}
poll 60 3 "contrato de entrega settled" contract2_settled
api_expect GET "/contracts/contracts/$CONTRACT2/deliveries" "$AURORA_TOKEN" "" 200 "GET deliveries"
DELIV_LEN=$(jval "$RESP" '.data | length')
DELIV_ON_TIME=$(jval "$RESP" '.data[0].on_time')
[[ "$DELIV_LEN" -ge 1 && "$DELIV_ON_TIME" == "true" ]] \
  || fail "sin entrega on_time en el contrato $CONTRACT2 (entregas=$DELIV_LEN, on_time=$DELIV_ON_TIME)"
pass "contrato settled con >=1 entrega on_time"

# Asserts psql de la entrega física
EMA_GREW=$(db "SELECT (supply_ema > ($EMA_BEFORE)::double precision)
               FROM world.city_demand WHERE city_id = '$CITY_FERROPOLIS' AND product_id = '$COAL'")
[[ "$EMA_GREW" == t ]] || fail "supply_ema de (Ferrópolis, coal) no creció (antes $EMA_BEFORE)"
pass "supply_ema de (Ferrópolis, coal) creció (antes $EMA_BEFORE)"

DELIV_COUNT=$(db "SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id = '$CONTRACT2' AND on_time")
[[ "$DELIV_COUNT" -ge 1 ]] || fail "entrega no registrada en ledger.contract_deliveries"
pass "entrega registrada en ledger.contract_deliveries"

TOTAL2=$((QTY2 * CITY_PRICE))
EXPECTED2=$((TOTAL2 + (TOTAL2 * 10) / 100))
CREDIT2=$(db "SELECT COALESCE(SUM(e.amount),0) FROM ledger.entries e
              JOIN ledger.transactions t ON t.id = e.transaction_id
             WHERE t.kind = 'delivery_settlement' AND t.reference_id = '$CONTRACT2'
               AND e.account_id = '$AURORA_CASH'")
[[ "$CREDIT2" == "$EXPECTED2" ]] || fail "pago de la entrega: esperado $EXPECTED2 a caja de Aurora, recibido $CREDIT2"
pass "pago recibido: caja de Aurora +$CREDIT2 (pago + garantía devuelta)"

# ═════════════════════════════════════════════════════════════════════════════
# 5. Bots orgánicos por la API pública
# ═════════════════════════════════════════════════════════════════════════════
step "Bots: make bots-run ~90 s → actividad orgánica"
[[ -d backend/bots/node_modules ]] || (cd backend/bots && npm install >/dev/null 2>&1)
setsid make bots-run > "$BOTS_LOG" 2>&1 &
BOTS_PID=$!
sleep 90
bot_pubs() {
  db "SELECT count(*) FROM ledger.publications p
      JOIN auth.accounts a ON a.id = p.publisher_account_id WHERE a.kind = 'bot'"
}
# 90 s cubren el ciclo del transformador (concesión→edificio→60 s de obra→buy);
# margen extra por el jitter de los ticks de los bots.
poll 60 5 "al menos 1 publicación creada por un bot" '[[ $(bot_pubs) -ge 1 ]]'
BOT_PUBS=$(bot_pubs)
kill -TERM -- "-$BOTS_PID" 2>/dev/null || true
sleep 1
kill -KILL -- "-$BOTS_PID" 2>/dev/null || true
BOTS_PID=""
pass "publicaciones de bots: $BOT_PUBS"

if grep -q '"statusCode":5[0-9][0-9]' "$GATEWAY_LOG"; then
  grep -m 3 '"statusCode":5[0-9][0-9]' "$GATEWAY_LOG" >&2
  fail "el gateway registró respuestas 5xx durante la ejecución"
fi
pass "sin errores 5xx en el log del gateway"

# ═════════════════════════════════════════════════════════════════════════════
# 6. Invariantes globales del ledger (exactas, vía psql)
# ═════════════════════════════════════════════════════════════════════════════
step "Invariantes globales del ledger"
UNBALANCED=$(db "SELECT count(*) FROM (
    SELECT e.transaction_id, COALESCE(a.product_id::text, 'MONEY') AS asset
      FROM ledger.entries e JOIN ledger.accounts a ON a.id = e.account_id
     GROUP BY 1, 2 HAVING SUM(e.amount) <> 0) x")
[[ "$UNBALANCED" == 0 ]] || fail "transacciones desbalanceadas por activo: $UNBALANCED"
pass "(a) cero transacciones desbalanceadas (por dinero y por producto)"

NEGATIVE=$(db "SELECT count(*) FROM ledger.accounts WHERE kind <> 'emission' AND balance < 0")
[[ "$NEGATIVE" == 0 ]] || fail "cuentas no-emission con saldo negativo: $NEGATIVE"
pass "(b) ninguna cuenta no-emission en negativo"

MONEY_OK=$(db "SELECT (SELECT -balance FROM ledger.accounts WHERE kind = 'emission' AND product_id IS NULL)
                    = (SELECT COALESCE(SUM(balance),0) FROM ledger.accounts
                        WHERE kind <> 'emission' AND product_id IS NULL)")
[[ "$MONEY_OK" == t ]] || fail "identidad monetaria rota: -(emission) != suma de cuentas monetarias"
pass "(c) identidad monetaria: -(emisión) = suma de cuentas monetarias"

PRODUCT_BAD=$(db "SELECT count(*) FROM (
    SELECT product_id FROM ledger.accounts WHERE product_id IS NOT NULL
     GROUP BY product_id HAVING SUM(balance) <> 0) x")
[[ "$PRODUCT_BAD" == 0 ]] || fail "identidad de stock rota en $PRODUCT_BAD producto(s)"
pass "(d) por producto: -(génesis) = stock_free + stock_reserved + custody"

SIM_NOW=$(db "SELECT sim_seconds FROM world.sim_clock WHERE id = 1")
[[ "$SIM_NOW" -gt 0 ]] || fail "el sim_clock no avanzó (sim_seconds=$SIM_NOW)"
pass "(e) sim_clock avanzó: sim_seconds=$SIM_NOW"

# ═════════════════════════════════════════════════════════════════════════════
# 7. WebSocket: hello + join corp → snapshot con contratos
# ═════════════════════════════════════════════════════════════════════════════
step "WS: hello + join corp:<Aurora> → snapshot con >= 1 contrato"
cat > "$TMP/ws_check.mjs" <<'WSEOF'
// Comprobación WS (specs/ws-protocol.md) con el WebSocket global de Node 22.
const [url, token, accountId] = process.argv.slice(2);
const ws = new WebSocket(url);
const timer = setTimeout(() => { console.error('WS: timeout'); process.exit(1); }, 15000);
ws.onopen = () => ws.send(JSON.stringify({ type: 'hello', token }));
ws.onerror = (e) => { console.error('WS: error', e?.message ?? ''); process.exit(1); };
ws.onmessage = (ev) => {
  const frame = JSON.parse(ev.data);
  if (frame.type === 'hello_ok') {
    if (frame.account?.id !== accountId) { console.error('WS: hello_ok de otra cuenta'); process.exit(1); }
    ws.send(JSON.stringify({ type: 'join', room: `corp:${accountId}` }));
  } else if (frame.type === 'snapshot') {
    const n = (frame.data?.contracts ?? []).length;
    if (frame.seq !== 0 || n < 1) { console.error(`WS: snapshot inválido (seq=${frame.seq}, contratos=${n})`); process.exit(1); }
    console.log(`snapshot corp OK: ${n} contrato(s)`);
    clearTimeout(timer); ws.close(); process.exit(0);
  } else if (frame.type === 'error') {
    console.error('WS: frame de error', JSON.stringify(frame)); process.exit(1);
  }
};
WSEOF
node "$TMP/ws_check.mjs" "$WS_URL" "$AURORA_TOKEN" "$AURORA_ID" || fail "comprobación WebSocket"
pass "WS hello + join corp + snapshot con contratos"

# ═════════════════════════════════════════════════════════════════════════════
# 8. Resumen (la limpieza de procesos la hace el trap)
# ═════════════════════════════════════════════════════════════════════════════
step "Resumen"
SETTLED=$(db "SELECT count(*) FROM ledger.contracts WHERE status = 'settled'")
DELIVERIES=$(db "SELECT count(*) FROM ledger.contract_deliveries")
ELAPSED=$(( $(date +%s) - VERIFY_T0 ))
echo ""
echo "==============================================================="
echo " VERIFY PASS  (${ELAPSED}s)"
echo "   contratos liquidados:      $SETTLED"
echo "   entregas registradas:      $DELIVERIES"
echo "   publicaciones de bots:     $BOT_PUBS"
echo "   sim_seconds finales:       $SIM_NOW"
echo "---------------------------------------------------------------"
echo " La base de datos queda poblada por el verify."
echo " Para limpiarla: make db-reset && make db-migrate && make db-seed"
echo "==============================================================="
