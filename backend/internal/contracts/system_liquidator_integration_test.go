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

// TestSystemLiquidatorIntegration ejercita la SUBASTA PÚBLICA del stock embargado
// (Incremento 6a, internal/contracts) contra una BD real: el consumidor
// system_liquidator que, desde building.seized (emitido por world/enforcement),
// transfiere el stock del moroso al banco central y lo publica como oferta sell
// del sistema al precio de remate; la idempotencia por building_id ante
// reprocesado; y el ciclo completo en el que un actor compra la subasta y el
// banco central cobra (efecto sink/absorción).
//
// Se omite si II_TEST_DATABASE_URL no está definida (misma BD efímera propia que
// el resto de tests de integración del módulo).
func TestSystemLiquidatorIntegration(t *testing.T) {
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
	opts.PublicationTTLSimSeconds = 100_000
	// II_LIQUIDATION_PRICE_BP = 6000 (default): iron_ore base_price 100 → remate 60.
	svc, err := contracts.NewService(pool, sim, opts, logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	worker, err := contracts.NewWorker(svc, contracts.WorkerOptions{SweepInterval: time.Second, BatchSize: 100}, logger, nil)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	reg := prometheus.NewRegistry()
	liq, err := contracts.NewSystemLiquidator(svc, logger, reg)
	if err != nil {
		t.Fatalf("NewSystemLiquidator: %v", err)
	}

	assertLedgerBalanced(t, ctx, pool)

	// Embargo sintético: 100 iron_ore del almacén de demo (retirada in situ en
	// demoNode). El liquidador no consulta el estado del edificio en world: opera
	// sobre el payload del evento (la frontera es de código Go, solo el outbox).
	const seizeQty int64 = 100
	const remate int64 = 60 // base_price(100) * 6000/10000
	const value = seizeQty * remate
	const guarantee = value / 10

	var pubID uuid.UUID

	t.Run("LiquidatesSeizedStockToBoard", func(t *testing.T) {
		sim.set(baseSim)
		demoStock0 := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		if demoStock0 < seizeQty {
			t.Fatalf("el seed debe dar a demo >= %d iron_ore (tiene %d)", seizeQty, demoStock0)
		}

		ev := seizedEvent(w.demoWarehouse, w.demo, w.regionID, w.demoNode, "abandoned",
			[]stockLine{{w.ironOre, seizeQty, w.demoWarehouse}}, baseSim)
		mustHandleSeized(t, ctx, pool, liq, ev)

		// El stock salió del moroso y pasó al sistema, ya BLOQUEADO en la reserva
		// de la publicación (stock_free del banco → stock_reserved al publicar).
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != demoStock0-seizeQty {
			t.Fatalf("stock_free del moroso tras el embargo: %d, esperado %d", got, demoStock0-seizeQty)
		}
		if got := balanceOf(t, ctx, pool, "stock_free", w.bank, &w.ironOre, &w.demoWarehouse); got != 0 {
			t.Fatalf("stock_free del banco tras publicar: %d, esperado 0 (todo en reserva)", got)
		}

		// La oferta sell del sistema es visible en el tablón, del banco central.
		pub := systemSellPublication(t, ctx, pool, w.bank)
		pubID = pub.id
		if pub.kind != "sell" || pub.channel != "board" || pub.quantityTotal != seizeQty ||
			pub.unitPrice != remate || pub.originNode != w.demoNode {
			t.Fatalf("oferta de subasta inesperada: %+v", pub)
		}
		if !pub.status.visible() {
			t.Fatalf("la oferta de subasta debe estar visible en el tablón: %s", pub.status)
		}
		// El stock embargado quedó reservado por la publicación (100 en el espejo).
		if got := balanceByID(t, ctx, pool, pub.stockReserve); got != seizeQty {
			t.Fatalf("stock reservado de la subasta: %d, esperado %d", got, seizeQty)
		}
		// Aparece también en el tablón consultable (QueryBoard).
		found := false
		page, _, err := svc.QueryBoard(ctx, contracts.BoardFilter{ProductID: &w.ironOre})
		if err != nil {
			t.Fatalf("QueryBoard: %v", err)
		}
		for _, p := range page {
			if p.ID == pubID {
				found = true
			}
		}
		if !found {
			t.Fatal("la oferta de subasta del sistema no apareció en el tablón")
		}

		// Evento publication.created emitido para la oferta del sistema.
		created := singleEvent(t, ctx, pool, pubID, "publication.created")
		if created["publisher_account_id"] != w.bank.String() || created["kind"] != "sell" ||
			created["quantity_total"] != strconv.FormatInt(seizeQty, 10) || created["unit_price"] != strconv.FormatInt(remate, 10) {
			t.Fatalf("payload de publication.created de la subasta: %v", created)
		}

		// Métricas de la subasta.
		if got := metricSum(t, reg, "ii_liquidation_publications_total", "", ""); got != 1 {
			t.Fatalf("ii_liquidation_publications_total: %v, esperado 1", got)
		}
		if got := metricSum(t, reg, "ii_liquidated_stock_total", "product", w.ironOre.String()); got != float64(seizeQty) {
			t.Fatalf("ii_liquidated_stock_total{iron_ore}: %v, esperado %d", got, seizeQty)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("IdempotentReprocess", func(t *testing.T) {
		sim.set(baseSim)
		skipBefore := metricSum(t, reg, "ii_liquidation_skipped_total", "", "")
		pubsBefore := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.publications WHERE publisher_account_id = $1`, w.bank)

		// Reproceso del MISMO embargo (mismo building_id): no re-subasta.
		ev := seizedEvent(w.demoWarehouse, w.demo, w.regionID, w.demoNode, "abandoned",
			[]stockLine{{w.ironOre, seizeQty, w.demoWarehouse}}, baseSim)
		mustHandleSeized(t, ctx, pool, liq, ev)

		if got := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.publications WHERE publisher_account_id = $1`, w.bank); got != pubsBefore {
			t.Fatalf("el reproceso creó publicaciones nuevas: %d, esperado %d", got, pubsBefore)
		}
		if got := metricSum(t, reg, "ii_liquidation_skipped_total", "", ""); got != skipBefore+1 {
			t.Fatalf("ii_liquidation_skipped_total: %v, esperado %v", got, skipBefore+1)
		}
		if got := metricSum(t, reg, "ii_liquidation_publications_total", "", ""); got != 1 {
			t.Fatalf("ii_liquidation_publications_total tras reproceso: %v, esperado 1 (sin duplicar)", got)
		}
		assertLedgerBalanced(t, ctx, pool)
	})

	t.Run("ActorBuysAuctionSystemCollects", func(t *testing.T) {
		sim.set(baseSim)
		if pubID == uuid.Nil {
			t.Skip("la subasta no se publicó en el subtest previo")
		}
		traderCash0 := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)
		bankCash0 := balanceOf(t, ctx, pool, "cash", w.bank, nil, nil) // baseline: tesorería del seed menos la garantía ya bloqueada
		traderStock0 := stockFreeQty(t, ctx, pool, w.trader, w.ironOre, w.demoWarehouse)

		// Un actor (trader) compra la oferta de subasta del sistema (escrow 100%).
		acc, err := svc.Accept(ctx, w.trader, pubID, contracts.AcceptInput{Quantity: seizeQty})
		if err != nil {
			t.Fatalf("Accept(subasta): %v", err)
		}
		waitWindow()
		worker.RunOnce(ctx) // resuelve el sorteo ⇒ venta in situ, liquida al 100%

		served, err := svc.GetAcceptance(ctx, w.trader, acc.ID)
		if err != nil {
			t.Fatalf("GetAcceptance: %v", err)
		}
		cid, err := svc.ResolveAcceptanceContract(ctx, served)
		if err != nil || cid == nil {
			t.Fatalf("ResolveAcceptanceContract: %v (%v)", err, cid)
		}
		c := getContractRow(t, ctx, pool, *cid)
		if c.status != "settled" || c.fillBP != 10000 || c.originNode != c.destNode {
			t.Fatalf("contrato de subasta in situ inesperado: %+v", c)
		}

		// El comprador pagó el remate (6000) y recibió el stock EN el almacén de origen.
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != traderCash0-value {
			t.Fatalf("caja del comprador de la subasta: %d, esperado %d", got, traderCash0-value)
		}
		if got := stockFreeQty(t, ctx, pool, w.trader, w.ironOre, w.demoWarehouse); got != traderStock0+seizeQty {
			t.Fatalf("stock del comprador en el almacén de origen: %d, esperado %d", got, traderStock0+seizeQty)
		}
		// El banco central COBRA: proceeds (6000, absorción/sink) + la garantía que
		// aportó (600, de su tesorería) que retorna a la caja al liquidar. Delta sobre
		// el baseline = value + guarantee, sea cual sea la tesorería de partida.
		if got := balanceOf(t, ctx, pool, "cash", w.bank, nil, nil); got != bankCash0+value+guarantee {
			t.Fatalf("caja del banco central tras cobrar la subasta: %d, esperado %d (proceeds %d + garantía %d)",
				got, bankCash0+value+guarantee, value, guarantee)
		}
		// Cuentas espejo del contrato liberadas.
		assertZero(t, ctx, pool, c.stockAccount, "reserva del contrato de subasta")
		assertZero(t, ctx, pool, c.guaranteeAccount, "garantía del contrato de subasta")
		assertZero(t, ctx, pool, c.escrowAccount, "escrow del contrato de subasta")

		settled := singleEvent(t, ctx, pool, *cid, "contract.settled")
		if settled["status"] != "settled" || settled["fill_bp"] != float64(10000) {
			t.Fatalf("payload de contract.settled de la subasta: %v", settled)
		}
		assertLedgerBalanced(t, ctx, pool)
	})
}

// ─── Helpers de la integración de la subasta ─────────────────────────────────

// stockLine es una línea de stock embargado para el payload building.seized.
type stockLine struct {
	product   uuid.UUID
	quantity  int64
	warehouse uuid.UUID
}

// seizedEvent construye el evento building.seized con el payload FIJO del
// Incremento 6a (contrato de integración world/enforcement↔contracts).
func seizedEvent(buildingID, owner, region, originNode uuid.UUID, reason string, stock []stockLine, seizedAtSim simtime.SimTime) outbox.Event {
	items := make([]map[string]any, len(stock))
	for i, s := range stock {
		items[i] = map[string]any{
			"product_id":            s.product.String(),
			"quantity":              strconv.FormatInt(s.quantity, 10),
			"warehouse_building_id": s.warehouse.String(),
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"building_id":      buildingID.String(),
		"owner_account_id": owner.String(),
		"region_id":        region.String(),
		"origin_node_id":   originNode.String(),
		"reason":           reason,
		"stock":            items,
		"seized_at_sim":    int64(seizedAtSim),
	})
	return outbox.Event{
		EventID:       uuid.Must(uuid.NewV7()),
		AggregateType: "building",
		AggregateID:   buildingID,
		EventType:     "building.seized",
		Payload:       payload,
		SimTimeAt:     int64(seizedAtSim),
	}
}

// mustHandleSeized ejecuta el handler del system_liquidator dentro de una
// transacción SERIALIZABLE (como el lote del outbox) y la confirma.
func mustHandleSeized(t *testing.T, ctx context.Context, pool *pgxpool.Pool, liq *contracts.SystemLiquidator, ev outbox.Event) {
	t.Helper()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("abriendo la tx del lote: %v", err)
	}
	if err := liq.Handle(ctx, tx, ev); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("system_liquidator.Handle: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("confirmando la tx del lote: %v", err)
	}
}

// auctionPub es la vista de una oferta sell del sistema leída de la BD.
type auctionPub struct {
	id            uuid.UUID
	kind          string
	channel       string
	quantityTotal int64
	unitPrice     int64
	originNode    uuid.UUID
	status        pubStatus
	stockReserve  uuid.UUID
}

// pubStatus expone si un estado de publicación es visible en el tablón.
type pubStatus string

func (s pubStatus) visible() bool {
	switch string(s) {
	case "draw_window", "open", "micro_window":
		return true
	}
	return false
}

// systemSellPublication lee la última oferta sell publicada por el banco central.
func systemSellPublication(t *testing.T, ctx context.Context, pool *pgxpool.Pool, bank uuid.UUID) auctionPub {
	t.Helper()
	var p auctionPub
	var status string
	if err := pool.QueryRow(ctx, `
		SELECT id, kind, channel, quantity_total, unit_price, origin_node_id, status, stock_reserve_account_id
		FROM ledger.publications
		WHERE publisher_account_id = $1 AND kind = 'sell'
		ORDER BY created_at DESC, id DESC
		LIMIT 1`, bank).Scan(
		&p.id, &p.kind, &p.channel, &p.quantityTotal, &p.unitPrice, &p.originNode, &status, &p.stockReserve); err != nil {
		t.Fatalf("leyendo la oferta de subasta del banco: %v", err)
	}
	p.status = pubStatus(status)
	return p
}
