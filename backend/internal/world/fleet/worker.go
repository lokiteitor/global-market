package fleet

import (
	"context"
	crand "crypto/rand"
	"errors"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Etiquetas de la métrica de duración por barrido.
const (
	sweepTransit    = "transit"
	sweepRecovery   = "recovery"
	sweepCongestion = "congestion"
	sweepTransship  = "transship"
)

// TransitWorker es el MOTOR DE TRÁNSITO event-driven (Incremento 3, Fase 1). Lo
// arranca el engine. Barre los segmentos vencidos (combustible, desgaste, avería
// probabilística, avance segmento/leg, llegada y entrega física), reanuda
// averías/mantenimiento y recalcula la congestión por segmento. Cada vehículo se
// procesa en SU transacción SERIALIZABLE, bloqueado con FOR UPDATE SKIP LOCKED,
// de modo que varias instancias corren en paralelo sin pisarse.
type TransitWorker struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   WorkerOptions
	logger *slog.Logger
	roll   func() float64

	inTransit      prometheus.Gauge
	delivered      prometheus.Counter
	transshipped   prometheus.Counter
	priorityServed prometheus.Counter
	fifoServed     prometheus.Counter
	breakdowns     prometheus.Counter
	arrivals       prometheus.Counter
	stranded       prometheus.Counter
	sweepDuration  *prometheus.HistogramVec
	congestion     *prometheus.GaugeVec
}

// NewTransitWorker construye el motor sobre el pool compartido. reg registra sus
// métricas (nil las deja sin instrumentar: tests); logger nil usa slog.Default.
func NewTransitWorker(pool *pgxpool.Pool, sim SimSource, opts WorkerOptions, logger *slog.Logger, reg prometheus.Registerer) (*TransitWorker, error) {
	if pool == nil {
		return nil, errors.New("world/fleet: el motor requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("world/fleet: el motor requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	roll := opts.Roll
	if roll == nil {
		roll = cryptoRoll
	}
	w := &TransitWorker{
		pool:   pool,
		repo:   NewRepo(pool),
		sim:    sim,
		opts:   opts,
		logger: logger,
		roll:   roll,
		inTransit: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_vehicles_in_transit",
			Help: "Vehículos actualmente en tránsito.",
		}),
		delivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipments_delivered_total",
			Help: "Total de cargamentos entregados físicamente en su nodo destino.",
		}),
		transshipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipment_transshipments_total",
			Help: "Total de transbordos de cargamentos en terminales intermodales (rutas multimodales).",
		}),
		priorityServed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_transshipment_priority_served_total",
			Help: "Total de transbordos servidos con PRIORIDAD por un slot de terminal vigente (GDD 7.3).",
		}),
		fifoServed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_transshipment_fifo_served_total",
			Help: "Total de transbordos servidos en orden FIFO (sin slot de prioridad vigente en la terminal).",
		}),
		breakdowns: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_vehicle_breakdowns_total",
			Help: "Total de averías de vehículos en tránsito.",
		}),
		arrivals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_transit_arrivals_total",
			Help: "Total de llegadas de vehículos a su nodo destino final.",
		}),
		stranded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_vehicles_stranded_total",
			Help: "Total de vehículos detenidos por falta de combustible (defensivo).",
		}),
		sweepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ii_transit_sweep_duration_seconds",
			Help:    "Duración de cada barrido del motor de tránsito, por tipo.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sweep"}),
		congestion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_segment_congestion",
			Help: "Factor de congestión (EMA) por segmento (1.0 = fluido).",
		}, []string{"segment"}),
	}
	if reg != nil {
		reg.MustRegister(w.inTransit, w.delivered, w.transshipped, w.priorityServed, w.fifoServed, w.breakdowns, w.arrivals, w.stranded, w.sweepDuration, w.congestion)
	}
	return w, nil
}

// Run ejecuta el bucle del motor hasta que ctx se cancele (nil al apagado
// limpio). Cada iteración barre tránsito y recuperación; la congestión se dispara
// cuando su intervalo propio venció.
func (w *TransitWorker) Run(ctx context.Context) error {
	w.logger.Info("world/fleet: motor de tránsito iniciado",
		slog.Duration("sweep_interval", w.opts.SweepInterval),
		slog.Int("batch_size", w.opts.BatchSize),
		slog.Int64("repair_sim_seconds", w.opts.RepairSimSeconds),
		slog.Duration("congestion_interval", w.opts.CongestionInterval),
		slog.Float64("congestion_capacity_ref", w.opts.CongestionCapacityRef))
	lastCongestion := time.Now()
	w.updateInTransitGauge(ctx)
	for {
		w.RunOnce(ctx)
		if time.Since(lastCongestion) >= w.opts.CongestionInterval {
			w.runSweep(ctx, sweepCongestion, func(ctx context.Context) (int, error) { return w.RunCongestionOnce(ctx) })
			lastCongestion = time.Now()
		}
		w.updateInTransitGauge(ctx)
		if !sleepJitter(ctx, w.opts.SweepInterval) {
			w.logger.Info("world/fleet: motor de tránsito detenido")
			return nil
		}
	}
}

// RunOnce ejecuta una pasada de los barridos de tránsito, recuperación y servicio
// de las colas de transbordo (en ese orden: el tránsito encola en la terminal y el
// servicio de cola la sirve en la misma pasada). Aislado para los tests, que
// controlan el disparo.
func (w *TransitWorker) RunOnce(ctx context.Context) {
	w.runSweep(ctx, sweepTransit, w.sweepTransit)
	w.runSweep(ctx, sweepRecovery, w.sweepRecovery)
	w.runSweep(ctx, sweepTransship, w.sweepTransship)
}

// sweepTransship sirve las colas de transbordo y adapta la firma a runSweep
// (devuelve el total de cargamentos servidos).
func (w *TransitWorker) sweepTransship(ctx context.Context) (int, error) {
	prio, fifo, err := w.RunTransshipOnce(ctx)
	return prio + fifo, err
}

// runSweep cronometra un barrido y registra su duración y un error global.
func (w *TransitWorker) runSweep(ctx context.Context, name string, fn func(context.Context) (int, error)) {
	start := time.Now()
	n, err := fn(ctx)
	w.sweepDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	if err != nil {
		w.logger.Warn("world/fleet: barrido con error al listar candidatos",
			slog.String("sweep", name), slog.Any("error", err))
		return
	}
	if n > 0 {
		w.logger.Debug("world/fleet: barrido completado", slog.String("sweep", name), slog.Int("procesados", n))
	}
}

// ─── (1) Barrido de segmentos vencidos ────────────────────────────────────────

// sweepTransit procesa los vehículos in_transit cuyo segmento venció.
func (w *TransitWorker) sweepTransit(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	ids, err := w.repo.ListDueTransitVehicleIDs(ctx, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		if err := w.processTransitVehicle(ctx, id); err != nil {
			w.logger.Warn("world/fleet: fallo procesando un vehículo en tránsito",
				slog.String("vehicle_id", id.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

// transitOutcome acumula los efectos de procesar un vehículo para volcarlos a las
// métricas UNA sola vez tras el COMMIT.
type transitOutcome struct {
	broke        bool
	arrived      bool
	stranded     bool
	delivered    int
	transshipped int
}

// processTransitVehicle bloquea y procesa un vehículo en su propia transacción:
// consume combustible/desgaste, decide avería/detención, y avanza al siguiente
// segmento/leg o llega y entrega.
func (w *TransitWorker) processTransitVehicle(ctx context.Context, id uuid.UUID) error {
	var oc transitOutcome
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		oc = transitOutcome{}
		r := w.repo.WithTx(tx)
		tv, err := r.LockTransitVehicle(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // tomado por otra instancia o ya no in_transit
			}
			return err
		}
		simNow := w.sim.Now(ctx)

		// (1) Combustible del segmento actual. Sin combustible: detención defensiva
		//     en el nodo previo (el combustible se validó para toda la ruta al
		//     despachar, GDD 7.3).
		fuelNeeded := fuelForDistance(tv.FuelPer100km, int64(tv.SegmentLengthM))
		if tv.Fuel < fuelNeeded {
			if err := r.StrandVehicle(ctx, id, tv.FromNodeID, simNow); err != nil {
				return err
			}
			oc.stranded = true
			return outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, id, EventVehicleStranded, VehicleStrandedPayload{
				VehicleID: id.String(), OwnerAccountID: tv.OwnerAccountID.String(), NodeID: tv.FromNodeID.String(), StrandedAtSim: int64(simNow),
			})
		}
		newFuel := tv.Fuel - fuelNeeded

		// (2) Desgaste (acotado a 100).
		newWear := tv.WearPct + w.opts.WearPerSegment
		if newWear > 100 {
			newWear = 100
		}

		// (3) Avería probabilística (p = wear_pct/1000 por segmento). La carga
		//     espera a bordo; el barrido reanuda al vencer la reparación (GDD 7.3).
		if w.roll() < float64(newWear)/1000.0 {
			repairUntil := simNow + simtime.SimTime(w.opts.RepairSimSeconds)
			if err := r.BreakVehicle(ctx, id, repairUntil, newFuel, newWear, simNow); err != nil {
				return err
			}
			oc.broke = true
			segID := ""
			if tv.OnSegmentID != nil {
				segID = tv.OnSegmentID.String()
			}
			return outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, id, EventVehicleBroken, VehicleBrokenPayload{
				VehicleID: id.String(), OwnerAccountID: tv.OwnerAccountID.String(), SegmentID: segID,
				RepairUntilSim: int64(repairUntil), BrokenAtSim: int64(simNow),
			})
		}

		// (4) Avance: siguiente segmento del mismo enlace, o del siguiente leg, o
		//     llegada al nodo destino final.
		if next, err := r.GetNextSegmentInLink(ctx, tv.LinkID, tv.SegmentSeq); err == nil {
			return w.advance(ctx, r, id, next.SegmentID, legIndexOr0(tv.RouteLegIndex),
				af(tv.SpeedKmh, next.BaseSpeedKmh, next.CongestionEma, next.LengthM), newFuel, newWear, simNow)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if tv.RouteID != nil && tv.RouteLegIndex != nil {
			if leg, err := r.GetNextLegFirstSegment(ctx, *tv.RouteID, *tv.RouteLegIndex+1); err == nil {
				return w.advance(ctx, r, id, leg.SegmentID, leg.LegIndex,
					af(tv.SpeedKmh, leg.BaseSpeedKmh, leg.CongestionEma, leg.LengthM), newFuel, newWear, simNow)
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		return w.arriveAndDeliver(ctx, r, tx, tv, newFuel, newWear, simNow, &oc)
	})
	if err == nil {
		w.flush(&oc)
	}
	return err
}

// advance mueve el vehículo al siguiente segmento (mismo leg o siguiente),
// guardando combustible/desgaste.
func (w *TransitWorker) advance(ctx context.Context, r *Repo, id, segment uuid.UUID, legIndex int32, a advanceFn, fuel int64, wear int32, simNow simtime.SimTime) error {
	bytes, err := a.marshal()
	if err != nil {
		return err
	}
	return r.AdvanceVehicleToSegment(ctx, advanceParams{
		ID: id, OnSegment: segment, LegIndex: legIndex, AdvanceFn: bytes, Fuel: fuel, WearPct: wear, SimNow: simNow,
	})
}

// arriveAndDeliver asienta la llegada al nodo destino final, emite
// vehicle.arrived y entrega los cargamentos a bordo con destino ese nodo
// (integrando su stock físico en el almacén del destino y emitiendo
// shipment.arrived, hito que el Contract Service consume).
func (w *TransitWorker) arriveAndDeliver(ctx context.Context, r *Repo, tx pgx.Tx, tv transitVehicle, fuel int64, wear int32, simNow simtime.SimTime, oc *transitOutcome) error {
	node := tv.ToNodeID
	if err := r.ArriveVehicle(ctx, tv.ID, node, fuel, wear, simNow); err != nil {
		return err
	}
	oc.arrived = true
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, tv.ID, EventVehicleArrived, VehicleArrivedPayload{
		VehicleID: tv.ID.String(), OwnerAccountID: tv.OwnerAccountID.String(), NodeID: node.String(), ArrivedAtSim: int64(simNow),
	}); err != nil {
		return err
	}

	deliveries, err := r.ListVehicleShipmentsForNode(ctx, tv.ID, node)
	if err != nil {
		return err
	}
	if len(deliveries) > 0 {
		// El almacén del nodo destino (network_nodes.building_id) recibe el stock
		// físico entregado; la propiedad contable la resuelve el settle del CCRI.
		nodeInfo, err := r.GetNode(ctx, node)
		if err != nil {
			return err
		}
		for _, sh := range deliveries {
			if err := r.DeliverShipment(ctx, sh.ID, node, simNow); err != nil {
				return err
			}
			if nodeInfo.BuildingID != nil {
				if err := r.AddInventory(ctx, *nodeInfo.BuildingID, sh.ProductID, sh.Quantity, simNow); err != nil {
					return err
				}
			} else {
				w.logger.Warn("world/fleet: nodo destino sin almacén; entrega sin integrar inventario físico",
					slog.String("node_id", node.String()), slog.String("shipment_id", sh.ID.String()))
			}
			if err := outbox.Emit(ctx, tx, int64(simNow), AggregateShipment, sh.ID, EventShipmentArrived, ShipmentArrivedPayload{
				ShipmentID: sh.ID.String(), ContractID: uuidOrEmpty(sh.ContractID), FreightContractID: uuidOrEmpty(sh.FreightContractID),
				Quantity: fixed(sh.Quantity), DestinationNodeID: node.String(), ArrivedAtSim: int64(simNow),
			}); err != nil {
				return err
			}
			oc.delivered++
		}
	}
	// Transbordo: la carga a bordo con destino MÁS ALLÁ de este nodo se ENCOLA en la
	// terminal intermodal (at_terminal) a la espera de ser servida por la cola de
	// transbordo (barrido sweepTransship), que fija su fin de servicio según su
	// prioridad de slot y su posición (GDD 7.3).
	return w.transshipAtTerminal(ctx, r, tx, tv.ID, node, simNow, oc)
}

// transshipAtTerminal ENCOLA en la terminal (at_terminal) los cargamentos a bordo
// cuyo destino no es el nodo de llegada: es el punto de cambio de modo de una ruta
// multimodal (GDD 7.3). El siguiente tramo lo despacha el jugador/bot en un vehículo
// del siguiente modo, tras el tiempo de transbordo que el servicio de cola le asigna
// (prioridad por slot; ver RunTransshipOnce). Si el nodo no tiene terminal, esa
// carga no debería estar ahí (el despacho lo previene): se avisa y se deja a bordo
// del vehículo idle.
func (w *TransitWorker) transshipAtTerminal(ctx context.Context, r *Repo, tx pgx.Tx, vehicleID, node uuid.UUID, simNow simtime.SimTime, oc *transitOutcome) error {
	candidates, err := r.ListVehicleShipmentsToTransship(ctx, vehicleID, node)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return nil
	}
	term, err := r.GetTerminalByNode(ctx, node)
	if errors.Is(err, pgx.ErrNoRows) {
		w.logger.Warn("world/fleet: fin de tramo en nodo sin terminal con carga de otro destino a bordo; queda a bordo",
			slog.String("node_id", node.String()), slog.String("vehicle_id", vehicleID.String()),
			slog.Int("cargamentos", len(candidates)))
		return nil
	}
	if err != nil {
		return err
	}
	for _, c := range candidates {
		// Encola el cargamento (at_terminal, sin servir): el servicio de la cola le
		// asignará el fin de transbordo. El payload informa el tiempo BASE de
		// transbordo (sin cola); la posición real la resuelve sweepTransship.
		if err := r.TransshipShipment(ctx, c.ID, node, simNow); err != nil {
			return err
		}
		vol, verr := requiredVolume(c.Quantity, w.unitVolumeOr1(ctx, r, c.ProductID))
		if verr != nil {
			vol = c.Quantity
		}
		if err := outbox.Emit(ctx, tx, int64(simNow), AggregateShipment, c.ID, EventShipmentAtTerminal, ShipmentAtTerminalPayload{
			ShipmentID: c.ID.String(), ContractID: uuidOrEmpty(c.ContractID), Quantity: fixed(c.Quantity),
			TerminalID: term.ID.String(), TerminalNodeID: node.String(), DestinationNodeID: uuidOrEmpty(c.Destination),
			TransshipmentSeconds: transshipmentSeconds(vol, term.TransshipmentPerHour), AtTerminalAtSim: int64(simNow),
		}); err != nil {
			return err
		}
		oc.transshipped++
	}
	return nil
}

// ─── Servicio de las colas de transbordo con prioridad de slots (GDD 7.3) ──────

// RunTransshipOnce sirve UNA vez las colas de transbordo de todas las terminales con
// carga encolada. Para cada terminal, en su propia transacción SERIALIZABLE, ordena
// la cola por PRIORIDAD (dueños con slot vigente primero, por priority_tier
// ascendente; el resto FIFO por orden de llegada) y asigna a cada cargamento su
// instante de fin de transbordo con un modelo de SERVIDOR ÚNICO a la tasa
// transshipment_per_hour de la terminal: el primero en la cola termina antes, y los
// siguientes se acumulan detrás. Así un cargamento con slot de tier menor se sirve
// (queda listo) ANTES que uno sin slot que llegó al mismo tiempo. Devuelve
// (servidos_con_prioridad, servidos_fifo) e incrementa
// ii_transshipment_priority_served_total / ii_transshipment_fifo_served_total.
// Aislado para los tests, que controlan el disparo.
func (w *TransitWorker) RunTransshipOnce(ctx context.Context) (int, int, error) {
	terms, err := w.repo.ListTerminalsWithPendingTransship(ctx, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, 0, err
	}
	totalPrio, totalFifo := 0, 0
	for _, t := range terms {
		prio, fifo, serr := w.serveTerminalQueue(ctx, t)
		if serr != nil {
			w.logger.Warn("world/fleet: fallo sirviendo la cola de transbordo de una terminal",
				slog.String("terminal_id", t.ID.String()), slog.Any("error", serr))
			continue
		}
		totalPrio += prio
		totalFifo += fifo
	}
	if totalPrio > 0 {
		w.priorityServed.Add(float64(totalPrio))
	}
	if totalFifo > 0 {
		w.fifoServed.Add(float64(totalFifo))
	}
	return totalPrio, totalFifo, nil
}

// serveTerminalQueue sirve la cola de UNA terminal en su propia transacción,
// bloqueando la terminal para serializar el servicio entre instancias del motor.
// Devuelve (servidos_con_prioridad, servidos_fifo).
func (w *TransitWorker) serveTerminalQueue(ctx context.Context, t pendingTerminal) (int, int, error) {
	var prio, fifo int
	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		prio, fifo = 0, 0
		r := w.repo.WithTx(tx)
		term, err := r.LockTerminalForServe(ctx, t.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // la terminal ya no existe (tomada por otra instancia)
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		queue, err := r.ListTerminalTransshipQueue(ctx, term.ID, term.NodeID, simNow)
		if err != nil {
			return err
		}
		if len(queue) == 0 {
			return nil // otra instancia la sirvió entre el listado y el lock
		}
		// freeAt = cuándo queda libre el servidor de la terminal (mayor fin de
		// servicio futuro ya asignado). Sobre él se acumula la cola (servidor único).
		freeAt, err := r.TerminalServerBusyUntil(ctx, term.NodeID, simNow)
		if err != nil {
			return err
		}
		for _, q := range queue {
			// Servidor único: el servicio arranca cuando el servidor queda libre, nunca
			// antes de que el cargamento llegara a la terminal (arrival). El fin de
			// servicio = inicio + tiempo de transbordo del volumen a la tasa de la
			// terminal.
			start := q.ArrivalSim
			if freeAt > start {
				start = freeAt
			}
			readyAt := start + transshipmentSeconds(q.Volume, term.TransshipmentPerHour)
			if err := r.SetShipmentTransshipReady(ctx, q.ID, readyAt); err != nil {
				return err
			}
			freeAt = readyAt
			if q.SlotTier < noSlotTier {
				prio++
			} else {
				fifo++
			}
		}
		return r.RecountTerminalQueue(ctx, term.ID, simNow)
	})
	if err != nil {
		return 0, 0, err
	}
	return prio, fifo, nil
}

// unitVolumeOr1 devuelve el volumen por unidad de un producto (1 si la consulta
// falla: la cantidad domina el cálculo informativo del tiempo de transbordo).
func (w *TransitWorker) unitVolumeOr1(ctx context.Context, r *Repo, product uuid.UUID) int32 {
	uv, err := r.GetProductUnitVolume(ctx, product)
	if err != nil || uv <= 0 {
		return 1
	}
	return uv
}

// flush vuelca los efectos acumulados a las métricas tras el COMMIT.
func (w *TransitWorker) flush(oc *transitOutcome) {
	if oc.broke {
		w.breakdowns.Inc()
	}
	if oc.arrived {
		w.arrivals.Inc()
	}
	if oc.stranded {
		w.stranded.Inc()
	}
	if oc.delivered > 0 {
		w.delivered.Add(float64(oc.delivered))
	}
	if oc.transshipped > 0 {
		w.transshipped.Add(float64(oc.transshipped))
	}
}

// ─── (2) Reanudación de averías y mantenimiento ───────────────────────────────

// sweepRecovery reanuda los vehículos broken/in_maintenance cuya recuperación
// venció.
func (w *TransitWorker) sweepRecovery(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	rows, err := w.repo.ListDueRecoveryVehicleIDs(ctx, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, row := range rows {
		if err := w.processRecovery(ctx, row.ID); err != nil {
			w.logger.Warn("world/fleet: fallo reanudando un vehículo",
				slog.String("vehicle_id", row.ID.String()), slog.Any("error", err))
			continue
		}
		processed++
	}
	return processed, nil
}

// processRecovery reanuda un vehículo: broken → in_transit re-entrando al mismo
// segmento; in_maintenance → idle.
func (w *TransitWorker) processRecovery(ctx context.Context, id uuid.UUID) error {
	return db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		r := w.repo.WithTx(tx)
		rv, err := r.LockRecoveryVehicle(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		simNow := w.sim.Now(ctx)
		switch rv.Status {
		case string(sqlcgen.WorldVehicleStatusBroken):
			return r.ResumeBrokenVehicle(ctx, id, simNow)
		case string(sqlcgen.WorldVehicleStatusInMaintenance):
			return r.FinishMaintenanceVehicle(ctx, id, simNow)
		default:
			return nil
		}
	})
}

// ─── (3) Congestión (job periódico) ───────────────────────────────────────────

// RunCongestionOnce recalcula la EMA de congestión de todos los segmentos y
// publica el gauge por segmento. Devuelve el número de segmentos actualizados.
func (w *TransitWorker) RunCongestionOnce(ctx context.Context) (int, error) {
	simNow := w.sim.Now(ctx)
	rows, err := w.repo.RecomputeSegmentCongestion(ctx, w.opts.CongestionCapacityRef, simNow)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		w.congestion.WithLabelValues(row.ID.String()).Set(row.CongestionEma)
	}
	return len(rows), nil
}

// updateInTransitGauge publica el número de vehículos en tránsito.
func (w *TransitWorker) updateInTransitGauge(ctx context.Context) {
	n, err := w.repo.CountInTransitVehicles(ctx)
	if err != nil {
		w.logger.Debug("world/fleet: no se pudo contar vehículos en tránsito", slog.Any("error", err))
		return
	}
	w.inTransit.Set(float64(n))
}

// ─── Utilidades ───────────────────────────────────────────────────────────────

// af construye la advance_fn de un segmento: velocidad efectiva = min(velocidad
// del vehículo, velocidad base del enlace); congestión snapshot; longitud; dir=1
// (los legs recorren el enlace en su orientación from_node→to_node).
func af(vehicleSpeed, linkSpeed int32, congestion float64, lengthM int32) advanceFn {
	return advanceFn{BaseSpeedKmh: minInt32(vehicleSpeed, linkSpeed), CongestionEma: congestion, LengthM: lengthM, Dir: 1}
}

func legIndexOr0(idx *int32) int32 {
	if idx == nil {
		return 0
	}
	return *idx
}

// cryptoRoll produce un número uniforme en [0,1) con crypto/rand. Ante un fallo
// de entropía falla cerrado (1.0 → nunca avería), preservando la carga.
func cryptoRoll() float64 {
	const scale = int64(1) << 53
	n, err := crand.Int(crand.Reader, big.NewInt(scale))
	if err != nil {
		return 1.0
	}
	return float64(n.Int64()) / float64(scale)
}

// sleepJitter espera d ± hasta 25% de jitter y devuelve false si el contexto se
// cancela antes (desincroniza instancias concurrentes del motor).
func sleepJitter(ctx context.Context, d time.Duration) bool {
	jitter := time.Duration(0)
	if d > 0 {
		span := int64(d / 4)
		if span > 0 {
			nBig, err := crand.Int(crand.Reader, big.NewInt(2*span+1))
			if err == nil {
				jitter = time.Duration(nBig.Int64() - span)
			}
		}
	}
	t := time.NewTimer(d + jitter)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
