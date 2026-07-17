package contracts_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// baseSim es el sim-time de arranque de los escenarios del worker (se avanza
// monótonamente para disparar los vencimientos de plazo y TTL).
const baseSim simtime.SimTime = 1_000_000

// mutableSim implementa contracts.SimSource con un sim-time ajustable (los
// vencimientos de plazo/TTL se prueban avanzando el reloj de simulación; las
// ventanas de sorteo son wall-clock de la BD, ajenas a este reloj).
type mutableSim struct {
	mu sync.Mutex
	v  simtime.SimTime
}

func (m *mutableSim) Now(context.Context) simtime.SimTime {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.v
}

func (m *mutableSim) set(v simtime.SimTime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.v = v
}

// TestWorkerIntegration ejercita los tres barridos del worker CCRI contra una
// BD real: resolución de sorteo con orden aleatorio y liquidación in situ,
// aceptación parcial que deja la publicación abierta, liquidación por
// vencimiento de una compra cross-node (fallo, compensación 50/50), y
// expiración por TTL. Tras cada escenario se comprueba que el ledger cuadra a
// cero por activo (dinero y cada producto).
func TestWorkerIntegration(t *testing.T) {
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
	w := loadWorld(t, ctx, pool)
	buyer2 := fundCorp(t, ctx, pool, "Buyer Dos")

	sim := &mutableSim{v: baseSim}
	opts := contracts.DefaultOptions()
	opts.DrawWindowSeconds = 1     // la ventana cierra en 1 s wall
	opts.MicroWindowSeconds = 1    //
	opts.CancelCooldownSeconds = 0 //
	opts.PublicationTTLSimSeconds = 5000
	svc, err := contracts.NewService(pool, sim, opts, logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	reg := prometheus.NewRegistry()
	worker, err := contracts.NewWorker(svc, contracts.WorkerOptions{
		SweepInterval: time.Second, BatchSize: 100,
	}, logger, reg)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	// El ledger arranca cuadrado a cero por activo (doble entrada del seed).
	assertLedgerBalanced(t, ctx, pool)

	t.Run("SellInSituDrawFullCycle", func(t *testing.T) {
		sim.set(baseSim)
		// Baselines antes de publicar (los tres implicados).
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)
		buyer2Cash0 := balanceOf(t, ctx, pool, "cash", buyer2, nil, nil)

		// Venta de 70 iron_ore @ 50 (valor 3500, garantía 350). Dos compradores
		// aceptan 70 cada uno: el sorteo sirve a uno (70) y libera al otro.
		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 70, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication(sell): %v", err)
		}
		accT, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 70})
		if err != nil {
			t.Fatalf("Accept(trader): %v", err)
		}
		accB, err := svc.Accept(ctx, buyer2, pub.ID, contracts.AcceptInput{Quantity: 70})
		if err != nil {
			t.Fatalf("Accept(buyer2): %v", err)
		}

		waitWindow()
		worker.RunOnce(ctx)

		// Publicación agotada; una aceptación servida (70), la otra liberada (0).
		gotPub, err := svc.GetPublication(ctx, w.demo, pub.ID)
		if err != nil {
			t.Fatalf("GetPublication: %v", err)
		}
		if gotPub.Status != contracts.StatusExhausted || gotPub.QuantityRemaining != 0 {
			t.Fatalf("publicación tras sorteo: status=%s remaining=%d", gotPub.Status, gotPub.QuantityRemaining)
		}

		baselines := map[uuid.UUID]int64{w.trader: traderCash0, buyer2: buyer2Cash0}
		winner, loser := resolveWinner(t, ctx, svc, accT, accB, w.trader, buyer2, baselines)
		if winner.acc.QuantityServed != 70 || winner.acc.Status != contracts.AcceptanceServed || winner.acc.DrawOrder == nil {
			t.Fatalf("aceptación ganadora inesperada: %+v", winner.acc)
		}
		if loser.acc.QuantityServed != 0 || loser.acc.Status != contracts.AcceptanceReleased || loser.acc.DrawOrder == nil {
			t.Fatalf("aceptación perdedora inesperada: %+v", loser.acc)
		}
		if winner.contractID == nil {
			t.Fatal("la aceptación servida no expone contract_id")
		}

		// El contrato quedó settled con fill 100% (retirada in situ).
		contract := getContractRow(t, ctx, pool, *winner.contractID)
		if contract.status != "settled" || contract.fillBP != 10000 ||
			contract.quantityDelivered != 70 || contract.originNode != contract.destNode {
			t.Fatalf("contrato in situ inesperado: %+v", contract)
		}

		// Contabilidad EXHAUSTIVA de los cuatro lados:
		// 1) vendedor (demo): cobra 3500, recupera garantía (neto +3500), -70 stock.
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0+3500 {
			t.Fatalf("caja del vendedor: %d, esperado %d", got, demoCash0+3500)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0-70 {
			t.Fatalf("stock_free del vendedor: %d, esperado %d", got, demoStock0-70)
		}
		// 2) comprador ganador: paga 3500 y recibe 70 de stock EN el almacén de origen.
		if got := balanceOf(t, ctx, pool, "cash", winner.account, nil, nil); got != winner.cash0-3500 {
			t.Fatalf("caja del comprador ganador: %d, esperado %d", got, winner.cash0-3500)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", winner.account, &w.ironOre, &w.demoWarehouse); got != 70 {
			t.Fatalf("stock del comprador ganador en el almacén de origen: %d, esperado 70", got)
		}
		// 3) comprador perdedor: recupera todo su escrow (neto 0 vs. pre-publicación).
		if got := balanceOf(t, ctx, pool, "cash", loser.account, nil, nil); got != loser.cash0 {
			t.Fatalf("caja del comprador perdedor: %d, esperado %d (recupera todo)", got, loser.cash0)
		}
		// 4) todas las cuentas espejo a cero.
		assertZero(t, ctx, pool, *pub.StockReserveAccountID, "reserva de la publicación")
		assertZero(t, ctx, pool, *pub.GuaranteeAccountID, "garantía de la publicación")
		assertZero(t, ctx, pool, *accT.EscrowAccountID, "escrow de trader")
		assertZero(t, ctx, pool, *accB.EscrowAccountID, "escrow de buyer2")
		assertZero(t, ctx, pool, contract.stockAccount, "reserva del contrato")
		assertZero(t, ctx, pool, contract.guaranteeAccount, "garantía del contrato")
		assertZero(t, ctx, pool, contract.escrowAccount, "escrow del contrato")

		// Una entrega registrada (in situ, on_time).
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id = $1`, *winner.contractID); n != 1 {
			t.Fatalf("entregas del contrato: %d, esperado 1", n)
		}
		// Eventos del ciclo emitidos.
		singleEvent(t, ctx, pool, *winner.contractID, "contract.confirmed")
		singleEvent(t, ctx, pool, *winner.contractID, "contract.delivered")
		settled := singleEvent(t, ctx, pool, *winner.contractID, "contract.settled")
		if settled["status"] != "settled" || settled["fill_bp"] != float64(10000) ||
			settled["quantity_delivered"] != "70" {
			t.Fatalf("payload de contract.settled: %v", settled)
		}

		// Métrica de sorteos resueltos.
		if got := metricSum(t, reg, "ii_draws_resolved_total", "", ""); got < 1 {
			t.Errorf("ii_draws_resolved_total: %v, esperado >= 1", got)
		}
		if got := metricSum(t, reg, "ii_contracts_settled_total", "status", "settled"); got < 1 {
			t.Errorf("ii_contracts_settled_total{settled}: %v, esperado >= 1", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("PartialAcceptanceLeavesOpen", func(t *testing.T) {
		sim.set(baseSim)
		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		acc, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 40})
		if err != nil {
			t.Fatalf("Accept: %v", err)
		}

		waitWindow()
		worker.RunOnce(ctx)

		// K de N: sirve 40, deja 60 publicadas en open.
		gotPub, err := svc.GetPublication(ctx, w.demo, pub.ID)
		if err != nil {
			t.Fatalf("GetPublication: %v", err)
		}
		if gotPub.Status != contracts.StatusOpen || gotPub.QuantityRemaining != 60 {
			t.Fatalf("publicación tras aceptación parcial: status=%s remaining=%d", gotPub.Status, gotPub.QuantityRemaining)
		}
		served, err := svc.GetAcceptance(ctx, w.trader, acc.ID)
		if err != nil {
			t.Fatalf("GetAcceptance: %v", err)
		}
		if served.Status != contracts.AcceptanceServed || served.QuantityServed != 40 {
			t.Fatalf("aceptación parcial: %+v", served)
		}
		contractID, err := svc.ResolveAcceptanceContract(ctx, served)
		if err != nil || contractID == nil {
			t.Fatalf("ResolveAcceptanceContract: %v (%v)", err, contractID)
		}
		c := getContractRow(t, ctx, pool, *contractID)
		if c.status != "settled" || c.quantityAgreed != 40 || c.quantityDelivered != 40 {
			t.Fatalf("contrato parcial: %+v", c)
		}
		assertLedgerBalanced(t, ctx, pool)

		// Se cancela el resto abierto para que no interfiera (expirando por TTL)
		// en los escenarios posteriores que avanzan el sim-time.
		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); err != nil {
			t.Fatalf("cancelando el resto abierto: %v", err)
		}
	})

	t.Run("BuyCrossNodeExpiresFailed", func(t *testing.T) {
		sim.set(baseSim)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		sink0 := sinkBalance(t, ctx, pool)

		// Compra de trader (destino traderNode), 50 @ 60 (valor 3000, garantía
		// 300). demo la acepta aportando su almacén (demoNode): origen != destino
		// ⇒ requiere tránsito; a Fase 0 sin logística vence sin entregar.
		pub, err := svc.CreatePublication(ctx, w.trader, buyCrossInput(w, 50, 60, 100))
		if err != nil {
			t.Fatalf("CreatePublication(buy): %v", err)
		}
		acc, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 50, OriginNodeID: &w.demoNode})
		if err != nil {
			t.Fatalf("Accept(buy): %v", err)
		}

		waitWindow()
		worker.RunOnce(ctx) // resuelve el sorteo ⇒ contrato active cross-node

		served, err := svc.GetAcceptance(ctx, w.demo, acc.ID)
		if err != nil {
			t.Fatalf("GetAcceptance: %v", err)
		}
		contractID, err := svc.ResolveAcceptanceContract(ctx, served)
		if err != nil || contractID == nil {
			t.Fatalf("ResolveAcceptanceContract: %v (%v)", err, contractID)
		}
		active := getContractRow(t, ctx, pool, *contractID)
		if active.status != "active" || active.originNode == active.destNode {
			t.Fatalf("contrato cross-node inesperado: %+v", active)
		}

		// Vence el plazo (deadline = base+100): avanza el sim y liquida.
		sim.set(baseSim + 10_000)
		worker.RunOnce(ctx)

		failed := getContractRow(t, ctx, pool, *contractID)
		if failed.status != "failed" || failed.fillBP != 0 || failed.quantityDelivered != 0 {
			t.Fatalf("contrato tras vencer: %+v", failed)
		}

		// Comprador (trader): escrow 3000 devuelto + compensación 150 (50% de la
		// garantía 300) ⇒ neto +150.
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0+150 {
			t.Fatalf("caja del comprador tras fallo: %d, esperado %d", got, traderCash0+150)
		}
		// Vendedor (demo): pierde la garantía (300) y recupera el stock in situ
		// en su almacén de origen ⇒ caja -300, stock igual que antes de aceptar.
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0-300 {
			t.Fatalf("caja del vendedor tras fallo: %d, esperado %d", got, demoCash0-300)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0 {
			t.Fatalf("stock liberado in situ: %d, esperado %d", got, demoStock0)
		}
		// Sink: recibe el resto de la garantía (150).
		if got := sinkBalance(t, ctx, pool); got != sink0+150 {
			t.Fatalf("sink tras fallo: %d, esperado %d", got, sink0+150)
		}
		// Cuentas espejo del contrato a cero.
		assertZero(t, ctx, pool, active.stockAccount, "reserva del contrato fallido")
		assertZero(t, ctx, pool, active.guaranteeAccount, "garantía del contrato fallido")
		assertZero(t, ctx, pool, active.escrowAccount, "escrow del contrato fallido")

		settled := singleEvent(t, ctx, pool, *contractID, "contract.settled")
		if settled["status"] != "failed" || settled["fill_bp"] != float64(0) {
			t.Fatalf("payload de contract.settled (failed): %v", settled)
		}
		if got := metricSum(t, reg, "ii_contracts_settled_total", "status", "failed"); got < 1 {
			t.Errorf("ii_contracts_settled_total{failed}: %v, esperado >= 1", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("PublicationExpiresByTTL", func(t *testing.T) {
		sim.set(baseSim + 100_000)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 30, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		// Madura a open (sin aceptaciones, la ventana cierra) y luego expira por
		// TTL (published_at + 5000 <= sim actual).
		waitWindow()
		worker.RunOnce(ctx)
		if p, _ := svc.GetPublication(ctx, w.demo, pub.ID); p.Status != contracts.StatusOpen {
			t.Fatalf("la publicación sin aceptaciones debe madurar a open: %s", p.Status)
		}

		sim.set(baseSim + 100_000 + 6_000) // > published_at + TTL(5000)
		worker.RunOnce(ctx)

		expired, err := svc.GetPublication(ctx, w.demo, pub.ID)
		if err != nil {
			t.Fatalf("GetPublication: %v", err)
		}
		if expired.Status != contracts.StatusExpired {
			t.Fatalf("estado tras TTL: %s, esperado expired", expired.Status)
		}
		// Garantía y stock restantes liberados (neto 0 respecto al pre-publicación).
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0 {
			t.Fatalf("stock_free tras expirar: %d, esperado %d", got, demoStock0)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0 {
			t.Fatalf("caja tras expirar: %d, esperado %d", got, demoCash0)
		}
		assertZero(t, ctx, pool, *pub.StockReserveAccountID, "reserva de la publicación expirada")
		assertZero(t, ctx, pool, *pub.GuaranteeAccountID, "garantía de la publicación expirada")

		payload := singleEvent(t, ctx, pool, pub.ID, "publication.expired")
		if payload["quantity_remaining"] != "30" {
			t.Fatalf("payload de publication.expired: %v", payload)
		}
		if got := metricSum(t, reg, "ii_publications_expired_total", "", ""); got < 1 {
			t.Errorf("ii_publications_expired_total: %v, esperado >= 1", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})
}

// ─── Helpers específicos del worker ──────────────────────────────────────────

// waitWindow espera a que cierre la ventana de sorteo de 1 s wall-clock (con
// margen para el reloj de la BD).
func waitWindow() { time.Sleep(1200 * time.Millisecond) }

// buyCrossInput construye una publicación buy de iron_ore con destino trader y
// un plazo de entrega corto (para el vencimiento).
func buyCrossInput(w world, qty, price, delivery int64) contracts.PublicationInput {
	product := w.ironOre
	node := w.traderNode
	return contracts.PublicationInput{
		Kind:               contracts.KindBuy,
		ProductID:          &product,
		QuantityTotal:      qty,
		UnitPrice:          price,
		DestinationNodeID:  &node,
		DeliverySimSeconds: delivery,
	}
}

// fundCorp crea una corporación humana con caja y capital semilla (emisión del
// banco central) para tener un segundo comprador en el sorteo.
func fundCorp(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	repo := auth.NewPGRepository(pool)
	acc, err := repo.CreateAccount(ctx, "human", name)
	if err != nil {
		t.Fatalf("creando la corporación %s: %v", name, err)
	}
	lsvc := ledger.NewService(pool, ledger.DefaultOptions(), nil)
	cash, err := lsvc.EnsureCashAccount(ctx, acc.ID)
	if err != nil {
		t.Fatalf("caja de %s: %v", name, err)
	}
	var emissionID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind = 'emission' LIMIT 1`).Scan(&emissionID); err != nil {
		t.Fatalf("cuenta emission: %v", err)
	}
	ref := acc.ID
	if _, err := lsvc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, baseSim, &ref,
		"Capital de prueba", []ledger.EntryInput{
			{AccountID: cash.ID, Amount: 1_000_000},
			{AccountID: emissionID, Amount: -1_000_000},
		}); err != nil {
		t.Fatalf("capital de %s: %v", name, err)
	}
	return acc.ID
}

// acceptResult resume una aceptación resuelta y el estado de su cuenta.
type acceptResult struct {
	acc        contracts.Acceptance
	account    uuid.UUID
	cash0      int64
	contractID *uuid.UUID
}

// resolveWinner determina, tras el sorteo, cuál de las dos aceptaciones resultó
// servida (ganadora) y cuál liberada (perdedora) — el orden es aleatorio —, y
// adjunta a cada una su caja PREVIA a la publicación (de baselines) para las
// comprobaciones contables netas.
func resolveWinner(t *testing.T, ctx context.Context, svc *contracts.Service,
	accA, accB contracts.Acceptance, accountA, accountB uuid.UUID, baselines map[uuid.UUID]int64) (winner, loser acceptResult) {
	t.Helper()
	ra := readAccept(t, ctx, svc, accountA, accA.ID)
	rb := readAccept(t, ctx, svc, accountB, accB.ID)
	if ra.acc.Status == contracts.AcceptanceServed {
		winner, loser = ra, rb
	} else {
		winner, loser = rb, ra
	}
	winner.cash0 = baselines[winner.account]
	loser.cash0 = baselines[loser.account]
	return winner, loser
}

func readAccept(t *testing.T, ctx context.Context, svc *contracts.Service, account, id uuid.UUID) acceptResult {
	t.Helper()
	a, err := svc.GetAcceptance(ctx, account, id)
	if err != nil {
		t.Fatalf("GetAcceptance(%s): %v", account, err)
	}
	cid, err := svc.ResolveAcceptanceContract(ctx, a)
	if err != nil {
		t.Fatalf("ResolveAcceptanceContract: %v", err)
	}
	return acceptResult{acc: a, account: account, contractID: cid}
}

// contractRow es la vista del contrato leída directamente de la BD.
type contractRow struct {
	status                         string
	fillBP                         int32
	quantityAgreed                 int64
	quantityDelivered              int64
	originNode, destNode           uuid.UUID
	stockAccount, guaranteeAccount uuid.UUID
	escrowAccount                  uuid.UUID
}

func getContractRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) contractRow {
	t.Helper()
	var c contractRow
	var fill *int32
	err := pool.QueryRow(ctx, `
		SELECT status, fill_bp, quantity_agreed, quantity_delivered,
		       origin_node_id, destination_node_id,
		       stock_reserve_account_id, seller_guarantee_account_id, escrow_account_id
		FROM ledger.contracts WHERE id = $1`, id).Scan(
		&c.status, &fill, &c.quantityAgreed, &c.quantityDelivered,
		&c.originNode, &c.destNode, &c.stockAccount, &c.guaranteeAccount, &c.escrowAccount)
	if err != nil {
		t.Fatalf("leyendo el contrato %s: %v", id, err)
	}
	if fill != nil {
		c.fillBP = *fill
	}
	return c
}

// sinkBalance devuelve el saldo de la cuenta sink del banco central.
func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind = 'sink' LIMIT 1`).Scan(&b); err != nil {
		t.Fatalf("saldo del sink: %v", err)
	}
	return b
}

// assertZero verifica que una cuenta del ledger quedó a saldo cero.
func assertZero(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, label string) {
	t.Helper()
	if got := balanceByID(t, ctx, pool, id); got != 0 {
		t.Fatalf("%s: saldo %d, esperado 0", label, got)
	}
}

// assertLedgerBalanced comprueba la invariante contable global: la suma de
// saldos de TODAS las cuentas del ledger es cero para cada activo (dinero y
// cada producto). Nada se crea ni se destruye fuera de emission/world_source.
func assertLedgerBalanced(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
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
