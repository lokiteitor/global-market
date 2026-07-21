// Cascada de insolvencia del Incremento 6a (GDD 5.9 / 11.2) de proceso a proceso
// contra una BD real: el gateway COMPLETO (internal/gateway.BuildServer) servido
// con httptest y TODOS los motores de la cascada corriendo como goroutines —el
// motor de enforcement (mantenimiento → degradación → abandono → canon impagado →
// gracia → embargo, internal/world/enforcement), el consumidor system_liquidator
// que subasta el stock embargado (internal/contracts) y el worker CCRI que
// resuelve el sorteo de la subasta— todos guiados por el mismo reloj de simulación
// CONGELADO y avanzado por SQL (patrón del resto de la suite). Ningún mock.
//
// Historia (una corporación POBRE sin ingresos, Demo, sobre el mundo sembrado):
//
//	(A) El mantenimiento la DRENA (cash → sink, cobrando solo lo disponible) y,
//	    sin fondos, su almacén DEGRADA operational → abandoned; el canon impagado
//	    marca la concesión delinquent (arranca la gracia). La caja jamás < 0.
//	(B) Agotada la gracia, el EMBARGO congela el edificio (seized), revierte la
//	    concesión (reverted) y emite building.seized con el stock libre in situ.
//	(C) El system_liquidator consume building.seized, transfiere el stock al banco
//	    central y lo PUBLICA como oferta sell del sistema al precio de remate
//	    (visible en el tablón del CCRI).
//	(D) OTRA corporación activa (Norte) COMPRA esa oferta; el sorteo la liquida in
//	    situ: el stock roto pasa a un jugador activo y el banco central COBRA
//	    (proceeds + garantía retornada) — efecto sink/absorción.
//	(E) La parcela quedó LIBRE: un POST de concesión de Norte sobre la parcela de
//	    Demo, que daba 409 CONCESSION_OVERLAP mientras la concesión estaba vigente,
//	    ahora tiene éxito.
//
// Y una sección de RETIRO DE BOTS (ADR-024, cierre de la cascada del lado bots):
// un bot aprovisionado llevado a la insolvencia-inactividad es retirado por el
// RetirementJob (caja absorbida al banco central, cuenta 'retired', bot.retired).
//
// Verificación contable final sobre la BD quiescente: la caja de la corp pobre
// nunca fue negativa, el ledger cierra a cero por activo y la reconciliación
// física↔contable es 0.
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/notify"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/world/enforcement"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// Parámetros de la cascada que el test reproduce y calibra para no depender de
// las semanas reales de gracia del default (la reduce a 2 días-sim).
const (
	// insSimBase es el ancla del reloj congelado (sim-time cómodo: los días-sim
	// caen redondos y las cuentas contables se leen sin ruido).
	insSimBase int64 = 1_000_000
	// insSimDay es un día-sim en segundos de simulación (world.sim_clock).
	insSimDay int64 = 86_400
	// insGrace es la gracia de embargo del test (2 días-sim), calibrada corta para
	// no avanzar 14 días; el default de producción son semanas reales en sim-time.
	insGrace int64 = 2 * insSimDay

	// insPoorCash es la caja mínima que se le deja a Demo: cubre ~2 días de
	// mantenimiento (50/día) y luego se agota, degradando el edificio. El canon
	// (1000) ya no se puede pagar (impago → delinquent).
	insPoorCash int64 = 100

	// Stock sembrado en el almacén de cada corporación (mismas cifras que el seed).
	insSeizedIron int64 = 5_000
	insSeizedCoal int64 = 3_000

	// remateIron es el precio de remate de la subasta de iron_ore:
	// base_price(100) × II_LIQUIDATION_PRICE_BP(6000) / 10000 = 60.
	remateIron int64 = 60
)

func TestInsolvencyCascadeE2E(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test E2E omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Mundo sembrado (Demo y Norte: caja 1M, concesión, almacén operativo con
	//    stock: 5000 iron_ore + 3000 coal cada una) ────────────────────────────
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: demoName, DemoSecret: demoSecret,
		TraderName: traderName, TraderSecret: traderSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, insSimBase)

	// ── IDs del mundo sembrado ───────────────────────────────────────────────
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, demoName)
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, traderName)
	bankID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, seed.CentralBankName)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	ironOreID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	demoNode, demoWarehouse := warehouseNodeOf(t, ctx, pool, demoID)
	demoConcession := queryUUID(t, ctx, pool,
		`SELECT id FROM world.land_concessions WHERE holder_account_id = $1 AND status = 'active'`, demoID)

	// La caja de sistema del banco central quedó dotada por el seed (garantía de
	// las ofertas sell del sistema, Incremento 6a): sin ella el liquidador tendría
	// que emitir colateral por operación.
	if got := cashOf(t, ctx, pool, bankID); got != seed.CentralBankTreasury {
		t.Fatalf("tesorería del banco central: %d, esperado %d", got, seed.CentralBankTreasury)
	}

	// ── Gateway COMPLETO servido con httptest ────────────────────────────────
	contractsOpts := contracts.DefaultOptions()
	contractsOpts.DrawWindowSeconds = 2                 // ventana de sorteo (wall-clock) holgada para aceptar
	contractsOpts.MicroWindowSeconds = 2                //
	contractsOpts.PublicationTTLSimSeconds = 10_000_000 // la subasta no expira durante el test
	notifyOpts := notify.DefaultOptions()
	notifyOpts.RouterInterval = 50 * time.Millisecond
	server, err := gateway.BuildServer(gateway.Deps{
		Pool:     pool,
		Logger:   logger,
		Registry: prometheus.NewRegistry(),
		Options: withWorldDefaults(gateway.Options{
			Auth:        auth.Options{LoginPerMin: 60, APIRPS: 1_000, APIBurst: 2_000},
			Ledger:      ledger.DefaultOptions(),
			Contracts:   contractsOpts,
			Market:      market.DefaultOptions(),
			Notify:      notifyOpts,
			ClockReader: clock.ReaderOptions{CacheTTL: 0},
		}),
	})
	if err != nil {
		t.Fatalf("BuildServer: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle(gateway.APIPrefix+"/", server.Handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	norteToken := login(t, srv, traderName, traderSecret)

	// ── Precondición del suelo: mientras la concesión de Demo está VIGENTE, un
	//    POST de Norte sobre su parcela da 409 CONCESSION_OVERLAP ──────────────
	var pminX, pminY, pmaxX, pmaxY float64
	if err := pool.QueryRow(ctx, `
		SELECT ST_XMin(parcel), ST_YMin(parcel), ST_XMax(parcel), ST_YMax(parcel)
		  FROM world.land_concessions WHERE id = $1`, demoConcession).
		Scan(&pminX, &pminY, &pmaxX, &pmaxY); err != nil {
		t.Fatalf("envolvente de la parcela de Demo: %v", err)
	}
	demoParcel := geoRect(int64(pminX), int64(pminY), int64(pmaxX), int64(pmaxY))
	r := call(t, srv, http.MethodPost, "/api/v1/world/concessions", norteToken, map[string]any{
		"region_id": regionID.String(), "parcel": demoParcel,
	})
	if r.status != http.StatusConflict {
		t.Fatalf("POST concesión solapada con la de Demo (vigente): status %d, esperado 409 (cuerpo: %s)", r.status, r.raw)
	}

	// ── Corporación POBRE: se le drena la caja a insPoorCash (SIN ingresos) y se
	//    pone su mantenimiento y su canon al vencimiento por SQL (fixture) ─────
	drainCashTo(t, ctx, pool, demoID, insPoorCash)
	mustExecE2E(t, ctx, pool,
		`UPDATE world.buildings SET status = 'operational', condition_pct = 100, maintenance_paid_until_sim = $1 WHERE id = $2`,
		insSimBase, demoWarehouse)
	mustExecE2E(t, ctx, pool,
		`UPDATE world.land_concessions SET status = 'active', expires_at_sim = $1, grace_until_sim = NULL WHERE id = $2`,
		insSimBase, demoConcession)

	// ── Motores de la cascada bajo runCtx (reloj congelado compartido, CacheTTL 0) ─
	reader := clock.NewReader(clock.NewStore(pool), clock.ReaderOptions{CacheTTL: 0}, logger)

	enfOpts := enforcement.DefaultWorkerOptions()
	enfOpts.MaintenanceInterval = 50 * time.Millisecond
	enfOpts.EnforcementInterval = 50 * time.Millisecond
	enfOpts.SeizeGraceSimSeconds = insGrace
	enfWorker, err := enforcement.NewWorker(pool, reader, enfOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("enforcement.NewWorker: %v", err)
	}

	ccriSvc, err := contracts.NewService(pool, reader, contractsOpts, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	ccriWorker, err := contracts.NewWorker(ccriSvc, contracts.WorkerOptions{
		SweepInterval: 100 * time.Millisecond, BatchSize: 100,
	}, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewWorker: %v", err)
	}
	liq, err := contracts.NewSystemLiquidator(ccriSvc, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("contracts.NewSystemLiquidator: %v", err)
	}
	liqConsumer := liq.NewConsumer(pool, outbox.WithLogger(logger))

	// Reconciliador físico↔contable (solo para la auditoría final; no se arranca).
	prodWorker, err := production.NewWorker(pool, reader, production.DefaultWorkerOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("production.NewWorker: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	var wg sync.WaitGroup
	var rmu sync.Mutex
	var runnerErrs []string
	startRunner := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(runCtx); err != nil {
				rmu.Lock()
				runnerErrs = append(runnerErrs, fmt.Sprintf("%s: %v", name, err))
				rmu.Unlock()
			}
		}()
	}
	startRunner("enforcement_worker", enfWorker.Run)
	startRunner("ccri_worker", ccriWorker.Run)
	startRunner("system_liquidator", func(ctx context.Context) error {
		return liqConsumer.Run(ctx, 50*time.Millisecond, liq.Handle)
	})

	// ── (A) Mantenimiento drena y degrada a abandoned; canon impaga → delinquent ─
	advanceSim(t, ctx, pool, 20*insSimDay)
	pollPhase(t, 60*time.Second, "fase (A): drena, degrada a abandoned y canon delinquent",
		nil,
		func() (bool, string) {
			bStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.buildings WHERE id = $1`, demoWarehouse)
			cStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.land_concessions WHERE id = $1`, demoConcession)
			ok := bStatus == "abandoned" && cStatus == "delinquent"
			return ok, fmt.Sprintf("edificio=%s concesión=%s", bStatus, cStatus)
		})
	assertNoNegativeCashE2E(t, ctx, pool)
	// El mantenimiento la drenó a 0 (cobró solo lo disponible; jamás negativa).
	if got := cashOf(t, ctx, pool, demoID); got != 0 {
		t.Fatalf("caja de Demo tras drenarla el mantenimiento: %d, esperado 0", got)
	}

	// ── (B) Agotada la gracia: embargo → seized + reverted + building.seized ──
	advanceSim(t, ctx, pool, 6*insSimDay) // > insGrace desde el abandono/impago
	pollPhase(t, 60*time.Second, "fase (B): embargo (seized + reverted + building.seized)",
		nil,
		func() (bool, string) {
			bStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.buildings WHERE id = $1`, demoWarehouse)
			cStatus := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM world.land_concessions WHERE id = $1`, demoConcession)
			seizedEv := countRows(t, ctx, pool,
				`SELECT count(*) FROM outbox.events WHERE event_type = 'building.seized' AND aggregate_id = $1`, demoWarehouse)
			revertedEv := countRows(t, ctx, pool,
				`SELECT count(*) FROM outbox.events WHERE event_type = 'concession.reverted' AND aggregate_id = $1`, demoConcession)
			ok := bStatus == "seized" && cStatus == "reverted" && seizedEv >= 1 && revertedEv >= 1
			return ok, fmt.Sprintf("edificio=%s concesión=%s building.seized=%d concession.reverted=%d",
				bStatus, cStatus, seizedEv, revertedEv)
		})
	assertNoNegativeCashE2E(t, ctx, pool)

	// ── (C) El system_liquidator subasta el stock embargado: publica la oferta
	//        sell del sistema (banco central) de iron_ore en el tablón ─────────
	var auctionPubID uuid.UUID
	pollPhase(t, 60*time.Second, "fase (C): oferta sell del sistema publicada (subasta)",
		nil,
		func() (bool, string) {
			auctionPubID = optionalUUID(t, ctx, pool, `
				SELECT id FROM ledger.publications
				 WHERE publisher_account_id = $1 AND kind = 'sell' AND product_id = $2
				   AND status IN ('draw_window', 'open', 'micro_window')`, bankID, ironOreID)
			return auctionPubID != uuid.Nil, "sin oferta de subasta de iron_ore aún"
		})
	// La oferta es exactamente el stock embargado, al precio de remate, in situ.
	var aQty, aPrice int64
	var aOrigin uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT quantity_total, unit_price, origin_node_id FROM ledger.publications WHERE id = $1`, auctionPubID).
		Scan(&aQty, &aPrice, &aOrigin); err != nil {
		t.Fatalf("oferta de subasta: %v", err)
	}
	if aQty != insSeizedIron || aPrice != remateIron || aOrigin != demoNode {
		t.Fatalf("oferta de subasta inesperada: qty=%d precio=%d origen=%s (esperado %d/%d/%s)",
			aQty, aPrice, aOrigin, insSeizedIron, remateIron, demoNode)
	}
	// El stock salió del moroso (Demo) hacia el sistema (ya reservado por la oferta).
	if got := stockFreeOf(t, ctx, pool, demoID, ironOreID, demoWarehouse); got != 0 {
		t.Fatalf("stock_free de iron_ore de Demo tras el embargo: %d, esperado 0 (embargado)", got)
	}

	// ── (D) Norte (activo) COMPRA la subasta; el sorteo la liquida in situ ────
	value := insSeizedIron * remateIron // proceeds que cobra el banco central
	guarantee := value / 10             // garantía del 10% que aportó el sistema y retorna
	bankCashBeforeBuy := cashOf(t, ctx, pool, bankID)
	norteCashBeforeBuy := cashOf(t, ctx, pool, norteID)

	r = callKeyed(t, srv, http.MethodPost,
		"/api/v1/contracts/publications/"+auctionPubID.String()+"/acceptances",
		norteToken, uuid.NewString(), map[string]any{"quantity": itoa(insSeizedIron)})
	if r.status != http.StatusCreated {
		t.Fatalf("Norte acepta la subasta: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}

	pollPhase(t, 60*time.Second, "fase (D): sorteo de la subasta → contrato liquidado in situ",
		nil,
		func() (bool, string) {
			cid := optionalUUID(t, ctx, pool, `
				SELECT id FROM ledger.contracts
				 WHERE buyer_account_id = $1 AND seller_account_id = $2 AND product_id = $3 AND status = 'settled'`,
				norteID, bankID, ironOreID)
			return cid != uuid.Nil, "sin contrato de subasta liquidado aún"
		})

	// El stock roto pasó a un jugador ACTIVO (Norte), in situ en el almacén de Demo.
	if got := stockFreeOf(t, ctx, pool, norteID, ironOreID, demoWarehouse); got != insSeizedIron {
		t.Fatalf("stock_free de iron_ore de Norte tras la subasta: %d, esperado %d", got, insSeizedIron)
	}
	// Norte pagó el remate; el banco central COBRÓ proceeds + la garantía retornada.
	if got := cashOf(t, ctx, pool, norteID); got != norteCashBeforeBuy-value {
		t.Fatalf("caja de Norte tras comprar la subasta: %d, esperado %d", got, norteCashBeforeBuy-value)
	}
	if got := cashOf(t, ctx, pool, bankID); got != bankCashBeforeBuy+value+guarantee {
		t.Fatalf("caja del banco central tras cobrar la subasta: %d, esperado %d (proceeds %d + garantía %d)",
			got, bankCashBeforeBuy+value+guarantee, value, guarantee)
	}
	assertNoNegativeCashE2E(t, ctx, pool)

	// ── (E) La parcela quedó LIBRE: el mismo POST de Norte ahora tiene éxito ──
	r = call(t, srv, http.MethodPost, "/api/v1/world/concessions", norteToken, map[string]any{
		"region_id": regionID.String(), "parcel": demoParcel,
	})
	if r.status != http.StatusCreated {
		t.Fatalf("POST concesión sobre la parcela revertida: status %d, esperado 201 (cuerpo: %s)", r.status, r.raw)
	}

	// ── Apagado ordenado de los motores antes de auditar ─────────────────────
	stopRun()
	wg.Wait()
	rmu.Lock()
	errsCopy := append([]string(nil), runnerErrs...)
	rmu.Unlock()
	if len(errsCopy) > 0 {
		t.Fatalf("motores de la cascada terminaron con error: %v", errsCopy)
	}

	// ── Auditoría contable sobre la BD quiescente ────────────────────────────
	assertNoNegativeCashE2E(t, ctx, pool)
	assertBalancedLedger(t, ctx, pool)
	// La mercancía no se teletransportó: sigue físicamente en el almacén de Demo
	// (solo cambió de dueño Demo → sistema → Norte). Reconciliación 0.
	if disc, err := prodWorker.Reconcile(ctx); err != nil || disc != 0 {
		t.Fatalf("reconciliación física↔contable: %d divergencias (err %v), esperado 0", disc, err)
	}
	if got := stockTotalOf(t, ctx, pool, norteID, ironOreID, demoWarehouse); got != insSeizedIron {
		t.Fatalf("stock total de iron_ore de Norte en el almacén de Demo: %d, esperado %d", got, insSeizedIron)
	}

	// ── Sección: RETIRO DE UN BOT INSOLVENTE (ADR-024) ───────────────────────
	t.Run("bot insolvente retirado (absorción + estado retired)", func(t *testing.T) {
		assertBotRetired(t, ctx, pool, reader, logger, bankID)
	})
}

// assertBotRetired aprovisiona un bot (capitalizado por el banco central), lo
// lleva a la insolvencia-inactividad (caja 0, sin edificios/contratos/
// publicaciones) y verifica que el RetirementJob lo retira en un barrido: absorbe
// su caja al banco central, marca la cuenta 'retired', emite bot.retired y el
// ledger sigue cuadrando (la emisión sube exactamente lo absorbido).
func assertBotRetired(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sim bots.SimSource, logger *slog.Logger, bankID uuid.UUID) {
	t.Helper()

	const botCapital int64 = 500_000
	orch, err := bots.NewOrchestrator(pool, bots.Options{
		Traders: 1, SecretSeed: "e2e-insolvency-retire", Capital: botCapital,
		Tick: time.Second, Addr: ":0", APIURL: "http://localhost:0/api/v1",
	}, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("bots.NewOrchestrator: %v", err)
	}
	provisioned, err := orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(provisioned) != 1 {
		t.Fatalf("bots aprovisionados: %d, esperado 1", len(provisioned))
	}
	botID := provisioned[0].AccountID
	if got := cashOf(t, ctx, pool, botID); got != botCapital {
		t.Fatalf("caja del bot tras capitalizar: %d, esperado %d", got, botCapital)
	}

	// Insolvencia: se le drena la caja por debajo del piso (< 1000) dejando un
	// resto que el retiro ABSORBE. Sin edificios/contratos/publicaciones (recién
	// aprovisionado) → insolvente-inactivo.
	const insolventCash int64 = 300
	drainCashTo(t, ctx, pool, botID, insolventCash)
	emissionBefore := emissionBalanceE2E(t, ctx, pool)

	// Barrido de retiro con ventana de gracia 0 (retiro instantáneo): un solo
	// RunOnce debe retirarlo.
	reg := prometheus.NewRegistry()
	job, err := bots.NewRetirementJob(pool, ledger.DefaultOptions(), bots.RetirementOptions{
		Interval: time.Minute, CashFloor: bots.DefaultRetireCashFloor, IdleSimSeconds: 0,
	}, sim, logger, reg)
	if err != nil {
		t.Fatalf("bots.NewRetirementJob: %v", err)
	}
	job.RunOnce(ctx)

	if st := stringOrEmpty(t, ctx, pool, `SELECT status::text FROM auth.accounts WHERE id = $1`, botID); st != "retired" {
		t.Fatalf("estado del bot tras el retiro: %q, esperado retired", st)
	}
	if got := cashOf(t, ctx, pool, botID); got != 0 {
		t.Fatalf("caja del bot tras absorber: %d, esperado 0", got)
	}
	// La caja absorbida subió la emisión (inverso de la capitalización): masa neta baja.
	if d := emissionBalanceE2E(t, ctx, pool) - emissionBefore; d != insolventCash {
		t.Fatalf("la emisión subió %d al absorber, esperado %d", d, insolventCash)
	}
	// bot.retired emitido con el importe absorbido.
	absorbed := stringOrEmpty(t, ctx, pool, `
		SELECT payload->>'absorbed_cash' FROM outbox.events
		 WHERE event_type = 'bot.retired' AND aggregate_id = $1 ORDER BY seq DESC LIMIT 1`, botID)
	if absorbed != itoa(insolventCash) {
		t.Fatalf("bot.retired absorbed_cash=%q, esperado %q", absorbed, itoa(insolventCash))
	}
	assertNoNegativeCashE2E(t, ctx, pool)
	assertBalancedLedger(t, ctx, pool)
	_ = bankID // la absorción va a la emisión del banco central; su identidad la resuelve el job
}

// ─── Helpers locales de la cascada de insolvencia ────────────────────────────

// drainCashTo mueve el excedente de caja (por encima de target) a la cuenta sink
// mediante un asiento cash → sink, forzando la insolvencia sin dejar la caja
// negativa. target debe ser <= saldo actual.
func drainCashTo(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID, target int64) {
	t.Helper()
	bal := cashOf(t, ctx, pool, owner)
	delta := bal - target
	if delta <= 0 {
		return
	}
	cashID := queryUUID(t, ctx, pool, `SELECT id FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1`, owner)
	sinkID := queryUUID(t, ctx, pool, `SELECT id FROM ledger.accounts WHERE kind = 'sink' ORDER BY id LIMIT 1`)
	postLedgerE2E(t, ctx, pool, "transfer", []ledgerEntryE2E{{cashID, -delta}, {sinkID, delta}})
}

// ledgerEntryE2E es una partida (cuenta, importe) de un asiento de fixture.
type ledgerEntryE2E struct {
	account uuid.UUID
	amount  int64
}

// postLedgerE2E asienta cabecera + partidas en UNA sola transacción (el balance
// por activo es un constraint trigger DIFERIDO: se evalúa en el COMMIT, así que
// todas las partidas confirman juntas).
func postLedgerE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, entries []ledgerEntryE2E) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback tras commit es no-op
	txID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx,
		`INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1, $2::ledger.transaction_kind, 0)`, txID, kind); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1, $2, $3, $4)`,
			uuid.Must(uuid.NewV7()), txID, e.account, e.amount); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// mustExecE2E ejecuta un UPDATE/INSERT de fixture, fallando el test ante error.
func mustExecE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// assertNoNegativeCashE2E comprueba la invariante dura de la cascada (GDD 5.9):
// ninguna caja quedó con saldo negativo.
func assertNoNegativeCashE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger.accounts WHERE kind = 'cash' AND balance < 0`).Scan(&n); err != nil {
		t.Fatalf("comprobando cajas negativas: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d caja(s) con saldo negativo (invariante GDD 5.9 violado)", n)
	}
}

// emissionBalanceE2E lee el saldo de la cuenta de emisión del banco central.
func emissionBalanceE2E(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind = 'emission'`).Scan(&b); err != nil {
		t.Fatalf("saldo de la emisión: %v", err)
	}
	return b
}
