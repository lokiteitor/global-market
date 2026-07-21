// El binario bots es el Bot Orchestration Service (ADR-024, GDD §13/§15.4):
// aprovisiona la población configurada de bots (cuentas kind=bot con
// credencial derivada, bot_profiles y capitalización única del banco central,
// por paquetes internos — operación de lifecycle, no de juego) y la ejecuta
// (una goroutine por bot, tick jitterizado, eventos WS que despiertan el
// Decide) donde TODO el gameplay pasa por pkg/botsdk contra la API pública —
// mismos endpoints y rate limits que cualquier jugador (ADR-010).
//
// Expone su propio servidor de observabilidad en II_BOTS_ADDR (default
// :8082) con /healthz, /readyz y /metrics (ii_bot_decisions_total,
// ii_bot_errors_total, ii_bot_cash).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
)

// serviceName etiqueta las métricas de plataforma del binario.
const serviceName = "bots"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bots:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	botsOpts, err := bots.OptionsFromEnv()
	if err != nil {
		return err
	}
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return err
	}
	retireOpts, err := bots.RetirementOptionsFromEnv()
	if err != nil {
		return err
	}

	app, err := service.New(ctx, serviceName, botsOpts.Addr, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	orch, err := bots.NewOrchestrator(app.Pool(), botsOpts, ledgerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}

	// Barrido de retiro de bots insolventes (ADR-024): lee el sim-time del reloj
	// de simulación (mismo lector cacheado que el resto de motores).
	simReader := clock.NewReader(clock.NewStore(app.Pool()), clock.ReaderOptions{CacheTTL: time.Second}, app.Logger())
	retireJob, err := bots.NewRetirementJob(app.Pool(), ledgerOpts, retireOpts, simReader, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}

	app.Logger().Info("orquestador de bots arrancando",
		slog.Int("coal_producers", botsOpts.CoalProducers),
		slog.Int("iron_producers", botsOpts.IronProducers),
		slog.Int("traders", botsOpts.Traders),
		slog.Int64("capital", botsOpts.Capital),
		slog.Duration("tick", botsOpts.Tick),
		slog.String("api_url", botsOpts.APIURL),
		slog.String("addr", botsOpts.Addr),
		slog.Duration("retire_interval", retireOpts.Interval),
		slog.Int64("retire_cash_floor", retireOpts.CashFloor),
		slog.Int64("retire_idle_sim_seconds", retireOpts.IdleSimSeconds))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := orch.Run(ctx); err != nil {
			app.Logger().Error("bots: el orquestador terminó con error", slog.Any("error", err))
		}
	}()

	// Barrido de retiro en paralelo a la población: retira los bots
	// insolventes-inactivos mientras el orquestador ejecuta el resto.
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := retireJob.Run(ctx); err != nil {
			app.Logger().Error("bots: el barrido de retiro terminó con error", slog.Any("error", err))
		}
	}()

	// Sirve las sondas y métricas hasta la señal; al retornar, ctx ya está
	// cancelado y los bots están parando: wg.Wait espera su cierre limpio.
	runErr := app.Run(ctx)
	wg.Wait()
	app.Logger().Info("bots detenidos")
	return runErr
}
