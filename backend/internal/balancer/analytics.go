package balancer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// AnalyticsWorker es la parte MACRO del Economy Balancer (Incremento 6b): el job
// que cierra el bucle de observabilidad y regulación de la economía. En cada
// barrido (II_BALANCER_ANALYTICS_INTERVAL) y en TRES pasos ordenados:
//
//  1. analítica (analytics.*): escribe, bucketizado por sim-time, las
//     estadísticas regionales (region_stats: ocupación industrial, edificios
//     activos, contratos liquidados, volumen), las fotos de ciudad
//     (city_snapshots: nivel, población, supply_index) y los indicadores macro
//     (economy_indicators: masa monetaria, PIB simulado, emisión vs. absorción,
//     poblaciones activas y ritmo de agotamiento con su proyección JSONB).
//  2. fórmula laboral (GDD 5.7): recalcula world.cities.base_salary como el
//     salario efectivo = salario_base(nivel) · factor_saturación(ocupación
//     regional) — consume la ocupación que el paso 1 acaba de escribir.
//  3. ajuste fiscal (banco central algorítmico, GDD 5.5): mueve tax_rate_bp y
//     canon_base de las regiones un paso ACOTADO según la tendencia inflación/
//     deflación (masa monetaria vs. PIB) que el paso 1 acaba de registrar.
//
// La analítica es MONITOREO y REGULACIÓN de parámetros, no movimiento de valor:
// escribe agregados de analytics y parámetros de world (base_salary, fiscalidad),
// nunca partidas del ledger. Cada paso corre en su propia transacción
// SERIALIZABLE. Es un worker INDEPENDIENTE del DemandWorker (parte de ciudades):
// comparten Repo, Options, Metrics y SimSource, pero distintos bucles.
type AnalyticsWorker struct {
	pool    *pgxpool.Pool
	repo    *Repo
	sim     SimSource
	opts    Options
	logger  *slog.Logger
	metrics *Metrics
}

// NewAnalyticsWorker construye el job macro. pool y sim son obligatorios; metrics
// puede ser nil (sin instrumentar); logger nil usa slog.Default(). Options
// inválidas devuelven error: no arrancar.
func NewAnalyticsWorker(pool *pgxpool.Pool, sim SimSource, opts Options, metrics *Metrics, logger *slog.Logger) (*AnalyticsWorker, error) {
	if pool == nil {
		return nil, errors.New("balancer: el AnalyticsWorker requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("balancer: el AnalyticsWorker requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AnalyticsWorker{
		pool: pool, repo: NewRepo(pool), sim: sim, opts: opts,
		logger: logger.With(slog.String("module", "balancer"), slog.String("job", "analytics")), metrics: metrics,
	}, nil
}

// Run ejecuta el bucle macro hasta que ctx se cancele (nil al apagado limpio).
func (w *AnalyticsWorker) Run(ctx context.Context) error {
	w.logger.Info("balancer: job macro iniciado",
		slog.Duration("analytics_interval", w.opts.AnalyticsInterval),
		slog.Int64("analytics_bucket_sim", w.opts.AnalyticsBucketSim),
		slog.Int("tax_min_bp", w.opts.TaxMinBP),
		slog.Int("tax_max_bp", w.opts.TaxMaxBP))
	for {
		w.RunOnce(ctx)
		if !sleepJitter(ctx, w.opts.AnalyticsInterval) {
			w.logger.Info("balancer: job macro detenido")
			return nil
		}
	}
}

// RunOnce ejecuta UN barrido macro: analítica → fórmula laboral → ajuste fiscal.
// Cada paso es best-effort e independiente: un fallo se registra y no impide los
// siguientes (la analítica es de baja prioridad, nunca compite con el CCRI).
// Aislado para los tests, que controlan el disparo.
func (w *AnalyticsWorker) RunOnce(ctx context.Context) {
	start := time.Now()

	if err := w.writeAnalytics(ctx); err != nil {
		w.logger.Warn("balancer: el paso de analítica falló", slog.Any("error", err))
	}
	if err := w.recalcLabor(ctx, w.sim.Now(ctx)); err != nil {
		w.logger.Warn("balancer: la fórmula laboral falló", slog.Any("error", err))
	}
	if _, err := w.recalcFiscal(ctx); err != nil {
		w.logger.Warn("balancer: el ajuste fiscal falló", slog.Any("error", err))
	}

	w.metrics.observeAnalytics(time.Since(start).Seconds())
}

// writeAnalytics computa y persiste los agregados del bucket actual
// (region_stats, city_snapshots, economy_indicators). Las lecturas se hacen
// fuera de la transacción (agregados idempotentes de sólo lectura); los UPSERT
// se confirman juntos en UNA transacción SERIALIZABLE. Los gauges espejo se fijan
// tras el COMMIT.
func (w *AnalyticsWorker) writeAnalytics(ctx context.Context) error {
	simNow := w.sim.Now(ctx)
	bucketStart := simtime.SimTime(bucketStartSim(int64(simNow), w.opts.AnalyticsBucketSim))
	bucketEnd := bucketStart + simtime.SimTime(w.opts.AnalyticsBucketSim)

	regions, err := w.repo.ListRegions(ctx)
	if err != nil {
		return err
	}
	activeBuildings, err := w.repo.RegionActiveBuildings(ctx)
	if err != nil {
		return err
	}
	settled, err := w.repo.RegionSettledStats(ctx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	cities, err := w.repo.ListCities(ctx)
	if err != nil {
		return err
	}
	money, err := w.repo.MoneySupply(ctx)
	if err != nil {
		return err
	}
	gdp, err := w.repo.BucketGDP(ctx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	emission, err := w.repo.BucketEmission(ctx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	absorption, err := w.repo.BucketAbsorption(ctx, bucketStart, bucketEnd)
	if err != nil {
		return err
	}
	bots, humans, err := w.repo.CountActiveAccounts(ctx)
	if err != nil {
		return err
	}
	byProduct, err := w.repo.FiniteDepletionByProduct(ctx)
	if err != nil {
		return err
	}

	depletionRate, projection := computeDepletion(simNow, byProduct, w.opts)
	projJSON, err := json.Marshal(projection)
	if err != nil {
		return err
	}

	indicators := economyIndicators{
		BucketStart:         bucketStart,
		MoneySupply:         money,
		SimulatedGDP:        gdp,
		EmissionTotal:       emission,
		AbsorptionTotal:     absorption,
		ActiveBotCount:      bots,
		ActiveHumanCount:    humans,
		GlobalDepletionRate: depletionRate,
		DepletionProjection: projJSON,
	}

	err = db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		r := w.repo.WithTx(tx)
		for _, reg := range regions {
			occupation := regionOccupation(activeBuildings[reg.ID], w.opts)
			s := settled[reg.ID]
			if err := r.UpsertRegionStats(ctx, reg.ID, bucketStart, occupation, activeBuildings[reg.ID], s.ContractsSettled, s.TradeVolume); err != nil {
				return err
			}
		}
		for _, c := range cities {
			if err := r.UpsertCitySnapshot(ctx, c.ID, bucketStart, c.Level, c.Population, c.SupplyIndex); err != nil {
				return err
			}
		}
		return r.UpsertEconomyIndicators(ctx, indicators)
	})
	if err != nil {
		return err
	}

	w.metrics.setMacroMoneySupply(money)
	w.metrics.setSimulatedGDP(gdp)
	w.metrics.setGlobalDepletionRate(depletionRate)
	w.logger.LogAttrs(ctx, slog.LevelDebug, "analítica macro asentada",
		slog.Int64("bucket_start_sim", int64(bucketStart)),
		slog.Int64("money_supply", money),
		slog.Int64("simulated_gdp", gdp),
		slog.Int64("emission_total", emission),
		slog.Int64("absorption_total", absorption),
		slog.Float64("global_depletion_rate", depletionRate))
	return nil
}

// regionOccupation normaliza la ocupación industrial de una región: edificios
// operativos / capacidad de referencia, con suelo 0. Es el factor de saturación
// laboral (GDD 5.7): >1 significa una región más saturada que su referencia.
func regionOccupation(activeBuildings int32, o Options) float64 {
	if activeBuildings <= 0 {
		return 0
	}
	return float64(activeBuildings) / o.LaborCapacityRef
}

// resourceProjection es la proyección de agotamiento de un recurso finito.
type resourceProjection struct {
	Remaining             int64   `json:"remaining"`
	RatePerSimDay         float64 `json:"rate_per_sim_day"`
	SimDaysToDepletion    float64 `json:"sim_days_to_depletion"` // -1: sin agotamiento (ritmo 0)
	DepletedWithinHorizon bool    `json:"depleted_within_horizon"`
}

// depletionReport es el documento JSONB de proyección de agotamiento por recurso.
type depletionReport struct {
	AsOfSim             int64                         `json:"as_of_sim"`
	HorizonSimDays      int64                         `json:"horizon_sim_days"`
	GlobalRatePerSimDay float64                       `json:"global_rate_per_sim_day"`
	Resources           map[string]resourceProjection `json:"resources"`
}

// computeDepletion deriva el ritmo global de agotamiento (unidades por día de
// juego) y la proyección por recurso a partir del extraído acumulado (initial −
// remaining) sobre el tiempo transcurrido desde el génesis: rate = extraído /
// días_de_juego_transcurridos. Es una proyección SIMPLE (media de vida): a ritmo
// constante, cuántos días de juego hasta agotar el restante, y si eso cae dentro
// del horizonte configurado. Sin recursos finitos el ritmo es 0.
func computeDepletion(simNow simtime.SimTime, byProduct []depletionProduct, o Options) (float64, depletionReport) {
	elapsedDays := float64(simNow) / float64(simtime.SimDay)
	if elapsedDays < 1 {
		elapsedDays = 1 // génesis / primer día: evita ritmos artificialmente enormes
	}
	report := depletionReport{
		AsOfSim:        int64(simNow),
		HorizonSimDays: o.DepletionHorizonSimDays,
		Resources:      make(map[string]resourceProjection, len(byProduct)),
	}
	var globalRate float64
	for _, p := range byProduct {
		rate := float64(p.Extracted) / elapsedDays
		globalRate += rate
		proj := resourceProjection{Remaining: p.Remaining, RatePerSimDay: rate, SimDaysToDepletion: -1}
		if rate > 0 {
			proj.SimDaysToDepletion = float64(p.Remaining) / rate
			proj.DepletedWithinHorizon = proj.SimDaysToDepletion <= float64(o.DepletionHorizonSimDays)
		}
		report.Resources[p.Code] = proj
	}
	report.GlobalRatePerSimDay = globalRate
	return globalRate, report
}

// bucketStartSim calcula el inicio del bucket en sim-time:
// floor(simNow / bucketSecs) * bucketSecs. simNow y bucketSecs son no negativos
// (dominio sim_time y configuración validada > 0): la división entera de Go
// trunca hacia cero, que para no negativos es floor.
func bucketStartSim(simNow, bucketSecs int64) int64 {
	if simNow < 0 {
		simNow = 0
	}
	return (simNow / bucketSecs) * bucketSecs
}
