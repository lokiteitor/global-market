-- =============================================================================
-- Imperio Industrial — queries sqlc del subpaquete world/land (ADR-020).
-- Suelo: concesiones renovables del sistema y su mercado secundario de
-- traspasos (GDD 11.1). Es el lado de ESCRITURA del contexto world para el
-- suelo: cada operación que mueve valor (canon, traspaso) corre en una
-- transacción SERIALIZABLE con outbox.Emit en la misma tx.
--
-- La frontera de módulo es de código Go, no de esquema: estas queries leen y
-- escriben world.* (land_concessions, concession_transfers, regions) y asientan
-- en ledger.* (canon/transfer como sink), igual que hizo internal/contracts.
--
-- Geometrías (parcel/bounds): SRID 0 planar, metros de mundo (ADR-019). La
-- ENTRADA llega como GeoJSON del contrato y se proyecta con
-- ST_SetSRID(ST_GeomFromGeoJSON(...), 0) — ST_GeomFromGeoJSON asume 4326, así
-- que se re-etiqueta a 0 sin transformar coordenadas. La SALIDA se proyecta con
-- ST_AsGeoJSON(...)::text, que los handlers embeben en el schema GeoPolygon.
--
-- La sección "Soporte de ledger" es COMPARTIDA con world/buildings (build_cost,
-- upgrade_cost): la frontera entre subpaquetes es de código Go, no de fichero
-- SQL — ambos comparten el paquete sqlcgen del contexto.
-- =============================================================================

-- ─── Concesiones: lectura ─────────────────────────────────────────────────────

-- ListConcessions devuelve las concesiones de un titular (SOLO propias) con
-- filtros opcionales por estado y región, y paginación keyset por id. parcel
-- sale como GeoJSON plano (SRID 0).
-- name: ListConcessions :many
SELECT id, region_id, holder_account_id,
       ST_AsGeoJSON(parcel)::text AS parcel,
       canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim
FROM world.land_concessions
WHERE holder_account_id = sqlc.arg(holder_account_id)
  AND (sqlc.narg(status)::world.concession_status IS NULL OR status = sqlc.narg(status)::world.concession_status)
  AND (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(after_id)::uuid IS NULL OR id > sqlc.narg(after_id)::uuid)
ORDER BY id
LIMIT sqlc.arg(page_limit);

-- GetConcession devuelve una concesión por id (la autorización por titular la
-- aplica la capa de servicio).
-- name: GetConcession :one
SELECT id, region_id, holder_account_id,
       ST_AsGeoJSON(parcel)::text AS parcel,
       canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim
FROM world.land_concessions
WHERE id = sqlc.arg(id);

-- GetConcessionForUpdate bloquea la fila (SELECT FOR UPDATE) para renovación y
-- traspaso: las validaciones de titular/estado y el cobro se deciden bajo lock.
-- name: GetConcessionForUpdate :one
SELECT id, region_id, holder_account_id,
       ST_AsGeoJSON(parcel)::text AS parcel,
       canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim
FROM world.land_concessions
WHERE id = sqlc.arg(id)
FOR UPDATE;

-- ─── Concesiones: emplazamiento ───────────────────────────────────────────────

-- RegionParcelWithin comprueba, en una sola consulta, que la región existe
-- (ErrNoRows si no) y que la parcela solicitada cae DENTRO de sus límites
-- (ST_Within); devuelve además el canon_base regional, base del canon del
-- periodo.
-- name: RegionParcelWithin :one
SELECT ST_Within(ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(parcel_geojson)::text), 0), bounds)::boolean AS within,
       canon_base
FROM world.regions
WHERE id = sqlc.arg(region_id);

-- ConcessionParcelOverlaps indica si la parcela solicitada se solapa
-- (ST_Intersects) con alguna concesión ACTIVA de la región (→ 409).
-- name: ConcessionParcelOverlaps :one
SELECT EXISTS (
    SELECT 1 FROM world.land_concessions
    WHERE region_id = sqlc.arg(region_id)
      AND status = 'active'
      AND ST_Intersects(parcel, ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(parcel_geojson)::text), 0))
)::boolean AS overlaps;

-- ─── Concesiones: escritura ───────────────────────────────────────────────────

-- InsertConcession crea la concesión activa (plazo de referencia 90 días). El
-- canon ya se cobró al sink en la misma transacción. parcel entra como GeoJSON
-- plano; sale proyectada.
-- name: InsertConcession :one
INSERT INTO world.land_concessions (
    id, region_id, holder_account_id, parcel, canon_amount,
    period_sim_days, expires_at_sim, status, granted_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(region_id), sqlc.arg(holder_account_id),
    ST_SetSRID(ST_GeomFromGeoJSON(sqlc.arg(parcel_geojson)::text), 0),
    sqlc.arg(canon_amount), sqlc.arg(period_sim_days),
    sqlc.arg(expires_at_sim), 'active', sqlc.arg(granted_at_sim))
RETURNING id, region_id, holder_account_id,
          ST_AsGeoJSON(parcel)::text AS parcel,
          canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim;

-- RenewConcession extiende el vencimiento otro periodo (expires += delta en
-- sim-time). El canon vigente ya se cobró al sink en la misma transacción.
-- name: RenewConcession :one
UPDATE world.land_concessions
   SET expires_at_sim = expires_at_sim + sqlc.arg(extend_sim_seconds)::bigint,
       updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING id, region_id, holder_account_id,
          ST_AsGeoJSON(parcel)::text AS parcel,
          canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim;

-- SetConcessionHolder cambia el titular de la concesión (traspaso). El pago
-- comprador→vendedor y la tasa comprador→sink ya se asentaron en la misma tx.
-- name: SetConcessionHolder :one
UPDATE world.land_concessions
   SET holder_account_id = sqlc.arg(holder_account_id), updated_at = now()
 WHERE id = sqlc.arg(id)
RETURNING id, region_id, holder_account_id,
          ST_AsGeoJSON(parcel)::text AS parcel,
          canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim;

-- InsertConcessionTransfer registra el traspaso ejecutado (mercado secundario,
-- con la tasa del sistema).
-- name: InsertConcessionTransfer :one
INSERT INTO world.concession_transfers (
    id, concession_id, from_account_id, to_account_id, price, system_fee, occurred_at_sim)
VALUES (
    sqlc.arg(id), sqlc.arg(concession_id), sqlc.arg(from_account_id),
    sqlc.arg(to_account_id), sqlc.arg(price), sqlc.arg(system_fee), sqlc.arg(occurred_at_sim))
RETURNING id, concession_id, from_account_id, to_account_id, price, system_fee, occurred_at_sim;

-- ═════════════════════════════════════════════════════════════════════════════
-- Soporte de ledger del contexto world (COMPARTIDO con world/buildings).
--
-- world consume el substrato de valor (ledger) EXACTAMENTE como lo hizo
-- internal/contracts: queries sqlc propias del contexto contra las tablas
-- ledger.* (la frontera de módulo es de código Go, no de esquema). Aquí solo se
-- ASIENTA como SINK (canon/maintenance/wage) y se hacen traspasos cash→cash; las
-- invariantes (no-negatividad, doble entrada diferida, inmutabilidad) las
-- garantizan los triggers de 0004_ledger — nunca se recalculan saldos.
-- ═════════════════════════════════════════════════════════════════════════════

-- GetCashAccount devuelve la única caja de una corporación (unicidad parcial
-- ux_accounts_cash); ErrNoRows si aún no tiene caja (→ fondos insuficientes).
-- name: GetCashAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'cash' AND owner_account_id = sqlc.arg(owner_account_id);

-- GetSinkAccount devuelve la cuenta sink del banco central (destino de canon,
-- coste de construcción/mejora, salarios y tasa de traspaso — GDD 5.5).
-- name: GetSinkAccount :one
SELECT id, balance FROM ledger.accounts
WHERE kind = 'sink' ORDER BY id LIMIT 1;

-- AccountExists comprueba la existencia de una cuenta de auth (destinatario de
-- un traspaso de concesión).
-- name: AccountExists :one
SELECT EXISTS (SELECT 1 FROM auth.accounts WHERE id = sqlc.arg(id))::boolean AS present;

-- InsertLedgerTransaction inserta la cabecera de un asiento (inmutable). El id
-- (UUIDv7) lo genera la aplicación (ADR-018).
-- name: InsertLedgerTransaction :exec
INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
VALUES (sqlc.arg(id), sqlc.arg(kind), sqlc.arg(sim_time_at),
        sqlc.narg(reference_id), sqlc.narg(description));

-- InsertLedgerEntry inserta una partida de doble entrada; los triggers aplican
-- saldo, no-negatividad y (diferido) el balance por activo del asiento.
-- name: InsertLedgerEntry :exec
INSERT INTO ledger.entries (id, transaction_id, account_id, amount)
VALUES (sqlc.arg(id), sqlc.arg(transaction_id), sqlc.arg(account_id), sqlc.arg(amount));
