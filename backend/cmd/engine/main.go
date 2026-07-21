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

	"github.com/lokiteitor/global-market/backend/internal/balancer"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
	"github.com/lokiteitor/global-market/backend/internal/platform/service"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/enforcement"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"
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
	// Métricas de las transacciones SERIALIZABLE (reintentos por conflicto y
	// presupuestos agotados): disparador MEDIDO de contención (SAD §13).
	db.RegisterTxMetrics(app.Metrics().Registry())

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
	// Consumidor de entregas: confirma shipment.arrived (emitido por world) y
	// liquida el CCRI al completar la cantidad a tiempo (GDD 5.3 pasos 5-6).
	deliveryConfirmer, err := contracts.NewDeliveryConfirmer(contractsSvc, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	deliveryConsumer := deliveryConfirmer.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// Liquidador de fletes: confirma shipment.arrived de cargamentos de flete y
	// liquida el CCRI-Flete pro-rata (custodia al cargador en destino, transportista
	// cobra y recupera garantía por lo entregado a tiempo; GDD 5.3.2).
	freightSettler, err := contracts.NewFreightSettler(contractsSvc, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	freightSettlerConsumer := freightSettler.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// Liquidador del sistema: consume building.seized (emitido por
	// world/enforcement al embargar) y subasta PÚBLICAMENTE el stock embargado —
	// lo transfiere al banco central y lo publica como oferta sell del sistema al
	// precio de remate; los proceeds los cobra el banco central (efecto
	// sink/absorción, GDD 11.2). Cierra la cascada de insolvencia.
	systemLiquidator, err := contracts.NewSystemLiquidator(contractsSvc, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	liquidatorConsumer := systemLiquidator.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

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

	// ── Motor de tránsito (Incremento 3): barrido de segmentos vencidos
	//    (combustible, desgaste, avería, avance, llegada y entrega), reanudación
	//    de averías/mantenimiento y job de congestión; más el consumidor
	//    shipment_creator (contract.confirmed → cargamento en el origen) ────────
	fleetWorkerOpts, err := fleet.WorkerOptionsFromEnv()
	if err != nil {
		return err
	}
	transitWorker, err := fleet.NewTransitWorker(app.Pool(), clockSimSource{clk}, fleetWorkerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	shipmentCreator := fleet.NewShipmentCreator(app.Logger(), app.Metrics().Registry())
	shipmentConsumer := shipmentCreator.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// Creador de cargamentos de flete: consume freight.confirmed (emitido por el
	// Contract Service al confirmar un flete) y materializa el cargamento del
	// cargador en el origen, descontando el inventario físico del almacén (la carga
	// ya está en custodia contable; el transportista la despacha, GDD 5.3.2).
	freightShipmentCreator := fleet.NewFreightShipmentCreator(app.Logger(), app.Metrics().Registry())
	freightShipmentConsumer := freightShipmentCreator.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// ── Motor de insolvencia (Incremento 6a): cascada de mantenimiento →
	//    degradación → abandono → embargo → reversión del suelo, y canon →
	//    gracia → embargo (GDD 5.9/11.2). Mismo reloj de simulación; emite
	//    building.seized (lo consume la liquidación del sistema de contracts) y
	//    concession.reverted ──────────────────────────────────────────────────
	enforcementWorkerOpts, err := enforcement.WorkerOptionsFromEnv()
	if err != nil {
		return err
	}
	enforcementWorker, err := enforcement.NewWorker(app.Pool(), clockSimSource{clk}, enforcementWorkerOpts, app.Logger(), app.Metrics().Registry())
	if err != nil {
		return err
	}
	// Liberación in situ: consume contract.expired_undelivered (emitido por el
	// Contract Service al vencer un contrato con cantidad sin entregar) y detiene
	// los cargamentos aún en tránsito de ese contrato, reintegrando su stock físico
	// al almacén de origen (GDD 7.1/5.3: nada se teletransporta, tampoco en fallos).
	shipmentReleaser := fleet.NewShipmentReleaser(app.Logger(), app.Metrics().Registry())
	releaseConsumer := shipmentReleaser.NewConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// ── Economy Balancer (Incremento 6b): las ciudades como consumidor final
	//    (faucet, GDD 5.5/5.6). El DemandWorker recalcula las curvas de demanda,
	//    corre la máquina de niveles y publica las buys de ciudad por el PORT
	//    (implementado aquí con contracts.CreatePublication: mismo camino estándar
	//    del Contract Service, sin canal privilegiado, GDD 18.1). El consumer
	//    consume las entregas urbanas (contract.settled con comprador ciudad) para
	//    que la ciudad sea sumidero final real (city stock_free → world_source) ──
	balancerOpts, err := balancer.OptionsFromEnv()
	if err != nil {
		return err
	}
	balancerMetrics := balancer.NewMetrics(app.Metrics().Registry())
	cityBuyPort := cityBuyCreator{svc: contractsSvc}
	demandWorker, err := balancer.NewDemandWorker(app.Pool(), cityBuyPort, clockSimSource{clk}, balancerOpts, balancerMetrics, app.Logger())
	if err != nil {
		return err
	}
	cityConsumer, err := balancer.NewConsumer(balancerOpts, balancerMetrics, app.Logger())
	if err != nil {
		return err
	}
	cityConsumerRunner := cityConsumer.NewOutboxConsumer(app.Pool(), outbox.WithLogger(app.Logger()))

	// Job MACRO del Balancer (Incremento 6b): analítica (analytics.region_stats/
	// city_snapshots/economy_indicators bucketizados), fórmula laboral (GDD 5.7:
	// recalcula world.cities.base_salary = salario_base(nivel)·saturación regional)
	// y ajuste fiscal algorítmico (GDD 5.5: mueve tax_rate_bp/canon_base un paso
	// acotado según la tendencia masa monetaria vs. PIB). Corre en su propio bucle
	// (II_BALANCER_ANALYTICS_INTERVAL); es monitoreo/regulación de parámetros, no
	// mueve valor del ledger.
	analyticsWorker, err := balancer.NewAnalyticsWorker(app.Pool(), clockSimSource{clk}, balancerOpts, balancerMetrics, app.Logger())
	if err != nil {
		return err
	}

	// ── Arranque de los procesos en segundo plano ────────────────────────────
	// Comparten el ctx de la señal: al apagar, los bucles observan ctx.Done() y
	// retornan nil (parada limpia). wg los sincroniza antes de cerrar el pool.
	var wg sync.WaitGroup
	wg.Add(14)
	go func() {
		defer wg.Done()
		if err := worker.Run(ctx); err != nil {
			app.Logger().Error("contracts: el worker de barridos terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := freightSettlerConsumer.Run(ctx, consumerInterval, freightSettler.Handle); err != nil {
			app.Logger().Error("contracts: el consumidor freight_settler terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := freightShipmentConsumer.Run(ctx, consumerInterval, freightShipmentCreator.Handle); err != nil {
			app.Logger().Error("world/fleet: el consumidor freight_shipment_creator terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := deliveryConsumer.Run(ctx, consumerInterval, deliveryConfirmer.Handle); err != nil {
			app.Logger().Error("contracts: el consumidor delivery_confirmer terminó con error", slog.Any("error", err))
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
	go func() {
		defer wg.Done()
		if err := transitWorker.Run(ctx); err != nil {
			app.Logger().Error("world/fleet: el motor de tránsito terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := shipmentConsumer.Run(ctx, consumerInterval, shipmentCreator.Handle); err != nil {
			app.Logger().Error("world/fleet: el consumidor shipment_creator terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := releaseConsumer.Run(ctx, consumerInterval, shipmentReleaser.Handle); err != nil {
			app.Logger().Error("world/fleet: el consumidor shipment_releaser terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := enforcementWorker.Run(ctx); err != nil {
			app.Logger().Error("world/enforcement: el motor de insolvencia terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := liquidatorConsumer.Run(ctx, consumerInterval, systemLiquidator.Handle); err != nil {
			app.Logger().Error("contracts: el consumidor system_liquidator terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := demandWorker.Run(ctx); err != nil {
			app.Logger().Error("balancer: el motor de demanda terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := cityConsumerRunner.Run(ctx, consumerInterval, cityConsumer.Handle); err != nil {
			app.Logger().Error("balancer: el consumidor city_consumer terminó con error", slog.Any("error", err))
		}
	}()
	go func() {
		defer wg.Done()
		if err := analyticsWorker.Run(ctx); err != nil {
			app.Logger().Error("balancer: el job macro terminó con error", slog.Any("error", err))
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
		slog.Duration("reconcile_interval", productionWorkerOpts.ReconcileInterval),
		slog.Duration("transit_sweep_interval", fleetWorkerOpts.SweepInterval),
		slog.Int("transit_sweep_batch", fleetWorkerOpts.BatchSize),
		slog.Int64("repair_sim_seconds", fleetWorkerOpts.RepairSimSeconds),
		slog.Duration("congestion_interval", fleetWorkerOpts.CongestionInterval),
		slog.String("shipment_creator", fleet.ConsumerShipmentCreator),
		slog.String("freight_shipment_creator", fleet.ConsumerFreightShipmentCreator),
		slog.String("shipment_releaser", fleet.ConsumerShipmentReleaser),
		slog.String("delivery_confirmer", contracts.ConsumerDeliveryConfirmer),
		slog.String("freight_settler", contracts.ConsumerFreightSettler),
		slog.String("system_liquidator", contracts.ConsumerSystemLiquidator),
		slog.Int("liquidation_price_bp", contractsOpts.LiquidationPriceBP),
		slog.Duration("maintenance_interval", enforcementWorkerOpts.MaintenanceInterval),
		slog.Duration("enforcement_interval", enforcementWorkerOpts.EnforcementInterval),
		slog.Int("degrade_pct_per_sim_day", int(enforcementWorkerOpts.DegradePctPerSimDay)),
		slog.Int("abandon_condition_pct", int(enforcementWorkerOpts.AbandonConditionPct)),
		slog.Int64("seize_grace_sim_seconds", enforcementWorkerOpts.SeizeGraceSimSeconds),
		slog.Duration("balancer_demand_interval", balancerOpts.DemandInterval),
		slog.Duration("balancer_analytics_interval", balancerOpts.AnalyticsInterval),
		slog.Int64("city_buy_deadline_sim", int64(balancerOpts.CityBuyDeadlineSim)),
		slog.String("city_consumer", balancer.ConsumerName))

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

// cityBuyCreator implementa balancer.PublicationCreator (el PORT del Balancer)
// con el Contract Service: publica la solicitud de compra de una ciudad por
// contracts.CreatePublication — el MISMO camino estándar (validación, escrow,
// ventana de sorteo) que cualquier otra publicación del tablón, sin canal
// privilegiado (GDD 18.1). La dependencia dirigida balancer→contracts vive AQUÍ,
// en el composition root: el paquete balancer solo conoce el PORT.
type cityBuyCreator struct{ svc *contracts.Service }

func (c cityBuyCreator) CreateCityBuy(ctx context.Context, by balancer.CityBuy) error {
	product := by.ProductID
	dest := by.DestinationNodeID
	_, err := c.svc.CreatePublication(ctx, by.CityAccountID, contracts.PublicationInput{
		Kind:               contracts.KindBuy,
		Channel:            contracts.ChannelBoard,
		ProductID:          &product,
		QuantityTotal:      by.Quantity,
		UnitPrice:          by.UnitPrice,
		DestinationNodeID:  &dest,
		DeliverySimSeconds: int64(by.DeliverySimSeconds),
	})
	return err
}

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
