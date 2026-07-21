package balancer

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// SimSource entrega el sim-time actual del mundo. En producción es *clock.Reader
// (internal/sim/clock, adaptado en el composition root); los tests inyectan un
// reloj fijo. Los plazos de dominio (updated_at_sim, deadline de las buys) usan
// SIEMPRE este reloj.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// DemandWorker es el motor periódico del Balancer: recalcula las curvas de
// demanda de cada ciudad, corre su máquina de niveles y publica/mantiene sus
// solicitudes de compra en el tablón (por el PORT, sin canal privilegiado). Cada
// ciudad se recalcula en SU PROPIA transacción SERIALIZABLE; las buys se publican
// después, cada una por el camino estándar del Contract Service.
type DemandWorker struct {
	pool    *pgxpool.Pool
	repo    *Repo
	port    PublicationCreator
	sim     SimSource
	opts    Options
	logger  *slog.Logger
	metrics *Metrics
}

// NewDemandWorker construye el motor de demanda. port es el PORT de publicación
// (obligatorio); reg (vía metrics) puede ser nil. logger nil usa slog.Default().
func NewDemandWorker(pool *pgxpool.Pool, port PublicationCreator, sim SimSource, opts Options, metrics *Metrics, logger *slog.Logger) (*DemandWorker, error) {
	if pool == nil {
		return nil, errors.New("balancer: el DemandWorker requiere un pool de BD")
	}
	if port == nil {
		return nil, errors.New("balancer: el DemandWorker requiere un PublicationCreator (PORT)")
	}
	if sim == nil {
		return nil, errors.New("balancer: el DemandWorker requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DemandWorker{
		pool: pool, repo: NewRepo(pool), port: port, sim: sim, opts: opts,
		logger: logger.With(slog.String("module", "balancer")), metrics: metrics,
	}, nil
}

// Run ejecuta el bucle del motor hasta que ctx se cancele (nil al apagado
// limpio). Cada iteración recalcula todas las ciudades y publica sus buys; el
// refresco macro (gauges) se dispara en su propio intervalo.
func (w *DemandWorker) Run(ctx context.Context) error {
	w.logger.Info("balancer: motor de demanda iniciado",
		slog.Duration("demand_interval", w.opts.DemandInterval),
		slog.Duration("analytics_interval", w.opts.AnalyticsInterval),
		slog.Int64("city_buy_deadline_sim", int64(w.opts.CityBuyDeadlineSim)),
		slog.Float64("supply_ema_alpha", w.opts.SupplyEMAAlpha),
		slog.Float64("supply_ema_floor", w.opts.SupplyEMAFloor),
		slog.Float64("levelup_index_base", w.opts.LevelupIndexBase))
	lastAnalytics := time.Now()
	for {
		w.RunOnce(ctx)
		if time.Since(lastAnalytics) >= w.opts.AnalyticsInterval {
			w.refreshMacro(ctx)
			lastAnalytics = time.Now()
		}
		if !sleepJitter(ctx, w.opts.DemandInterval) {
			w.logger.Info("balancer: motor de demanda detenido")
			return nil
		}
	}
}

// RunOnce ejecuta UNA pasada: recalcula cada ciudad y publica sus buys. Aislado
// para los tests, que controlan el disparo.
func (w *DemandWorker) RunOnce(ctx context.Context) {
	start := time.Now()
	cities, err := w.repo.ListCities(ctx)
	if err != nil {
		w.logger.Warn("balancer: no se pudieron listar las ciudades", slog.Any("error", err))
		return
	}
	for _, c := range cities {
		res, err := w.recalcCity(ctx, c.ID)
		if err != nil {
			w.logger.Warn("balancer: recálculo de ciudad con error",
				slog.String("city_id", c.ID.String()), slog.Any("error", err))
			continue
		}
		w.metrics.setCityLevel(res.Name, res.Level)
		w.publishCityBuys(ctx, res)
	}
	w.metrics.observeRecalc(time.Since(start).Seconds())
}

// recalcResult es el estado de una ciudad tras el recálculo, con los objetivos de
// compra para la fase de publicación (fuera de la transacción del recálculo).
type recalcResult struct {
	CityID             uuid.UUID
	CityAccountID      uuid.UUID
	Name               string
	Level              int32
	Direction          levelDirection
	DistributionNodeID uuid.UUID
	HasDistribution    bool
	Buys               []cityBuyTarget
}

// cityBuyTarget es la solicitud de compra objetivo de un (producto) activo.
type cityBuyTarget struct {
	ProductID uuid.UUID
	UnitPrice int64
	Quantity  int64
}

// recalcCity recalcula una ciudad en UNA transacción SERIALIZABLE: bloquea la
// ciudad, corre la máquina de niveles (con el suministro de la ventana), escala
// D0 si cambió de nivel, recalcula la curva de cada producto ACTIVO
// (unlocked_at_level <= nivel) con todos los clamps, persiste el crecimiento y
// emite el evento de nivel. Devuelve los objetivos de compra para publicarlos
// después (el PORT abre su propia transacción, no anidable aquí).
func (w *DemandWorker) recalcCity(ctx context.Context, cityID uuid.UUID) (recalcResult, error) {
	simNow := w.sim.Now(ctx)
	var res recalcResult

	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		r := w.repo.WithTx(tx)

		c, err := r.LockCity(ctx, cityID)
		if err != nil {
			return err
		}
		rows, err := r.ListCityDemand(ctx, cityID)
		if err != nil {
			return err
		}

		// Suministro total de la ventana (para el decaimiento) y ventana de la
		// ciudad en sim-segundos desde su último recálculo (marcador updated_at_sim,
		// que solo escribe UpdateCityGrowth). La curva usa la ventana POR FILA
		// (updated_at_sim de cada city_demand), también sello de su último recálculo.
		var totalRecent int64
		for _, d := range rows {
			totalRecent += d.RecentSupply
		}
		cityWindowSim := int64(simNow) - c.UpdatedAtSim

		// Máquina de niveles ANTES de recalcular la curva: así los productos
		// desbloqueados por una subida se recalculan y compran en esta misma pasada.
		lv := decideLevel(c, totalRecent, cityWindowSim, w.opts)
		if lv.D0FactorBP != 0 {
			if err := r.GrowCityDemandD0(ctx, cityID, lv.D0FactorBP); err != nil {
				return err
			}
		}

		res = recalcResult{CityID: cityID, CityAccountID: c.AccountID, Name: c.Name, Level: lv.Level, Direction: lv.Direction}

		for _, d := range rows {
			if d.UnlockedAtLevel > lv.Level {
				continue // categoría aún bloqueada para el nivel (nuevo) de la ciudad
			}
			d0 := d.D0PerSimDay
			if lv.D0FactorBP != 0 {
				d0 = d0 * lv.D0FactorBP / 10000 // reflejar en memoria el escalado ya asentado
			}
			anchor, err := r.GetProduct(ctx, d.ProductID)
			if err != nil {
				return err
			}
			out := recomputeCurve(curveInput{
				D0PerSimDay:  d0,
				SupplyEMAOld: d.SupplyEMA,
				RecentSupply: d.RecentSupply,
				WindowSim:    int64(simNow) - d.UpdatedAtSim,
				BasePrice:    anchor.BasePrice,
				PriceFloor:   anchor.PriceFloor,
				PriceCeiling: anchor.PriceCeiling,
				LuxuryClass:  anchor.Class == productClassLuxury,
			}, w.opts)
			if err := r.UpdateCityDemandCurve(ctx, cityID, d.ProductID, out.SupplyEMA, out.SaturationFactor, out.CurrentPrice, simNow); err != nil {
				return err
			}
			if qty := buyTargetQty(d0, out.SaturationFactor, w.opts); qty > 0 {
				res.Buys = append(res.Buys, cityBuyTarget{ProductID: d.ProductID, UnitPrice: out.CurrentPrice, Quantity: qty})
			}
		}

		if err := r.UpdateCityGrowth(ctx, cityID, lv.Level, lv.Population, lv.SupplyIndex, simNow); err != nil {
			return err
		}

		if lv.Direction != levelNone {
			if err := emitLevelEvent(ctx, tx, cityID, c.Level, lv, simNow); err != nil {
				return err
			}
		}

		// Nodo del centro de distribución (destino de las buys); su ausencia no
		// aborta el recálculo (solo impide publicar buys, se registra al publicar).
		if node, err := r.GetCityDistributionNode(ctx, cityID); err == nil {
			res.DistributionNodeID = node
			res.HasDistribution = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	})
	if err != nil {
		return recalcResult{}, err
	}

	// Métricas tras el COMMIT (no dentro de la transacción, que RunSerializable
	// puede reejecutar): contar el cambio de nivel una sola vez.
	if res.Direction != levelNone {
		w.metrics.incLevelChange(string(res.Direction))
		w.logger.Info("balancer: cambio de nivel de ciudad",
			slog.String("city_id", res.CityID.String()),
			slog.String("city", res.Name),
			slog.String("direction", string(res.Direction)),
			slog.Int("new_level", int(res.Level)))
	}
	return res, nil
}

// refreshMacro actualiza los gauges macroeconómicos (masa monetaria y nivel por
// ciudad): monitoreo del bucle faucet/sink (GDD 5.5), dentro de los permisos de
// lectura del motor (no escribe analytics.*). Best-effort: un fallo se registra
// y no detiene el motor.
func (w *DemandWorker) refreshMacro(ctx context.Context) {
	if total, err := w.repo.MoneySupply(ctx); err != nil {
		w.logger.Debug("balancer: no se pudo refrescar la masa monetaria", slog.Any("error", err))
	} else {
		w.metrics.setMoneySupply(total)
	}
	if cities, err := w.repo.ListCities(ctx); err == nil {
		for _, c := range cities {
			w.metrics.setCityLevel(c.Name, c.Level)
		}
	}
}

// emitLevelEvent emite city.level_up / city.level_down por el outbox (misma
// transacción que el cambio de nivel: si esta se revierte, el evento desaparece).
func emitLevelEvent(ctx context.Context, tx pgx.Tx, cityID uuid.UUID, oldLevel int32, lv levelOutcome, simNow simtime.SimTime) error {
	eventType := EventCityLevelUp
	if lv.Direction == levelDown {
		eventType = EventCityLevelDown
	}
	return outbox.Emit(ctx, tx, int64(simNow), aggregateCity, cityID, eventType, CityLevelChangedPayload{
		CityID:       cityID.String(),
		OldLevel:     int(oldLevel),
		NewLevel:     int(lv.Level),
		Population:   lv.Population,
		Direction:    string(lv.Direction),
		ChangedAtSim: int64(simNow),
	})
}

// CityLevelChangedPayload es el payload de city.level_up / city.level_down.
type CityLevelChangedPayload struct {
	CityID       string `json:"city_id"`
	OldLevel     int    `json:"old_level"`
	NewLevel     int    `json:"new_level"`
	Population   int64  `json:"population"`
	Direction    string `json:"direction"` // up | down
	ChangedAtSim int64  `json:"changed_at_sim"`
}

// sleepJitter espera d ± hasta 25% de jitter y devuelve false si el contexto se
// cancela antes (desincroniza instancias concurrentes del motor).
func sleepJitter(ctx context.Context, d time.Duration) bool {
	jitter := time.Duration(rand.Int64N(int64(d/2) + 1))
	wait := d - d/4 + jitter
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
