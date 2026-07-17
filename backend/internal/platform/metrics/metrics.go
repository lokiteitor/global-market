// Package metrics expone la instrumentación Prometheus de los binarios.
//
// Cada binario crea su propio registry (sin registro global) con los
// collectors estándar de Go/proceso y las métricas HTTP del contrato de
// observabilidad: ii_http_request_duration_seconds y ii_http_requests_total,
// ambas etiquetadas con service=gateway|engine.
package metrics

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Nombres de los servicios instrumentados (etiqueta service).
const (
	ServiceGateway = "gateway"
	ServiceEngine  = "engine"
)

// routeUnmatched es la etiqueta route de peticiones que no casan ninguna
// ruta del mux; evita que paths arbitrarios exploten la cardinalidad.
const routeUnmatched = "unmatched"

// Metrics agrupa el registry y las métricas HTTP de un servicio.
type Metrics struct {
	service      string
	registry     *prometheus.Registry
	httpDuration *prometheus.HistogramVec
	httpTotal    *prometheus.CounterVec
}

// New construye el registry del servicio con los collectors estándar de
// Go/proceso y las métricas HTTP registradas.
func New(service string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	constLabels := prometheus.Labels{"service": service}
	httpDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:        "ii_http_request_duration_seconds",
		Help:        "Duración de las peticiones HTTP servidas, en segundos.",
		Buckets:     prometheus.DefBuckets,
		ConstLabels: constLabels,
	}, []string{"method", "route", "status"})
	httpTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name:        "ii_http_requests_total",
		Help:        "Total de peticiones HTTP servidas.",
		ConstLabels: constLabels,
	}, []string{"method", "route", "status"})
	reg.MustRegister(httpDuration, httpTotal)

	return &Metrics{
		service:      service,
		registry:     reg,
		httpDuration: httpDuration,
		httpTotal:    httpTotal,
	}
}

// Service devuelve el nombre del servicio instrumentado.
func (m *Metrics) Service() string { return m.service }

// Registry expone el registry para registrar collectors adicionales
// (p. ej. el collector del pool de BD).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Handler devuelve el handler HTTP de /metrics para este registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{
		// Los errores de scrape se reflejan en el propio registry.
		Registry: m.registry,
	})
}

// Middleware observa cada petición en el histograma de duración y el
// contador de peticiones. La etiqueta route es el patrón de ruta casado por
// el ServeMux (nunca el path crudo) para acotar la cardinalidad.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		labels := []string{r.Method, routeLabel(r), strconv.Itoa(rec.status)}
		m.httpDuration.WithLabelValues(labels...).Observe(time.Since(start).Seconds())
		m.httpTotal.WithLabelValues(labels...).Inc()
	})
}

// routeLabel deriva la etiqueta route del patrón casado por el ServeMux,
// sin el prefijo de método ("GET /healthz" → "/healthz").
func routeLabel(r *http.Request) string {
	p := r.Pattern
	if p == "" {
		return routeUnmatched
	}
	if i := strings.IndexByte(p, ' '); i >= 0 {
		p = p[i+1:]
	}
	return p
}

// statusRecorder captura el status escrito para etiquetar las métricas.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap permite a http.ResponseController alcanzar el writer original.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
