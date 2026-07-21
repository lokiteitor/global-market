// El binario gateway sirve la API pública del backend: la plataforma base
// (healthz/readyz/metrics con la cadena de middlewares) y, bajo /api/v1, las
// rutas del contrato OpenAPI v1.2.0 que compone internal/gateway (auth,
// ledger, contracts, market y el lector del reloj de simulación). Este
// composition root es la única capa que junta los bounded contexts: ellos no
// se importan entre sí.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/lokiteitor/global-market/backend/internal/gateway"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
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
	app, err := service.New(ctx, metrics.ServiceGateway, cfg.HTTPAddr, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	// Métricas del outbox: el gateway emite eventos (publication.created,
	// acceptance.registered, publication.cancelled) al publicar/aceptar/cancelar;
	// el módulo registra sus contadores en el registry de este binario (outbox.go).
	outbox.RegisterMetrics(app.Metrics().Registry())

	opts, err := gateway.OptionsFromEnv()
	if err != nil {
		return err
	}
	srv, err := gateway.BuildServer(gateway.Deps{
		Pool:     app.Pool(),
		Logger:   app.Logger(),
		Registry: app.Metrics().Registry(),
		Options:  opts,
	})
	if err != nil {
		return err
	}
	app.Mux().Handle(gateway.APIPrefix+"/", srv.Handler)
	app.Logger().Info("rutas del contrato montadas",
		slog.String("prefix", gateway.APIPrefix),
		slog.Int("login_per_min", opts.Auth.LoginPerMin),
		slog.Float64("api_rps", opts.Auth.APIRPS),
		slog.Int("api_burst", opts.Auth.APIBurst),
		slog.Duration("simclock_cache_ttl", opts.ClockReader.CacheTTL))

	// Router del Notification Gateway (ADR-023): consumidor outbox
	// notification_gateway en goroutine propia. Comparte el ctx de la señal:
	// al apagar observa ctx.Done() y retorna nil; wg lo sincroniza antes de
	// cerrar el pool (defer app.Close()).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := srv.Run(ctx); err != nil {
			app.Logger().Error("notify: el router del gateway WS terminó con error", slog.Any("error", err))
		}
	}()
	defer wg.Wait()
	app.Logger().Info("notification gateway WS en marcha",
		slog.String("endpoint", gateway.APIPrefix+"/ws"),
		slog.Duration("router_interval", opts.Notify.RouterInterval),
		slog.Int("send_buffer", opts.Notify.SendBuffer),
		slog.Duration("ping_interval", opts.Notify.PingInterval),
		slog.Int("max_conns_per_account", opts.Notify.MaxConnsPerAccount))

	return app.Run(ctx)
}
