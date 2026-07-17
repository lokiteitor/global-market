// El binario engine ejecuta el motor de simulación: la plataforma base
// (healthz/readyz/metrics con la cadena de middlewares) y el reloj de
// simulación (internal/sim/clock), único reloj lógico del dominio (GDD 1.1):
// ancla persistida en world.sim_clock, ratio 24x y derivación analítica.
// La cola de eventos se monta sobre esta base en la fase siguiente.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

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

	return app.Run(ctx)
}
