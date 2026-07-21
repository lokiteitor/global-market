package balancer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/balancer/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Eventos del mercado spot eléctrico (contratos de evento fijos; los
// consumidores externos declaran los nombres como constantes locales).
const (
	aggregateRegion       = "region"
	aggregatePowerBldg    = "building"
	EventPowerSpotCleared = "power.spot_cleared" // informativo/WS: resultado del tick por región
	EventPowerCurtailed   = "power.curtailed"    // informativo/WS: edificio sin suministro este tick
)

// PowerSpotClearedPayload es el contrato del evento power.spot_cleared.
// Dinero/stock como string de punto fijo; sim-time como entero.
type PowerSpotClearedPayload struct {
	RegionID           string `json:"region_id"`
	TickSim            int64  `json:"tick_sim"`
	IntervalSim        int64  `json:"interval_sim"`
	ClosingPrice       string `json:"closing_price"`
	DemandUnits        string `json:"demand_units"`
	SuppliedUnits      string `json:"supplied_units"`
	CurtailedUnits     string `json:"curtailed_units"`
	CurtailedBuildings int    `json:"curtailed_buildings"`
}

// PowerCurtailedPayload es el contrato del evento power.curtailed. reason es
// "curtailed" (recorte rotatorio por déficit) o "insolvent" (la caja no cubre
// el pago; GDD 5.9: sin compra, sin deuda).
type PowerCurtailedPayload struct {
	BuildingID     string   `json:"building_id"`
	OwnerAccountID string   `json:"owner_account_id"`
	RegionID       string   `json:"region_id"`
	TickSim        int64    `json:"tick_sim"`
	Reason         string   `json:"reason"`
	PausedBatchIDs []string `json:"paused_batch_ids,omitempty"`
}

// PowerWorker es el tick del mercado spot eléctrico regional (GDD 5.8/18.1,
// ADR-025 §6): por cada región con líneas operativas y bucket de sim-time
// vencido casa oferta y demanda por orden de mérito, liquida los pagos
// (consumidores → generadores, UN asiento power_spot al precio de cierre
// uniforme), quema el combustible de las térmicas despachadas (consumption
// contra world_source, ADR-022) y pausa/reanuda la producción de los
// consumidores según el resultado — todo por región en UNA tx SERIALIZABLE
// con outbox.Emit en la misma tx.
type PowerWorker struct {
	pool    *pgxpool.Pool
	repo    *Repo
	sim     SimSource
	opts    Options
	logger  *slog.Logger
	metrics *Metrics
}

// NewPowerWorker construye el tick del spot. reg de métricas y logger según la
// convención del módulo (nil-safe).
func NewPowerWorker(pool *pgxpool.Pool, sim SimSource, opts Options, metrics *Metrics, logger *slog.Logger) (*PowerWorker, error) {
	if pool == nil {
		return nil, errors.New("balancer: el PowerWorker requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("balancer: el PowerWorker requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PowerWorker{
		pool:    pool,
		repo:    NewRepo(pool),
		sim:     sim,
		opts:    opts,
		logger:  logger.With(slog.String("module", "balancer"), slog.String("job", "power_spot")),
		metrics: metrics,
	}, nil
}

// Run ejecuta el bucle del tick hasta que ctx se cancele (nil al apagado limpio).
func (w *PowerWorker) Run(ctx context.Context) error {
	w.logger.Info("balancer: tick del mercado spot eléctrico iniciado",
		slog.Int64("spot_interval_sim", w.opts.PowerSpotIntervalSim),
		slog.Duration("sweep_interval", w.opts.PowerSpotSweepInterval),
		slog.Int64("connect_radius_m", w.opts.PowerConnectRadiusM),
		slog.Int64("default_bid_price", w.opts.PowerDefaultBidPrice))
	for {
		w.RunOnce(ctx)
		if !sleepJitter(ctx, w.opts.PowerSpotSweepInterval) {
			w.logger.Info("balancer: tick del mercado spot eléctrico detenido")
			return nil
		}
	}
}

// RunOnce procesa los buckets vencidos de todas las regiones con red. Aislado
// para los tests, que controlan el disparo.
func (w *PowerWorker) RunOnce(ctx context.Context) {
	regions, err := w.repo.ListPowerRegions(ctx)
	if err != nil {
		w.logger.Warn("balancer: fallo listando regiones con red eléctrica", slog.Any("error", err))
		return
	}
	if len(regions) == 0 {
		return
	}
	simNow := w.sim.Now(ctx)
	bucket := (int64(simNow) / w.opts.PowerSpotIntervalSim) * w.opts.PowerSpotIntervalSim
	for _, region := range regions {
		if region.LastTickSim >= bucket {
			continue // el bucket vigente ya está liquidado
		}
		start := time.Now()
		if err := w.processRegion(ctx, region, bucket); err != nil {
			w.logger.Warn("balancer: fallo liquidando el tick del spot",
				slog.String("region", region.Name), slog.Int64("tick_sim", bucket), slog.Any("error", err))
			continue
		}
		w.metrics.observePowerTick(time.Since(start).Seconds())
	}
}

// powerTickOutcome acumula los efectos del tick para métricas/log post-commit
// (RunSerializable puede re-ejecutar el cuerpo).
type powerTickOutcome struct {
	closing            int64
	demand             int64
	supplied           int64
	curtailedUnits     int64
	curtailedBuildings int
	fuelBurned         map[string]int64 // por code de producto
}

// processRegion liquida el bucket tickSim de una región en UNA tx SERIALIZABLE.
// Los buckets perdidos no se recuperan (la energía no es almacenable): solo se
// liquida el bucket VIGENTE.
func (w *PowerWorker) processRegion(ctx context.Context, region powerRegion, tickSim int64) error {
	var oc *powerTickOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = &powerTickOutcome{fuelBurned: map[string]int64{}}
		r := w.repo.WithTx(tx)

		exists, err := r.ExistsPowerSpotTick(ctx, region.ID, tickSim)
		if err != nil {
			return err
		}
		if exists {
			return nil // otra instancia liquidó este bucket
		}
		simNow := w.sim.Now(ctx)

		consumers, err := r.ListPowerConsumers(ctx, region.ID, w.opts.PowerConnectRadiusM)
		if err != nil {
			return err
		}
		generators, err := r.ListPowerGenerators(ctx, region.ID, w.opts.PowerConnectRadiusM)
		if err != nil {
			return err
		}

		energyPerHour := w.opts.PowerSpotIntervalSim / 3_600 // horas-sim por tick (entero por Validate)
		bids := make([]powerBid, 0, len(consumers))
		seen := make(map[uuid.UUID]bool, len(consumers))
		for _, c := range consumers {
			if seen[c.BuildingID] {
				continue // un lote activo por edificio gobierna su demanda
			}
			seen[c.BuildingID] = true
			bid := c.BidPrice
			if bid <= 0 {
				bid = w.opts.PowerDefaultBidPrice
			}
			bids = append(bids, powerBid{
				BuildingID:       c.BuildingID,
				OwnerID:          c.OwnerAccountID,
				Price:            bid,
				Energy:           c.PowerPerHour * energyPerHour,
				LastCurtailedSim: c.LastCurtailedAtSim,
				OwnerCash:        c.OwnerCash,
			})
		}

		offers := make([]powerOffer, 0, len(generators))
		genByBuilding := make(map[uuid.UUID]powerGeneratorRow, len(generators))
		for _, g := range generators {
			capacity := powerCapacityForLevel(g.Capacity, g.LevelCurve, g.Level) * energyPerHour
			if g.FuelPerUnit > 0 {
				// Sin combustible no despachan (GDD 5.8): el ofertable se limita por
				// el MÍNIMO de los dos planos (físico y contable), como consumable.
				fuel := min(g.FuelPhysical, g.FuelLedger)
				if limit := fuel / g.FuelPerUnit; limit < capacity {
					capacity = limit
				}
			}
			if capacity <= 0 {
				continue
			}
			genByBuilding[g.BuildingID] = g
			offers = append(offers, powerOffer{
				BuildingID: g.BuildingID,
				OwnerID:    g.OwnerAccountID,
				Price:      g.OfferPrice,
				Capacity:   capacity,
			})
		}

		res := clearSpotMarket(offers, bids)
		oc.closing = res.ClosingPrice
		oc.demand = res.DemandUnits
		oc.supplied = res.SuppliedUnits
		oc.curtailedUnits = res.CurtailedUnits
		oc.curtailedBuildings = len(res.Unserved)

		// ── Pagos: UN asiento power_spot (consumidores → generadores) ──
		if res.SuppliedUnits > 0 {
			paid := map[uuid.UUID]int64{}
			for _, s := range res.Served {
				paid[s.OwnerID] += s.Amount
			}
			earned := map[uuid.UUID]int64{}
			for _, d := range res.Dispatch {
				earned[d.OwnerID] += d.Revenue
			}
			entries := make([]entryAmount, 0, len(paid)+len(earned))
			for owner, amount := range paid {
				cash, err := r.EnsureCashAccount(ctx, owner)
				if err != nil {
					return err
				}
				entries = append(entries, entryAmount{AccountID: cash.ID, Amount: -amount})
			}
			for owner, amount := range earned {
				cash, err := r.EnsureCashAccount(ctx, owner)
				if err != nil {
					return err
				}
				entries = append(entries, entryAmount{AccountID: cash.ID, Amount: amount})
			}
			if _, err := r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindPowerSpot, simNow, region.ID,
				fmt.Sprintf("Spot eléctrico %s tick %d (cierre %d)", region.Name, tickSim, res.ClosingPrice),
				entries); err != nil {
				return err
			}

			// ── Quemado de combustible de las térmicas despachadas (ADR-022) ──
			for _, d := range res.Dispatch {
				g := genByBuilding[d.BuildingID]
				if g.FuelPerUnit <= 0 || g.FuelProductID == nil || d.Units <= 0 {
					continue
				}
				fuel, err := mulOverflow(d.Units, g.FuelPerUnit)
				if err != nil {
					return fmt.Errorf("balancer: combustible del despacho de %s desborda int64", d.BuildingID)
				}
				sf, err := r.GetStockFreeAccount(ctx, g.OwnerAccountID, *g.FuelProductID, g.BuildingID)
				if err != nil {
					return fmt.Errorf("balancer: stock_free de combustible de %s: %w", d.BuildingID, err)
				}
				ws, err := r.EnsureWorldSourceAccount(ctx, *g.FuelProductID)
				if err != nil {
					return err
				}
				if _, err := r.PostLedgerTransaction(ctx, txKindConsumption, simNow, d.BuildingID,
					fmt.Sprintf("Combustible de central eléctrica (%d)", fuel),
					[]entryAmount{{AccountID: ws, Amount: fuel}, {AccountID: sf.ID, Amount: -fuel}}); err != nil {
					return err
				}
				if err := r.ConsumeBuildingInventory(ctx, d.BuildingID, *g.FuelProductID, fuel, simNow); err != nil {
					return err
				}
				if err := r.RefreshPlantFuelMirror(ctx, d.BuildingID, *g.FuelProductID); err != nil {
					return err
				}
				oc.fuelBurned[g.FuelCode] += fuel
			}
		}

		// ── Plano físico del tick: despachos, suministro y pausas ──
		for _, s := range res.Served {
			if err := r.InsertPowerDispatch(ctx, sqlcgen.InsertPowerDispatchParams{
				RegionID: region.ID, TickSim: tickSim, BuildingID: s.BuildingID,
				OwnerAccountID: s.OwnerID, Role: sqlcgen.WorldPowerRoleConsumer,
				Units: s.Energy, UnitPrice: res.ClosingPrice, Amount: s.Amount,
			}); err != nil {
				return err
			}
			// Cobertura hasta tick + 1,5 intervalos (la media gracia absorbe el
			// desfase wall-clock hasta que el siguiente tick liquide), a la
			// TASA facturada: el cierre de lote exige power_per_hour <= tasa.
			until := tickSim + w.opts.PowerSpotIntervalSim + w.opts.PowerSpotIntervalSim/2
			if err := r.SetBuildingPowered(ctx, s.BuildingID, until, s.Energy/energyPerHour, simNow); err != nil {
				return err
			}
			if _, err := r.ResumeNoPowerBatches(ctx, s.BuildingID, simNow); err != nil {
				return err
			}
		}
		for _, d := range res.Dispatch {
			if d.Units <= 0 {
				continue
			}
			if err := r.InsertPowerDispatch(ctx, sqlcgen.InsertPowerDispatchParams{
				RegionID: region.ID, TickSim: tickSim, BuildingID: d.BuildingID,
				OwnerAccountID: d.OwnerID, Role: sqlcgen.WorldPowerRoleGenerator,
				Units: d.Units, UnitPrice: res.ClosingPrice, Amount: d.Revenue,
			}); err != nil {
				return err
			}
		}
		for _, u := range res.Unserved {
			// CIERRA la cobertura residual del tick anterior (until = tick,
			// tasa 0): sin esto, la gracia previa dejaría al barrido de
			// producción reanudar y completar lotes con energía no comprada.
			if err := r.SetBuildingPowered(ctx, u.BuildingID, tickSim, 0, simNow); err != nil {
				return err
			}
			pausedIDs, err := r.PauseRunningBatchesNoPower(ctx, u.BuildingID, simNow)
			if err != nil {
				return err
			}
			if u.Reason == unservedCurtailed {
				// La rotación solo sella el recorte por déficit: un insolvente no
				// compite y no debe acumular prioridad para cuando vuelva a pujar.
				if err := r.MarkBuildingCurtailed(ctx, u.BuildingID, simtime.SimTime(tickSim)); err != nil {
					return err
				}
			}
			paused := make([]string, 0, len(pausedIDs))
			for _, id := range pausedIDs {
				paused = append(paused, id.String())
			}
			if err := outbox.Emit(ctx, tx, int64(simNow), aggregatePowerBldg, u.BuildingID, EventPowerCurtailed,
				PowerCurtailedPayload{
					BuildingID:     u.BuildingID.String(),
					OwnerAccountID: u.OwnerID.String(),
					RegionID:       region.ID.String(),
					TickSim:        tickSim,
					Reason:         u.Reason,
					PausedBatchIDs: paused,
				}); err != nil {
				return err
			}
		}

		if err := r.InsertPowerSpotTick(ctx, sqlcgen.InsertPowerSpotTickParams{
			RegionID: region.ID, TickSim: tickSim, IntervalSim: w.opts.PowerSpotIntervalSim,
			ClosingPrice: res.ClosingPrice, DemandUnits: res.DemandUnits,
			SuppliedUnits: res.SuppliedUnits, CurtailedUnits: res.CurtailedUnits,
			CurtailedBuildings: int32(len(res.Unserved)), //nolint:gosec // acotado por participantes
		}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), aggregateRegion, region.ID, EventPowerSpotCleared,
			PowerSpotClearedPayload{
				RegionID:           region.ID.String(),
				TickSim:            tickSim,
				IntervalSim:        w.opts.PowerSpotIntervalSim,
				ClosingPrice:       strconv.FormatInt(res.ClosingPrice, 10),
				DemandUnits:        strconv.FormatInt(res.DemandUnits, 10),
				SuppliedUnits:      strconv.FormatInt(res.SuppliedUnits, 10),
				CurtailedUnits:     strconv.FormatInt(res.CurtailedUnits, 10),
				CurtailedBuildings: len(res.Unserved),
			})
	})
	if err != nil || oc == nil {
		return err
	}
	// Métricas y log SOLO tras el commit (un reintento de serialización
	// re-ejecuta el cuerpo).
	w.metrics.setPowerSpotPrice(region.Name, oc.closing)
	w.metrics.addPowerSupplied(region.Name, oc.supplied)
	w.metrics.addPowerCurtailed(region.Name, oc.curtailedUnits, oc.curtailedBuildings)
	for code, units := range oc.fuelBurned {
		w.metrics.addPowerFuelBurned(code, units)
	}
	if oc.demand > 0 || oc.supplied > 0 {
		w.logger.Info("balancer: tick del spot liquidado",
			slog.String("region", region.Name),
			slog.Int64("tick_sim", tickSim),
			slog.Int64("closing_price", oc.closing),
			slog.Int64("demand_units", oc.demand),
			slog.Int64("supplied_units", oc.supplied),
			slog.Int64("curtailed_units", oc.curtailedUnits),
			slog.Int("curtailed_buildings", oc.curtailedBuildings))
	}
	return nil
}

// powerCapacityForLevel aplica el multiplicador de capacidad del nivel
// (level_curve.capacity_mult, indexado nivel-1; default = el propio nivel,
// coherente con speed/storage de v1.3).
func powerCapacityForLevel(base int64, levelCurve []byte, level int32) int64 {
	mult := float64(level)
	if len(levelCurve) > 0 {
		var lc struct {
			CapacityMult []float64 `json:"capacity_mult"`
		}
		if err := json.Unmarshal(levelCurve, &lc); err == nil {
			idx := int(level) - 1
			if idx >= 0 && idx < len(lc.CapacityMult) && lc.CapacityMult[idx] > 0 {
				mult = lc.CapacityMult[idx]
			}
		}
	}
	out := int64(float64(base) * mult)
	if out < 0 {
		return 0
	}
	return out
}
