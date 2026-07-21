package contracts_test

import (
	"context"
	"encoding/json"
	"errors"
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

// TestFreightIntegration ejercita el CCRI-Flete de extremo a extremo contra una
// BD real: publicación (escrow del cargador), aceptación (garantía del
// transportista), confirmación (custodia asentada, carga NO vendible por el
// transportista), entrega on-time (liquidación: cargador recibe la carga en
// destino, transportista cobra y recupera garantía) y fallo por vencimiento
// (custodia liberada in situ, garantía repartida compensación/sink). Cada
// escenario comprueba que el ledger cuadra a cero por dinero y por producto.
func TestFreightIntegration(t *testing.T) {
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
	opts.PublicationTTLSimSeconds = 5_000_000
	opts.FreightGuaranteeBP = 1000    // 10% del valor declarado
	opts.FreightCompensationBP = 5000 // 50/50 en el fallo
	svc, err := contracts.NewService(pool, sim, opts, logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	reg := prometheus.NewRegistry()
	worker, err := contracts.NewWorker(svc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, reg)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	settler, err := contracts.NewFreightSettler(svc, logger, reg)
	if err != nil {
		t.Fatalf("NewFreightSettler: %v", err)
	}

	// shipper = demo (tiene iron_ore en su almacén y caja); carrier = trader (tiene
	// caja y un almacén propio, pero NO iron_ore: la custodia no será suya).
	const (
		qty          = int64(50)
		freightUnit  = int64(10) // precio del flete por unidad
		freightTotal = qty * freightUnit
		declared     = int64(100_000) // valor declarado de la carga
		guarantee    = declared / 10  // 10% del valor declarado
	)

	assertLedgerBalanced(t, ctx, pool)

	t.Run("PublishBlocksShipperEscrow", func(t *testing.T) {
		sim.set(baseSim)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, freightInput(w, qty, freightUnit, declared, 1_000_000))
		if err != nil {
			t.Fatalf("CreatePublication(freight): %v", err)
		}
		if pub.Kind != contracts.KindFreight || pub.EscrowAccountID == nil ||
			pub.DeclaredValue == nil || *pub.DeclaredValue != declared {
			t.Fatalf("publicación de flete inesperada: %+v", pub)
		}
		if got := balanceByID(t, ctx, pool, *pub.EscrowAccountID); got != freightTotal {
			t.Fatalf("escrow del flete: %d, esperado %d", got, freightTotal)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0-freightTotal {
			t.Fatalf("caja del cargador tras publicar: %d, esperado %d", got, demoCash0-freightTotal)
		}
		p := singleEvent(t, ctx, pool, pub.ID, "publication.created")
		if p["kind"] != "freight" || p["declared_value"] != strconv.FormatInt(declared, 10) {
			t.Fatalf("payload publication.created de flete inesperado: %v", p)
		}
		// Limpieza: cancelar para no interferir con los siguientes escenarios.
		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); err != nil {
			t.Fatalf("CancelPublication: %v", err)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("AcceptBlocksCarrierGuarantee", func(t *testing.T) {
		sim.set(baseSim)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, freightInput(w, qty, freightUnit, declared, 1_000_000))
		if err != nil {
			t.Fatalf("CreatePublication(freight): %v", err)
		}
		acc, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: qty})
		if err != nil {
			t.Fatalf("Accept(freight): %v", err)
		}
		if acc.GuaranteeAccountID == nil {
			t.Fatalf("la aceptación de flete debe bloquear una garantía: %+v", acc)
		}
		if got := balanceByID(t, ctx, pool, *acc.GuaranteeAccountID); got != guarantee {
			t.Fatalf("garantía del transportista: %d, esperado %d", got, guarantee)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0-guarantee {
			t.Fatalf("caja del transportista tras aceptar: %d, esperado %d", got, traderCash0-guarantee)
		}
		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); err != nil {
			t.Fatalf("CancelPublication: %v", err)
		}
		// Cancelar libera la aceptación pendiente: la garantía vuelve al transportista.
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0 {
			t.Fatalf("garantía no liberada al cancelar: %d, esperado %d", got, traderCash0)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("ConfirmLoadsCustodyNotSellableByCarrier", func(t *testing.T) {
		sim.set(baseSim)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)

		fc := confirmFreight(t, ctx, pool, svc, worker, w, qty, freightUnit, declared, 1_000_000)

		if fc.status != "active" {
			t.Fatalf("flete tras confirmar: status=%s", fc.status)
		}
		if got := balanceByID(t, ctx, pool, fc.escrow); got != freightTotal {
			t.Fatalf("escrow del contrato: %d, esperado %d", got, freightTotal)
		}
		if got := balanceByID(t, ctx, pool, fc.guarantee); got != guarantee {
			t.Fatalf("garantía del contrato: %d, esperado %d", got, guarantee)
		}
		if got := balanceByID(t, ctx, pool, fc.custody); got != qty {
			t.Fatalf("custodia del contrato: %d, esperado %d", got, qty)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0-qty {
			t.Fatalf("stock del cargador tras cargar custodia: %d, esperado %d", got, demoStock0-qty)
		}
		// La custodia es del CARGADOR (o del sistema), NUNCA del transportista.
		if owner := custodyOwner(t, ctx, pool, fc.custody); owner != w.demo {
			t.Fatalf("dueño de la custodia: %s, esperado el cargador %s", owner, w.demo)
		}
		// El transportista NO puede vender la carga que lleva: contablemente está en
		// custody (no en su stock_free). En el almacén de origen —donde se atribuye
		// la custodia— el transportista no tiene stock_free de ese producto, así que
		// intentar publicarla como sell falla por falta de stock (lo impide el ledger).
		_, err := svc.CreatePublication(ctx, w.trader, sellFromNode(w, w.demoNode, w.ironOre, qty, freightUnit))
		if !isCollateralStock(err) {
			t.Fatalf("el transportista no debería poder vender la carga en custodia: err=%v", err)
		}
		p := singleEvent(t, ctx, pool, fc.id, "freight.confirmed")
		if p["shipper_account_id"] != w.demo.String() || p["carrier_account_id"] != w.trader.String() ||
			p["quantity"] != strconv.FormatInt(qty, 10) {
			t.Fatalf("payload freight.confirmed inesperado: %v", p)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("OnTimeDeliverySettles", func(t *testing.T) {
		sim.set(baseSim)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)

		fc := confirmFreight(t, ctx, pool, svc, worker, w, qty, freightUnit, declared, 1_000_000)
		// Tras confirmar: cargador -freightTotal (escrow), transportista -guarantee.
		// deadline = baseSim + 1_000_000; la llegada a baseSim+5_000 es on-time.
		shipmentID := insertFreightShipment(t, ctx, pool, w.demo, w.ironOre, qty, fc.id, w.traderNode, "delivered")
		arrived := simtime.SimTime(baseSim + 5_000)
		mustHandleFreightArrived(t, ctx, pool, settler, freightArrivedEvent(shipmentID, fc.id, qty, w.traderNode, arrived))

		// La carga llega al cargador en el DESTINO; la custodia queda vacía.
		if got := balanceByID(t, ctx, pool, fc.custody); got != 0 {
			t.Fatalf("custodia tras entregar: %d, esperado 0", got)
		}
		if got := stockFreeQty(t, ctx, pool, w.demo, w.ironOre, w.traderWarehouse); got != qty {
			t.Fatalf("carga entregada al cargador en destino: %d, esperado %d", got, qty)
		}
		// Transportista: recupera garantía (+guarantee) y cobra el flete (+freightTotal).
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0-guarantee+guarantee+freightTotal {
			t.Fatalf("caja del transportista tras liquidar: %d, esperado %d", got, traderCash0+freightTotal)
		}
		// Cargador: pagó el flete (net -freightTotal).
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0-freightTotal {
			t.Fatalf("caja del cargador tras liquidar: %d, esperado %d", got, demoCash0-freightTotal)
		}
		row := freightRow(t, ctx, pool, fc.id)
		if row.status != "settled" || row.fillBP != 10000 {
			t.Fatalf("flete on-time: status=%s fill=%d, esperado settled/10000", row.status, row.fillBP)
		}
		p := singleEvent(t, ctx, pool, fc.id, "freight.settled")
		if p["status"] != "settled" || p["fill_bp"] != float64(10000) {
			t.Fatalf("payload freight.settled inesperado: %v", p)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("FailureSplitsGuaranteeAndReleasesCustody", func(t *testing.T) {
		sim.set(baseSim)
		demoCash0 := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		sink0 := sinkBalance(t, ctx, pool)

		// Plazo corto: no se entrega y vence.
		fc := confirmFreight(t, ctx, pool, svc, worker, w, qty, freightUnit, declared, 100)

		// Vence el plazo (deadline = baseSim+100): avanza el sim y barre.
		sim.set(baseSim + 500)
		worker.RunOnce(ctx)

		row := freightRow(t, ctx, pool, fc.id)
		if row.status != "failed" || row.fillBP != 0 {
			t.Fatalf("flete fallido: status=%s fill=%d, esperado failed/0", row.status, row.fillBP)
		}
		// Custodia liberada in situ en el ORIGEN (la carga vuelve al cargador allí).
		if got := balanceByID(t, ctx, pool, fc.custody); got != 0 {
			t.Fatalf("custodia tras el fallo: %d, esperado 0", got)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0 {
			t.Fatalf("carga liberada in situ en origen: %d, esperado %d", got, demoStock0)
		}
		// Cargador: el flete se le reembolsa (net 0) y cobra la compensación (50% de la garantía).
		comp := guarantee / 2
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != demoCash0+comp {
			t.Fatalf("caja del cargador tras el fallo: %d, esperado %d (reembolso + compensación %d)", got, demoCash0+comp, comp)
		}
		// Transportista: pierde la garantía íntegra (no cobra el flete).
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0-guarantee {
			t.Fatalf("caja del transportista tras el fallo: %d, esperado %d", got, traderCash0-guarantee)
		}
		// Sink: la otra mitad de la garantía se destruye.
		if got := sinkBalance(t, ctx, pool); got != sink0+(guarantee-comp) {
			t.Fatalf("sink tras el fallo: %d, esperado %d", got, sink0+(guarantee-comp))
		}
		_ = singleEvent(t, ctx, pool, fc.id, "freight.settled")
		_ = singleEvent(t, ctx, pool, fc.id, "freight.expired_undelivered")
		assertLedgerBalanced(t, ctx, pool)
	})
}

// ─── Helpers del flete ───────────────────────────────────────────────────────

// freightInput construye una solicitud de flete de iron_ore de demoNode a
// traderNode (el cargador la publica).
func freightInput(w world, qty, freightUnit, declared, delivery int64) contracts.PublicationInput {
	product := w.ironOre
	origin := w.demoNode
	dest := w.traderNode
	return contracts.PublicationInput{
		Kind:               contracts.KindFreight,
		ProductID:          &product,
		QuantityTotal:      qty,
		UnitPrice:          freightUnit,
		OriginNodeID:       &origin,
		DestinationNodeID:  &dest,
		DeliverySimSeconds: delivery,
		DeclaredValue:      declared,
	}
}

// sellFromNode construye una publicación sell desde un nodo dado (para probar que
// el transportista NO puede vender la carga en custodia).
func sellFromNode(w world, node, product uuid.UUID, qty, price int64) contracts.PublicationInput {
	p := product
	n := node
	return contracts.PublicationInput{
		Kind:               contracts.KindSell,
		ProductID:          &p,
		QuantityTotal:      qty,
		UnitPrice:          price,
		OriginNodeID:       &n,
		DeliverySimSeconds: 3600,
	}
}

// freightContractView es la vista mínima de un freight_contract y sus cuentas.
type freightContractView struct {
	id                         uuid.UUID
	status                     string
	fillBP                     int32
	escrow, guarantee, custody uuid.UUID
}

// confirmFreight publica, acepta y resuelve el sorteo de un flete y devuelve el
// contrato resultante con sus cuentas espejo.
func confirmFreight(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	svc *contracts.Service, worker *contracts.Worker, w world, qty, freightUnit, declared, delivery int64) freightContractView {
	t.Helper()
	pub, err := svc.CreatePublication(ctx, w.demo, freightInput(w, qty, freightUnit, declared, delivery))
	if err != nil {
		t.Fatalf("CreatePublication(freight): %v", err)
	}
	if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: qty}); err != nil {
		t.Fatalf("Accept(freight): %v", err)
	}
	waitWindow()
	worker.RunOnce(ctx)

	var v freightContractView
	err = pool.QueryRow(ctx, `
		SELECT id, status, COALESCE(fill_bp,0), escrow_account_id, carrier_guarantee_account_id, custody_account_id
		FROM ledger.freight_contracts WHERE publication_id = $1`, pub.ID).Scan(
		&v.id, &v.status, &v.fillBP, &v.escrow, &v.guarantee, &v.custody)
	if err != nil {
		t.Fatalf("leyendo el freight_contract de %s: %v", pub.ID, err)
	}
	return v
}

// freightRow relee un freight_contract por id.
func freightRow(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) freightContractView {
	t.Helper()
	var v freightContractView
	err := pool.QueryRow(ctx, `
		SELECT id, status, COALESCE(fill_bp,0), escrow_account_id, carrier_guarantee_account_id, custody_account_id
		FROM ledger.freight_contracts WHERE id = $1`, id).Scan(
		&v.id, &v.status, &v.fillBP, &v.escrow, &v.guarantee, &v.custody)
	if err != nil {
		t.Fatalf("leyendo el freight_contract %s: %v", id, err)
	}
	return v
}

// custodyOwner devuelve el dueño de una cuenta de custodia.
func custodyOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID) uuid.UUID {
	t.Helper()
	var owner uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT owner_account_id FROM ledger.accounts WHERE id = $1`, accountID).Scan(&owner); err != nil {
		t.Fatalf("dueño de la custodia %s: %v", accountID, err)
	}
	return owner
}

// isCollateralStock indica si err es un CollateralError por falta de stock.
func isCollateralStock(err error) bool {
	var collErr *contracts.CollateralError
	return errors.As(err, &collErr) && collErr.Resource == "stock"
}

// insertFreightShipment crea un cargamento de flete (owner=cargador,
// freight_contract_id) en el nodo destino con el estado dado.
func insertFreightShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	owner, product uuid.UUID, qty int64, freightID, atNode uuid.UUID, status string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.shipments (id, owner_account_id, product_id, quantity, freight_contract_id,
			at_node_id, status, destination_node_id, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,$7::world.shipment_status,$6,0)`,
		id, owner, product, qty, freightID, atNode, status); err != nil {
		t.Fatalf("insertando el cargamento de flete: %v", err)
	}
	return id
}

// freightArrivedEvent construye el evento shipment.arrived de un cargamento de
// flete (freight_contract_id presente, contract_id ausente).
func freightArrivedEvent(shipmentID, freightID uuid.UUID, qty int64, destNode uuid.UUID, arrivedAtSim simtime.SimTime) outbox.Event {
	payload, _ := json.Marshal(map[string]any{
		"shipment_id":         shipmentID.String(),
		"freight_contract_id": freightID.String(),
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

// mustHandleFreightArrived ejecuta el handler del freight_settler dentro de una
// transacción SERIALIZABLE (como el lote del outbox) y la confirma.
func mustHandleFreightArrived(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fs *contracts.FreightSettler, ev outbox.Event) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("abriendo la tx del lote: %v", err)
	}
	if err := fs.Handle(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("freight_settler.Handle: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("confirmando la tx del lote: %v", err)
	}
}
