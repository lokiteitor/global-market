package bots

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// sessionRetryWait es la espera antes de reintentar una sesión de bot caída
// (login fallido, API inaccesible, sesión expirada).
const sessionRetryWait = 5 * time.Second

// errBotRetired señala que la cuenta del bot fue retirada (ADR-024): su
// goroutine debe terminar sin reintentar (una cuenta retirada no juega).
var errBotRetired = errors.New("bots: cuenta retirada")

// configuredBehavior es un Behavior cuyos umbrales se persisten como behavior
// JSON en auth.bot_profiles (auditabilidad del ADR-024). Los tres arquetipos
// v1 lo implementan.
type configuredBehavior interface {
	Behavior
	ConfigJSON() ([]byte, error)
}

// ProvisionedBot es un bot listo para ejecutarse: cuenta asegurada,
// credencial derivada y capitalización asentada.
type ProvisionedBot struct {
	// Name es el nombre de la cuenta (clave de idempotencia del provisioning).
	Name string
	// AccountID es la cuenta kind=bot.
	AccountID uuid.UUID
	// Archetype es el valor del enum auth.bot_archetype persistido.
	Archetype string
	// Secret es el secreto derivado (solo en memoria: la BD guarda el hash).
	Secret string
	// Behavior es el arquetipo que jugará por el SDK.
	Behavior configuredBehavior
}

// Orchestrator es el Bot Orchestration Service (ADR-024): PROVISIONING por
// paquetes internos (auth + ledger, operación del banco central) y EJECUCIÓN
// de la población donde todo el gameplay pasa por pkg/botsdk (igualdad de API
// literal, ADR-010).
type Orchestrator struct {
	pool      *pgxpool.Pool
	opts      Options
	logger    *slog.Logger
	metrics   *Metrics
	repo      *auth.PGRepository
	ledgerSvc *ledger.Service

	// mu protege sup: el supervisor solo existe mientras Run está en vuelo y la
	// densidad dinámica lo consulta desde otra goroutine.
	mu  sync.Mutex
	sup *supervisor
}

// El orquestador ES la población gobernable por la densidad dinámica: sus bots
// se pausan y reanudan en caliente sin reiniciar el proceso.
var _ Population = (*Orchestrator)(nil)

// NewOrchestrator construye el orquestador. reg puede ser nil (tests sin
// instrumentar).
func NewOrchestrator(pool *pgxpool.Pool, opts Options, ledgerOpts ledger.Options, logger *slog.Logger, reg prometheus.Registerer) (*Orchestrator, error) {
	if pool == nil {
		return nil, errors.New("bots: el pool de BD es obligatorio")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Orchestrator{
		pool:      pool,
		opts:      opts,
		logger:    logger,
		metrics:   NewMetrics(reg),
		repo:      auth.NewPGRepository(pool),
		ledgerSvc: ledger.NewService(pool, ledgerOpts, nil),
	}, nil
}

// DeriveSecret deriva el secreto de un bot de la semilla y su nombre
// (HMAC-SHA256, hex): reproducible entre arranques sin almacenar el secreto
// en claro — la BD solo guarda el hash argon2id.
func DeriveSecret(seed, name string) string {
	mac := hmac.New(sha256.New, []byte(seed))
	mac.Write([]byte("imperio-bot-secret:" + name))
	return hex.EncodeToString(mac.Sum(nil))
}

// population construye la población configurada: nombres estables (clave de
// idempotencia) y arquetipos con sus umbrales por defecto.
func (o *Orchestrator) population() []ProvisionedBot {
	var bots []ProvisionedBot
	add := func(name, archetype string, behavior configuredBehavior) {
		bots = append(bots, ProvisionedBot{
			Name:      name,
			Archetype: archetype,
			Secret:    DeriveSecret(o.opts.SecretSeed, name),
			Behavior:  behavior,
		})
	}
	for i := 1; i <= o.opts.CoalProducers; i++ {
		name := fmt.Sprintf("Bot Carbonera %02d", i)
		add(name, "primary_producer", NewCoalProducer(DefaultCoalProducerConfig(), name, o.logger, o.metrics))
	}
	for i := 1; i <= o.opts.IronProducers; i++ {
		name := fmt.Sprintf("Bot Minera %02d", i)
		add(name, "primary_producer", NewIronProducer(DefaultIronProducerConfig(), name, o.logger, o.metrics))
	}
	for i := 1; i <= o.opts.Traders; i++ {
		name := fmt.Sprintf("Bot Mercader %02d", i)
		add(name, "arbitrageur", NewTrader(DefaultTraderConfig(o.opts.Capital), name, o.logger, o.metrics))
	}
	for i := 1; i <= o.opts.Transformers; i++ {
		name := fmt.Sprintf("Bot Siderúrgica %02d", i)
		add(name, "industrial_transformer",
			NewIndustrialTransformer(DefaultIndustrialTransformerConfig(o.opts.TransformerMarginBP), name, o.logger, o.metrics))
	}
	for i := 1; i <= o.opts.Freighters; i++ {
		name := fmt.Sprintf("Bot Transportista %02d", i)
		add(name, "freighter",
			NewFreighter(DefaultFreighterConfig(o.opts.FreighterMarginBP, o.opts.Capital), name, o.logger, o.metrics))
	}
	return bots
}

// Provision asegura la población completa de forma idempotente por nombre:
// cuenta kind=bot, credencial argon2id (nunca sobrescrita), bot_profile
// (arquetipo + behavior JSON) y la CAPITALIZACIÓN única si la cuenta aún no
// tenía caja (bot_capitalization: +capital cash / −capital emission del banco
// central). Requiere el mundo sembrado (banco central con cuenta de emisión).
func (o *Orchestrator) Provision(ctx context.Context) ([]ProvisionedBot, error) {
	emission, err := o.emissionAccount(ctx)
	if err != nil {
		return nil, err
	}
	simNow, err := o.currentSimTime(ctx)
	if err != nil {
		return nil, err
	}

	bots := o.population()
	for i := range bots {
		if err := o.provisionOne(ctx, &bots[i], emission, simNow); err != nil {
			return nil, err
		}
	}
	return bots, nil
}

// provisionOne asegura un bot (idempotente por nombre).
func (o *Orchestrator) provisionOne(ctx context.Context, bot *ProvisionedBot, emission ledger.Account, simNow simtime.SimTime) error {
	acc, err := o.repo.GetAccountByName(ctx, bot.Name)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		acc, err = o.repo.CreateAccount(ctx, "bot", bot.Name)
		if err != nil {
			return err
		}
		o.logger.Info("cuenta de bot creada",
			slog.String("bot", bot.Name), slog.String("account_id", acc.ID.String()))
	case err != nil:
		return err
	default:
		if acc.Kind != "bot" {
			return fmt.Errorf("bots: la cuenta %q existe con kind %q (esperado bot)", bot.Name, acc.Kind)
		}
	}
	bot.AccountID = acc.ID

	secretHash, err := auth.HashSecret(bot.Secret)
	if err != nil {
		return err
	}
	credCreated, err := o.repo.EnsureCredential(ctx, acc.ID, secretHash)
	if err != nil {
		return err
	}
	if credCreated {
		o.logger.Info("credencial de bot creada (argon2id, secreto derivado de la semilla)",
			slog.String("bot", bot.Name))
	}

	behaviorJSON, err := bot.Behavior.ConfigJSON()
	if err != nil {
		return fmt.Errorf("bots: serializando el behavior de %s: %w", bot.Name, err)
	}
	profileCreated, err := o.repo.EnsureBotProfile(ctx, acc.ID, bot.Archetype, behaviorJSON)
	if err != nil {
		return err
	}
	if profileCreated {
		o.logger.Info("bot_profile creado",
			slog.String("bot", bot.Name), slog.String("archetype", bot.Archetype))
	}

	// Capitalización ÚNICA: la caja es la clave de idempotencia (mismo patrón
	// que el capital semilla del seed). Si ya existe, el capital ya se emitió.
	existing, _, err := o.ledgerSvc.ListAccounts(ctx, acc.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindCash, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		o.logger.Info("caja del bot ya existía: capitalización omitida",
			slog.String("bot", bot.Name), slog.Int64("balance", existing[0].Balance))
		return nil
	}
	cash, err := o.ledgerSvc.EnsureCashAccount(ctx, acc.ID)
	if err != nil {
		return err
	}
	ref := acc.ID
	txID, err := o.ledgerSvc.PostTransaction(ctx, ledger.TransactionKindBotCapitalization, simNow, &ref,
		fmt.Sprintf("Capitalización de %s (emisión del banco central)", bot.Name),
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: o.opts.Capital},
			{AccountID: emission.ID, Amount: -o.opts.Capital},
		})
	if err != nil {
		return err
	}
	o.logger.Info("bot capitalizado",
		slog.String("bot", bot.Name),
		slog.Int64("capital", o.opts.Capital),
		slog.String("transaction_id", txID.String()),
		slog.Int64("sim_time_at", int64(simNow)))
	return nil
}

// emissionAccount localiza la cuenta de emisión del banco central sembrado.
// El orquestador exige el mundo sembrado: no re-crea la base monetaria.
func (o *Orchestrator) emissionAccount(ctx context.Context) (ledger.Account, error) {
	bank, err := o.repo.GetAccountByName(ctx, seed.CentralBankName)
	if errors.Is(err, auth.ErrNotFound) {
		return ledger.Account{}, fmt.Errorf("bots: no existe la cuenta del banco central %q: ejecuta antes el seed", seed.CentralBankName)
	}
	if err != nil {
		return ledger.Account{}, err
	}
	accounts, _, err := o.ledgerSvc.ListAccounts(ctx, bank.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindEmission, Limit: 1,
	})
	if err != nil {
		return ledger.Account{}, err
	}
	if len(accounts) == 0 {
		return ledger.Account{}, errors.New("bots: el banco central no tiene cuenta de emisión: ejecuta antes el seed")
	}
	return accounts[0], nil
}

// currentSimTime deriva el sim-time actual del ancla persistida en
// world.sim_clock.
func (o *Orchestrator) currentSimTime(ctx context.Context) (simtime.SimTime, error) {
	a, err := clock.NewStore(o.pool).Load(ctx)
	if err != nil {
		return 0, fmt.Errorf("bots: leyendo el reloj de simulación: %w", err)
	}
	return simtime.Derive(a.SimTimeAt, a.WallAnchor, time.Now(), a.Ratio, a.Frozen), nil
}

// ─── Ejecución de la población ──────────────────────────────────────────────

// Run aprovisiona la población y la ejecuta (una goroutine por bot activo, todo
// el gameplay por el SDK) hasta que ctx se cancele. Devuelve nil en el apagado
// limpio.
//
// La ejecución se delega en un SUPERVISOR de plazas: cada bot aprovisionado
// ocupa una plaza que se puede parar y arrancar EN CALIENTE (densidad dinámica,
// GDD §13.4 modo 2) conservando cuenta, capital y estado local. Al arrancar
// corre la población completa; el controlador de densidad —si está activo— la
// ajusta desde su primer ciclo.
func (o *Orchestrator) Run(ctx context.Context) error {
	provisioned, err := o.Provision(ctx)
	if err != nil {
		return err
	}
	o.logger.Info("población de bots aprovisionada",
		slog.Int("coal_producers", o.opts.CoalProducers),
		slog.Int("iron_producers", o.opts.IronProducers),
		slog.Int("traders", o.opts.Traders),
		slog.Int("transformers", o.opts.Transformers),
		slog.Int("freighters", o.opts.Freighters),
		slog.Duration("tick", o.opts.Tick),
		slog.String("api_url", o.opts.APIURL))

	sup := newSupervisor(ctx, provisioned, o.runBot, o.logger, o.metrics)
	o.setSupervisor(sup)
	defer o.setSupervisor(nil)

	for archetype, n := range sup.provisioned() {
		sup.start(archetype, n)
	}

	<-ctx.Done()
	sup.close()
	sup.wait()
	o.logger.Info("población de bots detenida")
	return nil
}

// setSupervisor publica (o retira) el supervisor vigente. Fuera de Run, las
// operaciones de Population son no-ops seguras: la densidad puede arrancar
// antes que el orquestador sin condición de carrera.
func (o *Orchestrator) setSupervisor(sup *supervisor) {
	o.mu.Lock()
	o.sup = sup
	o.mu.Unlock()
}

// supervisor devuelve el supervisor vigente (nil si Run no está en vuelo).
func (o *Orchestrator) supervisor() *supervisor {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sup
}

// ─── Population: pausa y reanudación en caliente (densidad dinámica) ─────────

// Provisioned devuelve la población aprovisionada por arquetipo (vacío mientras
// Run no haya aprovisionado).
func (o *Orchestrator) Provisioned() map[string]int {
	sup := o.supervisor()
	if sup == nil {
		return map[string]int{}
	}
	return sup.provisioned()
}

// Active devuelve cuántos bots del arquetipo están ejecutando su bucle.
func (o *Orchestrator) Active(archetype string) int {
	sup := o.supervisor()
	if sup == nil {
		return 0
	}
	return sup.active(archetype)
}

// Start reanuda hasta n bots parados del arquetipo y devuelve cuántos arrancó.
func (o *Orchestrator) Start(archetype string, n int) int {
	sup := o.supervisor()
	if sup == nil {
		return 0
	}
	return sup.start(archetype, n)
}

// Stop pausa hasta n bots activos del arquetipo (cierre limpio de su sesión) y
// devuelve cuántos paró. NO retira cuentas: el bot conserva capital, activos y
// contratos, y puede reanudarse en cualquier ciclo posterior.
func (o *Orchestrator) Stop(archetype string, n int) int {
	sup := o.supervisor()
	if sup == nil {
		return 0
	}
	return sup.stop(archetype, n)
}

// runBot mantiene vivas las sesiones de un bot: si la sesión cae (login
// rechazado, API inaccesible, token expirado) espera y vuelve a entrar. Si la
// cuenta fue retirada (ADR-024) la goroutine termina y devuelve true: una cuenta
// retirada no vuelve a jugar, ni siquiera si la densidad reanuda su plaza. La
// cancelación de ctx (apagado o pausa por densidad) devuelve false.
func (o *Orchestrator) runBot(ctx context.Context, bot ProvisionedBot, st *State) bool {
	log := o.logger.With(slog.String("bot", bot.Name), slog.String("behavior", bot.Behavior.Name()))
	for ctx.Err() == nil {
		if retired, err := o.accountRetired(ctx, bot.AccountID); err != nil {
			log.Warn("no se pudo comprobar el estado de la cuenta; se reintenta", slog.Any("error", err))
		} else if retired {
			log.Info("cuenta retirada: el bot deja de jugar")
			return true
		}
		err := o.botSession(ctx, bot, st, log)
		if errors.Is(err, errBotRetired) {
			log.Info("cuenta retirada: el bot deja de jugar")
			return true
		}
		if err != nil && ctx.Err() == nil {
			log.Warn("sesión de bot terminada; se reintenta", slog.Any("error", err))
		}
		if err := sleepCtx(ctx, sessionRetryWait); err != nil {
			return false
		}
	}
	return false
}

// accountRetired indica si la cuenta ya no está activa (retirada/suspendida).
// Una cuenta sin id (bot no aprovisionado) o inexistente se considera no
// retirada: el flujo normal la resuelve por otras vías.
func (o *Orchestrator) accountRetired(ctx context.Context, accountID uuid.UUID) (bool, error) {
	if accountID == uuid.Nil {
		return false, nil
	}
	status, err := o.repo.GetAccountStatus(ctx, accountID)
	if errors.Is(err, auth.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return status != accountStatusActive, nil
}

// botSession ejecuta una sesión completa del bot: Login por el SDK, conexión
// WS (JoinCorp: los eventos despiertan el Decide antes del tick) y el bucle
// de decisión con tick jitterizado ±20%, recuperación ante pánicos y apagado
// graceful. Devuelve error para re-entrar (p. ej. sesión expirada) o nil si
// ctx terminó.
func (o *Orchestrator) botSession(ctx context.Context, bot ProvisionedBot, st *State, log *slog.Logger) error {
	client, err := botsdk.New(botsdk.Options{
		BaseURL:   o.opts.APIURL,
		Logger:    log,
		UserAgent: "imperio-bots/1 (" + bot.Behavior.Name() + ")",
	})
	if err != nil {
		return err
	}
	session, err := client.Login(ctx, bot.Name, bot.Secret)
	if err != nil {
		return fmt.Errorf("bots: login de %s: %w", bot.Name, err)
	}
	st.AccountID = session.Account.ID
	log.Info("bot conectado", slog.String("account_id", st.AccountID))

	// WS: mejor esfuerzo — sin gateway de eventos el bot sigue decidiendo por
	// tick (el estado siempre se reconstruye por REST).
	var events <-chan botsdk.Event
	var reconnected <-chan int64
	ws, wsErr := client.Connect(ctx, botsdk.WSOptions{})
	if wsErr != nil {
		log.Warn("WS no disponible; el bot opera solo por tick", slog.Any("error", wsErr))
	} else {
		defer ws.Close() //nolint:errcheck // cierre de apagado
		if wm, err := ws.JoinCorp(ctx); err != nil {
			log.Warn("join a la room corp fallido; el bot opera solo por tick", slog.Any("error", err))
		} else {
			st.Watermark = wm
			log.Info("suscrito a la room corp", slog.Int64("watermark", wm))
		}
		events = ws.Events()
		reconnected = ws.Reconnected()
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(jitterTick(o.opts.Tick)):
		case ev, ok := <-events:
			if !ok {
				events = nil // la sesión WS terminó; se sigue por tick
				continue
			}
			// Un evento propio despierta el Decide antes del tick.
			log.Debug("evento WS recibido",
				slog.Int64("seq", ev.Seq), slog.String("event_type", ev.EventType))
			drainEvents(events, log)
		case wm := <-reconnected:
			st.Watermark = wm
			log.Info("WS reconectado: re-sincronización por REST",
				slog.Int64("watermark", wm))
		}

		// Una cuenta retirada no decide: se comprueba antes de cada pasada para
		// dejar de jugar en cuanto el barrido de retiro la marque.
		if retired, err := o.accountRetired(ctx, bot.AccountID); err == nil && retired {
			return errBotRetired
		}
		if err := o.safeDecide(ctx, bot, client, st, log); err != nil {
			return err // sesión inválida: re-login en runBot
		}
		if st.LastCashValid {
			o.metrics.Cash.WithLabelValues(bot.Name).Set(float64(st.LastCash))
		}
	}
}

// safeDecide ejecuta una pasada de decisión con recuperación ante pánicos.
// Devuelve error solo cuando la sesión debe reiniciarse (401).
func (o *Orchestrator) safeDecide(ctx context.Context, bot ProvisionedBot, client *botsdk.Client, st *State, log *slog.Logger) (sessionErr error) {
	defer func() {
		if r := recover(); r != nil {
			o.metrics.Errors.WithLabelValues(bot.Name).Inc()
			log.Error("pánico en Decide recuperado; el bucle continúa", slog.Any("panic", r))
		}
	}()
	if err := bot.Behavior.Decide(ctx, client, st); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		o.metrics.Errors.WithLabelValues(bot.Name).Inc()
		if apiErr, ok := botsdk.AsAPIError(err); ok && apiErr.Status == 401 {
			return fmt.Errorf("bots: sesión expirada: %w", err)
		}
		log.Error("fallo en la pasada de decisión", slog.Any("error", err))
	}
	return nil
}

// drainEvents vacía sin bloquear los eventos ya encolados (una pasada de
// Decide reconsulta todo por REST: no hace falta procesarlos uno a uno).
func drainEvents(events <-chan botsdk.Event, log *slog.Logger) {
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			log.Debug("evento WS recibido",
				slog.Int64("seq", ev.Seq), slog.String("event_type", ev.EventType))
		default:
			return
		}
	}
}

// ─── Supervisor de plazas: arranque y parada en caliente ────────────────────

// slotState es el estado de una plaza de ejecución.
type slotState int

const (
	slotIdle     slotState = iota // parada: aún sin arrancar o pausada por densidad
	slotRunning                   // ejecutando su bucle de sesión y decisión
	slotStopping                  // cancelada, esperando el cierre limpio
	slotRetired                   // cuenta retirada (ADR-024): no vuelve a arrancar
)

// botSlot es la plaza de UN bot aprovisionado. El State local SOBREVIVE a las
// pausas: al reanudar, el bot no vuelve a descubrir su implantación desde cero
// (la API sigue siendo la fuente de verdad; el State solo cachea ids).
type botSlot struct {
	bot    ProvisionedBot
	st     *State
	state  slotState
	cancel context.CancelFunc
	done   chan struct{}
}

// botRunner es el bucle de vida de un bot (Orchestrator.runBot): corre hasta
// que su contexto se cancela y devuelve true si terminó por cuenta retirada.
type botRunner func(ctx context.Context, bot ProvisionedBot, st *State) (retired bool)

// supervisor gestiona las goroutines de la población: una plaza por bot
// aprovisionado, arrancable y parable EN CALIENTE sin reiniciar el proceso ni
// re-aprovisionar. Es el sustrato de la densidad dinámica (GDD §13.4 modo 2).
type supervisor struct {
	root    context.Context
	run     botRunner
	logger  *slog.Logger
	metrics *Metrics

	mu      sync.Mutex
	slots   map[string][]*botSlot // arquetipo (Behavior.Name) → plazas
	closing bool                  // apagado en curso: no se arrancan más plazas
	wg      sync.WaitGroup
}

// newSupervisor agrupa la población aprovisionada en plazas por arquetipo. Las
// plazas nacen paradas: el llamante decide cuántas arranca.
func newSupervisor(root context.Context, provisioned []ProvisionedBot, run botRunner, logger *slog.Logger, metrics *Metrics) *supervisor {
	s := &supervisor{
		root:    root,
		run:     run,
		logger:  logger,
		metrics: metrics,
		slots:   make(map[string][]*botSlot),
	}
	for _, bot := range provisioned {
		archetype := bot.Behavior.Name()
		s.slots[archetype] = append(s.slots[archetype], &botSlot{bot: bot, st: NewState()})
	}
	for archetype := range s.slots {
		s.metrics.Active.WithLabelValues(archetype).Set(0)
	}
	return s
}

// provisioned devuelve las plazas vivas por arquetipo (las retiradas ya no
// cuentan: su cuenta no puede volver a jugar).
func (s *supervisor) provisioned() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.slots))
	for archetype, slots := range s.slots {
		n := 0
		for _, slot := range slots {
			if slot.state != slotRetired {
				n++
			}
		}
		out[archetype] = n
	}
	return out
}

// active cuenta las plazas del arquetipo que están ejecutando su bucle.
func (s *supervisor) active(archetype string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeLocked(archetype)
}

func (s *supervisor) activeLocked(archetype string) int {
	n := 0
	for _, slot := range s.slots[archetype] {
		if slot.state == slotRunning {
			n++
		}
	}
	return n
}

// start arranca hasta n plazas paradas del arquetipo (en orden estable) y
// devuelve cuántas arrancó.
func (s *supervisor) start(archetype string, n int) int {
	if n <= 0 {
		return 0
	}
	s.mu.Lock()
	started := 0
	if !s.closing && s.root.Err() == nil {
		for _, slot := range s.slots[archetype] {
			if started >= n {
				break
			}
			if slot.state != slotIdle {
				continue
			}
			s.launchLocked(slot)
			started++
		}
	}
	active := s.activeLocked(archetype)
	s.mu.Unlock()

	if started > 0 {
		s.metrics.Active.WithLabelValues(archetype).Set(float64(active))
		s.logger.Info("bots: plazas arrancadas",
			slog.String("archetype", archetype),
			slog.Int("started", started),
			slog.Int("active", active))
	}
	return started
}

// stop para hasta n plazas activas del arquetipo (las últimas primero, para que
// la población superviviente sea estable) y espera su cierre limpio. Devuelve
// cuántas paró. NO retira cuentas.
func (s *supervisor) stop(archetype string, n int) int {
	if n <= 0 {
		return 0
	}
	s.mu.Lock()
	slots := s.slots[archetype]
	waiting := make([]chan struct{}, 0, n)
	for i := len(slots) - 1; i >= 0 && len(waiting) < n; i-- {
		slot := slots[i]
		if slot.state != slotRunning {
			continue
		}
		slot.state = slotStopping
		if slot.cancel != nil {
			slot.cancel()
		}
		if slot.done != nil {
			waiting = append(waiting, slot.done)
		}
	}
	active := s.activeLocked(archetype)
	s.mu.Unlock()

	// El cierre se espera FUERA del lock: la goroutine necesita el mutex para
	// liberar su plaza al terminar.
	for _, done := range waiting {
		<-done
	}
	if len(waiting) > 0 {
		s.metrics.Active.WithLabelValues(archetype).Set(float64(active))
		s.logger.Info("bots: plazas paradas",
			slog.String("archetype", archetype),
			slog.Int("stopped", len(waiting)),
			slog.Int("active", active))
	}
	return len(waiting)
}

// launchLocked lanza la goroutine de una plaza. Exige el lock tomado.
func (s *supervisor) launchLocked(slot *botSlot) {
	ctx, cancel := context.WithCancel(s.root)
	done := make(chan struct{})
	slot.cancel = cancel
	slot.done = done
	slot.state = slotRunning

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer close(done)
		retired := s.run(ctx, slot.bot, slot.st)
		cancel() // libera el contexto hijo sea cual sea la salida

		archetype := slot.bot.Behavior.Name()
		s.mu.Lock()
		if slot.done == done { // sigue siendo esta encarnación de la plaza
			slot.cancel = nil
			slot.done = nil
			slot.state = slotIdle
		}
		if retired {
			slot.state = slotRetired
		}
		active := s.activeLocked(archetype)
		s.mu.Unlock()
		s.metrics.Active.WithLabelValues(archetype).Set(float64(active))
	}()
}

// close rechaza arranques posteriores: se invoca al apagar, antes de wait, para
// que ninguna plaza se lance mientras se espera el cierre de la población.
func (s *supervisor) close() {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
}

// wait espera a que todas las goroutines de la población terminen.
func (s *supervisor) wait() { s.wg.Wait() }

// jitterTick aplica jitter uniforme ±20% al tick.
func jitterTick(d time.Duration) time.Duration {
	j := time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	if j <= 0 {
		return d
	}
	return j
}

// sleepCtx duerme d respetando la cancelación del contexto.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
