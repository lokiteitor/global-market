package contracts

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// EnvSweepInterval es el periodo del barrido de los tres sweeps del worker
// (resolución de sorteo, expiración de TTL, liquidación por vencimiento), en
// formato time.ParseDuration. Default II_CONTRACTS_SWEEP_INTERVAL=2s.
const EnvSweepInterval = "II_CONTRACTS_SWEEP_INTERVAL"

// EnvSweepBatchSize acota cuántas publicaciones/contratos procesa cada sweep
// por iteración (cada uno en su propia transacción). Default 100.
const EnvSweepBatchSize = "II_CONTRACTS_SWEEP_BATCH_SIZE"

// Defaults documentados del worker.
const (
	DefaultSweepInterval  = 2 * time.Second
	DefaultSweepBatchSize = 100
)

// Etiquetas de la métrica de duración por sweep.
const (
	sweepDraw   = "draw"
	sweepExpire = "expire"
	sweepSettle = "settle"
)

// maxSettleEntries es el máximo de partidas que asienta
// ledger.settle_contract_prorata (6 del tramo entregado + 7 del faltante): se
// pre-generan siempre para cubrir cualquier fill; la función usa solo las que
// necesita según lo entregado.
const maxSettleEntries = 13

// confirmEntries es el número fijo de partidas de ledger.confirm_contract.
const confirmEntries = 6

// WorkerOptions es la configuración del worker de barridos. Se separa de
// Options (configuración del servicio) porque su periodo es wall-clock, no una
// invariante de dominio.
type WorkerOptions struct {
	// SweepInterval es el periodo entre barridos (con jitter). > 0.
	SweepInterval time.Duration
	// BatchSize es el máximo de elementos que cada sweep toma por iteración. > 0.
	BatchSize int
}

// DefaultWorkerOptions devuelve la configuración por defecto del worker.
func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{SweepInterval: DefaultSweepInterval, BatchSize: DefaultSweepBatchSize}
}

// WorkerOptionsFromEnv construye las opciones del worker desde el entorno; un
// valor inválido devuelve error (la configuración rota impide el arranque).
func WorkerOptionsFromEnv() (WorkerOptions, error) {
	opts := DefaultWorkerOptions()
	if v := strings.TrimSpace(os.Getenv(EnvSweepInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("contracts: %s inválido %q (formato de time.ParseDuration): %w", EnvSweepInterval, v, err)
		}
		opts.SweepInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvSweepBatchSize)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("contracts: %s inválido %q (entero): %w", EnvSweepBatchSize, v, err)
		}
		opts.BatchSize = n
	}
	if err := opts.Validate(); err != nil {
		return WorkerOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del worker.
func (o WorkerOptions) Validate() error {
	if o.SweepInterval <= 0 {
		return fmt.Errorf("contracts: %s debe ser una duración positiva (actual %s)", EnvSweepInterval, o.SweepInterval)
	}
	if o.BatchSize <= 0 {
		return fmt.Errorf("contracts: %s debe ser > 0 (actual %d)", EnvSweepBatchSize, o.BatchSize)
	}
	return nil
}

// Worker ejecuta los tres barridos periódicos del ciclo CCRI (ADR-011, GDD
// 5.3): resolución de sorteo al cerrar las ventanas, expiración de
// publicaciones por TTL de sim-time y liquidación de contratos al vencer el
// plazo. Cada publicación/contrato se procesa en SU PROPIA transacción
// SERIALIZABLE, bloqueado con FOR UPDATE SKIP LOCKED, de modo que varias
// instancias del worker pueden correr en paralelo sin pisarse ni bloquearse.
type Worker struct {
	svc    *Service
	opts   WorkerOptions
	logger *slog.Logger

	drawsResolved       prometheus.Counter
	contractsConfirmed  prometheus.Counter
	contractsSettled    *prometheus.CounterVec
	publicationsExpired prometheus.Counter
	sweepDuration       *prometheus.HistogramVec
}

// NewWorker construye el worker sobre un Service ya validado. reg registra sus
// métricas (nil las deja sin instrumentar: tests); logger nil usa el del
// servicio. Options inválidas devuelven error.
func NewWorker(svc *Service, opts WorkerOptions, logger *slog.Logger, reg prometheus.Registerer) (*Worker, error) {
	if svc == nil {
		return nil, errors.New("contracts: el worker requiere un Service")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = svc.logger
	}
	w := &Worker{
		svc:    svc,
		opts:   opts,
		logger: logger,
		drawsResolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_draws_resolved_total",
			Help: "Total de sorteos de publicaciones resueltos al cerrar su ventana.",
		}),
		contractsConfirmed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_contracts_confirmed_total",
			Help: "Total de contratos CCRI confirmados (bloqueo triple asentado).",
		}),
		contractsSettled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_contracts_settled_total",
			Help: "Total de contratos CCRI liquidados, por estado final (settled|failed).",
		}, []string{"status"}),
		publicationsExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_publications_expired_total",
			Help: "Total de publicaciones abiertas expiradas por TTL de sim-time.",
		}),
		sweepDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ii_contracts_sweep_duration_seconds",
			Help:    "Duración de cada barrido del worker de contratos, por tipo de sweep.",
			Buckets: prometheus.DefBuckets,
		}, []string{"sweep"}),
	}
	if reg != nil {
		reg.MustRegister(w.drawsResolved, w.contractsConfirmed, w.contractsSettled,
			w.publicationsExpired, w.sweepDuration)
	}
	return w, nil
}

// Run ejecuta el bucle de barridos hasta que ctx se cancele (devuelve nil al
// apagado limpio). Cada iteración corre los tres sweeps y espera el periodo
// configurado con jitter (desincroniza instancias concurrentes).
func (w *Worker) Run(ctx context.Context) error {
	w.logger.Info("contracts: worker de barridos iniciado",
		slog.Duration("interval", w.opts.SweepInterval),
		slog.Int("batch_size", w.opts.BatchSize))
	for {
		w.RunOnce(ctx)
		if !sleepJitter(ctx, w.opts.SweepInterval) {
			w.logger.Info("contracts: worker de barridos detenido")
			return nil
		}
	}
}

// RunOnce ejecuta una pasada de los tres barridos. Aislado para los tests, que
// controlan el disparo. Los errores por elemento se registran y no abortan el
// resto (aislamiento por transacción propia).
func (w *Worker) RunOnce(ctx context.Context) {
	w.runSweep(ctx, sweepDraw, w.resolveDraws)
	w.runSweep(ctx, sweepExpire, w.expirePublications)
	w.runSweep(ctx, sweepSettle, w.settleDueContracts)
}

// runSweep cronometra un sweep y registra su duración y un error global (el que
// impide obtener candidatos; los errores por elemento se registran dentro).
func (w *Worker) runSweep(ctx context.Context, name string, fn func(context.Context) (int, error)) {
	start := time.Now()
	n, err := fn(ctx)
	w.sweepDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
	if err != nil {
		w.logger.Warn("contracts: sweep con error al listar candidatos",
			slog.String("sweep", name), slog.Any("error", err))
		return
	}
	if n > 0 {
		w.logger.Debug("contracts: sweep completado",
			slog.String("sweep", name), slog.Int("procesados", n))
	}
}

// sweepCounts acumula, dentro de una transacción, los contratos confirmados y
// liquidados (por estado) para incrementar las métricas UNA sola vez tras el
// COMMIT: un reintento de serialización re-ejecuta el cuerpo, pero solo el
// commit definitivo cuenta (evita el doble conteo de los reintentos).
type sweepCounts struct {
	confirmed int
	settled   map[string]int
}

func newSweepCounts() *sweepCounts { return &sweepCounts{settled: map[string]int{}} }

// flush vuelca los recuentos acumulados a las métricas del worker.
func (w *Worker) flush(c *sweepCounts) {
	if c.confirmed > 0 {
		w.contractsConfirmed.Add(float64(c.confirmed))
	}
	for status, n := range c.settled {
		w.contractsSettled.WithLabelValues(status).Add(float64(n))
	}
}

// ─── (a) Resolución de sorteo ────────────────────────────────────────────────

// resolveDraws localiza las publicaciones con la ventana vencida y resuelve el
// sorteo de cada una en su propia transacción.
func (w *Worker) resolveDraws(ctx context.Context) (int, error) {
	ids, err := w.svc.repo.ListDueDrawPublicationIDs(ctx, int32(w.opts.BatchSize)) //nolint:gosec // BatchSize > 0 acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		done, err := w.resolveOneDraw(ctx, id)
		if err != nil {
			w.logger.Warn("contracts: fallo resolviendo el sorteo de una publicación",
				slog.String("publication_id", id.String()), slog.Any("error", err))
			continue
		}
		if done {
			processed++
			w.drawsResolved.Inc()
		}
	}
	return processed, nil
}

// resolveOneDraw resuelve el sorteo de una publicación: baraja las aceptaciones
// pending_draw con crypto/rand, las sirve en orden aleatorio hasta agotar la
// cantidad (creando y confirmando un contrato por cada servida), libera las
// garantías no servidas y actualiza la publicación (exhausted u open). Los
// contratos con origen = destino se entregan y liquidan al instante.
func (w *Worker) resolveOneDraw(ctx context.Context, id uuid.UUID) (bool, error) {
	simNow := w.svc.sim.Now(ctx)
	resolved := false
	var counts *sweepCounts
	err := db.RunSerializable(ctx, w.svc.pool, func(tx pgx.Tx) error {
		resolved = false
		counts = newSweepCounts()
		r := w.svc.repo.WithTx(tx)

		p, err := r.LockDueDrawPublication(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil // ya resuelta o tomada por otra instancia
		case err != nil:
			return err
		}

		pending, err := r.ListPendingAcceptancesForUpdate(ctx, p.ID)
		if err != nil {
			return err
		}
		if err := cryptoShuffle(pending); err != nil {
			return err
		}

		remaining := p.QuantityRemaining
		for i, acc := range pending {
			drawOrder := int32(i + 1) //nolint:gosec // i acotado por len(pending)
			served := min(remaining, acc.Quantity)
			if served <= 0 {
				if err := w.releaseUnserved(ctx, r, tx, p, acc, drawOrder, simNow); err != nil {
					return err
				}
				continue
			}
			if err := w.serveAcceptance(ctx, r, tx, p, acc, served, drawOrder, simNow, counts); err != nil {
				return err
			}
			remaining -= served
		}

		// remaining se decrementó por cada aceptación servida: es la cantidad
		// que resta publicada tras el sorteo.
		status := StatusOpen
		if remaining == 0 {
			status = StatusExhausted
		}
		if _, err := r.SetPublicationDrawResult(ctx, p.ID, remaining, status); err != nil {
			return err
		}
		// Al agotarse, la garantía sobrante por redondeo de la publicación (10%
		// entero por contrato) se libera: nada queda inmovilizado sin respaldo.
		if status == StatusExhausted {
			if err := w.svc.releasePublicationCollateral(ctx, r, p, simNow,
				"Sorteo agotó la publicación: garantía sobrante liberada"); err != nil {
				return err
			}
		}
		resolved = true
		return nil
	})
	if err == nil && resolved {
		w.flush(counts)
	}
	return resolved, err
}

// releaseUnserved libera la garantía íntegra de una aceptación no servida
// (sorteo perdido) y la resuelve como released, emitiendo acceptance.resolved.
func (w *Worker) releaseUnserved(ctx context.Context, r *Repo, tx pgx.Tx, p Publication, acc Acceptance, drawOrder int32, simNow simtime.SimTime) error {
	if err := w.svc.releaseAcceptanceCollateral(ctx, r, acc, simNow); err != nil {
		return err
	}
	released, err := r.ReleaseAcceptance(ctx, acc.ID, drawOrder)
	if err != nil {
		return err
	}
	return outbox.Emit(ctx, tx, int64(simNow), AggregateAcceptance, acc.ID, EventAcceptanceResolved, AcceptanceResolvedPayload{
		AcceptanceID:      acc.ID.String(),
		PublicationID:     p.ID.String(),
		AcceptorAccountID: acc.AcceptorAccountID.String(),
		Status:            string(released.Status),
		QuantityServed:    fixed(released.QuantityServed),
		ResolvedAtSim:     int64(simNow),
	})
}

// serveAcceptance crea y confirma el contrato de una aceptación servida, libera
// el remanente no servido de su garantía y —si el contrato es in situ (origen =
// destino)— lo entrega y liquida al instante. Emite acceptance.resolved,
// contract.confirmed y, cuando aplica, contract.delivered y contract.settled.
func (w *Worker) serveAcceptance(ctx context.Context, r *Repo, tx pgx.Tx, p Publication, acc Acceptance, served int64, drawOrder int32, simNow simtime.SimTime, counts *sweepCounts) error {
	contract, err := w.confirmContract(ctx, r, p, acc, served, simNow)
	if err != nil {
		return err
	}
	// Resolver la aceptación como servida y liberar su remanente no servido
	// (lo que confirm_contract dejó en sus cuentas espejo).
	if _, err := r.ServeAcceptance(ctx, acc.ID, served, drawOrder); err != nil {
		return err
	}
	if err := w.svc.releaseAcceptanceCollateral(ctx, r, acc, simNow); err != nil {
		return err
	}
	counts.confirmed++

	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateAcceptance, acc.ID, EventAcceptanceResolved, AcceptanceResolvedPayload{
		AcceptanceID:      acc.ID.String(),
		PublicationID:     p.ID.String(),
		AcceptorAccountID: acc.AcceptorAccountID.String(),
		Status:            string(AcceptanceServed),
		QuantityServed:    fixed(served),
		ContractID:        contract.ID.String(),
		ResolvedAtSim:     int64(simNow),
	}); err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateContract, contract.ID, EventContractConfirmed, ContractConfirmedPayload{
		ContractID:        contract.ID.String(),
		PublicationID:     uuidOrEmpty(contract.PublicationID),
		Channel:           string(contract.Channel),
		BuyerAccountID:    contract.BuyerAccountID.String(),
		SellerAccountID:   contract.SellerAccountID.String(),
		ProductID:         contract.ProductID.String(),
		QuantityAgreed:    fixed(contract.QuantityAgreed),
		UnitPrice:         fixed(contract.UnitPrice),
		OriginNodeID:      contract.OriginNodeID.String(),
		DestinationNodeID: contract.DestinationNodeID.String(),
		DeadlineSim:       int64(contract.DeadlineSim),
		ConfirmedAtSim:    int64(contract.ConfirmedAtSim),
	}); err != nil {
		return err
	}

	// Retirada in situ (origen = destino): entrega y liquidación inmediatas.
	if contract.OriginNodeID == contract.DestinationNodeID {
		return w.deliverAndSettleInSitu(ctx, r, tx, contract, simNow, counts)
	}
	return nil
}

// confirmContract crea las tres cuentas espejo del contrato, inserta la fila y
// asienta el bloqueo triple con ledger.confirm_contract (origen de las
// garantías según el kind de la publicación).
func (w *Worker) confirmContract(ctx context.Context, r *Repo, p Publication, acc Acceptance, served int64, simNow simtime.SimTime) (Contract, error) {
	var (
		buyer, seller        uuid.UUID
		originNode, destNode uuid.UUID
		originBuilding       uuid.UUID
		fromStock, fromGuar  uuid.UUID
		fromEscrow           uuid.UUID
	)
	product := *p.ProductID

	switch p.Kind {
	case KindSell: // publicador = vendedor; aceptante = comprador; entrega in situ
		seller, buyer = p.PublisherAccountID, acc.AcceptorAccountID
		originNode, destNode = *p.OriginNodeID, *p.OriginNodeID
		node, err := w.svc.warehouseNode(ctx, r, originNode, "origen")
		if err != nil {
			return Contract{}, err
		}
		originBuilding = *node.BuildingID
		fromStock, fromGuar, fromEscrow = *p.StockReserveAccountID, *p.GuaranteeAccountID, *acc.EscrowAccountID

	case KindBuy: // publicador = comprador; aceptante = vendedor; origen = su almacén
		buyer, seller = p.PublisherAccountID, acc.AcceptorAccountID
		destNode = *p.DestinationNodeID
		reserved, err := r.GetLedgerAccount(ctx, *acc.StockReserveAccountID)
		if err != nil {
			return Contract{}, fmt.Errorf("contracts: leyendo la reserva de la aceptación %s: %w", acc.ID, err)
		}
		originBuilding = *reserved.WarehouseBuildingID
		node, _, err := r.GetNodeByBuilding(ctx, originBuilding)
		if err != nil {
			return Contract{}, fmt.Errorf("contracts: reconstruyendo el nodo de origen (almacén %s) de la aceptación %s: %w", originBuilding, acc.ID, err)
		}
		originNode = node
		fromStock, fromGuar, fromEscrow = *acc.StockReserveAccountID, *acc.GuaranteeAccountID, *p.EscrowAccountID

	default:
		return Contract{}, fmt.Errorf("%w: kind de publicación no confirmable %q", ErrValidation, p.Kind)
	}

	contractID, err := newUUIDv7()
	if err != nil {
		return Contract{}, err
	}
	toStock, err := r.CreateMirrorAccount(ctx, accountKindStockReserved, seller, &product, &originBuilding, contractID)
	if err != nil {
		return Contract{}, err
	}
	toGuar, err := r.CreateMirrorAccount(ctx, accountKindGuarantee, seller, nil, nil, contractID)
	if err != nil {
		return Contract{}, err
	}
	toEscrow, err := r.CreateMirrorAccount(ctx, accountKindEscrow, buyer, nil, nil, contractID)
	if err != nil {
		return Contract{}, err
	}

	contract, err := r.InsertContract(ctx, insertContractParams{
		ID:                       contractID,
		PublicationID:            &p.ID,
		Channel:                  p.Channel,
		BuyerAccountID:           buyer,
		SellerAccountID:          seller,
		ProductID:                product,
		QuantityAgreed:           served,
		UnitPrice:                p.UnitPrice,
		OriginNodeID:             originNode,
		DestinationNodeID:        destNode,
		DeadlineSim:              simNow + p.DeliverySimSeconds,
		StockReserveAccountID:    toStock.ID,
		SellerGuaranteeAccountID: toGuar.ID,
		EscrowAccountID:          toEscrow.ID,
		ConfirmedAtSim:           simNow,
	})
	if err != nil {
		return Contract{}, err
	}

	txID, err := newUUIDv7()
	if err != nil {
		return Contract{}, err
	}
	entryIDs, err := newUUIDv7Batch(confirmEntries)
	if err != nil {
		return Contract{}, err
	}
	if err := r.ConfirmContract(ctx, confirmContractArgs{
		TxID:               txID,
		ContractID:         contractID,
		SimTime:            simNow,
		Quantity:           served,
		UnitPrice:          p.UnitPrice,
		FromStockAccount:   fromStock,
		FromGuaranteeAcc:   fromGuar,
		FromEscrowAccount:  fromEscrow,
		ToStockAccount:     toStock.ID,
		ToGuaranteeAccount: toGuar.ID,
		ToEscrowAccount:    toEscrow.ID,
		EntryIDs:           entryIDs,
	}); err != nil {
		return Contract{}, mapLedgerError(err)
	}
	return contract, nil
}

// deliverAndSettleInSitu entrega y liquida un contrato de retirada in situ en
// la misma transacción del sorteo: crea el cargamento released_in_situ, asienta
// la entrega íntegra a tiempo, fija quantity_delivered y liquida (fill 100% =>
// settled). Emite contract.delivered y contract.settled.
func (w *Worker) deliverAndSettleInSitu(ctx context.Context, r *Repo, tx pgx.Tx, contract Contract, simNow simtime.SimTime, counts *sweepCounts) error {
	shipmentID, err := r.InsertShipmentReleasedInSitu(ctx, contract.BuyerAccountID, contract.ProductID,
		contract.QuantityAgreed, contract.ID, contract.DestinationNodeID, simNow)
	if err != nil {
		return err
	}
	deliveryID, err := newUUIDv7()
	if err != nil {
		return err
	}
	delivery, err := r.InsertContractDelivery(ctx, deliveryID, contract.ID, shipmentID,
		contract.QuantityAgreed, simNow, true)
	if err != nil {
		return err
	}
	updated, err := r.SetContractQuantityDelivered(ctx, contract.ID, contract.QuantityAgreed)
	if err != nil {
		return err
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateContract, contract.ID, EventContractDelivered, ContractDeliveredPayload{
		ContractID:        contract.ID.String(),
		DeliveryID:        delivery.ID.String(),
		ShipmentID:        shipmentID.String(),
		Quantity:          fixed(delivery.Quantity),
		QuantityDelivered: fixed(updated.QuantityDelivered),
		DeliveredAtSim:    int64(simNow),
		OnTime:            true,
	}); err != nil {
		return err
	}
	return w.settleContract(ctx, r, tx, updated, simNow, counts)
}

// ─── (b) Expiración de publicaciones abiertas por TTL ─────────────────────────

// expirePublications localiza las publicaciones abiertas vencidas por TTL y
// expira cada una en su propia transacción.
func (w *Worker) expirePublications(ctx context.Context) (int, error) {
	simNow := w.svc.sim.Now(ctx)
	ids, err := w.svc.repo.ListExpiredPublicationIDs(ctx, w.svc.opts.PublicationTTLSimSeconds, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		done, err := w.expireOne(ctx, id, simNow)
		if err != nil {
			w.logger.Warn("contracts: fallo expirando una publicación",
				slog.String("publication_id", id.String()), slog.Any("error", err))
			continue
		}
		if done {
			processed++
			w.publicationsExpired.Inc()
		}
	}
	return processed, nil
}

// expireOne libera la garantía restante de una publicación abierta vencida y la
// marca expired, emitiendo publication.expired.
func (w *Worker) expireOne(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (bool, error) {
	expired := false
	err := db.RunSerializable(ctx, w.svc.pool, func(tx pgx.Tx) error {
		expired = false
		r := w.svc.repo.WithTx(tx)
		p, err := r.LockExpiredPublication(ctx, id, w.svc.opts.PublicationTTLSimSeconds, simNow)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil // ya cambió de estado o tomada por otra instancia
		case err != nil:
			return err
		}
		if err := w.svc.releasePublicationCollateral(ctx, r, p, simNow,
			"Expiración de la publicación: garantía restante liberada"); err != nil {
			return err
		}
		if _, err := r.SetPublicationExpired(ctx, p.ID); err != nil {
			return err
		}
		expired = true
		return outbox.Emit(ctx, tx, int64(simNow), AggregatePublication, p.ID, EventPublicationExpired, PublicationExpiredPayload{
			PublicationID:     p.ID.String(),
			Kind:              string(p.Kind),
			QuantityRemaining: fixed(p.QuantityRemaining),
			ExpiredAtSim:      int64(simNow),
		})
	})
	return expired, err
}

// ─── (c) Liquidación de contratos por vencimiento de plazo ────────────────────

// settleDueContracts localiza los contratos activos vencidos y liquida cada uno
// pro-rata en su propia transacción (fill 0 en Fase 0: sin logística, el
// cargamento nunca salió; el stock se libera in situ en el origen).
func (w *Worker) settleDueContracts(ctx context.Context) (int, error) {
	simNow := w.svc.sim.Now(ctx)
	ids, err := w.svc.repo.ListDueContractIDs(ctx, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		done, err := w.settleOneDue(ctx, id, simNow)
		if err != nil {
			w.logger.Warn("contracts: fallo liquidando un contrato vencido",
				slog.String("contract_id", id.String()), slog.Any("error", err))
			continue
		}
		if done {
			processed++
		}
	}
	return processed, nil
}

// settleOneDue liquida un contrato activo vencido en su propia transacción.
func (w *Worker) settleOneDue(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (bool, error) {
	settled := false
	var counts *sweepCounts
	err := db.RunSerializable(ctx, w.svc.pool, func(tx pgx.Tx) error {
		settled = false
		counts = newSweepCounts()
		r := w.svc.repo.WithTx(tx)
		contract, err := r.LockDueContract(ctx, id, simNow)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil // ya liquidado o tomado por otra instancia
		case err != nil:
			return err
		}
		if err := w.settleContract(ctx, r, tx, contract, simNow, counts); err != nil {
			return err
		}
		settled = true
		return nil
	})
	if err == nil && settled {
		w.flush(counts)
	}
	return settled, err
}

// settleContract liquida un contrato pro-rata con ledger.settle_contract_prorata
// (lo entregado a tiempo al comprador; lo faltante reembolsado y la garantía
// repartida compensación/sink; el stock no entregado liberado in situ en el
// origen). Emite contract.settled con el estado final. Reutilizada por la
// entrega in situ (fill 100%) y por el sweep de vencimiento (fill pro-rata).
func (w *Worker) settleContract(ctx context.Context, r *Repo, tx pgx.Tx, contract Contract, simNow simtime.SimTime, counts *sweepCounts) error {
	sellerCash, err := r.GetCashAccount(ctx, contract.SellerAccountID)
	if err != nil {
		return fmt.Errorf("contracts: caja del vendedor %s: %w", contract.SellerAccountID, err)
	}
	buyerCash, err := r.GetCashAccount(ctx, contract.BuyerAccountID)
	if err != nil {
		return fmt.Errorf("contracts: caja del comprador %s: %w", contract.BuyerAccountID, err)
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("contracts: cuenta sink del banco central: %w", err)
	}

	destNode, err := w.svc.warehouseNode(ctx, r, contract.DestinationNodeID, "destino")
	if err != nil {
		return err
	}
	buyerStock, err := r.EnsureStockFreeAccount(ctx, contract.BuyerAccountID, contract.ProductID, *destNode.BuildingID)
	if err != nil {
		return err
	}
	originNode, err := w.svc.warehouseNode(ctx, r, contract.OriginNodeID, "origen")
	if err != nil {
		return err
	}
	sellerStockRelease, err := r.EnsureStockFreeAccount(ctx, contract.SellerAccountID, contract.ProductID, *originNode.BuildingID)
	if err != nil {
		return err
	}

	txID, err := newUUIDv7()
	if err != nil {
		return err
	}
	entryIDs, err := newUUIDv7Batch(maxSettleEntries)
	if err != nil {
		return err
	}
	if err := r.SettleContractProrata(ctx, settleContractArgs{
		TxID:               txID,
		ContractID:         contract.ID,
		SimTime:            simNow,
		SellerCash:         sellerCash.ID,
		BuyerCash:          buyerCash.ID,
		BuyerStock:         buyerStock.ID,
		SinkAccount:        sink.ID,
		SellerStockRelease: sellerStockRelease.ID,
		CompensationBP:     int32(w.svc.opts.CompensationBP), //nolint:gosec // 0..10000 por Validate
		EntryIDs:           entryIDs,
	}); err != nil {
		return mapLedgerError(err)
	}

	// Re-leer el contrato para el estado final (status/fill_bp los fija la
	// función SQL) y emitir contract.settled con el shape exacto del contrato.
	settled, err := r.GetContract(ctx, contract.ID)
	if err != nil {
		return fmt.Errorf("contracts: releyendo el contrato liquidado %s: %w", contract.ID, err)
	}
	fill := 0
	if settled.FillBP != nil {
		fill = int(*settled.FillBP)
	}
	counts.settled[string(settled.Status)]++

	return outbox.Emit(ctx, tx, int64(simNow), AggregateContract, settled.ID, EventContractSettled, ContractSettledPayload{
		ContractID:          settled.ID.String(),
		ProductID:           settled.ProductID.String(),
		DestinationRegionID: destNode.RegionID.String(),
		UnitPrice:           fixed(settled.UnitPrice),
		QuantityAgreed:      fixed(settled.QuantityAgreed),
		QuantityDelivered:   fixed(settled.QuantityDelivered),
		FillBP:              fill,
		SettledAtSim:        int64(simNow),
		Status:              string(settled.Status),
	})
}

// ─── Utilidades ──────────────────────────────────────────────────────────────

// cryptoShuffle baraja s en sitio con crypto/rand (ADR-011: el orden del
// sorteo es aleatorio no predecible — la latencia no otorga ventaja).
func cryptoShuffle[T any](s []T) error {
	for i := len(s) - 1; i > 0; i-- {
		jBig, err := crand.Int(crand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("contracts: barajando el sorteo: %w", err)
		}
		j := int(jBig.Int64())
		s[i], s[j] = s[j], s[i]
	}
	return nil
}

// newUUIDv7Batch pre-genera n UUIDv7 para las partidas de las funciones SQL
// todo-o-nada (ADR-018: los IDs los produce la aplicación).
func newUUIDv7Batch(n int) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, n)
	for i := range ids {
		id, err := newUUIDv7()
		if err != nil {
			return nil, err
		}
		ids[i] = id
	}
	return ids, nil
}

// sleepJitter espera d ± hasta 25% de jitter y devuelve false si el contexto se
// cancela antes (desincroniza instancias concurrentes del worker).
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
