-- =============================================================================
-- Imperio Industrial — queries sqlc del módulo market (ADR-020).
-- Velas OHLC del historial de precios (GDD 5.2), construidas EXCLUSIVAMENTE a
-- partir de contratos liquidados (analytics.market_ohlc, 0005_analytics). El
-- agregador hace UPSERT dentro de la transacción del consumidor del outbox; el
-- lado de lectura sirve la serie tal cual está almacenada, sin re-agregar.
-- =============================================================================

-- UpsertOhlcCandle asienta una entrega liquidada en la vela de su bucket
-- (product_id, region_id, bucket_start_sim). La primera entrega del bucket abre
-- la vela (open=high=low=close=unit_price, volume=delivered, count=1); las
-- siguientes extienden high/low, fijan close al último precio procesado y
-- acumulan volumen y contratos. El orden de proceso lo garantiza el consumidor
-- (por seq): close queda con el unit_price del último evento del lote.
-- name: UpsertOhlcCandle :exec
INSERT INTO analytics.market_ohlc (
    product_id, region_id, bucket_start_sim, bucket_sim_secs,
    open_price, high_price, low_price, close_price, volume, contract_count)
VALUES (
    sqlc.arg(product_id), sqlc.arg(region_id), sqlc.arg(bucket_start_sim), sqlc.arg(bucket_sim_secs),
    sqlc.arg(unit_price), sqlc.arg(unit_price), sqlc.arg(unit_price), sqlc.arg(unit_price),
    sqlc.arg(quantity_delivered), 1)
ON CONFLICT (product_id, region_id, bucket_start_sim) DO UPDATE SET
    high_price     = GREATEST(analytics.market_ohlc.high_price, EXCLUDED.high_price),
    low_price      = LEAST(analytics.market_ohlc.low_price, EXCLUDED.low_price),
    close_price    = EXCLUDED.close_price,
    volume         = analytics.market_ohlc.volume + EXCLUDED.volume,
    contract_count = analytics.market_ohlc.contract_count + 1;

-- ListOhlcCandles devuelve la serie OHLC de un producto (contrato
-- GET /market/ohlc), con filtros opcionales por región y rango de sim-time
-- sobre bucket_start_sim, en orden cronológico. Cada vela expone su propio
-- bucket_sim_secs (la granularidad realmente almacenada): el lado de lectura
-- NO re-agrega a otro tamaño de bucket.
-- name: ListOhlcCandles :many
SELECT product_id, region_id, bucket_start_sim, bucket_sim_secs,
       open_price, high_price, low_price, close_price, volume, contract_count
FROM analytics.market_ohlc
WHERE product_id = sqlc.arg(product_id)
  AND (sqlc.narg(region_id)::uuid IS NULL OR region_id = sqlc.narg(region_id)::uuid)
  AND (sqlc.narg(from_sim)::bigint IS NULL OR bucket_start_sim >= sqlc.narg(from_sim)::bigint)
  AND (sqlc.narg(to_sim)::bigint IS NULL OR bucket_start_sim <= sqlc.narg(to_sim)::bigint)
ORDER BY bucket_start_sim, region_id
LIMIT sqlc.arg(page_limit);
