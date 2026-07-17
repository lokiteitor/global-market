package auth

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// Variables de entorno del módulo auth (prefijo II_, 12-factor).
const (
	// EnvRateLoginPerMin es el límite de intentos de login por minuto y
	// clave IP+nombre. Default 5.
	EnvRateLoginPerMin = "II_RATE_LOGIN_PER_MIN"
	// EnvRateAPIRPS es la tasa sostenida de peticiones autenticadas por
	// segundo y cuenta. Default 20.
	EnvRateAPIRPS = "II_RATE_API_RPS"
	// EnvRateAPIBurst es la ráfaga máxima de peticiones autenticadas por
	// cuenta. Default 40.
	EnvRateAPIBurst = "II_RATE_API_BURST"
)

// Valores por defecto de las políticas de rate limiting. Son IDÉNTICAS para
// humanos y bots (GDD §9: decisión de balance, no solo de protección).
const (
	DefaultRateLoginPerMin = 5
	DefaultRateAPIRPS      = 20.0
	DefaultRateAPIBurst    = 40
)

// Options es la configuración propia del módulo auth.
type Options struct {
	// LoginPerMin es el límite de intentos de login por minuto por IP+nombre.
	LoginPerMin int
	// APIRPS es la tasa sostenida de la API autenticada, por cuenta.
	APIRPS float64
	// APIBurst es la ráfaga máxima de la API autenticada, por cuenta.
	APIBurst int
}

// OptionsFromEnv construye las Options desde el entorno con los defaults
// documentados. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := Options{
		LoginPerMin: DefaultRateLoginPerMin,
		APIRPS:      DefaultRateAPIRPS,
		APIBurst:    DefaultRateAPIBurst,
	}
	if v := strings.TrimSpace(os.Getenv(EnvRateLoginPerMin)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Options{}, fmt.Errorf("auth: %s inválido %q (entero >= 1)", EnvRateLoginPerMin, v)
		}
		opts.LoginPerMin = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvRateAPIRPS)); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || math.IsInf(f, 0) {
			return Options{}, fmt.Errorf("auth: %s inválido %q (número > 0)", EnvRateAPIRPS, v)
		}
		opts.APIRPS = f
	}
	if v := strings.TrimSpace(os.Getenv(EnvRateAPIBurst)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Options{}, fmt.Errorf("auth: %s inválido %q (entero >= 1)", EnvRateAPIBurst, v)
		}
		opts.APIBurst = n
	}
	return opts, nil
}

// sweepInterval es la cadencia mínima entre barridos de claves inactivas.
const sweepInterval = time.Minute

// Limiter es un token bucket en memoria, concurrente-seguro, con un bucket
// por clave y limpieza opportunista de claves inactivas (sin goroutine de
// fondo: el barrido ocurre dentro de Allow como máximo una vez por
// sweepInterval).
type Limiter struct {
	mu        sync.Mutex
	rate      float64 // tokens por segundo
	burst     float64 // capacidad del bucket
	buckets   map[string]*bucket
	lastSweep time.Time
	// now es inyectable en tests; time.Now en producción.
	now func() time.Time
}

// bucket es el estado de una clave: tokens disponibles y último refill.
type bucket struct {
	tokens float64
	last   time.Time
}

// NewLimiter construye un token bucket con tasa sostenida ratePerSec y
// capacidad burst. Ambos deben ser > 0.
func NewLimiter(ratePerSec float64, burst int) *Limiter {
	if ratePerSec <= 0 {
		panic("auth: NewLimiter requiere ratePerSec > 0")
	}
	if burst < 1 {
		panic("auth: NewLimiter requiere burst >= 1")
	}
	l := &Limiter{
		rate:    ratePerSec,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
	l.lastSweep = l.now()
	return l
}

// Allow consume un token del bucket de la clave. Si no hay tokens devuelve
// (false, espera) donde espera es el tiempo hasta que se acumule el próximo
// token (base de la cabecera Retry-After).
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.maybeSweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
			b.last = now
		}
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
	return false, wait
}

// maybeSweep elimina, como máximo una vez por sweepInterval, los buckets
// inactivos el tiempo suficiente para estar llenos: son indistinguibles de
// un bucket recién creado, así que retirarlos no altera la semántica.
// Se llama con l.mu tomado.
func (l *Limiter) maybeSweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepInterval {
		return
	}
	l.lastSweep = now
	full := time.Duration(l.burst / l.rate * float64(time.Second))
	for key, b := range l.buckets {
		if now.Sub(b.last) >= full {
			delete(l.buckets, key)
		}
	}
}

// size devuelve el número de claves con bucket vivo (tests).
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// Ámbitos de la métrica ii_rate_limited_total.
const (
	ScopeLogin = "login"
	ScopeAPI   = "api"
)

// Metrics agrupa la instrumentación Prometheus del módulo auth.
type Metrics struct {
	rateLimited *prometheus.CounterVec
}

// NewMetrics registra las métricas del módulo en el registry del servicio
// (metrics.Metrics.Registry() de la plataforma).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	rateLimited := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ii_rate_limited_total",
		Help: "Total de peticiones rechazadas por rate limiting, por ámbito.",
	}, []string{"scope"})
	reg.MustRegister(rateLimited)
	return &Metrics{rateLimited: rateLimited}
}

// RateLimited incrementa el contador de rechazos del ámbito dado.
func (m *Metrics) RateLimited(scope string) {
	if m == nil {
		return
	}
	m.rateLimited.WithLabelValues(scope).Inc()
}

// writeRateLimited responde 429 con el envelope RATE_LIMITED del contrato y
// la cabecera Retry-After en segundos (mínimo 1, redondeo hacia arriba).
func writeRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	secs := int64(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	httpx.WriteError(w, http.StatusTooManyRequests, CodeRateLimited,
		"límite de peticiones excedido; reintenta más tarde",
		map[string]any{"retry_after_seconds": secs})
}
