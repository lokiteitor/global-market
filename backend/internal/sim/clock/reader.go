package clock

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Reader es el lector ligero del reloj de simulación para procesos que no
// son el motor (p. ej. el gateway, que estampa sim_time en el meta de cada
// respuesta). Cachea el ancla con un TTL y deriva el sim-time localmente;
// como la derivación es analítica, el tiempo avanza entre recargas sin tocar
// la BD. Si una recarga falla, sigue derivando de la última ancla conocida y
// lo deja registrado como warning: nunca rompe una respuesta.
//
// Todos los métodos son seguros para uso concurrente.
type Reader struct {
	store  AnchorLoader
	ttl    time.Duration
	logger *slog.Logger
	nowFn  func() time.Time // inyectable en tests

	mu        sync.Mutex
	anchor    Anchor
	loaded    bool
	loading   bool
	fetchedAt time.Time
}

// NewReader construye el lector sobre un AnchorLoader (normalmente *Store).
func NewReader(store AnchorLoader, opts ReaderOptions, logger *slog.Logger) *Reader {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reader{
		store:  store,
		ttl:    opts.CacheTTL,
		logger: logger,
		nowFn:  time.Now,
	}
}

// Now devuelve el sim-time actual derivado del ancla cacheada, recargándola
// de la BD si el TTL expiró. Solo una recarga vuela a la vez: las peticiones
// concurrentes derivan de la caché vigente sin bloquearse. Si la recarga
// falla se conserva la última ancla conocida (el fallo también consume un
// TTL, para no martillear una BD caída). Antes de la primera carga con éxito
// devuelve el génesis (0). El resultado nunca es negativo (dominio sim_time).
func (r *Reader) Now(ctx context.Context) simtime.SimTime {
	r.mu.Lock()
	stale := !r.loaded || r.nowFn().Sub(r.fetchedAt) >= r.ttl
	if stale && !r.loading {
		r.loading = true
		r.mu.Unlock()

		a, err := r.store.Load(ctx)

		r.mu.Lock()
		r.loading = false
		r.fetchedAt = r.nowFn()
		if err != nil {
			r.logger.Warn("clock: no se pudo recargar el ancla; se sigue derivando de la última conocida",
				slog.Any("error", err))
		} else {
			r.anchor = a
			r.loaded = true
		}
	}
	a, loaded := r.anchor, r.loaded
	r.mu.Unlock()

	if !loaded {
		return 0
	}
	derived := simtime.Derive(a.SimTimeAt, a.WallAnchor, r.nowFn(), a.Ratio, a.Frozen)
	if derived < 0 {
		return 0
	}
	return derived
}
