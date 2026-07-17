// El binario engine ejecuta el motor de simulación: la plataforma base
// (healthz/readyz/metrics con la cadena de middlewares), el reloj de
// simulación (internal/sim/clock) —único reloj lógico del dominio (GDD 1.1):
// ancla persistida en world.sim_clock, ratio 24x y derivación analítica— y los
// procesos en segundo plano: los tres barridos del ciclo CCRI
// (internal/contracts), el agregador OHLC del historial de mercado
// (internal/market) y el motor de producción del Incremento 2
// (internal/world/production: construcción diferida, barrido analítico de lotes
// y reconciliación física↔contable), todos guiados por el reloj de simulación y
// detenidos de forma graceful al recibir la señal de apagado.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// EnvOhlcConsumerInterval es el periodo de polling del consumidor OHLC del
// outbox, en formato time.ParseDuration. Default 1s. El drenaje encadena lotes
// llenos sin esperar el intervalo, así que este valor solo acota la latencia
// en reposo.
const EnvOhlcConsumerInterval = "II_OHLC_CONSUMER_INTERVAL"

// DefaultOhlcConsumerInterval es el periodo de polling por defecto del
// consumidor OHLC.
const DefaultOhlcConsumerInterval = time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "engine:", err)
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
	app, err := service.New(ctx, metrics.ServiceEngine, cfg.EngineAddr, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	// Métricas del outbox (emisión desde los sweeps + procesado del consumidor
	// OHLC): el módulo las registra en el registry de cada binario (outbox.go).
	outbox.RegisterMetrics(app.Metrics().Registry())

	// ── Reloj de simulación ──────────────────────────────────────────────────
	clkOpts, err := clock.OptionsFromEnv()
	if err != nil {
		return err
	}
	clk := clock.New(clock.NewStore(app.Pool()), clkOpts, app.Logger(), app.Metrics().Registry())
	if err := clk.Start(ctx); err != nil {
		return err
	}
	defer clk.Stop() // se ejecuta antes que app.Close(): el pool sigue abierto

	now := clk.Now()
	app.Logger().Info("reloj de simulación en marcha",
		slog.String("sim_time", simtime.Format(now)),
		slog.Int64("sim_time_seconds", int64(now)),
		slog.Bool("frozen", clk.Frozen()),
		slog.Duration("persist_interval", clkOpts.PersistInterval),
		slog.Duration("refresh_interval", clkOpts.RefreshInterval))

	// ── Worker CCRI: los tres barridos (sorteo, TTL, liquidación) ─────────────
	contractsOpts, err := contracts.OptionsFromEnv()
	if err != nil {
		return err
	}
	workerOpts, err := contracts.WorkerOptionsFromEnv()
	if err != nil {
		return err
	}
	contractsSvc, err := contracts.NewService(app.Pool(), clockSimSource{clk}, contractsOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	worker, err := contracts.NewWorker(contractsSvc, workerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}

	// ── Agregador OHLC: consumidor del outbox de contract.settled ────────────
	marketOpts, err := market.OptionsFromEnv()
	if err != nil {
		return err
	}
	consumerInterval, err := ohlcConsumerInterval()
	if err != nil {
		return err
	}
	aggregator, err := market.NewAggregator(marketOpts, market.NewMetrics(app.Metrics().Registry()), app.Logger())
	if err != nil {
		return err
	}
	ohlcConsumer := aggregator.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// ── Motor de producción (Incremento 2): construcción diferida, barrido
	//    analítico de lotes vencidos y reconciliación física↔contable (ADR-004),
	//    todo con el mismo reloj de simulación que los barridos CCRI ──────────
	productionWorkerOpts, err := production.WorkerOptionsFromEnv()
	if err != nil {
		return err
	}
	productionWorker, err := production.NewWorker(app.Pool(), clockSimSource{clk}, productionWorkerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}

	// ── Arranque de los procesos en segundo plano ────────────────────────────
	// Comparten el ctx de la señal: al apagar, los bucles observan ctx.Done() y
	// retornan nil (parada limpia). wg los sincroniza antes de cerrar el pool.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := worker.Run(ctx); err != nil {
			app.Logger().Error("contracts: el worker de barridos terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := ohlcConsumer.Run(ctx, consumerInterval, aggregator.Handle); err != nil {
			app.Logger().Error("market: el consumidor OHLC terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := productionWorker.Run(ctx); err != nil {
			app.Logger().Error("world/production: el motor terminó con error", slog.Any("error", err))
		}
	}()

	app.Logger().Info("procesos del motor en marcha",
		slog.Duration("contracts_sweep_interval", workerOpts.SweepInterval),
		slog.Int("contracts_sweep_batch", workerOpts.BatchSize),
		slog.Int64("draw_window_seconds", contractsOpts.DrawWindowSeconds),
		slog.Int64("micro_window_seconds", contractsOpts.MicroWindowSeconds),
		slog.Int64("publication_ttl_sim_seconds", contractsOpts.PublicationTTLSimSeconds),
		slog.Int("compensation_bp", contractsOpts.CompensationBP),
		slog.String("ohlc_consumer", market.ConsumerName),
		slog.Duration("ohlc_consumer_interval", consumerInterval),
		slog.Int64("ohlc_bucket_sim_seconds", marketOpts.OhlcBucketSimSeconds),
		slog.Duration("production_sweep_interval", productionWorkerOpts.SweepInterval),
		slog.Int("production_sweep_batch", productionWorkerOpts.BatchSize),
		slog.Int64("build_sim_seconds", productionWorkerOpts.BuildSimSeconds),
		slog.Duration("reconcile_interval", productionWorkerOpts.ReconcileInterval))

	// Sirve HTTP (sondas/métricas) hasta la señal; entonces app.Run apaga el
	// servidor de forma graceful. Al retornar, el ctx ya está cancelado y los
	// procesos en segundo plano están parando: wg.Wait espera su cierre limpio.
	runErr := app.Run(ctx)
	wg.Wait()
	app.Logger().Info("procesos del motor detenidos")
	return runErr
}

// clockSimSource adapta el reloj del motor (*clock.Clock, con Now() sin
// contexto) a la interfaz contracts.SimSource (Now(ctx)). El worker deriva de
// él los sim-time de dominio (published_at_sim, deadline_sim); las ventanas
// wall-clock del sorteo usan now() de la BD, ajenas a este reloj.
type clockSimSource struct{ clk *clock.Clock }

func (c clockSimSource) Now(context.Context) simtime.SimTime { return c.clk.Now() }

// ohlcConsumerInterval lee II_OHLC_CONSUMER_INTERVAL (time.ParseDuration) con
// su default; un valor inválido o no positivo devuelve error (la configuración
// rota debe impedir el arranque).
func ohlcConsumerInterval() (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(EnvOhlcConsumerInterval))
	if v == "" {
		return DefaultOhlcConsumerInterval, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("engine: %s inválido %q (formato de time.ParseDuration): %w", EnvOhlcConsumerInterval, v, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("engine: %s debe ser una duración positiva (actual %s)", EnvOhlcConsumerInterval, d)
	}
	return d, nil
}
