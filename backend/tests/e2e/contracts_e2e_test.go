// Ciclo económico completo del Incremento 1 de proceso a proceso contra una BD
// real: el árbol de rutas REAL del gateway (internal/gateway.BuildHandler,
// idéntico a cmd/gateway) servido con httptest, los tres barridos del worker
// CCRI (internal/contracts) disparados directamente y el agregador OHLC
// (internal/market) consumiendo el outbox. Ningún mock: publicar → consultar el
// tablón → aceptar (con Idempotency-Key, y reintentar sin duplicar) → sortear →
// liquidar in situ → comprobar saldos y la vela de mercado → cooldown en
// cancelación.
//
// Se omite si II_TEST_DATABASE_URL no está definida (mismo patrón que el resto
// de tests de integración: BD efímera propia, migraciones reales, destruida al
// terminar).
package e2e

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
)

// e2eRefs reúne los IDs del mundo sembrado que el ciclo necesita, resueltos por
// clave natural.
type e2eRefs struct {
	demo, norte    uuid.UUID
	ironOre        uuid.UUID
	region         uuid.UUID
	norteNode      uuid.UUID
	norteWarehouse uuid.UUID
}

func TestEconomicCycleE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Seed del mundo (Demo, Norte, Askadia, iron_ore, almacenes) ───────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	refs := loadE2ERefs(t, ctx, pool)

	// ── Gateway real con ventanas cortas: el sorteo cierra en 1 s wall; el
	//    cooldown anti-parpadeo (10 s) sigue activo para el 409 de cancelación ─
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	// CancelCooldownSeconds queda en su default (10 s): lo exige el caso 409.

	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth: auth.Options{
				LoginPerMin: auth.DefaultRateLoginPerMin,
				APIRPS:      auth.DefaultRateAPIRPS,
				APIBurst:    auth.DefaultRateAPIBurst,
			},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		}),
	})
	if err != nil {
		t.Fatalf("BuildHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── Worker CCRI para disparar el barrido directamente (SimSource = lector
	//    del reloj real, igual que el gateway) ─────────────────────────────────
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	workerSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService (worker): %v", err)
	}
	worker, err := contracts.NewWorker(workerSvc, contracts.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}

	// ── Agregador OHLC (consumidor del outbox) ───────────────────────────────
	aggregator, err := market.NewAggregator(market.DefaultOptions(),
		market.NewMetrics(prometheus.NewRegistry()), logger)
	if err != nil {
		t.Fatalf("market.NewAggregator: %v", err)
	}

	// ── Login de las dos corporaciones ───────────────────────────────────────
	norteToken := login(t, srv, traderName, traderSecret)
	demoToken := login(t, srv, demoName, demoSecret)

	// ── Norte publica: sell 500 iron_ore @ 120, min_lot 50 ───────────────────
	r := call(t, srv, http.MethodPost, "/api/v1/contracts/publications", norteToken, map[string]any{
		"kind":                 "sell",
		"product_id":           refs.ironOre.String(),
		"quantity_total":       "500",
		"unit_price":           "120",
		"min_lot":              "50",
		"origin_node_id":       refs.norteNode.String(),
		"delivery_sim_seconds": 3600,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar sell: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	pub := asMap(t, r.body["data"], "data")
	pubID, _ := pub["id"].(string)
	if pub["kind"] != "sell" || pub["status"] != "draw_window" || pub["quantity_remaining"] != "500" ||
		pub["unit_price"] != "120" || pub["min_lot"] != "50" || pubID == "" {
		t.Fatalf("publicación inesperada: %v", pub)
	}
	assertMeta(t, r.body, "publicar")

	// Al publicar, Norte congela 500 de stock y la garantía del 10% (6000).
	if got := cashOf(t, ctx, pool, refs.norte); got != 1_000_000-6_000 {
		t.Fatalf("caja de Norte tras publicar: %d, esperado %d", got, 1_000_000-6_000)
	}

	// ── Demo consulta el tablón y ve la publicación ejecutable ───────────────
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/board?product_id="+refs.ironOre.String(), demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("board: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	board, ok := r.body["data"].([]any)
	if !ok || len(board) != 1 {
		t.Fatalf("tablón: esperada 1 publicación, cuerpo: %s", r.raw)
	}
	if boardPub := asMap(t, board[0], "board[0]"); boardPub["id"] != pubID {
		t.Fatalf("la publicación del tablón no coincide: %v", boardPub)
	}

	// ── Demo acepta 200 con Idempotency-Key; el reintento con la misma clave
	//    reproduce la respuesta sin crear una segunda aceptación ──────────────
	idemKey := uuid.NewString()
	acceptPath := "/api/v1/contracts/publications/" + pubID + "/acceptances"
	r1 := callKeyed(t, srv, http.MethodPost, acceptPath, demoToken, idemKey, map[string]any{"quantity": "200"})
	if r1.status != http.StatusCreated {
		t.Fatalf("aceptar: status %d, esperado 201 (cuerpo: %s)", r1.status, r1.raw)
	}
	acc := asMap(t, r1.body["data"], "data")
	accID, _ := acc["id"].(string)
	if acc["status"] != "pending_draw" || acc["quantity"] != "200" || accID == "" {
		t.Fatalf("aceptación inesperada: %v", acc)
	}
	if replayed := r1.header.Get("Idempotency-Replayed"); replayed != "" {
		t.Fatalf("la primera aceptación no debe venir marcada como reproducida (Idempotency-Replayed=%q)", replayed)
	}

	// Reintento idéntico: misma respuesta reproducida, sin ejecutar de nuevo.
	r2 := callKeyed(t, srv, http.MethodPost, acceptPath, demoToken, idemKey, map[string]any{"quantity": "200"})
	if r2.status != http.StatusCreated {
		t.Fatalf("reintento idempotente: status %d, esperado 201 (cuerpo: %s)", r2.status, r2.raw)
	}
	if r2.header.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("el reintento debe traer Idempotency-Replayed: true (cabeceras: %v)", r2.header)
	}
	if replayAcc := asMap(t, r2.body["data"], "data reintento"); replayAcc["id"] != accID {
		t.Fatalf("el reintento devolvió otra aceptación: %v (esperado id %s)", replayAcc, accID)
	}
	// Una sola aceptación persistida: el reintento no duplicó.
	if n := countRows(t, ctx, pool,
		`SELECT count(*) FROM ledger.publication_acceptances WHERE publication_id = $1`, uuid.MustParse(pubID)); n != 1 {
		t.Fatalf("aceptaciones de la publicación: %d, esperada exactamente 1 (idempotencia)", n)
	}

	// ── Resolución del sorteo: la ventana cierra en 1 s wall; se dispara el
	//    barrido hasta que la aceptación queda servida (in situ ⇒ liquidada) ──
	contractID := driveDrawUntilServed(t, ctx, srv, worker, demoToken, accID)

	// La aceptación queda served con su contract_id.
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/acceptances/"+accID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("acceptance: status %d (cuerpo: %s)", r.status, r.raw)
	}
	served := asMap(t, r.body["data"], "data")
	if served["status"] != "served" || served["quantity_served"] != "200" || served["contract_id"] != contractID {
		t.Fatalf("aceptación servida inesperada: %v", served)
	}

	// ── El contrato nació y se liquidó in situ al 100% (fill 10000) ──────────
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("contract: status %d (cuerpo: %s)", r.status, r.raw)
	}
	c := asMap(t, r.body["data"], "data")
	if c["status"] != "settled" || c["quantity_agreed"] != "200" || c["quantity_delivered"] != "200" ||
		c["unit_price"] != "120" || c["buyer_account_id"] != refs.demo.String() ||
		c["seller_account_id"] != refs.norte.String() {
		t.Fatalf("contrato liquidado inesperado: %v", c)
	}
	if fill, _ := c["fill_bp"].(float64); fill != 10000 {
		t.Fatalf("fill_bp: %v, esperado 10000", c["fill_bp"])
	}
	// Retirada in situ: origen == destino.
	if c["origin_node_id"] != c["destination_node_id"] {
		t.Fatalf("contrato in situ: origin (%v) != destination (%v)", c["origin_node_id"], c["destination_node_id"])
	}

	// La entrega quedó registrada (in situ, a tiempo).
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID+"/deliveries", demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("deliveries: status %d (cuerpo: %s)", r.status, r.raw)
	}
	deliveries, ok := r.body["data"].([]any)
	if !ok || len(deliveries) != 1 {
		t.Fatalf("entregas: esperada 1, cuerpo: %s", r.raw)
	}
	if d := asMap(t, deliveries[0], "delivery[0]"); d["quantity"] != "200" || d["on_time"] != true {
		t.Fatalf("entrega inesperada: %v", d)
	}

	// ── Saldos tras la liquidación ───────────────────────────────────────────
	// Demo (comprador): paga 24000 (200 × 120) y recibe 200 de stock EN el
	// almacén de Norte (retirada in situ).
	if got := cashOf(t, ctx, pool, refs.demo); got != 1_000_000-24_000 {
		t.Fatalf("caja de Demo: %d, esperado %d", got, 1_000_000-24_000)
	}
	if got := stockFreeOf(t, ctx, pool, refs.demo, refs.ironOre, refs.norteWarehouse); got != 200 {
		t.Fatalf("stock_free de Demo en el almacén de Norte: %d, esperado 200", got)
	}
	// Norte (vendedor): cobra 24000 y recupera la garantía del contrato (2400);
	// la publicación sigue abierta con 300 sin vender ⇒ 3600 de garantía siguen
	// congelados. Neto: 1000000 − 6000(garantía inicial) + 24000 + 2400 = 1020400.
	if got := cashOf(t, ctx, pool, refs.norte); got != 1_020_400 {
		t.Fatalf("caja de Norte: %d, esperado 1020400 (+24000 cobro, garantía del contrato recuperada)", got)
	}
	// La garantía del CONTRATO se recuperó por completo: su cuenta espejo a cero.
	guaranteeAcc, _ := c["seller_guarantee_account_id"].(string)
	if got := balanceByAccountID(t, ctx, pool, uuid.MustParse(guaranteeAcc)); got != 0 {
		t.Fatalf("garantía del contrato: %d, esperado 0 (recuperada)", got)
	}
	// El ledger cuadra a cero por activo (dinero y cada producto).
	assertBalancedLedger(t, ctx, pool)

	// ── El agregador OHLC construye la vela del contrato liquidado ────────────
	runOhlcConsumer(t, ctx, pool, aggregator)

	r = call(t, srv, http.MethodGet,
		"/api/v1/market/ohlc?product_id="+refs.ironOre.String()+"&region_id="+refs.region.String(), demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("market/ohlc: status %d (cuerpo: %s)", r.status, r.raw)
	}
	candles, ok := r.body["data"].([]any)
	if !ok || len(candles) != 1 {
		t.Fatalf("velas OHLC: esperada 1, cuerpo: %s", r.raw)
	}
	candle := asMap(t, candles[0], "candle[0]")
	if candle["open_price"] != "120" || candle["high_price"] != "120" || candle["low_price"] != "120" ||
		candle["close_price"] != "120" || candle["volume"] != "200" {
		t.Fatalf("vela OHLC inesperada: %v", candle)
	}
	if cc, _ := candle["contract_count"].(float64); cc != 1 {
		t.Fatalf("contract_count de la vela: %v, esperado 1", candle["contract_count"])
	}
	assertMeta(t, r.body, "market/ohlc")

	// ── Cancelar una publicación dentro del cooldown ⇒ 409 ───────────────────
	r = call(t, srv, http.MethodPost, "/api/v1/contracts/publications", norteToken, map[string]any{
		"kind":                 "sell",
		"product_id":           refs.ironOre.String(),
		"quantity_total":       "100",
		"unit_price":           "50",
		"min_lot":              "10",
		"origin_node_id":       refs.norteNode.String(),
		"delivery_sim_seconds": 3600,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar (cooldown): status %d (cuerpo: %s)", r.status, r.raw)
	}
	cooldownPubID, _ := asMap(t, r.body["data"], "data")["id"].(string)

	r = call(t, srv, http.MethodDelete, "/api/v1/contracts/publications/"+cooldownPubID, norteToken, nil)
	assertErrorEnvelope(t, r, http.StatusConflict, "CANCEL_COOLDOWN_ACTIVE", "cancelar dentro del cooldown")
}

// ─── Flujo del sorteo ─────────────────────────────────────────────────────────

// driveDrawUntilServed espera a que cierre la ventana de sorteo y dispara el
// barrido del worker hasta que la aceptación queda servida (o falla por
// timeout). Devuelve el contract_id resultante. La ventana wall-clock es de 1 s;
// se reintenta con margen para el reloj de la BD.
func driveDrawUntilServed(t *testing.T, ctx context.Context, srv *httptest.Server, worker *contracts.Worker, token, accID string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		worker.RunOnce(ctx)

		r := call(t, srv, http.MethodGet, "/api/v1/contracts/acceptances/"+accID, token, nil)
		if r.status == http.StatusOK {
			data := asMap(t, r.body["data"], "data")
			if data["status"] == "served" {
				cid, _ := data["contract_id"].(string)
				if cid == "" {
					t.Fatalf("aceptación servida sin contract_id: %v", data)
				}
				return cid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando la resolución del sorteo (última respuesta: %s)", r.raw)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// runOhlcConsumer arranca el consumidor del agregador OHLC hasta que el cursor
// alcanza el último evento contract.settled emitido, y lo detiene limpiamente.
func runOhlcConsumer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregator *market.Aggregator) {
	t.Helper()
	target := maxSeqOf(t, ctx, pool, "contract.settled")
	if target == 0 {
		t.Fatal("no hay eventos contract.settled que agregar")
	}
	consumer := aggregator.NewConsumer(pool,
		outbox.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, aggregator.Handle) }()

	deadline := time.Now().Add(20 * time.Second)
	for cursorOf(t, ctx, pool, market.ConsumerName) < target {
		if time.Now().After(deadline) {
			stop()
			<-done
			t.Fatal("timeout esperando al agregador OHLC")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("consumidor OHLC devolvió error en el apagado: %v", err)
	}
}

// ─── Cliente HTTP con Idempotency-Key ─────────────────────────────────────────

// callKeyed ejecuta una petición como call, añadiendo la cabecera
// Idempotency-Key del contrato v1.2.0.
func callKeyed(t *testing.T, srv *httptest.Server, method, path, token, idempotencyKey string, payload any) response {
	t.Helper()
	req := buildRequest(t, srv, method, path, token, payload)
	req.Header.Set("Idempotency-Key", idempotencyKey)
	return doRequest(t, srv, req, method, path)
}

// login autentica una corporación y devuelve su token bearer.
func login(t *testing.T, srv *httptest.Server, name, secret string) string {
	t.Helper()
	r := call(t, srv, http.MethodPost, "/api/v1/auth/sessions", "",
		map[string]any{"account_name": name, "secret": secret})
	if r.status != http.StatusCreated {
		t.Fatalf("login %s: status %d (cuerpo: %s)", name, r.status, r.raw)
	}
	token, _ := asMap(t, r.body["data"], "data")["token"].(string)
	if token == "" {
		t.Fatalf("login %s: token ausente", name)
	}
	return token
}

// ─── Resolución de IDs y saldos ───────────────────────────────────────────────

func loadE2ERefs(t *testing.T, ctx context.Context, pool *pgxpool.Pool) e2eRefs {
	t.Helper()
	var refs e2eRefs
	refs.demo = queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	refs.norte = queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	refs.ironOre = queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	refs.region = queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	if err := pool.QueryRow(ctx, `
		SELECT n.id, b.id
		  FROM world.network_nodes n
		  JOIN world.buildings b ON b.id = n.building_id
		 WHERE b.owner_account_id = $1`, refs.norte).Scan(&refs.norteNode, &refs.norteWarehouse); err != nil {
		t.Fatalf("implantación de Norte: %v", err)
	}
	return refs
}

func queryUUID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return id
}

func cashOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT balance FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1`, owner).Scan(&b); err != nil {
		t.Fatalf("caja de %s: %v", owner, err)
	}
	return b
}

func stockFreeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT balance FROM ledger.accounts
		 WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2 AND warehouse_building_id = $3`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock_free de %s: %v", owner, err)
	}
	return b
}

func balanceByAccountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE id = $1`, id).Scan(&b); err != nil {
		t.Fatalf("saldo de la cuenta %s: %v", id, err)
	}
	return b
}

// assertBalancedLedger comprueba la invariante contable global: la suma de
// saldos de TODAS las cuentas es cero para cada activo (dinero y cada producto).
func assertBalancedLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(product_id::text, 'MONEY') AS asset, SUM(balance) AS total
		  FROM ledger.accounts
		 GROUP BY COALESCE(product_id::text, 'MONEY')`)
	if err != nil {
		t.Fatalf("sumando el ledger: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var asset string
		var total int64
		if err := rows.Scan(&asset, &total); err != nil {
			t.Fatalf("leyendo la suma del ledger: %v", err)
		}
		if total != 0 {
			t.Fatalf("el ledger no cuadra a cero para el activo %s: suma %d", asset, total)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando las sumas del ledger: %v", err)
	}
}

// ─── Consultas del outbox ─────────────────────────────────────────────────────

func maxSeqOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(ctx,
		`SELECT COALESCE(max(seq), 0) FROM outbox.events WHERE event_type = $1`, eventType).Scan(&seq); err != nil {
		t.Fatalf("max(seq) de %s: %v", eventType, err)
	}
	return seq
}

func cursorOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var seq int64
	err := pool.QueryRow(ctx,
		`SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = $1`, name).Scan(&seq)
	if err != nil {
		return 0 // cursor aún no registrado
	}
	return seq
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}
