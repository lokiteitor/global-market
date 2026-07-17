package market_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// settledPayload replica el payload documentado de contract.settled (contrato
// entre agentes): dinero y stock como string, jamás float. Se declara en el
// test (paquete externo) para emitir eventos sintéticos sin acoplarse al
// struct interno del módulo.
type settledPayload struct {
	ContractID          string `json:"contract_id"`
	ProductID           string `json:"product_id"`
	DestinationRegionID string `json:"destination_region_id"`
	UnitPrice           string `json:"unit_price"`
	QuantityAgreed      string `json:"quantity_agreed"`
	QuantityDelivered   string `json:"quantity_delivered"`
	FillBP              int    `json:"fill_bp"`
	SettledAtSim        int64  `json:"settled_at_sim"`
	Status              string `json:"status"`
}

// TestMarketIntegration ejercita el módulo market contra una BD real con el
// esquema migrado y el seed del Incremento 1: el agregador construye velas
// OHLC a partir de eventos contract.settled sintéticos emitidos por el outbox
// (open/high/low/close/volume/count), ignora los fallidos y las entregas
// nulas, y es exactly-once (un lote revertido por un fallo del handler no
// duplica la vela; reejecutar el consumer no añade nada). Después valida el
// endpoint GET /market/ohlc contra el servicio real vía httptest.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica el
// servidor: el test crea una BD EFÍMERA propia (el rol debe tener CREATEDB),
// le aplica las migraciones reales y la destruye al terminar.
func TestMarketIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	iron := productID(t, ctx, pool, "iron_ore")
	coal := productID(t, ctx, pool, "coal")
	region := regionID(t, ctx, pool, seed.RegionName)

	const bucket int64 = 3600

	// Eventos sintéticos. Los tres primeros del par (iron, region) caen en el
	// bucket 0; el cuarto es un fallo (se ignora) y el quinto una entrega nula
	// (se ignora). F abre otra vela del mismo par en el bucket 3600; G una vela
	// del par (coal, region) en el bucket 0.
	events := []settledPayload{
		settled(iron, region, 1000, 100, 100),       // A: bucket 0
		settled(iron, region, 1500, 50, 200),        // B: high 1500
		settled(iron, region, 800, 30, 300),         // C: low 800, close 800
		failed(iron, region, 2000, 40, 400),         // D: ignorado (failed)
		settled(iron, region, 5000, 0, 500),         // E: ignorado (delivered 0)
		settled(iron, region, 1200, 20, bucket+100), // F: bucket 3600
		settled(coal, region, 300, 10, 150),         // G: bucket 0, otro producto
	}
	for _, ev := range events {
		mustEmit(t, ctx, pool, ev.SettledAtSim, "contract", uuid.Must(uuid.NewV7()), "contract.settled", ev)
	}
	settledSeqs := seqsOf(t, ctx, pool, "contract.settled")
	if len(settledSeqs) != len(events) {
		t.Fatalf("eventos contract.settled: %d, esperado %d", len(settledSeqs), len(events))
	}
	failSeq := settledSeqs[2] // evento C: fuerza un rollback del lote que lo contiene

	reg := prometheus.NewRegistry()
	metrics := market.NewMetrics(reg)
	agg, err := market.NewAggregator(market.Options{OhlcBucketSimSeconds: bucket, QueryTimeout: 10 * time.Second}, metrics, logger)
	if err != nil {
		t.Fatalf("NewAggregator: %v", err)
	}

	// ── Agregación con un fallo inyectado (exactly-once ante rollback) ──────
	t.Run("agrega velas e ignora failed/entrega-nula; exactly-once ante fallo", func(t *testing.T) {
		var failed atomic.Bool
		var failAttempts atomic.Int64
		handler := func(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
			// El efecto (UPSERT) se aplica DENTRO de la tx del lote; si luego
			// inyectamos el fallo, el rollback debe revertirlo junto con el
			// cursor: la vela no puede quedar doblemente contada.
			if err := agg.Handle(ctx, tx, ev); err != nil {
				return err
			}
			if ev.Seq == failSeq {
				failAttempts.Add(1)
				if failed.CompareAndSwap(false, true) {
					return errors.New("fallo transitorio inyectado")
				}
			}
			return nil
		}

		consumer := agg.NewConsumer(pool, outbox.WithBatchSize(3),
			outbox.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, handler) }()

		// El cursor de ohlc_aggregator debe alcanzar el último seq emitido.
		maxSeq := settledSeqs[len(settledSeqs)-1]
		waitFor(t, 30*time.Second, "cursor del agregador al día", func() bool {
			return cursorOf(t, ctx, pool, market.ConsumerName) >= maxSeq
		})
		stop()
		if err := <-done; err != nil {
			t.Fatalf("Run devolvió error en el apagado: %v", err)
		}
		if got := failAttempts.Load(); got != 2 {
			t.Fatalf("intentos sobre el evento fallido: %d, esperado 2 (fallo + reintento)", got)
		}

		// Vela (iron, region, bucket 0): open=1000, high=1500, low=800,
		// close=800 (último por seq), volume=180, count=3. El evento fallido y
		// la entrega nula NO cuentan.
		c := candleAt(t, ctx, pool, iron, region, 0)
		if c.open != 1000 || c.high != 1500 || c.low != 800 || c.close != 800 ||
			c.volume != 180 || c.count != 3 || c.bucketSecs != bucket {
			t.Fatalf("vela (iron, bucket 0) inesperada: %+v", c)
		}
		// Vela (iron, region, bucket 3600): una sola entrega.
		f := candleAt(t, ctx, pool, iron, region, bucket)
		if f.open != 1200 || f.high != 1200 || f.low != 1200 || f.close != 1200 ||
			f.volume != 20 || f.count != 1 {
			t.Fatalf("vela (iron, bucket 3600) inesperada: %+v", f)
		}
		// Vela (coal, region, bucket 0): separada por producto.
		g := candleAt(t, ctx, pool, coal, region, 0)
		if g.open != 300 || g.high != 300 || g.low != 300 || g.close != 300 ||
			g.volume != 10 || g.count != 1 {
			t.Fatalf("vela (coal, bucket 0) inesperada: %+v", g)
		}
		// Exactamente 3 velas persistidas (D y E no crean ninguna).
		if got := countRows(t, ctx, pool, `SELECT count(*) FROM analytics.market_ohlc`); got != 3 {
			t.Fatalf("velas persistidas: %d, esperado 3", got)
		}
		// Métrica: al menos las 5 entregas efectivas asentadas (A,B,C,F,G); el
		// lote reintentado puede sumar más (cuenta intentos).
		if v := counterValue(t, reg, "ii_ohlc_candles_upserted_total"); v < 5 {
			t.Fatalf("ii_ohlc_candles_upserted_total = %v, esperado >= 5", v)
		}
	})

	// ── Reejecutar el consumer no duplica (exactly-once por cursor) ─────────
	t.Run("reejecutar el consumer no duplica velas", func(t *testing.T) {
		before := candleAt(t, ctx, pool, iron, region, 0)
		consumer := agg.NewConsumer(pool, outbox.WithBatchSize(3),
			outbox.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, agg.Handle) }()
		time.Sleep(300 * time.Millisecond) // margen para un hipotético reproceso
		stop()
		if err := <-done; err != nil {
			t.Fatalf("Run (reejecución) error: %v", err)
		}
		after := candleAt(t, ctx, pool, iron, region, 0)
		if before != after {
			t.Fatalf("la vela cambió al reejecutar el consumer: %+v → %+v", before, after)
		}
		if got := countRows(t, ctx, pool, `SELECT count(*) FROM analytics.market_ohlc`); got != 3 {
			t.Fatalf("velas tras reejecutar: %d, esperado 3", got)
		}
	})

	// ── Endpoint GET /market/ohlc contra el servicio real ──────────────────
	t.Run("endpoint sirve la serie del producto filtrada por región y rango", func(t *testing.T) {
		svc := market.NewService(pool, market.DefaultOptions())
		h := market.NewHandlers(svc, integrationMeta{}, logger)
		mux := http.NewServeMux()
		h.Register(mux)

		// Serie completa de iron en la región: dos velas (bucket 0 y 3600).
		rec := do(t, mux, "/market/ohlc?product_id="+iron.String()+"&region_id="+region.String())
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
		}
		data := decodeCandles(t, rec)
		if len(data) != 2 {
			t.Fatalf("velas de iron: %d, esperado 2", len(data))
		}
		// Orden cronológico y valores string de punto fijo.
		if data[0].BucketStartSim != 0 || data[0].OpenPrice != "1000" || data[0].ClosePrice != "800" ||
			data[0].Volume != "180" || data[0].ContractCount != 3 || data[0].BucketSimSecs != bucket {
			t.Fatalf("primera vela inesperada: %+v", data[0])
		}
		if data[1].BucketStartSim != bucket || data[1].OpenPrice != "1200" || data[1].Volume != "20" {
			t.Fatalf("segunda vela inesperada: %+v", data[1])
		}

		// Rango que excluye el bucket 3600.
		rec = do(t, mux, "/market/ohlc?product_id="+iron.String()+"&to_sim=100")
		if data := decodeCandles(t, rec); len(data) != 1 || data[0].BucketStartSim != 0 {
			t.Fatalf("filtro to_sim=100 inesperado: %+v", data)
		}

		// Producto sin velas: serie vacía (array, nunca null).
		rec = do(t, mux, "/market/ohlc?product_id="+uuid.Must(uuid.NewV7()).String())
		if data := decodeCandles(t, rec); data == nil || len(data) != 0 {
			t.Fatalf("serie vacía inesperada: %+v", data)
		}
	})
}

// ─── Helpers de dominio del test ─────────────────────────────────────────────

func settled(product, region uuid.UUID, price, delivered, at int64) settledPayload {
	return settledPayload{
		ContractID: uuid.Must(uuid.NewV7()).String(), ProductID: product.String(),
		DestinationRegionID: region.String(), UnitPrice: itoa(price),
		QuantityAgreed: itoa(delivered), QuantityDelivered: itoa(delivered),
		FillBP: 10000, SettledAtSim: at, Status: "settled",
	}
}

func failed(product, region uuid.UUID, price, agreed, at int64) settledPayload {
	p := settled(product, region, price, agreed, at)
	p.QuantityDelivered = "0"
	p.FillBP = 0
	p.Status = "failed"
	return p
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// candle es la vela leída de analytics.market_ohlc para las aserciones.
type candle struct {
	open, high, low, close, volume int64
	count                          int32
	bucketSecs                     int64
}

func candleAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, product, region uuid.UUID, bucketStart int64) candle {
	t.Helper()
	var c candle
	err := pool.QueryRow(ctx, `
		SELECT open_price, high_price, low_price, close_price, volume, contract_count, bucket_sim_secs
		FROM analytics.market_ohlc
		WHERE product_id = $1 AND region_id = $2 AND bucket_start_sim = $3`,
		product, region, bucketStart).
		Scan(&c.open, &c.high, &c.low, &c.close, &c.volume, &c.count, &c.bucketSecs)
	if err != nil {
		t.Fatalf("leyendo la vela (%s, %s, %d): %v", product, region, bucketStart, err)
	}
	return c
}

// ─── Helpers HTTP del test ───────────────────────────────────────────────────

type integrationMeta struct{}

func (integrationMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(7200), SimTimeSeconds: 7200, ServerTime: time.Now().UTC()}
}

type candleDTO struct {
	ProductID      string `json:"product_id"`
	RegionID       string `json:"region_id"`
	BucketStartSim int64  `json:"bucket_start_sim"`
	BucketSimSecs  int64  `json:"bucket_sim_secs"`
	OpenPrice      string `json:"open_price"`
	HighPrice      string `json:"high_price"`
	LowPrice       string `json:"low_price"`
	ClosePrice     string `json:"close_price"`
	Volume         string `json:"volume"`
	ContractCount  int32  `json:"contract_count"`
}

func do(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func decodeCandles(t *testing.T, rec *httptest.ResponseRecorder) []candleDTO {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []candleDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v (body: %s)", err, rec.Body.String())
	}
	return resp.Data
}

// ─── Infraestructura del test (mismo patrón que ledger/contracts/outbox) ─────

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("markettest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(conn, "../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
		t.Fatalf("aplicando las migraciones: %v", err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("cerrando la conexión de migraciones: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando la URL del pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustEmit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, simTime int64, aggType string, aggID uuid.UUID, eventType string, payload any) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := outbox.Emit(ctx, tx, simTime, aggType, aggID, eventType, payload); err != nil {
		t.Fatalf("Emit(%s): %v", eventType, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", eventType, err)
	}
}

func productID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("producto %q: %v", code, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func seqsOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, types ...string) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT seq FROM outbox.events WHERE event_type = ANY($1) ORDER BY seq`, types)
	if err != nil {
		t.Fatalf("consultando seqs de %v: %v", types, err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando seqs: %v", err)
	}
	return seqs
}

func cursorOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var seq int64
	err := pool.QueryRow(ctx,
		`SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = $1`, name).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0 // cursor aún no registrado
	}
	if err != nil {
		t.Fatalf("cursor de %s: %v", name, err)
	}
	return seq
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout esperando %s", what)
}

func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
		return sum
	}
	t.Fatalf("métrica %q no encontrada", name)
	return 0
}
