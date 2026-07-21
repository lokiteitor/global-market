package stress

import "github.com/prometheus/client_golang/prometheus"

// Metrics es la instrumentación Prometheus PROPIA DEL HARNESS (expuesta en
// II_STRESS_ADDR). No confundir con las métricas del sistema bajo prueba, que
// el harness RASPA al terminar para el informe (ver probe.go).
type Metrics struct {
	// Ops cuenta las operaciones por tipo y resultado
	// (ii_stress_ops_total{op,outcome}).
	Ops *prometheus.CounterVec
	// Duration mide la latencia de cada petición emitida
	// (ii_stress_op_duration_seconds{op}).
	Duration *prometheus.HistogramVec
	// Errors cuenta los errores por tipo y clase de status
	// (ii_stress_errors_total{op,class}).
	Errors *prometheus.CounterVec
	// ActiveBots publica los bots actualmente en carga (ii_stress_active_bots).
	ActiveBots prometheus.Gauge
	// ProvisionedBots publica las cuentas aprovisionadas del run
	// (ii_stress_provisioned_bots).
	ProvisionedBots prometheus.Gauge
}

// NewMetrics registra las métricas del harness en el registry (nil = sin
// instrumentar, para tests).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Ops: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_stress_ops_total",
			Help: "Total de operaciones del harness de stress, por tipo y resultado (ok|skipped|error).",
		}, []string{"op", "outcome"}),
		Duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ii_stress_op_duration_seconds",
			Help:    "Latencia de cada petición emitida por el harness contra la API pública.",
			Buckets: prometheus.DefBuckets,
		}, []string{"op"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_stress_errors_total",
			Help: "Errores del harness por operación y clase de status (429, 409, 422, 4xx, 5xx, network).",
		}, []string{"op", "class"}),
		ActiveBots: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_stress_active_bots",
			Help: "Bots del harness actualmente generando carga.",
		}),
		ProvisionedBots: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_stress_provisioned_bots",
			Help: "Cuentas aprovisionadas por la corrida de stress.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.Ops, m.Duration, m.Errors, m.ActiveBots, m.ProvisionedBots)
	}
	return m
}

// observe refleja un Result en las métricas del harness.
func (m *Metrics) observe(r Result) {
	if m == nil {
		return
	}
	m.Ops.WithLabelValues(string(r.Op), string(r.Outcome)).Inc()
	if r.Outcome == OutcomeSkipped {
		return
	}
	m.Duration.WithLabelValues(string(r.Op)).Observe(r.Latency.Seconds())
	if r.Outcome == OutcomeError {
		class := r.Class
		if class == "" {
			class = ClassNetwork
		}
		m.Errors.WithLabelValues(string(r.Op), class).Inc()
	}
}
