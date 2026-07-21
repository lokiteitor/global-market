package fleet_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
)

// TestTerminalSlotsIntegration ejercita los slots de prioridad de terminal (GDD
// 7.3): la lectura real de terminal/slots, la compra (pago al dueño de la
// terminal, titular y vigencia), el 409 sobre un slot con titular vigente y el
// filtro only_available.
func TestTerminalSlotsIntegration(t *testing.T) {
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
		Ledger:              ledger.DefaultOptions(),
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)    // comprador del slot
	norte := accountID(t, ctx, pool, seed.DefaultTraderName) // dueño de la terminal
	region := regionID(t, ctx, pool, seed.RegionName)

	simNow := int64(1000)
	sim := &advSim{now: &simNow}
	opts := fleet.DefaultOptions()
	opts.SlotValiditySim = 30_000
	svc, err := fleet.NewService(pool, sim, opts, logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Terminal de norte sobre un nodo junction, con un slot (tier 1, precio 5000).
	node := insertNode(t, ctx, pool, region, nil, "junction", 5000, 5000)
	terminalID := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.terminals (id, node_id, owner_account_id, transshipment_per_hour, queue_length, updated_at_sim)
		VALUES ($1,$2,$3,120,0,0)`, terminalID, node, norte)
	slotID := uuid.Must(uuid.NewV7())
	const slotPrice = int64(5000)
	exec(t, ctx, pool, `INSERT INTO world.terminal_slots (id, terminal_id, priority_tier, price)
		VALUES ($1,$2,1,$3)`, slotID, terminalID, slotPrice)

	t.Run("GetTerminal devuelve datos reales", func(t *testing.T) {
		term, err := svc.GetTerminal(ctx, terminalID)
		if err != nil {
			t.Fatalf("GetTerminal: %v", err)
		}
		if term.NodeID != node || term.OwnerAccountID != norte || term.TransshipmentPerHour != 120 {
			t.Fatalf("terminal inesperada: %+v", term)
		}
	})

	t.Run("ListTerminalSlots only_available lista el slot en venta", func(t *testing.T) {
		slots, err := svc.ListTerminalSlots(ctx, terminalID, true)
		if err != nil {
			t.Fatalf("ListTerminalSlots: %v", err)
		}
		if len(slots) != 1 || slots[0].ID != slotID || slots[0].Price != slotPrice || slots[0].HolderAccountID != nil {
			t.Fatalf("slots inesperados: %+v", slots)
		}
	})

	t.Run("PurchaseSlot paga al dueño y fija titular/vigencia", func(t *testing.T) {
		demoCash0 := cashBalance(t, ctx, pool, demo)
		norteCash0 := cashBalance(t, ctx, pool, norte)

		slot, err := svc.PurchaseSlot(ctx, demo, slotID)
		if err != nil {
			t.Fatalf("PurchaseSlot: %v", err)
		}
		if slot.HolderAccountID == nil || *slot.HolderAccountID != demo {
			t.Fatalf("titular del slot: %+v, esperado %s", slot.HolderAccountID, demo)
		}
		if slot.ValidUntilSim == nil || *slot.ValidUntilSim != simNow+opts.SlotValiditySim {
			t.Fatalf("vigencia del slot: %+v, esperado %d", slot.ValidUntilSim, simNow+opts.SlotValiditySim)
		}
		if got := cashBalance(t, ctx, pool, demo); got != demoCash0-slotPrice {
			t.Fatalf("caja del comprador: %d, esperado %d", got, demoCash0-slotPrice)
		}
		if got := cashBalance(t, ctx, pool, norte); got != norteCash0+slotPrice {
			t.Fatalf("caja del dueño: %d, esperado %d", got, norteCash0+slotPrice)
		}
	})

	t.Run("only_available excluye el slot ya ocupado", func(t *testing.T) {
		slots, err := svc.ListTerminalSlots(ctx, terminalID, true)
		if err != nil {
			t.Fatalf("ListTerminalSlots: %v", err)
		}
		if len(slots) != 0 {
			t.Fatalf("only_available debería excluir el slot ocupado: %+v", slots)
		}
		// Sin filtro, sigue apareciendo con su titular.
		all, err := svc.ListTerminalSlots(ctx, terminalID, false)
		if err != nil || len(all) != 1 || all[0].HolderAccountID == nil {
			t.Fatalf("listado completo inesperado: %+v (%v)", all, err)
		}
	})

	t.Run("comprar un slot con titular vigente da 409", func(t *testing.T) {
		_, err := svc.PurchaseSlot(ctx, norte, slotID)
		if !errors.Is(err, fleet.ErrSlotHeld) {
			t.Fatalf("comprar slot ocupado: %v, esperado ErrSlotHeld", err)
		}
	})

	t.Run("comprar un slot sin fondos da 422 INSUFFICIENT_FUNDS", func(t *testing.T) {
		// Comprador = demo (NO es el dueño de la terminal); slot por encima de su caja.
		pricey := uuid.Must(uuid.NewV7())
		demoCash := cashBalance(t, ctx, pool, demo)
		exec(t, ctx, pool, `INSERT INTO world.terminal_slots (id, terminal_id, priority_tier, price)
			VALUES ($1,$2,2,$3)`, pricey, terminalID, demoCash+1)
		_, err := svc.PurchaseSlot(ctx, demo, pricey)
		if !errors.Is(err, fleet.ErrInsufficientFunds) {
			t.Fatalf("comprar slot sin fondos: %v, esperado ErrInsufficientFunds", err)
		}
		var fe *fleet.FundsError
		if !errors.As(err, &fe) || fe.Required != demoCash+1 || fe.Available != demoCash {
			t.Fatalf("FundsError inesperado: %+v (esperado required=%d available=%d)", fe, demoCash+1, demoCash)
		}
	})
}

// TestTransshipPriorityQueue ejercita la COLA DE TRANSBORDO con prioridad de slots
// (GDD 7.3): dos cargamentos compiten por la misma terminal, llegados en el MISMO
// instante; el dueño de uno posee un slot de prioridad vigente (tier menor) y el del
// otro no. Al servir la cola (RunTransshipOnce), el del slot se sirve ANTES (fin de
// transbordo más temprano) y las métricas priority/fifo cuentan 1 y 1.
func TestTransshipPriorityQueue(t *testing.T) {
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
		Ledger:              ledger.DefaultOptions(),
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)    // dueño CON slot de prioridad
	norte := accountID(t, ctx, pool, seed.DefaultTraderName) // dueño SIN slot (FIFO)
	bank := accountID(t, ctx, pool, seed.CentralBankName)    // dueño de la terminal
	region := regionID(t, ctx, pool, seed.RegionName)

	const arrival = int64(1000)
	simNow := arrival
	sim := &advSim{now: &simNow}
	worker, err := fleet.NewTransitWorker(pool, sim, fleet.DefaultWorkerOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewTransitWorker: %v", err)
	}

	widget := createProduct(t, ctx, pool, "prio_widget", false) // unit_volume = 1
	termNode := insertNode(t, ctx, pool, region, nil, "junction", 30000, 10000)
	destNode := insertNode(t, ctx, pool, region, nil, "station", 50000, 10000)

	// Terminal (owner = banco) con tasa 60/hora: un cargamento de volumen 60 tarda
	// 1 h = 3600 s en transbordarse.
	terminalID := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.terminals (id, node_id, owner_account_id, transshipment_per_hour, queue_length, updated_at_sim)
		VALUES ($1,$2,$3,60,0,0)`, terminalID, termNode, bank)
	// Slot tier 1 (mejor prioridad) VIGENTE de demo en la terminal.
	exec(t, ctx, pool, `INSERT INTO world.terminal_slots (id, terminal_id, priority_tier, price, holder_account_id, valid_until_sim)
		VALUES ($1,$2,1,5000,$3,$4)`, uuid.Must(uuid.NewV7()), terminalID, demo, arrival+1_000_000)

	// Dos cargamentos ENCOLADOS (at_terminal, sin servir) en el nodo de la terminal,
	// misma llegada (updated_at_sim). demo tiene slot; norte no.
	const qty = int64(60)
	shDemo := insertAtTerminalShipment(t, ctx, pool, demo, widget, termNode, destNode, qty, arrival)
	shNorte := insertAtTerminalShipment(t, ctx, pool, norte, widget, termNode, destNode, qty, arrival)

	prio, fifo, err := worker.RunTransshipOnce(ctx)
	if err != nil {
		t.Fatalf("RunTransshipOnce: %v", err)
	}
	if prio != 1 || fifo != 1 {
		t.Fatalf("servidos: priority=%d fifo=%d, esperado 1 y 1", prio, fifo)
	}

	demoReady := transshipReady(t, ctx, pool, shDemo)
	norteReady := transshipReady(t, ctx, pool, shNorte)
	if demoReady == nil || norteReady == nil {
		t.Fatalf("ambos deben quedar servidos: demo=%v norte=%v", demoReady, norteReady)
	}
	// Servidor único a 60/h: demo (con slot) primero → 1000+3600; norte detrás → +3600.
	if *demoReady != arrival+3600 {
		t.Fatalf("fin de transbordo de demo = %d, esperado %d", *demoReady, arrival+3600)
	}
	if *norteReady != arrival+7200 {
		t.Fatalf("fin de transbordo de norte = %d, esperado %d", *norteReady, arrival+7200)
	}
	if *demoReady >= *norteReady {
		t.Fatalf("el dueño con slot debe servirse ANTES: demo=%d norte=%d", *demoReady, *norteReady)
	}

	// queue_length refleja los dos en servicio (ready > sim_now).
	if got := terminalQueueLength(t, ctx, pool, terminalID); got != 2 {
		t.Fatalf("queue_length = %d, esperado 2 (ambos en servicio)", got)
	}

	// Idempotencia: una segunda pasada no re-sirve nada.
	prio2, fifo2, err := worker.RunTransshipOnce(ctx)
	if err != nil || prio2 != 0 || fifo2 != 0 {
		t.Fatalf("segunda pasada: priority=%d fifo=%d err=%v, esperado 0/0/nil", prio2, fifo2, err)
	}
}

// insertAtTerminalShipment inserta un cargamento ENCOLADO (at_terminal, sin servir)
// en el nodo de una terminal, con arrival como updated_at_sim (clave FIFO).
func insertAtTerminalShipment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, product, atNode, destNode uuid.UUID, qty, arrival int64) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	exec(t, ctx, pool, `INSERT INTO world.shipments
		(id, owner_account_id, product_id, quantity, at_node_id, destination_node_id, status, transship_ready_at_sim, updated_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,'at_terminal',NULL,$7)`, id, owner, product, qty, atNode, destNode, arrival)
	return id
}

// transshipReady lee transship_ready_at_sim de un cargamento (nil si aún sin servir).
func transshipReady(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) *int64 {
	t.Helper()
	var ready *int64
	if err := pool.QueryRow(ctx, `SELECT transship_ready_at_sim FROM world.shipments WHERE id = $1`, id).Scan(&ready); err != nil {
		t.Fatalf("leyendo transship_ready_at_sim de %s: %v", id, err)
	}
	return ready
}

// terminalQueueLength lee queue_length de una terminal.
func terminalQueueLength(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) int32 {
	t.Helper()
	var n int32
	if err := pool.QueryRow(ctx, `SELECT queue_length FROM world.terminals WHERE id = $1`, id).Scan(&n); err != nil {
		t.Fatalf("leyendo queue_length de %s: %v", id, err)
	}
	return n
}
