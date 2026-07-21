package notify

import "github.com/prometheus/client_golang/prometheus"

// Metrics agrupa la instrumentación Prometheus del Notification Gateway. Se
// registra en el registry del binario que lo hospeda (patrón de la
// plataforma: sin registry global). nil deja el módulo sin instrumentar
// (tests): todos los métodos son nil-safe.
type Metrics struct {
	// connections es el número de conexiones WS registradas en el hub.
	connections prometheus.Gauge
	// framesSent cuenta los frames aceptados hacia el buffer de envío de una
	// conexión (control y eventos). Un frame descartado por cierre de cliente
	// lento NO cuenta: dispara slowClientCloses.
	framesSent prometheus.Counter
	// slowClientCloses cuenta los cierres 1013 por buffer de envío lleno.
	slowClientCloses prometheus.Counter
	// eventsRouted cuenta los eventos del outbox despachados por el router,
	// por tipo de evento. Cuenta despachos EJECUTADOS dentro de la transacción
	// del lote: un lote revertido y reintentado vuelve a contar (misma
	// convención que ii_outbox_events_emitted_total; entrega at-least-once
	// hacia sockets efímeros, ADR-023).
	eventsRouted *prometheus.CounterVec
}

// NewMetrics construye las métricas del módulo y las registra en reg (nil las
// deja sin registrar). MustRegister entra en pánico ante un registro
// duplicado: llamar una sola vez por registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		connections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_ws_connections",
			Help: "Conexiones WebSocket registradas en el hub del Notification Gateway.",
		}),
		framesSent: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_ws_frames_sent_total",
			Help: "Frames aceptados hacia el buffer de envío de las conexiones WS (control y eventos).",
		}),
		slowClientCloses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_ws_slow_client_closes_total",
			Help: "Conexiones WS cerradas con 1013 por buffer de envío lleno (cliente lento).",
		}),
		eventsRouted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_ws_events_routed_total",
			Help: "Eventos del outbox despachados por el router del Notification Gateway, por tipo.",
		}, []string{"event_type"}),
	}
	if reg != nil {
		reg.MustRegister(m.connections, m.framesSent, m.slowClientCloses, m.eventsRouted)
	}
	return m
}

func (m *Metrics) connOpened() {
	if m != nil {
		m.connections.Inc()
	}
}

func (m *Metrics) connClosed() {
	if m != nil {
		m.connections.Dec()
	}
}

func (m *Metrics) frameSent() {
	if m != nil {
		m.framesSent.Inc()
	}
}

func (m *Metrics) slowClientClose() {
	if m != nil {
		m.slowClientCloses.Inc()
	}
}

func (m *Metrics) eventRouted(eventType string) {
	if m != nil {
		m.eventsRouted.WithLabelValues(eventType).Inc()
	}
}
