// Package market implementa el historial de precios del mercado (GDD 5.2):
// velas OHLC construidas EXCLUSIVAMENTE a partir de contratos efectivamente
// liquidados (contract.settled con entrega > 0), nunca de órdenes vivas, por
// producto y región de destino — la referencia de precio visible para todos
// los jugadores.
//
// El módulo tiene dos piezas:
//
//   - Aggregator: consumidor del outbox transaccional ("ohlc_aggregator",
//     internal/outbox) suscrito a contract.settled. Hace UPSERT de la vela en
//     analytics.market_ohlc (0005_analytics) DENTRO de la transacción del
//     lote del consumidor, de modo que el avance del cursor y la vela se
//     confirman juntos: exactly-once por consumidor, reejecutar no duplica
//     volumen ni contratos.
//
//   - Service + Handlers: GET /market/ohlc del contrato OpenAPI. Lectura pura
//     de analytics.market_ohlc; no re-agrega. Si el bucket_sim_secs pedido no
//     coincide con el almacenado (II_OHLC_BUCKET_SIM_SECONDS), la serie se
//     sirve en la granularidad almacenada y cada vela declara la suya en el
//     campo bucket_sim_secs, tal cual está el dato.
//
// El acceso a datos es código generado por sqlc (ADR-020: solo codegen de
// queries, nunca del esquema) en el subpaquete sqlcgen, a partir de
// queries/market.sql y del esquema real de db/migrations. La generación la
// dispara la directiva go:generate compartida del backend
// (internal/ledger/generate.go → sqlc.yaml).
package market
