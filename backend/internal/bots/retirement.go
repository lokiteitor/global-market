package bots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// insolventSinceMark es la clave del behavior JSON del bot_profile donde se
// registra el sim-time en que un bot empezó a cumplir la condición de
// insolvencia-inactividad. Es el reloj de la ventana de gracia previa al retiro
// (II_BOT_RETIRE_IDLE_SIM_SECONDS): se estampa al detectar la condición, se
// limpia si el bot se recupera y desencadena el retiro cuando lleva sostenida el
// tiempo configurado.
const insolventSinceMark = "insolvent_since_sim"

// Contrato de evento fijo del Incremento 6a: bot.retired (informativo). Lo emite
// el orquestador en la MISMA tx que la absorción de caja y el cambio de estado.
const (
	aggregateBot    = "bot"
	eventBotRetired = "bot.retired"
)

// retireBatchSize acota cuántas cuentas de bot evalúa cada barrido (cada una en
// su propia transacción). La población de bots es pequeña (GDD §19); el cap es
// holgado. No se expone como env: no es una palanca de operación.
const retireBatchSize int32 = 500

// accountStatusActive es el valor 'active' del enum auth.account_status: una
// cuenta debe estar activa para jugar y para ser candidata al retiro.
const accountStatusActive = "active"

// SimSource entrega el sim-time actual del mundo (inyectado: en cmd/bots es el
// lector del reloj de simulación —*clock.Reader—; en los tests, un reloj fijo
// avanzado por SQL).
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// BotRetiredPayload es el payload de bot.retired (contrato fijo del Incremento
// 6a). absorbed_cash viaja como string de punto fijo (jamás float); el sim-time
// como entero; el uuid como string.
type BotRetiredPayload struct {
	AccountID    string `json:"account_id"`
	AbsorbedCash string `json:"absorbed_cash"`
	RetiredAtSim int64  `json:"retired_at_sim"`
}

// RetirementMetrics es la instrumentación Prometheus del barrido de retiro.
type RetirementMetrics struct {
	// Retired cuenta los bots retirados (ii_bots_retired_total).
	Retired prometheus.Counter
	// AbsorbedCash acumula la caja absorbida al banco central en unidades
	// menores (ii_bots_absorbed_cash_total).
	AbsorbedCash prometheus.Counter
	// SweepDuration mide la duración de cada barrido
	// (ii_bots_retire_sweep_duration_seconds).
	SweepDuration prometheus.Histogram
}

// NewRetirementMetrics registra las métricas del barrido en el registry (nil las
// deja sin instrumentar, para tests).
func NewRetirementMetrics(reg prometheus.Registerer) *RetirementMetrics {
	m := &RetirementMetrics{
		Retired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_bots_retired_total",
			Help: "Total de bots retirados por insolvencia-inactividad sostenida (ADR-024).",
		}),
		AbsorbedCash: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_bots_absorbed_cash_total",
			Help: "Caja total absorbida al banco central por el retiro de bots, en unidades menores.",
		}),
		SweepDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ii_bots_retire_sweep_duration_seconds",
			Help:    "Duración de cada barrido de retiro de bots.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Retired, m.AbsorbedCash, m.SweepDuration)
	}
	return m
}

// RetirementJob es el barrido de retiro del Bot Orchestration Service (ADR-024:
// retiro = liquidación + absorción monetaria). Cada barrido evalúa las cuentas
// de bot activas y retira las insolventes-inactivas: absorbe TODA su caja al
// banco central (inverso contable de la capitalización del provisioning),
// marca la cuenta retirada y desactiva su perfil, todo en UNA transacción
// serializable junto al evento bot.retired del outbox. Cada bot se procesa en su
// propia tx (FOR UPDATE sobre la cuenta), de modo que varias instancias del
// orquestador pueden barrer en paralelo sin pisarse.
type RetirementJob struct {
	pool      *pgxpool.Pool
	opts      RetirementOptions
	sim       SimSource
	logger    *slog.Logger
	metrics   *RetirementMetrics
	repo      *auth.PGRepository
	ledgerSvc *ledger.Service

	// emissionID cachea la cuenta de emisión del banco central (estable tras el
	// seed); se resuelve en el primer barrido con éxito.
	emissionID uuid.UUID
}

// NewRetirementJob construye el barrido sobre el pool compartido. ledgerOpts
// configura el servicio de ledger interno (asientos de absorción); sim entrega
// el sim-time; reg registra las métricas (nil = sin instrumentar); logger nil
// usa slog.Default.
func NewRetirementJob(pool *pgxpool.Pool, ledgerOpts ledger.Options, opts RetirementOptions, sim SimSource, logger *slog.Logger, reg prometheus.Registerer) (*RetirementJob, error) {
	if pool == nil {
		return nil, errors.New("bots: RetirementJob requiere un pool de BD")
	}
	if sim == nil {
		return nil, errors.New("bots: RetirementJob requiere un SimSource")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &RetirementJob{
		pool:      pool,
		opts:      opts,
		sim:       sim,
		logger:    logger,
		metrics:   NewRetirementMetrics(reg),
		repo:      auth.NewPGRepository(pool),
		ledgerSvc: ledger.NewService(pool, ledgerOpts, nil),
	}, nil
}

// Run ejecuta el bucle del barrido hasta que ctx se cancele (nil al apagado
// limpio). El primer barrido dispara de inmediato; los siguientes, cada
// Interval con jitter ±20%.
func (j *RetirementJob) Run(ctx context.Context) error {
	j.logger.Info("bots: barrido de retiro iniciado",
		slog.Duration("interval", j.opts.Interval),
		slog.Int64("cash_floor", j.opts.CashFloor),
		slog.Int64("idle_sim_seconds", j.opts.IdleSimSeconds))
	for {
		j.RunOnce(ctx)
		if err := sleepCtx(ctx, jitterTick(j.opts.Interval)); err != nil {
			j.logger.Info("bots: barrido de retiro detenido")
			return nil
		}
	}
}

// RunOnce ejecuta una pasada del barrido. Aislado para los tests, que controlan
// el disparo y el reloj.
func (j *RetirementJob) RunOnce(ctx context.Context) {
	start := time.Now()
	retired, err := j.sweep(ctx)
	j.metrics.SweepDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		j.logger.Warn("bots: barrido de retiro con error", slog.Any("error", err))
		return
	}
	if retired > 0 {
		j.logger.Info("bots: barrido de retiro completado", slog.Int("retirados", retired))
	}
}

// sweep evalúa las cuentas de bot activas y retira las que corresponda. Devuelve
// cuántas retiró.
func (j *RetirementJob) sweep(ctx context.Context) (int, error) {
	emissionID, err := j.emissionAccountID(ctx)
	if err != nil {
		return 0, err
	}
	ids, err := j.repo.ListActiveBotAccounts(ctx, retireBatchSize)
	if err != nil {
		return 0, err
	}
	retired := 0
	for _, id := range ids {
		did, err := j.evaluate(ctx, id, emissionID)
		if err != nil {
			j.logger.Warn("bots: fallo evaluando el retiro de un bot",
				slog.String("account_id", id.String()), slog.Any("error", err))
			continue
		}
		if did {
			retired++
		}
	}
	return retired, nil
}

// evaluate procesa una cuenta de bot en su propia transacción serializable:
// bloquea la cuenta, evalúa la insolvencia instantánea, mantiene la marca de
// insolvencia sostenida y, si la ventana de gracia se cumplió, retira el bot
// (absorción de caja + estado + evento). Devuelve si retiró.
func (j *RetirementJob) evaluate(ctx context.Context, id, emissionID uuid.UUID) (bool, error) {
	var retired bool
	var absorbed int64
	err := db.RunSerializable(ctx, j.pool, func(tx pgx.Tx) error {
		retired = false
		absorbed = 0
		arepo := auth.NewPGRepository(tx)

		// Bloqueo de la fila de la cuenta: serializa el retiro concurrente y fija
		// el estado observado durante toda la tx.
		kind, status, err := arepo.LockAccountForUpdate(ctx, id)
		if errors.Is(err, auth.ErrNotFound) {
			return nil // la cuenta desapareció (no debería): nada que hacer
		}
		if err != nil {
			return err
		}
		if kind != "bot" || status != accountStatusActive {
			return nil // ya retirada/suspendida o tomada por otra instancia
		}

		simNow := j.sim.Now(ctx)

		insolvent, cashID, cashBal, err := j.isInsolventNow(ctx, tx, id)
		if err != nil {
			return err
		}
		if !insolvent {
			// El bot volvió a tener actividad o caja: reinicia el reloj de gracia.
			return arepo.ClearBotProfileMark(ctx, id, insolventSinceMark)
		}

		// Insolvencia sostenida: arranca o consulta el reloj de gracia.
		since, err := arepo.GetBotProfileMark(ctx, id, insolventSinceMark)
		if err != nil && !errors.Is(err, auth.ErrNotFound) {
			return err
		}
		insolventSince := int64(simNow)
		if since != nil {
			insolventSince = *since
		}
		if int64(simNow)-insolventSince < j.opts.IdleSimSeconds {
			if since == nil {
				return arepo.SetBotProfileMark(ctx, id, insolventSinceMark, int64(simNow))
			}
			return nil // dentro de la ventana de gracia: aún no se retira
		}

		// ── Retiro: absorción + estado + evento, todo atómico ──────────────────

		// Absorción monetaria = mover TODA la caja del bot a la cuenta de emisión
		// (inverso de la capitalización +cash/−emission del provisioning: reduce
		// la masa emitida neta). Solo si hay caja: un asiento de importe cero es
		// inválido, y la caja jamás debe quedar negativa (la mueve entera a 0).
		if cashBal > 0 {
			if _, err := j.ledgerSvc.PostTransactionTx(ctx, tx, ledger.TransactionKindBotRetirement, simNow, &id,
				fmt.Sprintf("Retiro de bot %s: absorción de caja (%d) al banco central", id, cashBal),
				[]ledger.EntryInput{
					{AccountID: cashID, Amount: -cashBal},
					{AccountID: emissionID, Amount: cashBal},
				}); err != nil {
				return err
			}
			absorbed = cashBal
		}

		ok, err := arepo.RetireBotAccount(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return nil // carrera perdida (no debería con el lock): sin efectos
		}
		if err := arepo.ClearBotProfileMark(ctx, id, insolventSinceMark); err != nil {
			return err
		}

		if err := outbox.Emit(ctx, tx, int64(simNow), aggregateBot, id, eventBotRetired, BotRetiredPayload{
			AccountID:    id.String(),
			AbsorbedCash: strconv.FormatInt(absorbed, 10),
			RetiredAtSim: int64(simNow),
		}); err != nil {
			return err
		}
		retired = true
		return nil
	})
	if err == nil && retired {
		j.metrics.Retired.Inc()
		if absorbed > 0 {
			j.metrics.AbsorbedCash.Add(float64(absorbed))
		}
		j.logger.Info("bot retirado por insolvencia-inactividad",
			slog.String("account_id", id.String()),
			slog.Int64("absorbed_cash", absorbed))
	}
	return retired, err
}

// isInsolventNow evalúa la condición de insolvencia-inactividad INSTANTÁNEA de un
// bot y devuelve además su caja (id + saldo, para la absorción):
//
//	caja < II_BOTS_RETIRE_CASH_FLOOR
//	  Y sin edificios en estado distinto de 'seized' (todos embargados o ninguno)
//	  Y sin contratos activos como comprador/vendedor (ni fletes como cargador/portador)
//	  Y sin publicaciones vivas (draw_window/open/micro_window).
//
// Si la caja ya alcanza el piso, es solvente y no se consultan los activos.
func (j *RetirementJob) isInsolventNow(ctx context.Context, tx pgx.Tx, owner uuid.UUID) (insolvent bool, cashID uuid.UUID, cashBal int64, err error) {
	cashID, cashBal, err = j.cashAccount(ctx, tx, owner)
	if err != nil {
		return false, uuid.Nil, 0, err
	}
	if cashBal >= j.opts.CashFloor {
		return false, cashID, cashBal, nil
	}
	live, err := j.hasLiveEngagements(ctx, tx, owner)
	if err != nil {
		return false, cashID, cashBal, err
	}
	return !live, cashID, cashBal, nil
}

// cashAccount devuelve la caja del bot (id + saldo). Sin caja (pgx.ErrNoRows) →
// saldo 0 y uuid.Nil (no habrá absorción).
func (j *RetirementJob) cashAccount(ctx context.Context, tx pgx.Tx, owner uuid.UUID) (uuid.UUID, int64, error) {
	var id uuid.UUID
	var bal int64
	err := tx.QueryRow(ctx, cashAccountSQL, owner).Scan(&id, &bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, 0, nil
	}
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("bots: consultando la caja de %s: %w", owner, err)
	}
	return id, bal, nil
}

// hasLiveEngagements indica si el bot conserva algún vínculo vivo con el mundo:
// un edificio no embargado, un contrato activo (compra/venta o flete) o una
// publicación viva. Cualquiera de ellos lo excluye del retiro. Una sola ida y
// vuelta a la BD (lectura de world/ledger desde la raíz de composición del
// orquestador).
func (j *RetirementJob) hasLiveEngagements(ctx context.Context, tx pgx.Tx, owner uuid.UUID) (bool, error) {
	var live bool
	if err := tx.QueryRow(ctx, liveEngagementsSQL, owner).Scan(&live); err != nil {
		return false, fmt.Errorf("bots: comprobando la actividad de %s: %w", owner, err)
	}
	return live, nil
}

// emissionAccountID resuelve (y cachea) la cuenta de emisión del banco central
// sembrado. El orquestador exige el mundo sembrado: no re-crea la base monetaria.
func (j *RetirementJob) emissionAccountID(ctx context.Context) (uuid.UUID, error) {
	if j.emissionID != uuid.Nil {
		return j.emissionID, nil
	}
	bank, err := j.repo.GetAccountByName(ctx, seed.CentralBankName)
	if errors.Is(err, auth.ErrNotFound) {
		return uuid.Nil, fmt.Errorf("bots: no existe la cuenta del banco central %q: ejecuta antes el seed", seed.CentralBankName)
	}
	if err != nil {
		return uuid.Nil, err
	}
	accounts, _, err := j.ledgerSvc.ListAccounts(ctx, bank.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindEmission, Limit: 1,
	})
	if err != nil {
		return uuid.Nil, err
	}
	if len(accounts) == 0 {
		return uuid.Nil, errors.New("bots: el banco central no tiene cuenta de emisión: ejecuta antes el seed")
	}
	j.emissionID = accounts[0].ID
	return j.emissionID, nil
}

// cashAccountSQL lee la caja (id + saldo) de una corporación.
const cashAccountSQL = `
SELECT id, balance FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1
`

// liveEngagementsSQL comprueba en una sola consulta si el bot conserva un
// edificio no embargado, un contrato activo (compra/venta o flete) o una
// publicación viva.
const liveEngagementsSQL = `
SELECT
  EXISTS (SELECT 1 FROM world.buildings
           WHERE owner_account_id = $1 AND status <> 'seized')
  OR EXISTS (SELECT 1 FROM ledger.contracts
              WHERE (buyer_account_id = $1 OR seller_account_id = $1) AND status = 'active')
  OR EXISTS (SELECT 1 FROM ledger.freight_contracts
              WHERE (shipper_account_id = $1 OR carrier_account_id = $1) AND status = 'active')
  OR EXISTS (SELECT 1 FROM ledger.publications
              WHERE publisher_account_id = $1
                AND status IN ('draw_window', 'open', 'micro_window'))
`
