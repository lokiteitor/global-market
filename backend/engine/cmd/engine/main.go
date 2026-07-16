// Motor de simulación de Imperio Industrial (ADR-IMPL-03): ejecuta TODO lo
// dirigido por tiempo — sim-clock 24×, producción, sorteos y liquidaciones
// CCRI, auto-despacho y tránsito, balancer de ciudades y cargos diarios —
// emitiendo eventos por el outbox transaccional.
package main

import (
	"context"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/balancer"
	"imperio/engine/internal/clock"
	"imperio/engine/internal/config"
	"imperio/engine/internal/contracts"
	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/logistics"
	"imperio/engine/internal/sim"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("engine: sin conexión a la base", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	bank, err := loadBankRefs(ctx, pool)
	if err != nil {
		log.Error("engine: bootstrap del banco central", "err", err)
		os.Exit(1)
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	clk := clock.New(pool)
	production := &sim.Processor{Pool: pool, Bank: bank, Log: log}
	market := &contracts.Service{Pool: pool, Bank: bank, Log: log, Rand: rng}
	transport := &logistics.Processor{Pool: pool, Bank: bank, Log: log, Rand: rng,
		// La liquidación desde logística reutiliza el flujo completo de
		// contratos (incluido el consumo urbano) sin import cruzado.
		Settle: func(ctx context.Context, tx pgx.Tx, contractID uuid.UUID, simNow int64) error {
			return market.SettleWithCityFlow(ctx, tx, contractID, simNow)
		}}
	cities := &balancer.Processor{Pool: pool, Bank: bank, Log: log, DrawWindow: cfg.DrawWindow}

	log.Info("engine: arrancado", "tick", cfg.TickInterval.String(), "sim_time", core.FormatSimTime(clk.Now()))

	ticker := time.NewTicker(cfg.TickInterval)
	defer ticker.Stop()
	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			log.Info("engine: apagado limpio")
			return
		case <-ticker.C:
		}
		simNow, frozen, err := clk.Tick(ctx)
		if err != nil {
			log.Error("engine: tick de reloj", "err", err)
			continue
		}
		if frozen {
			// Ventana de mantenimiento: el mundo está congelado; ningún
			// procesador dirigido por tiempo debe correr.
			continue
		}
		tickCount++

		production.Run(ctx, simNow)
		market.RunDraws(ctx, simNow)
		transport.RunDispatch(ctx, simNow)
		transport.RunTransit(ctx, simNow)
		market.RunDeadlines(ctx, simNow)

		if tickCount%60 == 0 {
			transport.RunCongestion(ctx, simNow)
			cities.RunPeriodic(ctx, simNow)
		}
		cities.RunDaily(ctx, simNow)
	}
}

// loadBankRefs resuelve las cuentas de sistema del banco central (sembradas
// por seed_world.sql) una vez al arrancar.
func loadBankRefs(ctx context.Context, pool *pgxpool.Pool) (core.BankRefs, error) {
	var refs core.BankRefs
	if err := pool.QueryRow(ctx,
		`SELECT id FROM auth.accounts WHERE kind = 'system' AND name = 'Banco Central'`).
		Scan(&refs.BancoCentralID); err != nil {
		return refs, err
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM ledger.accounts WHERE kind = 'sink' AND owner_account_id = $1`,
		refs.BancoCentralID).Scan(&refs.SinkAccountID); err != nil {
		return refs, err
	}
	if err := pool.QueryRow(ctx,
		`SELECT id FROM ledger.accounts WHERE kind = 'emission' AND product_id IS NULL`).
		Scan(&refs.EmissionMoneyID); err != nil {
		return refs, err
	}
	return refs, nil
}
