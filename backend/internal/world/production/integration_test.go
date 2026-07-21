package production_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// Parámetros de las recetas del fixture (sim-time y cantidades fijos, para
// controlar el vencimiento analítico de los lotes).
const (
	batchSimSecs   int64 = 100
	buildSimSecs   int64 = 3600
	baseSalary     int64 = 100
	extractWorkers int32 = 5
	burnWorkers    int32 = 3
	extractOutput  int64 = 500
	extractFuel    int64 = 10
	burnInput      int64 = 2
	burnFuel       int64 = 1
	burnOutput     int64 = 1
)

// TestProductionIntegration ejercita el subpaquete world/production contra una BD
// real migrada con el seed del Incremento 1, más un fixture propio (tipos mina y
// horno, recetas de extracción y manufactura, yacimientos, ciudad, edificios
// operativos con su stock). Valida el motor completo: construcción diferida,
// extracción y manufactura (físico + contable en lockstep), pausas por
// combustible y por salario, agotamiento de yacimiento, progreso analítico,
// los handlers GET/POST/DELETE y la reconciliación (0 discrepancias).
func TestProductionIntegration(t *testing.T) {
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
		// El test aporta sus propios fixtures industriales (tipos, recetas,
		// yacimiento, ciudad) con las mismas claves naturales que el mundo
		// industrial del seed: se omite este para no colisionar.
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	region := regionID(t, ctx, pool, seed.RegionName)
	iron := productID(t, ctx, pool, "iron_ore")
	coal := productID(t, ctx, pool, "coal")
	steel := createProduct(t, ctx, pool, "steel_ingot", false)

	// Corporación pobre (sin caja): fuerza paused_no_workers.
	poor := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO auth.accounts (id, kind, name) VALUES ($1,'human'::auth.account_kind,$2)`, poor, "Corp Pobre")

	fx := seedFixtures(t, ctx, pool, region, demo, poor, iron, coal, steel)

	simNow := int64(1000)
	sim := &advSim{now: &simNow}
	svc, err := production.NewService(pool, sim, production.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	wopts := production.DefaultWorkerOptions()
	wopts.BuildSimSeconds = buildSimSecs
	worker, err := production.NewWorker(pool, sim, wopts, logger, nil)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	demoMux := newMux(svc, demo, &simNow, logger)

	// ── (b) Extracción: mina + yacimiento → iron_ore, combustible y salario ────
	t.Run("extraccion mueve fisico y contable en lockstep", func(t *testing.T) {
		q := simNow
		batchID := queueViaHTTP(t, demoMux, fx.mineBuilding, fx.extractRecipe, 1)

		coalBefore := inventoryQty(t, ctx, pool, fx.mineBuilding, coal)
		coalSFBefore := stockFree(t, ctx, pool, demo, coal, fx.mineBuilding)
		wsCoalBefore := worldSourceBal(t, ctx, pool, coal)
		wsIronBefore := worldSourceBal(t, ctx, pool, iron)
		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)
		depBefore := depositRemaining(t, ctx, pool, fx.mineDeposit)

		simNow = q + 200 // el batch (started=q, dur=100) vence
		worker.RunOnce(ctx)

		// Físico: iron_ore aparece en el edificio, igual al stock_free contable.
		if got := inventoryQty(t, ctx, pool, fx.mineBuilding, iron); got != extractOutput {
			t.Fatalf("inventario físico iron_ore = %d, esperado %d", got, extractOutput)
		}
		if got := stockFree(t, ctx, pool, demo, iron, fx.mineBuilding); got != extractOutput {
			t.Fatalf("stock_free iron_ore = %d, esperado %d", got, extractOutput)
		}
		// world_source(iron) baja en lo producido (ADR-022).
		if d := wsIronBefore - worldSourceBal(t, ctx, pool, iron); d != extractOutput {
			t.Fatalf("world_source iron_ore bajó %d, esperado %d", d, extractOutput)
		}
		// Yacimiento agota lo extraído.
		if d := depBefore - depositRemaining(t, ctx, pool, fx.mineDeposit); d != extractOutput {
			t.Fatalf("yacimiento bajó %d, esperado %d", d, extractOutput)
		}
		// Combustible consumido (físico y contable) y espejo fuel_stock.
		if d := coalBefore - inventoryQty(t, ctx, pool, fx.mineBuilding, coal); d != extractFuel {
			t.Fatalf("coal físico bajó %d, esperado %d", d, extractFuel)
		}
		if d := coalSFBefore - stockFree(t, ctx, pool, demo, coal, fx.mineBuilding); d != extractFuel {
			t.Fatalf("coal stock_free bajó %d, esperado %d", d, extractFuel)
		}
		if d := worldSourceBal(t, ctx, pool, coal) - wsCoalBefore; d != extractFuel {
			t.Fatalf("world_source coal subió %d, esperado %d (consumo)", d, extractFuel)
		}
		if fs := fuelStockOf(t, ctx, pool, fx.mineBuilding); fs != coalBefore-extractFuel {
			t.Fatalf("fuel_stock espejo = %d, esperado %d", fs, coalBefore-extractFuel)
		}
		// Salario al sink; caja del dueño baja lo mismo.
		wantWage := int64(extractWorkers) * baseSalary
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != wantWage {
			t.Fatalf("sink subió %d, esperado salario %d", d, wantWage)
		}
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != wantWage {
			t.Fatalf("caja bajó %d, esperado salario %d", d, wantWage)
		}
		// Lote completado.
		status, done := batchState(t, ctx, pool, uuid.MustParse(batchID))
		if status != "completed" || done != 1 {
			t.Fatalf("lote = (%s, done %d), esperado (completed, 1)", status, done)
		}
		// Contabilidad cuadrada por activo (suma de saldos por producto == 0).
		for _, p := range []uuid.UUID{iron, coal} {
			if s := productAssetSum(t, ctx, pool, p); s != 0 {
				t.Fatalf("suma de saldos del producto %s = %d, esperado 0", p, s)
			}
		}
	})

	// ── (c) Manufactura: horno consume iron_ore(2)+coal(1) → steel_ingot(1) ────
	t.Run("manufactura consume insumos y combustible", func(t *testing.T) {
		q := simNow
		if _, err := svc.QueueBatches(ctx, demo, fx.furnaceBuilding, production.BatchInput{RecipeID: fx.burnRecipe, BatchesQueued: 1}); err != nil {
			t.Fatalf("QueueBatches horno: %v", err)
		}
		ironBefore := inventoryQty(t, ctx, pool, fx.furnaceBuilding, iron)
		coalBefore := inventoryQty(t, ctx, pool, fx.furnaceBuilding, coal)
		wsSteelBefore := worldSourceBal(t, ctx, pool, steel) // 0 o inexistente
		sinkBefore := sinkBalance(t, ctx, pool)

		simNow = q + 200
		worker.RunOnce(ctx)

		if got := inventoryQty(t, ctx, pool, fx.furnaceBuilding, steel); got != burnOutput {
			t.Fatalf("steel físico = %d, esperado %d", got, burnOutput)
		}
		if got := stockFree(t, ctx, pool, demo, steel, fx.furnaceBuilding); got != burnOutput {
			t.Fatalf("steel stock_free = %d, esperado %d", got, burnOutput)
		}
		if got := wsSteelBefore - worldSourceBal(t, ctx, pool, steel); got != burnOutput {
			t.Fatalf("world_source steel bajó %d, esperado %d", got, burnOutput)
		}
		if d := ironBefore - inventoryQty(t, ctx, pool, fx.furnaceBuilding, iron); d != burnInput {
			t.Fatalf("iron consumido %d, esperado %d", d, burnInput)
		}
		if d := coalBefore - inventoryQty(t, ctx, pool, fx.furnaceBuilding, coal); d != burnFuel {
			t.Fatalf("coal consumido %d, esperado %d", d, burnFuel)
		}
		wantWage := int64(burnWorkers) * baseSalary
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != wantWage {
			t.Fatalf("sink subió %d, esperado salario %d", d, wantWage)
		}
		if s := productAssetSum(t, ctx, pool, steel); s != 0 {
			t.Fatalf("suma de saldos de steel = %d, esperado 0", s)
		}
	})

	// ── (d) Pausa por combustible insuficiente ────────────────────────────────
	t.Run("pausa por combustible: paused_no_fuel sin producir ni cobrar", func(t *testing.T) {
		q := simNow
		batchID, err := svc.QueueBatches(ctx, demo, fx.mineNoFuel, production.BatchInput{RecipeID: fx.extractRecipe, BatchesQueued: 1})
		if err != nil {
			t.Fatalf("QueueBatches mineNoFuel: %v", err)
		}
		sinkBefore := sinkBalance(t, ctx, pool)

		simNow = q + 200
		worker.RunOnce(ctx)

		status, done := batchState(t, ctx, pool, batchID.ID)
		if status != "paused_no_fuel" || done != 0 {
			t.Fatalf("lote = (%s, done %d), esperado (paused_no_fuel, 0)", status, done)
		}
		if got := inventoryQty(t, ctx, pool, fx.mineNoFuel, iron); got != 0 {
			t.Fatalf("no debió producir iron; hay %d", got)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != 0 {
			t.Fatalf("no debió cobrar salario; sink subió %d", d)
		}
	})

	// ── (e) Pausa por fondos insuficientes para el salario ────────────────────
	t.Run("pausa por salario: paused_no_workers", func(t *testing.T) {
		q := simNow
		batchID, err := svc.QueueBatches(ctx, poor, fx.poorBuilding, production.BatchInput{RecipeID: fx.extractRecipe, BatchesQueued: 1})
		if err != nil {
			t.Fatalf("QueueBatches poorBuilding: %v", err)
		}
		coalBefore := inventoryQty(t, ctx, pool, fx.poorBuilding, coal)

		simNow = q + 200
		worker.RunOnce(ctx)

		status, done := batchState(t, ctx, pool, batchID.ID)
		if status != "paused_no_workers" || done != 0 {
			t.Fatalf("lote = (%s, done %d), esperado (paused_no_workers, 0)", status, done)
		}
		// Ni combustible ni yacimiento se tocaron (pausa antes de mutar).
		if got := inventoryQty(t, ctx, pool, fx.poorBuilding, coal); got != coalBefore {
			t.Fatalf("no debió consumir combustible; coal %d != %d", got, coalBefore)
		}
	})

	// ── (f) Agotamiento de yacimiento: el lote no avanza (running) ────────────
	t.Run("agotamiento de yacimiento no produce ni avanza", func(t *testing.T) {
		q := simNow
		batchID, err := svc.QueueBatches(ctx, demo, fx.mineEmpty, production.BatchInput{RecipeID: fx.extractRecipe, BatchesQueued: 1})
		if err != nil {
			t.Fatalf("QueueBatches mineEmpty: %v", err)
		}
		depBefore := depositRemaining(t, ctx, pool, fx.emptyDeposit)

		simNow = q + 200
		worker.RunOnce(ctx)

		status, done := batchState(t, ctx, pool, batchID.ID)
		if status != "running" || done != 0 {
			t.Fatalf("lote = (%s, done %d), esperado (running, 0) por yacimiento agotado", status, done)
		}
		if got := inventoryQty(t, ctx, pool, fx.mineEmpty, iron); got != 0 {
			t.Fatalf("no debió extraer; hay %d de iron", got)
		}
		if got := depositRemaining(t, ctx, pool, fx.emptyDeposit); got != depBefore {
			t.Fatalf("el yacimiento cambió (%d → %d) pese a no poder extraer", depBefore, got)
		}
	})

	// ── (g) Progreso analítico derivado del lote en curso ─────────────────────
	t.Run("progreso y eta derivados analiticamente", func(t *testing.T) {
		q := simNow
		if _, err := svc.QueueBatches(ctx, demo, fx.progressBuilding, production.BatchInput{RecipeID: fx.burnRecipe, BatchesQueued: 2}); err != nil {
			t.Fatalf("QueueBatches progress: %v", err)
		}
		// A mitad del batch en curso (dur=100): progreso ~50%, eta = q+100.
		simNow = q + 50
		batches, _, err := svc.ListBatches(ctx, demo, fx.progressBuilding, production.BatchFilter{})
		if err != nil {
			t.Fatalf("ListBatches: %v", err)
		}
		if len(batches) != 1 {
			t.Fatalf("se esperaba 1 lote, hay %d", len(batches))
		}
		b := batches[0]
		if b.Status != "running" || b.ProgressPct == nil || b.EtaSim == nil {
			t.Fatalf("lote running sin derivados: %+v", b)
		}
		if *b.ProgressPct < 45 || *b.ProgressPct > 55 {
			t.Fatalf("progress_pct = %.1f, esperado ~50", *b.ProgressPct)
		}
		if *b.EtaSim != q+batchSimSecs {
			t.Fatalf("eta_sim = %d, esperado %d", *b.EtaSim, q+batchSimSecs)
		}
		// Vía HTTP el progreso también viaja en la respuesta.
		list, _ := listViaHTTP(t, demoMux, fx.progressBuilding)
		if len(list) != 1 || list[0].ProgressPct == nil {
			t.Fatalf("GET no derivó el progreso: %+v", list)
		}
	})

	// ── Handlers: encolar, listar y cancelar (409 al recancelar) ──────────────
	t.Run("handlers encolan, listan y cancelan", func(t *testing.T) {
		batchID := queueViaHTTP(t, demoMux, fx.furnaceBuilding, fx.burnRecipe, 3)
		// Cancelar la orden aún no producida → 200.
		rec := do(t, demoMux, http.MethodDelete, "/world/production-batches/"+batchID, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE: status %d (body %s)", rec.Code, rec.Body.String())
		}
		if b := dataOf[batchDTO](t, rec); b.Status != "cancelled" {
			t.Fatalf("estado tras cancelar = %q, esperado cancelled", b.Status)
		}
		// Recancelar → 409.
		rec = do(t, demoMux, http.MethodDelete, "/world/production-batches/"+batchID, "")
		if rec.Code != http.StatusConflict {
			t.Fatalf("recancelar: status %d, esperado 409 (body %s)", rec.Code, rec.Body.String())
		}
		// Encolar sobre edificio ajeno (norte) → 403 vía servicio.
		norte := accountID(t, ctx, pool, seed.DefaultTraderName)
		if _, err := svc.QueueBatches(ctx, norte, fx.furnaceBuilding, production.BatchInput{RecipeID: fx.burnRecipe, BatchesQueued: 1}); err == nil {
			t.Fatal("encolar sobre edificio ajeno no devolvió error")
		}
	})

	// ── (a) Construcción diferida: under_construction → operational ───────────
	t.Run("construccion diferida completa el edificio", func(t *testing.T) {
		// Antes del tiempo de construcción: sigue under_construction.
		simNow = fx.constructAtSim + buildSimSecs - 1
		worker.RunOnce(ctx)
		if s := buildingStatusOf(t, ctx, pool, fx.constructBuilding); s != "under_construction" {
			t.Fatalf("antes de tiempo: estado %q, esperado under_construction", s)
		}
		// Cumplido el tiempo: pasa a operational.
		simNow = fx.constructAtSim + buildSimSecs
		worker.RunOnce(ctx)
		if s := buildingStatusOf(t, ctx, pool, fx.constructBuilding); s != "operational" {
			t.Fatalf("tras el tiempo: estado %q, esperado operational", s)
		}
	})

	// ── (h) Reconciliación física↔contable: 0 discrepancias ───────────────────
	t.Run("reconciliacion reporta 0 discrepancias", func(t *testing.T) {
		n, err := worker.Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
		if n != 0 {
			t.Fatalf("reconciliación halló %d discrepancias, esperado 0", n)
		}
	})

	// ── (i) Regresión: el stock RESERVADO no dispara divergencia (ADR-004) ─────
	// Al publicar/aceptar una venta (flujo CCRI del Incremento 1) el stock pasa
	// stock_free → stock_reserved en el MISMO almacén, sin mover el físico
	// (building_inventories). El comprometible = stock_free + stock_reserved
	// sigue igualando al físico, así que la ventana reservada NO debe reportar
	// divergencia. Antes del fix, la subconsulta sumaba solo stock_free y esta
	// ventana producía un falso positivo (gauge > 0, logs ERROR) hasta liquidar.
	t.Run("stock reservado no dispara divergencia de reconciliacion", func(t *testing.T) {
		reservedProduct := createProduct(t, ctx, pool, "recon_reserved_ore", false)
		// Estado reconciliado de partida: físico == stock_free == 150.
		seedStock(t, ctx, pool, demo, fx.mineBuilding, reservedProduct, 150)
		if n, err := worker.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("baseline: Reconcile=%d err=%v, esperado 0 sin error", n, err)
		}
		// Reservar 100 (stock_free → stock_reserved) en el mismo almacén, como
		// hace una publicación de venta (asiento publication_lock). El físico
		// (building_inventories) NO cambia: los bienes siguen en el almacén.
		sfID := stockFreeAccountID(t, ctx, pool, demo, reservedProduct, fx.mineBuilding)
		srID := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `
			INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
			VALUES ($1,'stock_reserved',$2,$3,$4)`, srID, demo, reservedProduct, fx.mineBuilding)
		postLedger(t, ctx, pool, "publication_lock", []ledgerEntry{{sfID, -100}, {srID, 100}})
		// Físico intacto; comprometible = 50 (free) + 100 (reserved) = 150.
		if got := inventoryQty(t, ctx, pool, fx.mineBuilding, reservedProduct); got != 150 {
			t.Fatalf("físico tras reservar = %d, esperado 150 (no se mueve)", got)
		}
		if got := stockFree(t, ctx, pool, demo, reservedProduct, fx.mineBuilding); got != 50 {
			t.Fatalf("stock_free tras reservar = %d, esperado 50", got)
		}
		n, err := worker.Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile durante ventana reservada: %v", err)
		}
		if n != 0 {
			t.Fatalf("ventana reservada halló %d divergencias, esperado 0 (falso positivo del bug)", n)
		}
	})

	// ── (j) CUSTODIA de flete: cuenta EN EL LADO FÍSICO (Incremento 8) ─────────
	// Un CCRI-Flete en vuelo saca la carga del almacén (building_inventories baja) y
	// la sostiene en una cuenta 'custody' (ledger). El lado físico de la
	// reconciliación DEBE volver a contarla vía el cargamento de flete atribuido al
	// almacén de origen de la cuenta de custodia; así físico(shipment) == custody y
	// no aparece divergencia. Antes del Incremento 8, custody quedaba fuera del lado
	// físico y un flete en vuelo disparaba un falso positivo.
	t.Run("custodia de flete cuenta en el lado fisico", func(t *testing.T) {
		norte := accountID(t, ctx, pool, seed.DefaultTraderName) // transportista (carrier)
		custProduct := createProduct(t, ctx, pool, "recon_custody_ore", false)
		const qty = int64(40)
		// building_inventories[mineBuilding][custProduct] = 0 (la carga dejó el almacén).
		// Cuenta de custodia (kind custody) en el almacén de origen con balance qty.
		custAcct := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
			VALUES ($1,'custody',$2,$3,$4)`, custAcct, demo, custProduct, fx.mineBuilding)
		ws := ensureWorldSource(t, ctx, pool, custProduct)
		postLedger(t, ctx, pool, "custody_load", []ledgerEntry{{custAcct, qty}, {ws, -qty}})

		// Cuentas satélite mínimas y contrato de flete que referencia la custodia.
		escrow := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id) VALUES ($1,'escrow',$2)`, escrow, demo)
		guarantee := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id) VALUES ($1,'guarantee',$2)`, guarantee, norte)
		node := anyNodeID(t, ctx, pool)
		freightID := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `
			INSERT INTO ledger.freight_contracts
			  (id, channel, shipper_account_id, carrier_account_id, origin_node_id, destination_node_id,
			   freight_price, declared_value, deadline_sim, status,
			   escrow_account_id, carrier_guarantee_account_id, custody_account_id, confirmed_at_sim)
			VALUES ($1,'board',$2,$3,$4,$4,100,1000,9000000,'active',$5,$6,$7,0)`,
			freightID, demo, norte, node, escrow, guarantee, custAcct)
		// Cargamento de flete en vuelo (freight_contract_id, sin contract_id).
		exec(t, ctx, pool, `INSERT INTO world.shipments
			(id, owner_account_id, product_id, quantity, freight_contract_id, at_node_id, status, updated_at_sim)
			VALUES ($1,$2,$3,$4,$5,$6,'in_warehouse',0)`,
			uuid.Must(uuid.NewV7()), demo, custProduct, qty, freightID, node)

		// El físico (0 en el almacén + qty del cargamento de flete) cuadra con la
		// custodia contable (qty): sin divergencia.
		n, err := worker.Reconcile(ctx)
		if err != nil {
			t.Fatalf("Reconcile con custodia de flete en vuelo: %v", err)
		}
		if n != 0 {
			t.Fatalf("custodia de flete halló %d divergencias, esperado 0 (custody debe contar en el lado físico)", n)
		}
	})

	// ── (k) Divergencia TRANSITORIA (una sola pasada) NO escala ───────────────
	// La ventana ~250 ms entre la entrega física y su asiento contable produce una
	// divergencia que aparece en UNA pasada y desaparece en la siguiente. Con
	// ReconcileGrace=2 no debe escalar (Reconcile devuelve 0) ni en la pasada en que
	// existe ni tras resolverse.
	t.Run("divergencia transitoria de una pasada no escala", func(t *testing.T) {
		transientProduct := createProduct(t, ctx, pool, "recon_transient_ore", false)
		// Físico 50 sin cuenta de stock: físico(50) <> contable(0) → divergencia.
		exec(t, ctx, pool, `INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
			VALUES ($1,$2,50,0)`, fx.mineBuilding, transientProduct)
		if n, err := worker.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("pasada 1 (transitoria): Reconcile=%d err=%v, esperado 0 (streak 1 < gracia 2)", n, err)
		}
		// La divergencia se resuelve antes de la siguiente pasada (el asiento llegó):
		// añade la cuenta de stock que la cuadra.
		sf := uuid.Must(uuid.NewV7())
		exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
			VALUES ($1,'stock_free',$2,$3,$4)`, sf, demo, transientProduct, fx.mineBuilding)
		ws := ensureWorldSource(t, ctx, pool, transientProduct)
		postLedger(t, ctx, pool, "production_output", []ledgerEntry{{sf, 50}, {ws, -50}})
		if n, err := worker.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("pasada 2 (resuelta): Reconcile=%d err=%v, esperado 0", n, err)
		}
	})

	// ── (l) Divergencia PERSISTENTE (>= gracia pasadas consecutivas) SÍ escala ──
	t.Run("divergencia persistente escala tras la gracia", func(t *testing.T) {
		persistProduct := createProduct(t, ctx, pool, "recon_persist_ore", false)
		// Físico 77 sin cuenta de stock: divergencia que NO se resuelve.
		exec(t, ctx, pool, `INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
			VALUES ($1,$2,77,0)`, fx.mineBuilding, persistProduct)
		// Pasada 1: dentro de la gracia (streak 1 < 2) → no escala.
		if n, err := worker.Reconcile(ctx); err != nil || n != 0 {
			t.Fatalf("pasada 1 (persistente): Reconcile=%d err=%v, esperado 0", n, err)
		}
		// Pasada 2: persiste (streak 2 >= gracia 2) → escala a ERROR + gauge.
		n, err := worker.Reconcile(ctx)
		if err != nil {
			t.Fatalf("pasada 2 (persistente): %v", err)
		}
		if n != 1 {
			t.Fatalf("pasada 2 (persistente): Reconcile=%d, esperado 1 (divergencia escalada)", n)
		}
	})
}

// anyNodeID devuelve el id de un network_node cualquiera (para FKs de fixtures que
// no dependen de su geometría).
func anyNodeID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.network_nodes LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("anyNodeID: %v", err)
	}
	return id
}

// ─── Fixtures ─────────────────────────────────────────────────────────────────

type fixtures struct {
	mineType, furnaceType         uuid.UUID
	extractRecipe, burnRecipe     uuid.UUID
	concession                    uuid.UUID
	mineBuilding, furnaceBuilding uuid.UUID
	mineNoFuel, poorBuilding      uuid.UUID
	mineEmpty, progressBuilding   uuid.UUID
	constructBuilding             uuid.UUID
	constructAtSim                int64
	mineDeposit, emptyDeposit     uuid.UUID
}

func seedFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, demo, poor, iron, coal, steel uuid.UUID) fixtures {
	t.Helper()
	fx := fixtures{
		mineType:       uuid.Must(uuid.NewV7()),
		furnaceType:    uuid.Must(uuid.NewV7()),
		extractRecipe:  uuid.Must(uuid.NewV7()),
		burnRecipe:     uuid.Must(uuid.NewV7()),
		concession:     uuid.Must(uuid.NewV7()),
		constructAtSim: 10_000_000, // muy por encima de los sim-time de producción
	}
	// Tipos: mina (near_resource → extracción) y horno (manufactura). level_curve
	// vacía ⇒ defaults (speed/storage = nivel); base_storage holgado.
	exec(t, ctx, pool, `
		INSERT INTO world.building_types (id, code, name, footprint_cells, max_level, base_storage, placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1,'iron_mine','Mina',4,4,1000000,'{"near_resource":"iron_ore","max_distance_m":5000}'::jsonb,'{}'::jsonb,80000,100)`, fx.mineType)
	exec(t, ctx, pool, `
		INSERT INTO world.building_types (id, code, name, footprint_cells, max_level, base_storage, placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1,'blast_furnace','Horno',6,4,1000000,'{}'::jsonb,'{}'::jsonb,120000,200)`, fx.furnaceType)

	// Recetas: extracción (output iron_ore, fuel coal) y manufactura
	// (input iron_ore, fuel coal, output steel_ingot).
	exec(t, ctx, pool, `
		INSERT INTO world.recipes (id, building_type_id, code, name, batch_sim_seconds, fuel_product_id, fuel_per_batch, workers_required, min_city_level)
		VALUES ($1,$2,'extract_iron_ore','Extracción',$3,$4,$5,$6,1)`, fx.extractRecipe, fx.mineType, batchSimSecs, coal, extractFuel, extractWorkers)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1,$2,'output',$3)`, fx.extractRecipe, iron, extractOutput)
	exec(t, ctx, pool, `
		INSERT INTO world.recipes (id, building_type_id, code, name, batch_sim_seconds, fuel_product_id, fuel_per_batch, workers_required, min_city_level)
		VALUES ($1,$2,'burn_steel','Fundición',$3,$4,$5,$6,1)`, fx.burnRecipe, fx.furnaceType, batchSimSecs, coal, burnFuel, burnWorkers)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1,$2,'input',$3)`, fx.burnRecipe, iron, burnInput)
	exec(t, ctx, pool, `INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity) VALUES ($1,$2,'output',$3)`, fx.burnRecipe, steel, burnOutput)

	// Ciudad nivel 5 (cualificación y salario base).
	cityAcc := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO auth.accounts (id, kind, name) VALUES ($1,'city'::auth.account_kind,$2)`, cityAcc, "Ciudad Test")
	exec(t, ctx, pool, `
		INSERT INTO world.cities (id, region_id, account_id, name, location, level, population, supply_index, influence_radius_m, base_salary)
		VALUES ($1,$2,$3,'Ciudad Test',ST_GeomFromText('POINT(25000 25000)',0),5,50000,1.0,50000,$4)`, uuid.Must(uuid.NewV7()), region, cityAcc, baseSalary)

	// Concesión de Demo (parcela amplia; los edificios se insertan directamente).
	exec(t, ctx, pool, `
		INSERT INTO world.land_concessions (id, region_id, holder_account_id, parcel, canon_amount, period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1,$2,$3,ST_GeomFromText('POLYGON((0 0,90000 0,90000 90000,0 90000,0 0))',0),1000,90,$4,'active',0)`, fx.concession, region, demo, int64(9_000_000))

	// Edificios operativos con su nodo. Yacimientos junto al nodo de cada mina.
	fx.mineBuilding = insertBuilding(t, ctx, pool, region, demo, fx.concession, fx.mineType, "operational", 20000, 20000)
	insertNode(t, ctx, pool, region, fx.mineBuilding, 20000, 20000)
	fx.mineDeposit = insertDeposit(t, ctx, pool, region, iron, 20000, 20000, 1_000_000)
	seedStock(t, ctx, pool, demo, fx.mineBuilding, coal, 1000)

	fx.furnaceBuilding = insertBuilding(t, ctx, pool, region, demo, fx.concession, fx.furnaceType, "operational", 30000, 30000)
	insertNode(t, ctx, pool, region, fx.furnaceBuilding, 30000, 30000)
	seedStock(t, ctx, pool, demo, fx.furnaceBuilding, iron, 1000)
	seedStock(t, ctx, pool, demo, fx.furnaceBuilding, coal, 1000)

	fx.mineNoFuel = insertBuilding(t, ctx, pool, region, demo, fx.concession, fx.mineType, "operational", 40000, 40000)
	insertNode(t, ctx, pool, region, fx.mineNoFuel, 40000, 40000)
	insertDeposit(t, ctx, pool, region, iron, 40000, 40000, 1_000_000)
	// sin combustible: fuerza paused_no_fuel

	fx.poorBuilding = insertBuilding(t, ctx, pool, region, poor, fx.concession, fx.mineType, "operational", 45000, 45000)
	insertNode(t, ctx, pool, region, fx.poorBuilding, 45000, 45000)
	insertDeposit(t, ctx, pool, region, iron, 45000, 45000, 1_000_000)
	seedStock(t, ctx, pool, poor, fx.poorBuilding, coal, 1000) // combustible ok; falla el salario

	fx.mineEmpty = insertBuilding(t, ctx, pool, region, demo, fx.concession, fx.mineType, "operational", 50000, 50000)
	insertNode(t, ctx, pool, region, fx.mineEmpty, 50000, 50000)
	fx.emptyDeposit = insertDeposit(t, ctx, pool, region, iron, 50000, 50000, 100) // < output: agotado
	seedStock(t, ctx, pool, demo, fx.mineEmpty, coal, 1000)

	fx.progressBuilding = insertBuilding(t, ctx, pool, region, demo, fx.concession, fx.furnaceType, "operational", 60000, 60000)
	insertNode(t, ctx, pool, region, fx.progressBuilding, 60000, 60000)
	seedStock(t, ctx, pool, demo, fx.progressBuilding, iron, 1000)
	seedStock(t, ctx, pool, demo, fx.progressBuilding, coal, 1000)

	// Edificio en construcción (para el barrido diferido); sin nodo ni stock.
	fx.constructBuilding = insertBuildingAt(t, ctx, pool, region, demo, fx.concession, fx.furnaceType, "under_construction", 70000, 70000, fx.constructAtSim)
	return fx
}

// ─── Helpers de inserción ─────────────────────────────────────────────────────

func insertBuilding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, owner, concession, btype uuid.UUID, status string, x, y int) uuid.UUID {
	return insertBuildingAt(t, ctx, pool, region, owner, concession, btype, status, x, y, 0)
}

func insertBuildingAt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, owner, concession, btype uuid.UUID, status string, x, y int, updatedAtSim int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	wkt := fmt.Sprintf("POLYGON((%d %d,%d %d,%d %d,%d %d,%d %d))", x, y, x+100, y, x+100, y+100, x, y+100, x, y)
	exec(t, ctx, pool, `
		INSERT INTO world.buildings (id, owner_account_id, region_id, concession_id, building_type_id, footprint, level, status, condition_pct, fuel_stock, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,ST_GeomFromText($6,0),1,$7::world.building_status,100,0,$8)`, id, owner, region, concession, btype, wkt, status, updatedAtSim)
	return id
}

func insertNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, building uuid.UUID, x, y int) {
	t.Helper()
	exec(t, ctx, pool, `
		INSERT INTO world.network_nodes (id, kind, region_id, building_id, location)
		VALUES ($1,'mine'::world.node_kind,$2,$3,ST_GeomFromText($4,0))`,
		uuid.Must(uuid.NewV7()), region, building, fmt.Sprintf("POINT(%d %d)", x, y))
}

func insertDeposit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region, product uuid.UUID, x, y int, remaining int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `
		INSERT INTO world.resource_deposits (id, region_id, product_id, location, initial_amount, remaining_amount, renewable, regen_per_sim_day)
		VALUES ($1,$2,$3,ST_GeomFromText($4,0),$5,$5,false,0)`, id, region, product, fmt.Sprintf("POINT(%d %d)", x, y), remaining)
	return id
}

func createProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string, isFuel bool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `
		INSERT INTO world.products (id, code, name, class, unit_volume, base_price, price_floor, price_ceiling, is_fuel)
		VALUES ($1,$2,$2,'basic',1,100,10,1000,$3)`, id, code, isFuel)
	return id
}

// seedStock funda el stock de un producto en un edificio de forma coherente
// físico↔contable: crea la cuenta stock_free, asienta production_output
// (+stock_free / -world_source) e inserta la fila de inventario físico.
func seedStock(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, building, product uuid.UUID, qty int64) {
	t.Helper()
	ws := ensureWorldSource(t, ctx, pool, product)
	sf := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `
		INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
		VALUES ($1,'stock_free',$2,$3,$4)`, sf, owner, product, building)
	postLedger(t, ctx, pool, "production_output", []ledgerEntry{{sf, qty}, {ws, -qty}})
	exec(t, ctx, pool, `
		INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1,$2,$3,0)`, building, product, qty)
}

func ensureWorldSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, product uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='world_source' AND product_id=$1`, product).Scan(&id)
	if err == nil {
		return id
	}
	bank := accountID(t, ctx, pool, seed.CentralBankName)
	id = uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id) VALUES ($1,'world_source',$2,$3)`, id, bank, product)
	return id
}

type ledgerEntry struct {
	account uuid.UUID
	amount  int64
}

// postLedger asienta una transacción con sus partidas en UNA transacción SQL
// (el trigger diferido de doble entrada por activo se valida en el COMMIT).
func postLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, entries []ledgerEntry) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	txID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1,$2::ledger.transaction_kind,0)`, txID, kind); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(ctx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1,$2,$3,$4)`, uuid.Must(uuid.NewV7()), txID, e.account, e.amount); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit ledger: %v", err)
	}
}

// ─── Consultas de aserción ────────────────────────────────────────────────────

func inventoryQty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building, product uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT quantity FROM world.building_inventories WHERE building_id=$1 AND product_id=$2),0)`, building, product)
}

func stockFree(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, building uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT balance FROM ledger.accounts WHERE kind='stock_free' AND owner_account_id=$1 AND product_id=$2 AND warehouse_building_id=$3),0)`, owner, product, building)
}

// stockFreeAccountID devuelve el id de la cuenta stock_free de (dueño, producto,
// almacén) — la unicidad parcial ux_accounts_stock_free garantiza que hay una.
func stockFreeAccountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, building uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='stock_free' AND owner_account_id=$1 AND product_id=$2 AND warehouse_building_id=$3`, owner, product, building).Scan(&id); err != nil {
		t.Fatalf("stockFreeAccountID: %v", err)
	}
	return id
}

func worldSourceBal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, product uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT balance FROM ledger.accounts WHERE kind='world_source' AND product_id=$1),0)`, product)
}

func productAssetSum(t *testing.T, ctx context.Context, pool *pgxpool.Pool, product uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE(SUM(balance),0) FROM ledger.accounts WHERE product_id=$1`, product)
}

func depositRemaining(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT remaining_amount FROM world.resource_deposits WHERE id=$1`, id)
}

func fuelStockOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT fuel_stock FROM world.buildings WHERE id=$1`, building)
}

func cashBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, acc uuid.UUID) int64 {
	return scalarInt(t, ctx, pool, `SELECT COALESCE((SELECT balance FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1),0)`, acc)
}

func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	return scalarInt(t, ctx, pool, `SELECT balance FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`)
}

func batchState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (string, int32) {
	t.Helper()
	var status string
	var done int32
	if err := pool.QueryRow(ctx, `SELECT status::text, batches_done FROM world.production_batches WHERE id=$1`, id).Scan(&status, &done); err != nil {
		t.Fatalf("estado del lote %s: %v", id, err)
	}
	return status, done
}

func buildingStatusOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(ctx, `SELECT status::text FROM world.buildings WHERE id=$1`, id).Scan(&s); err != nil {
		t.Fatalf("estado del edificio %s: %v", id, err)
	}
	return s
}

func scalarInt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return v
}

// ─── HTTP helpers ─────────────────────────────────────────────────────────────

type batchDTO struct {
	ID            string   `json:"id"`
	BuildingID    string   `json:"building_id"`
	Status        string   `json:"status"`
	BatchesQueued int32    `json:"batches_queued"`
	BatchesDone   int32    `json:"batches_done"`
	ProgressPct   *float64 `json:"progress_pct"`
	EtaSim        *int64   `json:"eta_sim"`
}

func queueViaHTTP(t *testing.T, mux *http.ServeMux, building, recipe uuid.UUID, n int) string {
	t.Helper()
	rec := do(t, mux, http.MethodPost, "/world/buildings/"+building.String()+"/production-batches",
		fmt.Sprintf(`{"recipe_id":%q,"batches_queued":%d}`, recipe, n))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST batch: status %d (body %s)", rec.Code, rec.Body.String())
	}
	return dataOf[batchDTO](t, rec).ID
}

func listViaHTTP(t *testing.T, mux *http.ServeMux, building uuid.UUID) ([]batchDTO, string) {
	t.Helper()
	rec := do(t, mux, http.MethodGet, "/world/buildings/"+building.String()+"/production-batches", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET batches: status %d (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []batchDTO `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET batches: json inválido: %v", err)
	}
	return resp.Data, resp.Meta.NextCursor
}

// ─── Infra del test ───────────────────────────────────────────────────────────

type advSim struct{ now *int64 }

func (a *advSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(*a.now) }

type fakeMeta struct{ now *int64 }

func (m fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(simtime.SimTime(*m.now)), SimTimeSeconds: *m.now, ServerTime: time.Now().UTC()}
}

type fakeIdentity struct{ acc uuid.UUID }

func (i fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return i.acc, true }

func newMux(svc *production.Service, acc uuid.UUID, now *int64, logger *slog.Logger) *http.ServeMux {
	h := production.NewHandlers(svc, fakeIdentity{acc}, fakeMeta{now}, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func dataOf[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decodificando data: %v (body %s)", err, rec.Body.String())
	}
	return resp.Data
}

func accountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("cuenta %q: %v", name, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func productID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatalf("producto %q: %v", code, err)
	}
	return id
}

func exec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldproductiontest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	if _, err := migrate.New(conn, "../../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
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
