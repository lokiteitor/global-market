package contracts_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// simNow es el sim-time fijo que inyecta el reloj stub del test.
const simNow simtime.SimTime = 12_345

// simStub implementa contracts.SimSource con un reloj fijo.
type simStub struct{}

func (simStub) Now(context.Context) simtime.SimTime { return simNow }

// world reúne los IDs del mundo mínimo sembrado (internal/seed), resueltos
// por clave natural.
type world struct {
	demo, trader, bank          uuid.UUID
	ironOre, coal               uuid.UUID
	regionID                    uuid.UUID
	demoNode, demoWarehouse     uuid.UUID
	traderNode, traderWarehouse uuid.UUID
	bareNode                    uuid.UUID // nodo sin edificio (junction)
}

// TestContractsIntegration ejercita el ciclo de publicación y aceptación del
// CCRI contra una BD real con el esquema migrado y el seed del Incremento 1:
// bloqueos contables verificados saldo a saldo, todo-o-nada ante colateral
// insuficiente, cooldown anti-parpadeo, liberaciones en cancelación (propias
// y de aceptantes pendientes), min_lot, canal privado, micro-ventana y la
// consulta del tablón con filtros, órdenes y cursor keyset.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales y la destruye al acabar.
func TestContractsIntegration(t *testing.T) {
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

	reg := prometheus.NewRegistry()
	opts := contracts.DefaultOptions()
	opts.CancelCooldownSeconds = 0 // los tests de cancelación no esperan
	svc, err := contracts.NewService(pool, simStub{}, opts, logger, reg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	cooldownOpts := opts
	cooldownOpts.CancelCooldownSeconds = 3600 // cooldown siempre activo
	svcCooldown, err := contracts.NewService(pool, simStub{}, cooldownOpts, logger, nil)
	if err != nil {
		t.Fatalf("NewService (cooldown): %v", err)
	}

	t.Run("PublishSellHappy", func(t *testing.T) {
		stockBefore := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		cashBefore := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 10))
		if err != nil {
			t.Fatalf("CreatePublication(sell): %v", err)
		}
		if pub.Kind != contracts.KindSell || pub.Status != contracts.StatusDrawWindow ||
			pub.Channel != contracts.ChannelBoard || pub.QuantityRemaining != 100 ||
			pub.QuantityTotal != 100 || pub.UnitPrice != 50 || pub.MinLot != 10 ||
			pub.PublishedAtSim != simNow {
			t.Fatalf("publicación inesperada: %+v", pub)
		}
		if pub.WindowClosesAt == nil || pub.CancelCooldownUntil == nil {
			t.Fatal("window_closes_at y cancel_cooldown_until deben fijarse al publicar")
		}
		if pub.StockReserveAccountID == nil || pub.GuaranteeAccountID == nil || pub.EscrowAccountID != nil {
			t.Fatalf("cuentas espejo de sell inesperadas: %+v", pub)
		}

		// Verificación CONTABLE completa: -100 stock_free → +100 reservado;
		// -500 caja (10% de 5000) → +500 garantía.
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != stockBefore-100 {
			t.Fatalf("stock_free tras publicar: %d, esperado %d", got, stockBefore-100)
		}
		if got := balanceByID(t, ctx, pool, *pub.StockReserveAccountID); got != 100 {
			t.Fatalf("stock_reserved espejo: %d, esperado 100", got)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != cashBefore-500 {
			t.Fatalf("caja tras publicar: %d, esperado %d", got, cashBefore-500)
		}
		if got := balanceByID(t, ctx, pool, *pub.GuaranteeAccountID); got != 500 {
			t.Fatalf("guarantee espejo: %d, esperado 500", got)
		}

		// Asiento publication_lock con 4 partidas, referenciado a la publicación.
		if got := countRows(t, ctx, pool, `
			SELECT count(*) FROM ledger.entries e
			JOIN ledger.transactions tr ON tr.id = e.transaction_id
			WHERE tr.kind = 'publication_lock' AND tr.reference_id = $1`, pub.ID); got != 4 {
			t.Fatalf("partidas del publication_lock: %d, esperado 4", got)
		}

		// Evento publication.created en la MISMA transacción, con dinero/stock
		// como string.
		payload := singleEvent(t, ctx, pool, pub.ID, "publication.created")
		if payload["unit_price"] != "50" || payload["quantity_total"] != "100" ||
			payload["kind"] != "sell" || payload["published_at_sim"] != float64(simNow) {
			t.Fatalf("payload de publication.created inesperado: %v", payload)
		}

		if got := metricSum(t, reg, "ii_publications_created_total", "kind", "sell"); got < 1 {
			t.Errorf("ii_publications_created_total{kind=sell}: %v, esperado >= 1", got)
		}
	})

	t.Run("PublishSellInsufficientStock", func(t *testing.T) {
		before := snapshotCounts(t, ctx, pool)
		available := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)

		_, err := svc.CreatePublication(ctx, w.demo, sellInput(w, available+1, 50, 1))
		var collErr *contracts.CollateralError
		if !errors.Is(err, contracts.ErrInsufficientCollateral) || !errors.As(err, &collErr) {
			t.Fatalf("stock insuficiente: %v, esperado CollateralError", err)
		}
		if collErr.Resource != "stock" || collErr.Required != available+1 || collErr.Available != available {
			t.Fatalf("details del colateral: %+v", collErr)
		}
		assertNoEffects(t, ctx, pool, before)
	})

	t.Run("PublishSellInsufficientCash", func(t *testing.T) {
		before := snapshotCounts(t, ctx, pool)
		cash := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		// 100 unidades sí hay; la garantía del 10% (1e10) excede la caja.
		_, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 1_000_000_000, 1))
		var collErr *contracts.CollateralError
		if !errors.As(err, &collErr) || collErr.Resource != "cash" {
			t.Fatalf("caja insuficiente: %v, esperado CollateralError{cash}", err)
		}
		if collErr.Required != 10_000_000_000 || collErr.Available != cash {
			t.Fatalf("details del colateral: %+v", collErr)
		}
		assertNoEffects(t, ctx, pool, before)
	})

	t.Run("PublishBuyEscrow", func(t *testing.T) {
		cashBefore := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.trader, buyInput(w, 10, 100))
		if err != nil {
			t.Fatalf("CreatePublication(buy): %v", err)
		}
		if pub.Kind != contracts.KindBuy || pub.Status != contracts.StatusDrawWindow ||
			pub.EscrowAccountID == nil || pub.StockReserveAccountID != nil || pub.GuaranteeAccountID != nil {
			t.Fatalf("publicación buy inesperada: %+v", pub)
		}
		if got := balanceByID(t, ctx, pool, *pub.EscrowAccountID); got != 1000 {
			t.Fatalf("escrow espejo: %d, esperado 1000 (100%% del valor)", got)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != cashBefore-1000 {
			t.Fatalf("caja tras publicar buy: %d, esperado %d", got, cashBefore-1000)
		}
		if got := metricSum(t, reg, "ii_publications_created_total", "kind", "buy"); got < 1 {
			t.Errorf("ii_publications_created_total{kind=buy}: %v, esperado >= 1", got)
		}
	})

	t.Run("PublishValidations", func(t *testing.T) {
		before := snapshotCounts(t, ctx, pool)
		ghost := uuid.New()

		cases := []struct {
			name string
			in   contracts.PublicationInput
			want error
		}{
			{"freight es Fase 2", contracts.PublicationInput{Kind: contracts.KindFreight}, contracts.ErrFreightPhase2},
			{"producto inexistente", func() contracts.PublicationInput {
				in := sellInput(w, 10, 10, 1)
				in.ProductID = &ghost
				return in
			}(), contracts.ErrValidation},
			{"nodo de origen inexistente", func() contracts.PublicationInput {
				in := sellInput(w, 10, 10, 1)
				in.OriginNodeID = &ghost
				return in
			}(), contracts.ErrValidation},
			{"nodo de origen sin almacén", func() contracts.PublicationInput {
				in := sellInput(w, 10, 10, 1)
				in.OriginNodeID = &w.bareNode
				return in
			}(), contracts.ErrValidation},
			{"desbordamiento qty*price", sellInput(w, 4_000_000_000, 4_000_000_000, 1), contracts.ErrOverflow},
			{"desbordamiento del margen ×10", sellInput(w, 1_000_000_000, 1_200_000_000, 1), contracts.ErrOverflow},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := svc.CreatePublication(ctx, w.demo, tc.in); !errors.Is(err, tc.want) {
					t.Fatalf("%s: %v, esperado %v", tc.name, err, tc.want)
				}
			})
		}
		assertNoEffects(t, ctx, pool, before)
	})

	t.Run("CancelCooldownActive", func(t *testing.T) {
		pub, err := svcCooldown.CreatePublication(ctx, w.demo, sellInput(w, 10, 10, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		_, err = svcCooldown.CancelPublication(ctx, w.demo, pub.ID)
		var cdErr *contracts.CooldownError
		if !errors.Is(err, contracts.ErrCancelCooldownActive) || !errors.As(err, &cdErr) {
			t.Fatalf("cancelación en cooldown: %v, esperado CooldownError", err)
		}
		if pub.CancelCooldownUntil == nil || !cdErr.Until.Equal(*pub.CancelCooldownUntil) {
			t.Fatalf("details del cooldown: %v, esperado %v", cdErr.Until, pub.CancelCooldownUntil)
		}
		// La fila conserva el cooldown de su creación: también bloquea al
		// servicio sin cooldown (la regla vive en el dato, no en la config).
		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); !errors.Is(err, contracts.ErrCancelCooldownActive) {
			t.Fatalf("cooldown persistido en la fila: %v", err)
		}
	})

	t.Run("CancelHappyReleasesCollateral", func(t *testing.T) {
		stockBefore := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		cashBefore := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 200, 40, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		// Solo el publicador puede cancelar.
		if _, err := svc.CancelPublication(ctx, w.trader, pub.ID); !errors.Is(err, contracts.ErrNotPublisher) {
			t.Fatalf("cancelación ajena: %v, esperado ErrNotPublisher", err)
		}

		cancelled, err := svc.CancelPublication(ctx, w.demo, pub.ID)
		if err != nil {
			t.Fatalf("CancelPublication: %v", err)
		}
		if cancelled.Status != contracts.StatusCancelled {
			t.Fatalf("estado tras cancelar: %s", cancelled.Status)
		}
		// Liberación íntegra: saldos como antes de publicar; espejos a cero.
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != stockBefore {
			t.Fatalf("stock_free tras cancelar: %d, esperado %d", got, stockBefore)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != cashBefore {
			t.Fatalf("caja tras cancelar: %d, esperado %d", got, cashBefore)
		}
		if got := balanceByID(t, ctx, pool, *pub.StockReserveAccountID); got != 0 {
			t.Fatalf("stock_reserved espejo tras cancelar: %d", got)
		}
		if got := balanceByID(t, ctx, pool, *pub.GuaranteeAccountID); got != 0 {
			t.Fatalf("guarantee espejo tras cancelar: %d", got)
		}
		singleEvent(t, ctx, pool, pub.ID, "publication.cancelled")

		// Re-cancelar una publicación cancelada → PUBLICATION_EXHAUSTED.
		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); !errors.Is(err, contracts.ErrPublicationExhausted) {
			t.Fatalf("re-cancelación: %v, esperado ErrPublicationExhausted", err)
		}
		// Publicación inexistente → NOT_FOUND.
		if _, err := svc.CancelPublication(ctx, w.demo, uuid.New()); !errors.Is(err, contracts.ErrPublicationNotFound) {
			t.Fatalf("cancelar inexistente: %v, esperado ErrPublicationNotFound", err)
		}
	})

	t.Run("AcceptSellLocksEscrow", func(t *testing.T) {
		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		cashBefore := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)

		// El publicador no puede aceptarse a sí mismo.
		if _, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 10}); !errors.Is(err, contracts.ErrValidation) {
			t.Fatalf("autoaceptación: %v, esperado ErrValidation", err)
		}
		// Publicación inexistente → NOT_FOUND.
		if _, err := svc.Accept(ctx, w.trader, uuid.New(), contracts.AcceptInput{Quantity: 10}); !errors.Is(err, contracts.ErrPublicationNotFound) {
			t.Fatalf("aceptar inexistente: %v, esperado ErrPublicationNotFound", err)
		}

		acc, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 50})
		if err != nil {
			t.Fatalf("Accept(sell): %v", err)
		}
		if acc.Status != contracts.AcceptancePendingDraw || acc.Quantity != 50 ||
			acc.QuantityServed != 0 || acc.EscrowAccountID == nil ||
			acc.StockReserveAccountID != nil || acc.GuaranteeAccountID != nil {
			t.Fatalf("aceptación inesperada: %+v", acc)
		}
		// Escrow del comprador: 100% del valor (50×50=2500).
		if got := balanceByID(t, ctx, pool, *acc.EscrowAccountID); got != 2500 {
			t.Fatalf("escrow del aceptante: %d, esperado 2500", got)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != cashBefore-2500 {
			t.Fatalf("caja del aceptante: %d, esperado %d", got, cashBefore-2500)
		}
		// La aceptación en draw_window NO abre micro-ventana.
		if p, _ := svc.GetPublication(ctx, w.demo, pub.ID); p.Status != contracts.StatusDrawWindow {
			t.Fatalf("estado tras aceptar en draw_window: %s", p.Status)
		}
		payload := singleEvent(t, ctx, pool, acc.ID, "acceptance.registered")
		if payload["quantity"] != "50" || payload["publication_id"] != pub.ID.String() {
			t.Fatalf("payload de acceptance.registered: %v", payload)
		}

		// Visibilidad de la aceptación: solo el aceptante.
		if _, err := svc.GetAcceptance(ctx, w.demo, acc.ID); !errors.Is(err, contracts.ErrNotAcceptor) {
			t.Fatalf("aceptación ajena: %v, esperado ErrNotAcceptor", err)
		}
		got, err := svc.GetAcceptance(ctx, w.trader, acc.ID)
		if err != nil || got.ID != acc.ID {
			t.Fatalf("GetAcceptance: %v (%+v)", err, got)
		}
		if _, err := svc.GetAcceptance(ctx, w.trader, uuid.New()); !errors.Is(err, contracts.ErrAcceptanceNotFound) {
			t.Fatalf("aceptación inexistente: %v", err)
		}

		// Colateral insuficiente del aceptante: cero efectos. El publicador
		// puede cubrir la garantía del 10% (99.000), pero el 100% del valor
		// (9.900.000) excede la caja del aceptante.
		bigPub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 99_000, 1))
		if err != nil {
			t.Fatalf("CreatePublication(cara): %v", err)
		}
		before := snapshotCounts(t, ctx, pool)
		var collErr *contracts.CollateralError
		if _, err := svc.Accept(ctx, w.trader, bigPub.ID, contracts.AcceptInput{Quantity: 100}); !errors.As(err, &collErr) || collErr.Resource != "cash" {
			t.Fatalf("aceptación sin caja: %v, esperado CollateralError{cash}", err)
		}
		assertNoEffects(t, ctx, pool, before)
		// Se cancela para devolver la garantía (990.000) a la caja de demo:
		// los subtests posteriores parten de saldos verificables.
		if _, err := svc.CancelPublication(ctx, w.demo, bigPub.ID); err != nil {
			t.Fatalf("cancelando la publicación cara: %v", err)
		}

		if got := metricSum(t, reg, "ii_acceptances_total", "", ""); got < 1 {
			t.Errorf("ii_acceptances_total: %v, esperado >= 1", got)
		}
	})

	t.Run("AcceptBuyLocksStockAndGuarantee", func(t *testing.T) {
		pub, err := svc.CreatePublication(ctx, w.trader, buyInput(w, 100, 60))
		if err != nil {
			t.Fatalf("CreatePublication(buy): %v", err)
		}
		stockBefore := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		cashBefore := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)

		// origin_node_id es obligatorio en buy.
		if _, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 50}); !errors.Is(err, contracts.ErrValidation) {
			t.Fatalf("buy sin origin_node_id: %v, esperado ErrValidation", err)
		}
		// El nodo debe ser un almacén del propio aceptante.
		if _, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 50, OriginNodeID: &w.traderNode}); !errors.Is(err, contracts.ErrNotNodeOwner) {
			t.Fatalf("nodo ajeno: %v, esperado ErrNotNodeOwner", err)
		}
		ghost := uuid.New()
		if _, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 50, OriginNodeID: &ghost}); !errors.Is(err, contracts.ErrValidation) {
			t.Fatalf("nodo inexistente: %v, esperado ErrValidation", err)
		}

		acc, err := svc.Accept(ctx, w.demo, pub.ID, contracts.AcceptInput{Quantity: 50, OriginNodeID: &w.demoNode})
		if err != nil {
			t.Fatalf("Accept(buy): %v", err)
		}
		if acc.StockReserveAccountID == nil || acc.GuaranteeAccountID == nil || acc.EscrowAccountID != nil {
			t.Fatalf("cuentas espejo de la aceptación buy: %+v", acc)
		}
		// Vendedor-aceptante: 50 de stock congelado + garantía 10% de 3000.
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != stockBefore-50 {
			t.Fatalf("stock_free del aceptante: %d, esperado %d", got, stockBefore-50)
		}
		if got := balanceByID(t, ctx, pool, *acc.StockReserveAccountID); got != 50 {
			t.Fatalf("stock_reserved de la aceptación: %d, esperado 50", got)
		}
		if got := balanceByID(t, ctx, pool, *acc.GuaranteeAccountID); got != 300 {
			t.Fatalf("guarantee de la aceptación: %d, esperado 300 (10%% de 3000)", got)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != cashBefore-300 {
			t.Fatalf("caja del aceptante: %d, esperado %d", got, cashBefore-300)
		}
	})

	t.Run("CancelWithPendingAcceptancesReleasesBoth", func(t *testing.T) {
		pubStockBefore := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse)
		pubCashBefore := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil)
		accCashBefore := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil)

		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		acc, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 40})
		if err != nil {
			t.Fatalf("Accept: %v", err)
		}

		if _, err := svc.CancelPublication(ctx, w.demo, pub.ID); err != nil {
			t.Fatalf("CancelPublication con aceptación pendiente: %v", err)
		}

		// Ambas garantías liberadas: publicador y aceptante recuperan todo.
		if got := balanceOf(t, ctx, pool, "stock_free", w.demo, &w.ironOre, &w.demoWarehouse); got != pubStockBefore {
			t.Fatalf("stock_free del publicador: %d, esperado %d", got, pubStockBefore)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.demo, nil, nil); got != pubCashBefore {
			t.Fatalf("caja del publicador: %d, esperado %d", got, pubCashBefore)
		}
		if got := balanceOf(t, ctx, pool, "cash", w.trader, nil, nil); got != accCashBefore {
			t.Fatalf("caja del aceptante: %d, esperado %d", got, accCashBefore)
		}
		if got := balanceByID(t, ctx, pool, *acc.EscrowAccountID); got != 0 {
			t.Fatalf("escrow de la aceptación tras cancelar: %d", got)
		}

		released, err := svc.GetAcceptance(ctx, w.trader, acc.ID)
		if err != nil {
			t.Fatalf("GetAcceptance: %v", err)
		}
		if released.Status != contracts.AcceptanceReleased || released.ResolvedAt == nil ||
			released.DrawOrder == nil || *released.DrawOrder != 1 || released.QuantityServed != 0 {
			t.Fatalf("aceptación liberada inesperada: %+v", released)
		}
		payload := singleEvent(t, ctx, pool, acc.ID, "acceptance.resolved")
		if payload["status"] != "released" || payload["quantity_served"] != "0" {
			t.Fatalf("payload de acceptance.resolved: %v", payload)
		}
		if payload := singleEvent(t, ctx, pool, pub.ID, "publication.cancelled"); payload["released_acceptances"] != float64(1) {
			t.Fatalf("payload de publication.cancelled: %v", payload)
		}

		// Tras la cancelación la publicación no admite aceptaciones.
		if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 10}); !errors.Is(err, contracts.ErrPublicationExhausted) {
			t.Fatalf("aceptar cancelada: %v, esperado ErrPublicationExhausted", err)
		}
	})

	t.Run("MinLot", func(t *testing.T) {
		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 30))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		var lotErr *contracts.MinLotError
		if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 10}); !errors.Is(err, contracts.ErrBelowMinLot) || !errors.As(err, &lotErr) {
			t.Fatalf("lote bajo mínimo: %v, esperado MinLotError", err)
		}
		if lotErr.MinLot != 30 || lotErr.QuantityRemaining != 100 {
			t.Fatalf("details del min_lot: %+v", lotErr)
		}
		// Por encima del restante también viola la regla del contexto.
		if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 101}); !errors.Is(err, contracts.ErrBelowMinLot) {
			t.Fatalf("sobre el restante: %v, esperado ErrBelowMinLot", err)
		}

		// min(min_lot, remaining): con total 20 y min_lot 30, aceptar 20 vale.
		small, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 20, 50, 30))
		if err != nil {
			t.Fatalf("CreatePublication(min_lot>total): %v", err)
		}
		if _, err := svc.Accept(ctx, w.trader, small.ID, contracts.AcceptInput{Quantity: 20}); err != nil {
			t.Fatalf("aceptar min(min_lot, remaining): %v", err)
		}
	})

	t.Run("PrivateChannelVisibility", func(t *testing.T) {
		in := sellInput(w, 50, 7777, 1)
		in.Channel = contracts.ChannelPrivate
		in.CounterpartyAccountID = &w.trader
		pub, err := svc.CreatePublication(ctx, w.demo, in)
		if err != nil {
			t.Fatalf("CreatePublication(private): %v", err)
		}

		// No aparece en el tablón (aislado por banda de precio).
		minP, maxP := int64(7777), int64(7777)
		page, _, err := svc.QueryBoard(ctx, contracts.BoardFilter{MinUnitPrice: &minP, MaxUnitPrice: &maxP})
		if err != nil {
			t.Fatalf("QueryBoard: %v", err)
		}
		if len(page) != 0 {
			t.Fatalf("una publicación private apareció en el tablón: %+v", page)
		}

		// Visible solo para las partes.
		if _, err := svc.GetPublication(ctx, w.trader, pub.ID); err != nil {
			t.Fatalf("GetPublication(counterparty): %v", err)
		}
		if _, err := svc.GetPublication(ctx, w.demo, pub.ID); err != nil {
			t.Fatalf("GetPublication(publicador): %v", err)
		}
		if _, err := svc.GetPublication(ctx, w.bank, pub.ID); !errors.Is(err, contracts.ErrNotParty) {
			t.Fatalf("GetPublication(tercero): %v, esperado ErrNotParty", err)
		}

		// Aceptable solo por la counterparty (la autorización precede al colateral).
		if _, err := svc.Accept(ctx, w.bank, pub.ID, contracts.AcceptInput{Quantity: 10}); !errors.Is(err, contracts.ErrNotParty) {
			t.Fatalf("Accept(tercero): %v, esperado ErrNotParty", err)
		}
		if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 10}); err != nil {
			t.Fatalf("Accept(counterparty): %v", err)
		}
	})

	t.Run("MicroWindowOnAcceptOverOpen", func(t *testing.T) {
		pub, err := svc.CreatePublication(ctx, w.demo, sellInput(w, 100, 50, 1))
		if err != nil {
			t.Fatalf("CreatePublication: %v", err)
		}
		// El sweep del sorteo (worker del incremento) es quien madura a open;
		// aquí se simula su efecto para probar la transición de aceptación.
		if _, err := pool.Exec(ctx, `
			UPDATE ledger.publications SET status = 'open', window_closes_at = NULL
			WHERE id = $1`, pub.ID); err != nil {
			t.Fatalf("madurando la publicación: %v", err)
		}

		if _, err := svc.Accept(ctx, w.trader, pub.ID, contracts.AcceptInput{Quantity: 10}); err != nil {
			t.Fatalf("Accept sobre open: %v", err)
		}
		after, err := svc.GetPublication(ctx, w.trader, pub.ID)
		if err != nil {
			t.Fatalf("GetPublication: %v", err)
		}
		if after.Status != contracts.StatusMicroWindow || after.WindowClosesAt == nil {
			t.Fatalf("la aceptación sobre open debe abrir micro-ventana: %+v", after)
		}
	})

	t.Run("BoardQueryFiltersAndCursor", func(t *testing.T) {
		// Banda de precio 9001..9004 para aislarse del resto de subtests.
		coalSell1, err := svc.CreatePublication(ctx, w.demo, sellWith(w, w.coal, 100, 9001, 1000))
		if err != nil {
			t.Fatalf("coalSell1: %v", err)
		}
		coalSell2, err := svc.CreatePublication(ctx, w.demo, sellWith(w, w.coal, 100, 9002, 500))
		if err != nil {
			t.Fatalf("coalSell2: %v", err)
		}
		coalBuy, err := svc.CreatePublication(ctx, w.trader, contracts.PublicationInput{
			Kind: contracts.KindBuy, ProductID: &w.coal, QuantityTotal: 50,
			UnitPrice: 9003, DestinationNodeID: &w.traderNode, DeliverySimSeconds: 2000,
		})
		if err != nil {
			t.Fatalf("coalBuy: %v", err)
		}
		ironSell, err := svc.CreatePublication(ctx, w.demo, sellWith(w, w.ironOre, 10, 9004, 100))
		if err != nil {
			t.Fatalf("ironSell: %v", err)
		}
		band := func() contracts.BoardFilter {
			minP, maxP := int64(9001), int64(9004)
			return contracts.BoardFilter{MinUnitPrice: &minP, MaxUnitPrice: &maxP}
		}

		queryIDs := func(f contracts.BoardFilter) []uuid.UUID {
			t.Helper()
			page, _, err := svc.QueryBoard(ctx, f)
			if err != nil {
				t.Fatalf("QueryBoard(%+v): %v", f, err)
			}
			ids := make([]uuid.UUID, len(page))
			for i, p := range page {
				ids[i] = p.ID
			}
			return ids
		}

		// Sin filtros extra: los 4, orden unit_price_asc por defecto.
		got := queryIDs(band())
		want := []uuid.UUID{coalSell1.ID, coalSell2.ID, coalBuy.ID, ironSell.ID}
		assertIDs(t, "banda completa", got, want)

		// kind.
		f := band()
		f.Kind = contracts.KindBuy
		assertIDs(t, "kind=buy", queryIDs(f), []uuid.UUID{coalBuy.ID})

		// product.
		f = band()
		f.ProductID = &w.coal
		assertIDs(t, "product=coal", queryIDs(f), []uuid.UUID{coalSell1.ID, coalSell2.ID, coalBuy.ID})

		// min_quantity_remaining.
		f = band()
		minQty := int64(60)
		f.MinQuantityRemaining = &minQty
		assertIDs(t, "min_quantity_remaining=60", queryIDs(f), []uuid.UUID{coalSell1.ID, coalSell2.ID})

		// max_delivery_sim_seconds.
		f = band()
		maxDelivery := int64(600)
		f.MaxDeliverySimSeconds = &maxDelivery
		assertIDs(t, "max_delivery=600", queryIDs(f), []uuid.UUID{coalSell2.ID, ironSell.ID})

		// Región de origen (solo sells tienen nodo de origen) y de destino.
		f = band()
		f.OriginRegionID = &w.regionID
		assertIDs(t, "origin_region=Askadia", queryIDs(f), []uuid.UUID{coalSell1.ID, coalSell2.ID, ironSell.ID})
		f = band()
		f.DestinationRegionID = &w.regionID
		assertIDs(t, "destination_region=Askadia", queryIDs(f), []uuid.UUID{coalBuy.ID})
		f = band()
		ghostRegion := uuid.New()
		f.OriginRegionID = &ghostRegion
		assertIDs(t, "origin_region inexistente", queryIDs(f), nil)

		// Órdenes.
		f = band()
		f.Sort = contracts.SortUnitPriceDesc
		assertIDs(t, "unit_price_desc", queryIDs(f), []uuid.UUID{ironSell.ID, coalBuy.ID, coalSell2.ID, coalSell1.ID})
		// deadline_asc = plazo de entrega ascendente: 100, 500, 1000, 2000.
		f = band()
		f.Sort = contracts.SortDeadlineAsc
		assertIDs(t, "deadline_asc", queryIDs(f), []uuid.UUID{ironSell.ID, coalSell2.ID, coalSell1.ID, coalBuy.ID})
		// published_at_desc: mismo published_at_sim (reloj fijo) → desempate
		// por id DESC = orden de inserción inverso.
		f = band()
		f.Sort = contracts.SortPublishedAtDesc
		assertIDs(t, "published_at_desc", queryIDs(f), []uuid.UUID{ironSell.ID, coalBuy.ID, coalSell2.ID, coalSell1.ID})

		// Cursor keyset: páginas de 2 sin duplicados ni huecos.
		f = band()
		f.Limit = 2
		page1, cur, err := svc.QueryBoard(ctx, f)
		if err != nil || len(page1) != 2 || cur == "" {
			t.Fatalf("página 1: %d filas, cursor %q (err %v)", len(page1), cur, err)
		}
		f.Cursor = cur
		page2, cur2, err := svc.QueryBoard(ctx, f)
		if err != nil || len(page2) != 2 || cur2 != "" {
			t.Fatalf("página 2: %d filas, cursor %q (err %v)", len(page2), cur2, err)
		}
		assertIDs(t, "paginación completa",
			append([]uuid.UUID{page1[0].ID, page1[1].ID}, page2[0].ID, page2[1].ID), want)

		// Cursor de otro orden o basura → ErrInvalidCursor.
		f = band()
		f.Sort = contracts.SortUnitPriceDesc
		f.Cursor = cur
		if _, _, err := svc.QueryBoard(ctx, f); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatalf("cursor de otro orden: %v, esperado ErrInvalidCursor", err)
		}
		f = band()
		f.Cursor = "zzz*"
		if _, _, err := svc.QueryBoard(ctx, f); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Fatalf("cursor basura: %v, esperado ErrInvalidCursor", err)
		}

		// Gauge del tablón: coincide con el recuento SQL.
		n, err := svc.UpdateBoardGauge(ctx)
		if err != nil {
			t.Fatalf("UpdateBoardGauge: %v", err)
		}
		sqlCount := countRows(t, ctx, pool, `
			SELECT count(*) FROM ledger.publications
			WHERE channel = 'board' AND status IN ('draw_window','open','micro_window')`)
		if int(n) != sqlCount {
			t.Fatalf("gauge del tablón: %d, esperado %d", n, sqlCount)
		}
		if got := gaugeValue(t, reg, "ii_board_open_publications"); got != float64(n) {
			t.Fatalf("ii_board_open_publications: %v, esperado %d", got, n)
		}
	})
}

// ─── Constructores de inputs ────────────────────────────────────────────────

// sellInput construye una publicación sell de iron_ore desde el almacén demo.
func sellInput(w world, qty, price, minLot int64) contracts.PublicationInput {
	product := w.ironOre
	node := w.demoNode
	return contracts.PublicationInput{
		Kind:               contracts.KindSell,
		ProductID:          &product,
		QuantityTotal:      qty,
		UnitPrice:          price,
		MinLot:             minLot,
		OriginNodeID:       &node,
		DeliverySimSeconds: 3600,
	}
}

// sellWith construye una publicación sell parametrizada (tablón).
func sellWith(w world, product uuid.UUID, qty, price, delivery int64) contracts.PublicationInput {
	node := w.demoNode
	p := product
	return contracts.PublicationInput{
		Kind:               contracts.KindSell,
		ProductID:          &p,
		QuantityTotal:      qty,
		UnitPrice:          price,
		OriginNodeID:       &node,
		DeliverySimSeconds: delivery,
	}
}

// buyInput construye una publicación buy de iron_ore con destino trader.
func buyInput(w world, qty, price int64) contracts.PublicationInput {
	product := w.ironOre
	node := w.traderNode
	return contracts.PublicationInput{
		Kind:               contracts.KindBuy,
		ProductID:          &product,
		QuantityTotal:      qty,
		UnitPrice:          price,
		DestinationNodeID:  &node,
		DeliverySimSeconds: 3600,
	}
}

// ─── Infraestructura del test ───────────────────────────────────────────────

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella (mismo patrón que ledger y seed).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("contractstest_%d_%d", os.Getpid(), time.Now().UnixNano())
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

// loadWorld resuelve los IDs del mundo sembrado por sus claves naturales y
// crea un nodo sin edificio para las validaciones.
func loadWorld(t *testing.T, ctx context.Context, pool *pgxpool.Pool) world {
	t.Helper()
	var w world
	byName := func(name string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name = $1`, name).Scan(&id); err != nil {
			t.Fatalf("cuenta %q: %v", name, err)
		}
		return id
	}
	w.demo = byName(seed.DefaultDemoName)
	w.trader = byName(seed.DefaultTraderName)
	w.bank = byName(seed.CentralBankName)

	product := func(code string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, code).Scan(&id); err != nil {
			t.Fatalf("producto %q: %v", code, err)
		}
		return id
	}
	w.ironOre = product("iron_ore")
	w.coal = product("coal")

	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName).Scan(&w.regionID); err != nil {
		t.Fatalf("región: %v", err)
	}

	site := func(owner uuid.UUID) (node, warehouse uuid.UUID) {
		if err := pool.QueryRow(ctx, `
			SELECT n.id, b.id FROM world.network_nodes n
			JOIN world.buildings b ON b.id = n.building_id
			WHERE b.owner_account_id = $1`, owner).Scan(&node, &warehouse); err != nil {
			t.Fatalf("implantación de %s: %v", owner, err)
		}
		return node, warehouse
	}
	w.demoNode, w.demoWarehouse = site(w.demo)
	w.traderNode, w.traderWarehouse = site(w.trader)

	// Nodo junction sin edificio: inválido como origen/destino del CCRI.
	bare, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, location)
		VALUES ($1, 'junction', $2, ST_GeomFromText('POINT(1 1)', 0))`, bare, w.regionID); err != nil {
		t.Fatalf("creando el nodo junction: %v", err)
	}
	w.bareNode = bare
	return w
}

// balanceOf lee el saldo de la cuenta (kind, owner[, product, warehouse]).
func balanceOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, owner uuid.UUID, product, warehouse *uuid.UUID) int64 {
	t.Helper()
	var balance int64
	err := pool.QueryRow(ctx, `
		SELECT balance FROM ledger.accounts
		WHERE kind = $1::ledger.account_kind AND owner_account_id = $2
		  AND ($3::uuid IS NULL OR product_id = $3::uuid)
		  AND ($4::uuid IS NULL OR warehouse_building_id = $4::uuid)`,
		kind, owner, product, warehouse).Scan(&balance)
	if err != nil {
		t.Fatalf("saldo de %s/%s: %v", kind, owner, err)
	}
	return balance
}

// balanceByID lee el saldo de una cuenta del ledger por id.
func balanceByID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) int64 {
	t.Helper()
	var balance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE id = $1`, id).Scan(&balance); err != nil {
		t.Fatalf("saldo de la cuenta %s: %v", id, err)
	}
	return balance
}

// tableCounts captura el número de filas de las tablas que las operaciones
// del módulo escriben, para verificar el todo-o-nada.
type tableCounts map[string]int

func snapshotCounts(t *testing.T, ctx context.Context, pool *pgxpool.Pool) tableCounts {
	t.Helper()
	tables := []string{
		"ledger.publications", "ledger.publication_acceptances",
		"ledger.accounts", "ledger.transactions", "ledger.entries",
		"outbox.events",
	}
	counts := make(tableCounts, len(tables))
	for _, table := range tables {
		counts[table] = countRows(t, ctx, pool, "SELECT count(*) FROM "+table)
	}
	return counts
}

// assertNoEffects verifica que una operación rechazada no dejó NINGÚN rastro.
func assertNoEffects(t *testing.T, ctx context.Context, pool *pgxpool.Pool, before tableCounts) {
	t.Helper()
	after := snapshotCounts(t, ctx, pool)
	for table, want := range before {
		if after[table] != want {
			t.Fatalf("la operación rechazada dejó rastro en %s: %d filas, esperado %d",
				table, after[table], want)
		}
	}
}

// singleEvent devuelve el payload del único evento (aggregate_id, event_type)
// del outbox y verifica su unicidad.
func singleEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, aggregateID uuid.UUID, eventType string) map[string]any {
	t.Helper()
	rows, err := pool.Query(ctx, `
		SELECT payload FROM outbox.events
		WHERE aggregate_id = $1 AND event_type = $2 ORDER BY seq`, aggregateID, eventType)
	if err != nil {
		t.Fatalf("consultando el outbox: %v", err)
	}
	defer rows.Close()
	var payloads [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatalf("leyendo el payload: %v", err)
		}
		payloads = append(payloads, raw)
	}
	if len(payloads) != 1 {
		t.Fatalf("eventos %s de %s: %d, esperado exactamente 1", eventType, aggregateID, len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal(payloads[0], &payload); err != nil {
		t.Fatalf("payload de %s no es JSON: %v", eventType, err)
	}
	return payload
}

// assertIDs compara dos listas ordenadas de IDs.
func assertIDs(t *testing.T, label string, got, want []uuid.UUID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d resultados %v, esperado %d %v", label, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: posición %d = %s, esperado %s (got %v)", label, i, got[i], want[i], got)
		}
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// metricSum suma las series de un counter; con label vacío suma todas.
func metricSum(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	var sum float64
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if label == "" {
				sum += m.GetCounter().GetValue()
				continue
			}
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}

// gaugeValue lee el valor de un gauge sin labels.
func gaugeValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == name && len(mf.GetMetric()) > 0 {
			return mf.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge %s no registrado", name)
	return 0
}
