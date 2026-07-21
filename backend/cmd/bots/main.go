// El binario bots es el Bot Orchestration Service (ADR-024, GDD §13/§15.4):
// aprovisiona la población configurada de bots (cuentas kind=bot con
// credencial derivada, bot_profiles y capitalización única del banco central,
// por paquetes internos — operación de lifecycle, no de juego) y la ejecuta
// (una goroutine por bot, tick jitterizado, eventos WS que despiertan el
// Decide) donde TODO el gameplay pasa por pkg/botsdk contra la API pública —
// mismos endpoints y rate limits que cualquier jugador (ADR-010).
//
// Corre además dos barridos de lifecycle en paralelo a la población:
//
//   - DENSIDAD DINÁMICA (GDD §13.4 modo 2, §19): ajusta continuamente cuántos
//     bots de cada arquetipo están ACTIVOS según la actividad humana, la
//     saturación del sistema (lag de outbox y cola de transbordo) y la
//     cobertura del tablón — la válvula de carga principal, que reduce la
//     población de bots antes que degradar la experiencia humana. Pausa y
//     reanuda bots ya aprovisionados; no retira cuentas.
//   - RETIRO (ADR-024): retira los bots insolventes-inactivos de forma
//     sostenida, absorbiendo su caja al banco central.
//
// Expone su propio servidor de observabilidad en II_BOTS_ADDR (default
// :8082) con /healthz, /readyz y /metrics (ii_bot_decisions_total,
// ii_bot_errors_total, ii_bot_cash, ii_bots_active, ii_bots_density_target,
// ii_bots_density_adjustments_total, ii_outbox_lag_observed).
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
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
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
	densityOpts, err := bots.DensityOptionsFromEnv()
	if err != nil {
		return err
	}

	app, err := service.New(ctx, serviceName, botsOpts.Addr, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	// Métricas de las transacciones SERIALIZABLE del lifecycle (aprovisionado,
	// capitalización, retiro): disparador MEDIDO de contención (SAD §13).
	db.RegisterTxMetrics(app.Metrics().Registry())

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

	// Densidad dinámica (GDD §13.4 modo 2): gobierna la población ACTIVA del
	// propio orquestador (que implementa bots.Population: pausa y reanudación
	// en caliente). Desactivable con II_BOTS_DENSITY_ENABLED=false, en cuyo
	// caso la población arrancada queda fija.
	var density *bots.DensityController
	if densityOpts.Enabled {
		density, err = bots.NewDensityController(app.Pool(), densityOpts, orch, app.Logger(), app.Metrics().Registry())
		if err != nil {
			return err
		}
	}

	app.Logger().Info("orquestador de bots arrancando",
		slog.Int("coal_producers", botsOpts.CoalProducers),
		slog.Int("iron_producers", botsOpts.IronProducers),
		slog.Int("traders", botsOpts.Traders),
		slog.Int("transformers", botsOpts.Transformers),
		slog.Int("freighters", botsOpts.Freighters),
		slog.Int64("capital", botsOpts.Capital),
		slog.Duration("tick", botsOpts.Tick),
		slog.String("api_url", botsOpts.APIURL),
		slog.String("addr", botsOpts.Addr),
		slog.Duration("retire_interval", retireOpts.Interval),
		slog.Int64("retire_cash_floor", retireOpts.CashFloor),
		slog.Int64("retire_idle_sim_seconds", retireOpts.IdleSimSeconds),
		slog.Bool("density_enabled", densityOpts.Enabled),
		slog.Duration("density_interval", densityOpts.Interval))

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

	// Densidad dinámica en paralelo: mientras el orquestador aprovisiona, sus
	// ciclos son no-ops seguras (población aún vacía).
	if density != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := density.Run(ctx); err != nil {
				app.Logger().Error("bots: la densidad dinámica terminó con error", slog.Any("error", err))
			}
		}()
	}

	// Sirve las sondas y métricas hasta la señal; al retornar, ctx ya está
	// cancelado y los bots están parando: wg.Wait espera su cierre limpio.
	runErr := app.Run(ctx)
	wg.Wait()
	app.Logger().Info("bots detenidos")
	return runErr
}
