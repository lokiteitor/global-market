package clock

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// shutdownPersistTimeout acota el re-anclaje final durante el apagado, cuando
// el contexto del proceso ya está cancelado.
const shutdownPersistTimeout = 5 * time.Second

// AnchorLoader lee el ancla persistida. Es todo lo que necesita el Reader.
type AnchorLoader interface {
	Load(ctx context.Context) (Anchor, error)
}

// AnchorStore es el acceso completo al ancla que necesita el Clock.
type AnchorStore interface {
	AnchorLoader
	EnsureExists(ctx context.Context) error
	PersistAnchor(ctx context.Context, derived simtime.SimTime) error
}

// Clock es el reloj de simulación del motor: mantiene el ancla cacheada en
// memoria, deriva el sim-time actual bajo demanda (sin ticks) y en segundo
// plano re-persiste el ancla y la refresca desde la BD.
//
// Todos los métodos son seguros para uso concurrente.
type Clock struct {
	store  AnchorStore
	opts   Options
	logger *slog.Logger
	nowFn  func() time.Time // inyectable en tests

	mu      sync.RWMutex
	anchor  Anchor
	loaded  bool
	started bool
	cancel  context.CancelFunc
	done    chan struct{}

	simGauge    prometheus.GaugeFunc
	frozenGauge prometheus.GaugeFunc
}

// New construye el Clock sobre un AnchorStore. Si reg no es nil registra los
// gauges ii_sim_time_seconds (sim-time actual derivado) y ii_sim_clock_frozen
// (1 si el mundo está congelado), evaluados en el momento del scrape.
func New(store AnchorStore, opts Options, logger *slog.Logger, reg prometheus.Registerer) *Clock {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Clock{
		store:  store,
		opts:   opts,
		logger: logger,
		nowFn:  time.Now,
		done:   make(chan struct{}),
	}
	c.simGauge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ii_sim_time_seconds",
		Help: "Sim-time actual derivado del ancla, en segundos de simulación desde el génesis.",
	}, func() float64 { return float64(c.Now()) })
	c.frozenGauge = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "ii_sim_clock_frozen",
		Help: "1 si el mundo está congelado (el sim-time no avanza), 0 en caso contrario.",
	}, func() float64 {
		if c.Frozen() {
			return 1
		}
		return 0
	})
	if reg != nil {
		reg.MustRegister(c.simGauge, c.frozenGauge)
	}
	return c
}

// Start garantiza la fila del reloj (EnsureExists), carga el ancla en la
// caché y lanza la goroutine de mantenimiento, que cada RefreshInterval relee
// el ancla (por si otro proceso la cambió, p. ej. frozen) y cada
// PersistInterval re-persiste el ancla derivada. La goroutine termina cuando
// ctx se cancela, re-anclando una última vez; Stop espera a que acabe.
func (c *Clock) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return errors.New("clock: Start llamado más de una vez")
	}
	c.mu.Unlock()

	if err := c.store.EnsureExists(ctx); err != nil {
		return err
	}
	a, err := c.store.Load(ctx)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.anchor = a
	c.loaded = true
	c.started = true
	c.cancel = cancel
	c.mu.Unlock()

	go c.run(runCtx)
	return nil
}

// Stop detiene la goroutine de mantenimiento y espera su cierre limpio
// (incluido el re-anclaje final). Es segura si Start falló o no se llamó.
func (c *Clock) Stop() {
	c.mu.Lock()
	started := c.started
	cancel := c.cancel
	c.mu.Unlock()
	if !started {
		return
	}
	cancel()
	<-c.done
}

// Now devuelve el sim-time actual derivado del ancla cacheada. Antes de Start
// devuelve el génesis (0). El resultado nunca es negativo (dominio sim_time).
func (c *Clock) Now() simtime.SimTime {
	c.mu.RLock()
	a, loaded := c.anchor, c.loaded
	c.mu.RUnlock()
	if !loaded {
		return 0
	}
	derived := simtime.Derive(a.SimTimeAt, a.WallAnchor, c.nowFn(), a.Ratio, a.Frozen)
	if derived < 0 {
		return 0
	}
	return derived
}

// Frozen indica si, según el ancla cacheada, el mundo está congelado.
func (c *Clock) Frozen() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded && c.anchor.Frozen
}

// run es el bucle de mantenimiento: refresco de caché y re-anclaje periódico.
func (c *Clock) run(ctx context.Context) {
	defer close(c.done)
	refresh := time.NewTicker(c.opts.RefreshInterval)
	defer refresh.Stop()
	persist := time.NewTicker(c.opts.PersistInterval)
	defer persist.Stop()

	for {
		select {
		case <-ctx.Done():
			c.persistOnShutdown()
			return
		case <-refresh.C:
			c.refresh(ctx)
		case <-persist.C:
			c.persist(ctx)
		}
	}
}

// refresh relee el ancla de la BD y actualiza la caché. Un fallo transitorio
// no detiene el reloj: se sigue derivando de la última ancla conocida.
func (c *Clock) refresh(ctx context.Context) {
	a, err := c.store.Load(ctx)
	if err != nil {
		c.logger.Warn("clock: no se pudo refrescar el ancla; se sigue derivando de la última conocida",
			slog.Any("error", err))
		return
	}
	c.mu.Lock()
	prev := c.anchor
	c.anchor = a
	c.mu.Unlock()
	if prev.Frozen != a.Frozen {
		c.logger.Info("clock: cambio de estado del reloj de simulación",
			slog.Bool("frozen", a.Frozen),
			slog.String("sim_time", simtime.Format(a.SimTimeAt)))
	}
}

// persist re-ancla el reloj con el sim-time derivado ahora. Con el mundo
// congelado no hay nada que persistir (y el UPDATE lo garantiza en la BD
// aunque la caché estuviera desactualizada).
func (c *Clock) persist(ctx context.Context) {
	if c.Frozen() {
		return
	}
	derived := c.Now()
	if err := c.store.PersistAnchor(ctx, derived); err != nil {
		c.logger.Warn("clock: no se pudo persistir el ancla", slog.Any("error", err))
	}
}

// persistOnShutdown re-ancla una última vez durante el apagado, con un
// contexto propio porque el del proceso ya está cancelado.
func (c *Clock) persistOnShutdown() {
	if c.Frozen() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownPersistTimeout)
	defer cancel()
	derived := c.Now()
	if err := c.store.PersistAnchor(ctx, derived); err != nil {
		c.logger.Warn("clock: no se pudo persistir el ancla en el apagado", slog.Any("error", err))
		return
	}
	c.logger.Info("clock: ancla persistida en el apagado",
		slog.String("sim_time", simtime.Format(derived)))
}
