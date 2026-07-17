// Lazo construir→producir→vender del Incremento 2 de proceso a proceso contra
// una BD real: el árbol de rutas REAL del gateway (internal/gateway.BuildHandler,
// idéntico a cmd/gateway) servido con httptest, el MOTOR de producción real
// (internal/world/production.Worker) disparado directamente y el worker CCRI del
// Incremento 1 (internal/contracts) para el sorteo. Ningún mock.
//
// Demo obtiene una concesión en la zona del yacimiento → construye una iron_mine
// (el emplazamiento valida por near_resource) → el motor completa la construcción
// diferida → operational → configura la receta mine_iron → encola 3 lotes → el
// motor los completa: el iron_ore de Demo sube A LA VEZ en stock_free (contable)
// y building_inventories (físico) —iguales—, el yacimiento baja, el coal se
// consume y el salario va al sink. Entonces Demo PUBLICA una venta de ese
// iron_ore recién producido vía el módulo contracts (integración con el
// Incremento 1) y Norte la acepta → sorteo → settled: el iron_ore PRODUCIDO
// cambia de dueño. La contabilidad cuadra en cada paso y la reconciliación
// física↔contable final es 0.
//
// El reloj de simulación se CONGELA y se avanza por SQL para que los lotes (3600
// s de sim) y la construcción venzan de forma determinista sin esperas de pared:
// el gateway (que sella started_at_sim al encolar) y el motor (que decide el
// vencimiento) leen el mismo ancla congelada (CacheTTL 0). El sorteo del CCRI usa
// ventanas wall-clock cortas, ajenas al reloj de simulación.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia, mismo
// patrón que el resto de tests de integración).
package e2e

import (
	"context"
	"encoding/json"
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
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/buildings"
	"github.com/lokiteitor/global-market/backend/internal/world/catalog"
	"github.com/lokiteitor/global-market/backend/internal/world/land"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// Parámetros del lote del seed que el test reproduce en sus aserciones.
const (
	prodBatchSimSeconds int64 = 3_600 // mine_iron.batch_sim_seconds
	prodBatchesQueued         = 3
	prodOutputPerBatch  int64 = 50 // iron_ore por lote
	prodFuelPerBatch    int64 = 5  // coal por lote
	prodWorkers         int64 = 3
	prodCityBaseSalary  int64 = 30
	prodDepositInitial  int64 = 1_000_000
	prodCoalLoaded      int64 = 100 // coal precargado en la mina (entrega abstraída)
	prodSellQty         int64 = 100
	prodSellPrice       int64 = 120
)

func TestProductionCycleE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Seed del mundo (incluye el mundo industrial del Incremento 2) ─────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ── Reloj de simulación CONGELADO: lo controlamos por SQL ────────────────
	const simBase int64 = 100_000
	freezeSim(t, ctx, pool, simBase)

	// ── IDs del mundo sembrado que el ciclo necesita ─────────────────────────
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "iron_ore")
	coalID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = $1`, "coal")
	ironMineTypeID := queryUUID(t, ctx, pool, `SELECT id FROM world.building_types WHERE code = $1`, "iron_mine")
	mineIronRecipeID := queryUUID(t, ctx, pool, `SELECT id FROM world.recipes WHERE code = $1`, "mine_iron")
	depositID := queryUUID(t, ctx, pool, `SELECT id FROM world.resource_deposits WHERE product_id = $1`, ironOreID)

	// ── Gateway real: mismo árbol que cmd/gateway; ventanas de sorteo cortas ──
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 1
	contractsOpts.MicroWindowSeconds = 1
	handler, err := gateway.BuildHandler(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: gateway.Options{
			Auth: auth.Options{
				LoginPerMin: auth.DefaultRateLoginPerMin,
				APIRPS:      auth.DefaultRateAPIRPS,
				APIBurst:    auth.DefaultRateAPIBurst,
			},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			Catalog:     catalog.DefaultOptions(),
			Land:        land.DefaultOptions(),
			Buildings:   buildings.DefaultOptions(),
			Production:  production.DefaultOptions(),
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		},
	})
	if err != nil {
		t.Fatalf("BuildHandler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// ── Motor de producción real (SimSource = lector congelado, CacheTTL 0);
	//    II_BUILD_SIM_SECONDS = 0 ⇒ la construcción vence de inmediato ────────
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)
	prodWorker, err := production.NewWorker(pool, reader, production.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100, BuildSimSeconds: 0,
		ReconcileInterval: time.Second,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}

	// ── Worker CCRI para el sorteo (mismo lector congelado) ──────────────────
	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}

	demoToken := login(t, srv, demoName, demoSecret)
	norteToken := login(t, srv, traderName, traderSecret)

	demoCash0 := cashOf(t, ctx, pool, demoID)
	sink0 := sinkBalance(t, ctx, pool)

	// ── (1) Demo obtiene una concesión en la zona del yacimiento (20000,20000) ─
	r := call(t, srv, http.MethodPost, "/api/v1/world/concessions", demoToken, map[string]any{
		"region_id": regionID.String(),
		"parcel":    geoRect(19_000, 19_000, 21_000, 21_000),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("crear concesión: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	concession := asMap(t, r.body["data"], "data")
	concessionID, _ := concession["id"].(string)
	if concession["status"] != "active" || concessionID == "" {
		t.Fatalf("concesión inesperada: %v", concession)
	}
	canonPaid := int64Str(t, concession["canon_amount"], "canon_amount")
	assertMeta(t, r.body, "crear concesión")

	// ── (2) Demo construye una iron_mine (footprint dentro de la parcela, sobre
	//        el yacimiento): el emplazamiento valida por near_resource ─────────
	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings", demoToken, map[string]any{
		"building_type_id": ironMineTypeID.String(),
		"concession_id":    concessionID,
		"footprint":        geoRect(19_800, 19_800, 20_200, 20_200),
	})
	if r.status != http.StatusCreated {
		t.Fatalf("construir mina: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	building := asMap(t, r.body["data"], "data")
	mineID, _ := building["id"].(string)
	if building["status"] != "under_construction" || mineID == "" {
		t.Fatalf("edificio inesperado (esperado under_construction): %v", building)
	}
	mineUUID := uuid.MustParse(mineID)
	// El coste de construcción se asentó al sink al crear.
	if got := cashOf(t, ctx, pool, demoID); got != demoCash0-canonPaid-ironMineBuildCost {
		t.Fatalf("caja de Demo tras construir: %d, esperado %d", got, demoCash0-canonPaid-ironMineBuildCost)
	}

	// ── (3) El motor completa la construcción diferida (BuildSimSeconds 0) ────
	drivePlacement(t, ctx, srv, prodWorker, demoToken, mineID, "operational")

	// ── (4) Demo configura la receta mine_iron ───────────────────────────────
	r = call(t, srv, http.MethodPatch, "/api/v1/world/buildings/"+mineID, demoToken, map[string]any{
		"active_recipe_id": mineIronRecipeID.String(),
	})
	if r.status != http.StatusOK {
		t.Fatalf("configurar receta: status %d, esperado 200 (cuerpo: %s)", r.status, r.raw)
	}
	if asMap(t, r.body["data"], "data")["active_recipe_id"] != mineIronRecipeID.String() {
		t.Fatalf("la receta activa no se fijó: %v", r.body["data"])
	}

	// ── (5) Precarga de combustible en la mina (entrega abstraída; logística =
	//        Incremento 3), asentada físico↔contable a la vez para no romper la
	//        reconciliación ────────────────────────────────────────────────────
	loadFuel(t, ctx, pool, demoID, coalID, mineUUID, prodCoalLoaded, simBase)
	assertBalancedLedger(t, ctx, pool)

	// ── (6) Demo encola 3 lotes de mine_iron; el primero arranca en running ───
	r = call(t, srv, http.MethodPost, "/api/v1/world/buildings/"+mineID+"/production-batches", demoToken, map[string]any{
		"recipe_id":      mineIronRecipeID.String(),
		"batches_queued": prodBatchesQueued,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("encolar lotes: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	batch := asMap(t, r.body["data"], "data")
	if batch["status"] != "running" || int(int64Num(t, batch["batches_queued"], "batches_queued")) != prodBatchesQueued {
		t.Fatalf("lote inesperado (esperado running con 3 en cola): %v", batch)
	}

	// ── (7) El motor completa los 3 lotes: se avanza el reloj congelado por
	//        encima de la duración efectiva del lote antes de cada barrido ─────
	driveBatchesUntilCompleted(t, ctx, srv, pool, prodWorker, demoToken, mineID)

	// ── (8) Producción real: físico == contable, yacimiento bajó, coal
	//        consumido, salario al sink ─────────────────────────────────────────
	wantIron := prodOutputPerBatch * prodBatchesQueued // 150
	wantCoalLeft := prodCoalLoaded - prodFuelPerBatch*prodBatchesQueued
	wantWage := prodWorkers * prodCityBaseSalary * prodBatchesQueued // 3*30*3 = 270

	if got := stockFreeOf(t, ctx, pool, demoID, ironOreID, mineUUID); got != wantIron {
		t.Fatalf("stock_free de iron_ore de Demo en la mina: %d, esperado %d", got, wantIron)
	}
	if got := inventoryQty(t, ctx, pool, mineUUID, ironOreID); got != wantIron {
		t.Fatalf("inventario físico de iron_ore en la mina: %d, esperado %d", got, wantIron)
	}
	if got := inventoryQty(t, ctx, pool, mineUUID, coalID); got != wantCoalLeft {
		t.Fatalf("coal físico restante en la mina: %d, esperado %d", got, wantCoalLeft)
	}
	if got := stockFreeOf(t, ctx, pool, demoID, coalID, mineUUID); got != wantCoalLeft {
		t.Fatalf("stock_free de coal de Demo en la mina: %d, esperado %d", got, wantCoalLeft)
	}
	if got := depositRemaining(t, ctx, pool, depositID); got != prodDepositInitial-wantIron {
		t.Fatalf("yacimiento restante: %d, esperado %d", got, prodDepositInitial-wantIron)
	}
	if got := wageToSink(t, ctx, pool); got != wantWage {
		t.Fatalf("salario asentado al sink: %d, esperado %d", got, wantWage)
	}
	// Caja de Demo: capital − canon − build_cost − salarios (nada más se gastó).
	if got := cashOf(t, ctx, pool, demoID); got != demoCash0-canonPaid-ironMineBuildCost-wantWage {
		t.Fatalf("caja de Demo tras producir: %d, esperado %d", got, demoCash0-canonPaid-ironMineBuildCost-wantWage)
	}
	assertBalancedLedger(t, ctx, pool)

	// ── (9) Demo PUBLICA la venta del iron_ore recién producido; Norte acepta ─
	mineNodeID := queryUUID(t, ctx, pool,
		`SELECT id FROM world.network_nodes WHERE building_id = $1 AND kind = 'mine'`, mineUUID)
	r = call(t, srv, http.MethodPost, "/api/v1/contracts/publications", demoToken, map[string]any{
		"kind":                 "sell",
		"product_id":           ironOreID.String(),
		"quantity_total":       itoa(prodSellQty),
		"unit_price":           itoa(prodSellPrice),
		"min_lot":              "50",
		"origin_node_id":       mineNodeID.String(),
		"delivery_sim_seconds": 3600,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("publicar venta: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	pub := asMap(t, r.body["data"], "data")
	pubID, _ := pub["id"].(string)
	if pub["kind"] != "sell" || pub["status"] != "draw_window" || pubID == "" {
		t.Fatalf("publicación inesperada: %v", pub)
	}
	// Al publicar, 100 de iron_ore de Demo se congelan (stock_free → reservado).
	if got := stockFreeOf(t, ctx, pool, demoID, ironOreID, mineUUID); got != wantIron-prodSellQty {
		t.Fatalf("stock_free de iron_ore de Demo tras publicar: %d, esperado %d", got, wantIron-prodSellQty)
	}

	// Norte acepta las 100.
	idemKey := uuid.NewString()
	r = callKeyed(t, srv, http.MethodPost, "/api/v1/contracts/publications/"+pubID+"/acceptances",
		norteToken, idemKey, map[string]any{"quantity": itoa(prodSellQty)})
	if r.status != http.StatusCreated {
		t.Fatalf("aceptar venta: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}
	accID, _ := asMap(t, r.body["data"], "data")["id"].(string)
	if accID == "" {
		t.Fatal("aceptación sin id")
	}

	// ── (10) Sorteo → contrato liquidado in situ: el iron_ore cambia de dueño ─
	contractID := driveDrawUntilServed(t, ctx, srv, ccriWorker, norteToken, accID)
	r = call(t, srv, http.MethodGet, "/api/v1/contracts/contracts/"+contractID, demoToken, nil)
	if r.status != http.StatusOK {
		t.Fatalf("contrato: status %d (cuerpo: %s)", r.status, r.raw)
	}
	c := asMap(t, r.body["data"], "data")
	if c["status"] != "settled" || c["quantity_delivered"] != itoa(prodSellQty) ||
		c["seller_account_id"] != demoID.String() || c["buyer_account_id"] != norteID.String() {
		t.Fatalf("contrato liquidado inesperado: %v", c)
	}

	// El iron_ore PRODUCIDO cambió de dueño: Demo 150 − 100 = 50; Norte +100.
	if got := stockFreeOf(t, ctx, pool, demoID, ironOreID, mineUUID); got != wantIron-prodSellQty {
		t.Fatalf("stock_free de iron_ore de Demo tras liquidar: %d, esperado %d", got, wantIron-prodSellQty)
	}
	if got := stockFreeOf(t, ctx, pool, norteID, ironOreID, mineUUID); got != prodSellQty {
		t.Fatalf("stock_free de iron_ore de Norte tras liquidar: %d, esperado %d", got, prodSellQty)
	}
	assertBalancedLedger(t, ctx, pool)

	// ── (11) Reconciliación física↔contable final: 0 divergencias ────────────
	disc, err := prodWorker.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconciliación: %v", err)
	}
	if disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d divergencias, esperado 0", disc)
	}

	_ = sink0 // el balance del sink se verifica vía wageToSink (salario) por separado
}

// ─── Conducción del motor ─────────────────────────────────────────────────────

// drivePlacement dispara el barrido de construcción del motor hasta que el
// edificio alcanza el estado esperado (o falla por timeout).
func drivePlacement(t *testing.T, ctx context.Context, srv *httptest.Server, w *production.Worker, token, buildingID, wantStatus string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		w.RunOnce(ctx)
		r := call(t, srv, http.MethodGet, "/api/v1/world/buildings/"+buildingID, token, nil)
		if r.status == http.StatusOK {
			if asMap(t, r.body["data"], "data")["status"] == wantStatus {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el estado %q del edificio (última respuesta: %s)", wantStatus, r.raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// driveBatchesUntilCompleted avanza el reloj congelado por encima de la duración
// efectiva del lote y dispara el barrido de producción, repitiéndolo hasta que
// el lote queda completed (o falla por timeout).
func driveBatchesUntilCompleted(t *testing.T, ctx context.Context, srv *httptest.Server, pool *pgxpool.Pool, w *production.Worker, token, buildingID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		// Avanza el sim-time por encima de la duración de un lote para que el que
		// esté en curso venza en el siguiente barrido.
		advanceSim(t, ctx, pool, prodBatchSimSeconds+400)
		w.RunOnce(ctx)

		r := call(t, srv, http.MethodGet, "/api/v1/world/buildings/"+buildingID+"/production-batches", token, nil)
		if r.status == http.StatusOK {
			list, _ := r.body["data"].([]any)
			if len(list) == 1 {
				b := asMap(t, list[0], "batch")
				if b["status"] == "completed" {
					if done := int(int64Num(t, b["batches_done"], "batches_done")); done != prodBatchesQueued {
						t.Fatalf("lote completado con batches_done %d, esperado %d", done, prodBatchesQueued)
					}
					return
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando la finalización de los lotes (última respuesta: %s)", r.raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ─── Reloj de simulación congelado (control por SQL) ──────────────────────────

// freezeSim congela el reloj de simulación en el valor dado (ambos lectores, con
// CacheTTL 0, verán este ancla hasta que se avance con advanceSim).
func freezeSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, at int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE world.sim_clock SET frozen = true, sim_time_at = $1, wall_anchor = now(), updated_at = now() WHERE id = 1`, at); err != nil {
		t.Fatalf("congelando el reloj: %v", err)
	}
}

// advanceSim adelanta el sim-time congelado en delta sim-segundos.
func advanceSim(t *testing.T, ctx context.Context, pool *pgxpool.Pool, delta int64) {
	t.Helper()
	if _, err := pool.Exec(ctx,
		`UPDATE world.sim_clock SET sim_time_at = sim_time_at + $1, updated_at = now() WHERE id = 1`, delta); err != nil {
		t.Fatalf("avanzando el reloj: %v", err)
	}
}

// ─── Precarga de combustible (físico + contable) ──────────────────────────────

// loadFuel deposita qty de combustible en la mina, asentándolo a la vez en el
// plano físico (world.building_inventories) y en el contable (production_output:
// +qty stock_free / -qty world_source, ADR-022), como haría una entrega real. Así
// la reconciliación física↔contable no se rompe.
func loadFuel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, building uuid.UUID, qty, simNow int64) {
	t.Helper()
	ledgerSvc := ledger.NewService(pool, ledger.DefaultOptions(), nil)

	worldSourceID := queryUUID(t, ctx, pool,
		`SELECT id FROM ledger.accounts WHERE kind = 'world_source' AND product_id = $1`, product)
	ownerID, productID, buildingID := owner, product, building
	stockFree, err := ledgerSvc.CreateAccount(ctx, ledger.AccountKindStockFree, &ownerID, &productID, &buildingID, nil)
	if err != nil {
		t.Fatalf("creando stock_free de combustible: %v", err)
	}
	ref := building
	if _, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindProductionOutput, simtime.SimTime(simNow), &ref,
		"Precarga de combustible en la mina (entrega abstraída)", []ledger.EntryInput{
			{AccountID: stockFree.ID, Amount: qty},
			{AccountID: worldSourceID, Amount: -qty},
		}); err != nil {
		t.Fatalf("asentando la precarga de combustible: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (building_id, product_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
		building, product, qty, simNow); err != nil {
		t.Fatalf("insertando el inventario físico de combustible: %v", err)
	}
}

// ─── Lecturas auxiliares ──────────────────────────────────────────────────────

func inventoryQty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	t.Helper()
	var q int64
	err := pool.QueryRow(ctx,
		`SELECT quantity FROM world.building_inventories WHERE building_id = $1 AND product_id = $2`,
		building, product).Scan(&q)
	if err != nil {
		t.Fatalf("inventario físico (%s, %s): %v", building, product, err)
	}
	return q
}

func depositRemaining(t *testing.T, ctx context.Context, pool *pgxpool.Pool, depositID uuid.UUID) int64 {
	t.Helper()
	var q int64
	if err := pool.QueryRow(ctx,
		`SELECT remaining_amount FROM world.resource_deposits WHERE id = $1`, depositID).Scan(&q); err != nil {
		t.Fatalf("yacimiento %s: %v", depositID, err)
	}
	return q
}

// wageToSink devuelve la suma asentada al sink por transacciones de salario
// (transacción kind 'wage', partida positiva sobre la cuenta sink).
func wageToSink(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var total int64
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(e.amount), 0)
		  FROM ledger.entries e
		  JOIN ledger.transactions t ON t.id = e.transaction_id
		  JOIN ledger.accounts a ON a.id = e.account_id
		 WHERE t.kind = 'wage' AND a.kind = 'sink' AND e.amount > 0`).Scan(&total)
	if err != nil {
		t.Fatalf("salario al sink: %v", err)
	}
	return total
}

func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind = 'sink'`).Scan(&b); err != nil {
		t.Fatalf("saldo del sink: %v", err)
	}
	return b
}

// ─── Helpers de construcción de peticiones ────────────────────────────────────

// geoRect devuelve un GeoPolygon del contrato (SRID 0 planar, anillo cerrado CCW)
// como objeto JSON listo para el cuerpo de la petición.
func geoRect(minX, minY, maxX, maxY int64) map[string]any {
	return map[string]any{
		"type": "Polygon",
		"coordinates": [][][]int64{{
			{minX, minY}, {maxX, minY}, {maxX, maxY}, {minX, maxY}, {minX, minY},
		}},
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// int64Str interpreta un importe de punto fijo del contrato (string de dígitos).
func int64Str(t *testing.T, v any, field string) int64 {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s no es un string de punto fijo: %T (%v)", field, v, v)
	}
	var n int64
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("%s %q no es un entero: %v", field, s, err)
	}
	return n
}

// int64Num interpreta un entero JSON (campos numéricos como batches_queued).
func int64Num(t *testing.T, v any, field string) int64 {
	t.Helper()
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("%s no es numérico: %T (%v)", field, v, v)
	}
	return int64(f)
}

// ironMineBuildCost es el coste de construcción de la mina del seed (world
// industrial), reproducido aquí para las aserciones de caja.
const ironMineBuildCost int64 = 80_000

// withWorldDefaults rellena las opciones de los subpaquetes world del gateway con
// sus defaults (QueryTimeout > 0). Lo comparten los tests E2E que construyen
// gateway.Options a mano: el árbol de rutas del gateway monta ahora también el
// contexto world, cuyos servicios exigen una configuración válida.
func withWorldDefaults(o gateway.Options) gateway.Options {
	o.Catalog = catalog.DefaultOptions()
	o.Land = land.DefaultOptions()
	o.Buildings = buildings.DefaultOptions()
	o.Production = production.DefaultOptions()
	return o
}
