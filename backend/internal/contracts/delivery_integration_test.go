package contracts_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// TestDeliveryConfirmerIntegration ejercita la integración de ENTREGA del CCRI
// (Incremento 3, internal/contracts) contra una BD real: el evento
// contract.confirmed enriquecido con el contrato de integración completo; el
// consumidor delivery_confirmer que, desde shipment.arrived, asienta la entrega,
// acumula lo entregado a tiempo y liquida al completarse (verificación contable
// de las cuatro partes); la entrega tardía que no cuenta; la entrega parcial en
// dos cargamentos que acumula y liquida; y la idempotencia ante reprocesado.
//
// Se omite si II_TEST_DATABASE_URL no está definida (misma BD efímera propia que
// el resto de tests de integración del módulo).
func TestDeliveryConfirmerIntegration(t *testing.T) {
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

	sim := &mutableSim{v: baseSim}
	opts := contracts.DefaultOptions()
	opts.DrawWindowSeconds = 1
	opts.MicroWindowSeconds = 1
	opts.CancelCooldownSeconds = 0
	svc, err := contracts.NewService(pool, sim, opts, logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	worker, err := contracts.NewWorker(svc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, nil)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	reg := prometheus.NewRegistry()
	dc, err := contracts.NewDeliveryConfirmer(svc, logger, reg)
	if err != nil {
		t.Fatalf("NewDeliveryConfirmer: %v", err)
	}

	assertLedgerBalanced(t, ctx, pool)

	// deliverySimSeconds amplio: la entrega a tiempo cabe holgadamente y la tardía
	// se fuerza con un arrived_at_sim posterior a deadline (base+deliverySim).
	const deliverySim = 100_000

	t.Run("ContractConfirmedPayloadCompleto", func(t *testing.T) {
		sim.set(baseSim)
		cid := activeCrossNodeContract(t, ctx, pool, svc, worker, w, 50, 60, deliverySim)

		p := singleEvent(t, ctx, pool, cid, "contract.confirmed")
		// Todos los campos del contrato de integración FIJO CCRI↔Logística.
		if p["contract_id"] != cid.String() {
			t.Fatalf("contract_id: %v", p["contract_id"])
		}
		if p["kind"] != "buy" {
			t.Fatalf("kind: %v, esperado buy", p["kind"])
		}
		if p["buyer_account_id"] != w.trader.String() || p["seller_account_id"] != w.demo.String() {
			t.Fatalf("partes: buyer=%v seller=%v", p["buyer_account_id"], p["seller_account_id"])
		}
		if p["product_id"] != w.ironOre.String() {
			t.Fatalf("product_id: %v", p["product_id"])
		}
		if p["quantity"] != "50" {
			t.Fatalf("quantity: %v, esperado \"50\"", p["quantity"])
		}
		if p["origin_node_id"] != w.demoNode.String() || p["destination_node_id"] != w.traderNode.String() {
			t.Fatalf("nodos: origin=%v dest=%v", p["origin_node_id"], p["destination_node_id"])
		}
		if p["deadline_sim"] != float64(baseSim+deliverySim) {
			t.Fatalf("deadline_sim: %v, esperado %d", p["deadline_sim"], baseSim+deliverySim)
		}
		if p["confirmed_at_sim"] != float64(baseSim) {
			t.Fatalf("confirmed_at_sim: %v, esperado %d", p["confirmed_at_sim"], baseSim)
		}
	})

	t.Run("OnTimeDeliverySettles", func(t *testing.T) {
		sim.set(baseSim)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		traderStock0 := stockFreeQty(t, ctx, pool, w.trader, w.ironOre, w.traderWarehouse)

		cid := activeCrossNodeContract(t, ctx, pool, svc, worker, w, 50, 60, deliverySim)
		row := getContractRow(t, ctx, pool, cid)

		arrived := simtime.SimTime(baseSim + 1_000) // <= deadline
		shID := insertArrivedShipment(t, ctx, pool, w.demo, w.ironOre, 50, cid, w.traderNode, w.traderNode, baseSim+deliverySim, arrived)
		mustHandleArrived(t, ctx, pool, dc, arrivedEvent(shID, cid, 50, w.traderNode, arrived))

		// Contrato liquidado, fill 100%, 50 entregado.
		settled := getContractRow(t, ctx, pool, cid)
		if settled.status != "settled" || settled.fillBP != 10000 || settled.quantityDelivered != 50 {
			t.Fatalf("contrato tras entrega íntegra: %+v", settled)
		}
		// Contabilidad de las cuatro partes:
		// comprador (trader) pagó 3000 (escrow → vendedor) y recibió 50 en destino.
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0-3000 {
			t.Fatalf("caja del comprador: %d, esperado %d", got, traderCash0-3000)
		}
		if got := stockFreeQty(t, ctx, pool, w.trader, w.ironOre, w.traderWarehouse); got != traderStock0+50 {
			t.Fatalf("stock del comprador en destino: %d, esperado %d", got, traderStock0+50)
		}
		// vendedor (demo) cobró 3000 (neto: -300 garantía +3300) y entregó 50.
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0+3000 {
			t.Fatalf("caja del vendedor: %d, esperado %d", got, demoCash0+3000)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0-50 {
			t.Fatalf("stock_free del vendedor: %d, esperado %d", got, demoStock0-50)
		}
		// Garantías/escrow del contrato liberados (cuentas espejo a cero).
		assertZero(t, ctx, pool, row.stockAccount, "reserva del contrato entregado")
		assertZero(t, ctx, pool, row.guaranteeAccount, "garantía del contrato entregado")
		assertZero(t, ctx, pool, row.escrowAccount, "escrow del contrato entregado")

		// Una entrega on_time y los eventos del cierre.
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id=$1`, cid); n != 1 {
			t.Fatalf("entregas: %d, esperado 1", n)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id=$1 AND on_time`, cid); n != 1 {
			t.Fatalf("entregas on_time: %d, esperado 1", n)
		}
		delivered := singleEvent(t, ctx, pool, cid, "contract.delivered")
		if delivered["quantity"] != "50" || delivered["quantity_delivered"] != "50" || delivered["on_time"] != true {
			t.Fatalf("payload de contract.delivered: %v", delivered)
		}
		s := singleEvent(t, ctx, pool, cid, "contract.settled")
		if s["status"] != "settled" || s["fill_bp"] != float64(10000) || s["quantity_delivered"] != "50" {
			t.Fatalf("payload de contract.settled: %v", s)
		}
		if got := metricSum(t, reg, "ii_contract_deliveries_confirmed_total", "", ""); got < 1 {
			t.Errorf("ii_contract_deliveries_confirmed_total: %v, esperado >= 1", got)
		}
		if got := metricSum(t, reg, "ii_contract_deliveries_settled_total", "", ""); got < 1 {
			t.Errorf("ii_contract_deliveries_settled_total: %v, esperado >= 1", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("LateDeliveryNotCounted", func(t *testing.T) {
		sim.set(baseSim)
		cid := activeCrossNodeContract(t, ctx, pool, svc, worker, w, 40, 50, deliverySim)

		arrived := simtime.SimTime(baseSim + deliverySim + 5_000) // > deadline
		shID := insertArrivedShipment(t, ctx, pool, w.demo, w.ironOre, 40, cid, w.traderNode, w.traderNode, baseSim+deliverySim, arrived)
		mustHandleArrived(t, ctx, pool, dc, arrivedEvent(shID, cid, 40, w.traderNode, arrived))

		// La entrega tardía NO cuenta para el pago: quantity_delivered sigue 0 y el
		// contrato sigue activo (lo liquidará el vencimiento, pro-rata 0).
		row := getContractRow(t, ctx, pool, cid)
		if row.status != "active" || row.quantityDelivered != 0 {
			t.Fatalf("contrato tras entrega tardía: %+v", row)
		}
		// Se registra la partida como no on_time; no hay contract.settled.
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id=$1 AND NOT on_time`, cid); n != 1 {
			t.Fatalf("entregas tardías registradas: %d, esperado 1", n)
		}
		delivered := singleEvent(t, ctx, pool, cid, "contract.delivered")
		if delivered["on_time"] != false || delivered["quantity_delivered"] != "0" {
			t.Fatalf("payload de contract.delivered tardío: %v", delivered)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events WHERE aggregate_id=$1 AND event_type='contract.settled'`, cid); n != 0 {
			t.Fatalf("una entrega tardía no debe liquidar: %d eventos contract.settled", n)
		}
		if got := metricSum(t, reg, "ii_contract_deliveries_late_total", "", ""); got < 1 {
			t.Errorf("ii_contract_deliveries_late_total: %v, esperado >= 1", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("PartialThenCompleteSettles", func(t *testing.T) {
		sim.set(baseSim)
		cid := activeCrossNodeContract(t, ctx, pool, svc, worker, w, 60, 70, deliverySim)

		arrived1 := simtime.SimTime(baseSim + 1_000)
		sh1 := insertArrivedShipment(t, ctx, pool, w.demo, w.ironOre, 25, cid, w.traderNode, w.traderNode, baseSim+deliverySim, arrived1)
		mustHandleArrived(t, ctx, pool, dc, arrivedEvent(sh1, cid, 25, w.traderNode, arrived1))

		// Entrega parcial: acumula 25, contrato sigue activo (sin liquidar).
		mid := getContractRow(t, ctx, pool, cid)
		if mid.status != "active" || mid.quantityDelivered != 25 {
			t.Fatalf("contrato tras primera entrega parcial: %+v", mid)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events WHERE aggregate_id=$1 AND event_type='contract.settled'`, cid); n != 0 {
			t.Fatalf("una entrega parcial no debe liquidar aún: %d contract.settled", n)
		}

		arrived2 := simtime.SimTime(baseSim + 2_000)
		sh2 := insertArrivedShipment(t, ctx, pool, w.demo, w.ironOre, 35, cid, w.traderNode, w.traderNode, baseSim+deliverySim, arrived2)
		mustHandleArrived(t, ctx, pool, dc, arrivedEvent(sh2, cid, 35, w.traderNode, arrived2))

		// Completa 60: liquida con fill 100%.
		full := getContractRow(t, ctx, pool, cid)
		if full.status != "settled" || full.quantityDelivered != 60 || full.fillBP != 10000 {
			t.Fatalf("contrato tras completar: %+v", full)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE contract_id=$1`, cid); n != 2 {
			t.Fatalf("entregas acumuladas: %d, esperado 2", n)
		}
		s := singleEvent(t, ctx, pool, cid, "contract.settled")
		if s["quantity_delivered"] != "60" || s["fill_bp"] != float64(10000) {
			t.Fatalf("payload de contract.settled (parcial completado): %v", s)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("IdempotentReprocess", func(t *testing.T) {
		sim.set(baseSim)
		cid := activeCrossNodeContract(t, ctx, pool, svc, worker, w, 30, 40, deliverySim)

		arrived := simtime.SimTime(baseSim + 1_000)
		shID := insertArrivedShipment(t, ctx, pool, w.demo, w.ironOre, 15, cid, w.traderNode, w.traderNode, baseSim+deliverySim, arrived)
		ev := arrivedEvent(shID, cid, 15, w.traderNode, arrived)

		dupBefore := metricSum(t, reg, "ii_contract_deliveries_duplicate_total", "", "")
		mustHandleArrived(t, ctx, pool, dc, ev) // primera: entrega parcial 15
		mustHandleArrived(t, ctx, pool, dc, ev) // reproceso del MISMO cargamento

		// Idempotente: una sola entrega, quantity_delivered = 15 (no 30).
		row := getContractRow(t, ctx, pool, cid)
		if row.quantityDelivered != 15 {
			t.Fatalf("reproceso duplicó la entrega: quantity_delivered=%d, esperado 15", row.quantityDelivered)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.contract_deliveries WHERE shipment_id=$1`, shID); n != 1 {
			t.Fatalf("partidas para el cargamento: %d, esperado 1", n)
		}
		if n := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events WHERE aggregate_id=$1 AND event_type='contract.delivered'`, cid); n != 1 {
			t.Fatalf("eventos contract.delivered: %d, esperado 1 (el reproceso no reemite)", n)
		}
		if got := metricSum(t, reg, "ii_contract_deliveries_duplicate_total", "", ""); got != dupBefore+1 {
			t.Fatalf("ii_contract_deliveries_duplicate_total: %v, esperado %v", got, dupBefore+1)
		}
		assertLedgerBalanced(t, ctx, pool)
	})
}

// ─── Helpers de la integración de entrega ────────────────────────────────────

// activeCrossNodeContract publica una compra de trader (destino traderNode) que
// demo acepta aportando su almacén (demoNode): origen != destino ⇒ contrato
// cross-node que, tras el sorteo, queda 'active' a la espera de tránsito físico.
// Devuelve el id del contrato.
func activeCrossNodeContract(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	svc *contracts.Service, worker *contracts.Worker, w world, qty, price, delivery int64) uuid.UUID {
	t.Helper()
	pub, err := svc.CreatePublication(ctx, w.trader, buyCrossInput(w, qty, price, delivery))
	if err != nil {
		t.Fatalf("CreatePublication(buy cross-node): %v", err)
	}
	acc, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: qty, OriginNodeID: &w.demoNode})
	if err != nil {
		t.Fatalf("Accept(buy): %v", err)
	}
	waitWindow()
	worker.RunOnce(ctx)
	served, err := svc.GetAcceptance(ctx, w.demo, acc.ID)
	if err != nil {
		t.Fatalf("GetAcceptance: %v", err)
	}
	cid, err := svc.ResolveAcceptanceContract(ctx, served)
	if err != nil || cid == nil {
		t.Fatalf("ResolveAcceptanceContract: %v (%v)", err, cid)
	}
	row := getContractRow(t, ctx, pool, *cid)
	if row.status != "active" || row.originNode == row.destNode {
		t.Fatalf("contrato cross-node inesperado: %+v", row)
	}
	return *cid
}

// insertArrivedShipment crea la fila world.shipments (FK de contract_deliveries)
// que world habría materializado y entregado: un cargamento del contrato, ya en
// el nodo de destino (status 'delivered'). Devuelve su id.
func insertArrivedShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	owner, product uuid.UUID, qty int64, contractID, atNode, destNode uuid.UUID, deadline, simNow simtime.SimTime) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.shipments (id, owner_account_id, product_id, quantity, contract_id,
			at_node_id, status, destination_node_id, deadline_sim, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,'delivered',$7,$8,$9)`,
		id, owner, product, qty, contractID, atNode, destNode, int64(deadline), int64(simNow)); err != nil {
		t.Fatalf("insertando el cargamento: %v", err)
	}
	return id
}

// arrivedEvent construye el evento shipment.arrived con el payload FIJO.
func arrivedEvent(shipmentID, contractID uuid.UUID, qty int64, destNode uuid.UUID, arrivedAtSim simtime.SimTime) outbox.Event {
	payload, _ := json.Marshal(map[string]any{
		"shipment_id":         shipmentID.String(),
		"contract_id":         contractID.String(),
		"quantity":            strconv.FormatInt(qty, 10),
		"destination_node_id": destNode.String(),
		"arrived_at_sim":      int64(arrivedAtSim),
	})
	return outbox.Event{
		EventID:       uuid.Must(uuid.NewV7()),
		AggregateType: "shipment",
		AggregateID:   shipmentID,
		EventType:     "shipment.arrived",
		Payload:       payload,
		SimTimeAt:     int64(arrivedAtSim),
	}
}

// mustHandleArrived ejecuta el handler del delivery_confirmer dentro de una
// transacción SERIALIZABLE (como el lote del outbox) y la confirma.
func mustHandleArrived(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dc *contracts.DeliveryConfirmer, ev outbox.Event) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("abriendo la tx del lote: %v", err)
	}
	if err := dc.Handle(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("delivery_confirmer.Handle: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("confirmando la tx del lote: %v", err)
	}
}

// stockFreeQty lee el stock_free de (dueño, producto, almacén) tolerando la
// ausencia de la cuenta (0): el settle la crea on-demand en el destino.
func stockFreeQty(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE((SELECT balance FROM ledger.accounts
			WHERE kind='stock_free' AND owner_account_id=$1 AND product_id=$2 AND warehouse_building_id=$3), 0)`,
		owner, product, warehouse).Scan(&b); err != nil {
		t.Fatalf("stock_free de %s: %v", owner, err)
	}
	return b
}
