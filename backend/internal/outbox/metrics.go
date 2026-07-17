package outbox

import "github.com/prometheus/client_golang/prometheus"

// Métricas del módulo. Son variables de paquete porque Emit es una función de
// paquete (contrato del módulo), pero NO hay registro global: cada binario
// las registra en su propio registry con RegisterMetrics, coherente con el
// resto de la plataforma (internal/platform/metrics).
var (
	// eventsEmitted cuenta los eventos insertados por Emit, por tipo. Cuenta
	// inserciones dentro de la transacción del emisor: si esa transacción se
	// revierte el evento no existirá, pero el intento queda contado.
	eventsEmitted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ii_outbox_events_emitted_total",
		Help: "Total de eventos insertados en el outbox por Emit, por tipo de evento.",
	}, []string{"event_type"})

	// eventsProcessed cuenta los eventos procesados Y CONFIRMADOS por cada
	// consumidor (los lotes revertidos no cuentan).
	eventsProcessed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ii_outbox_events_processed_total",
		Help: "Total de eventos procesados y confirmados por cada consumidor.",
	}, []string{"consumer"})

	// consumerLag es el retraso de cada consumidor: max(seq) del outbox menos
	// su cursor, actualizado en cada polling (incluye eventos de tipos a los
	// que el consumidor no está suscrito).
	consumerLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ii_outbox_consumer_lag",
		Help: "Retraso del consumidor: max(seq) del outbox menos su cursor, actualizado en cada polling.",
	}, []string{"consumer"})
)

// RegisterMetrics registra las métricas del módulo en el registry del
// binario. Debe llamarse UNA sola vez por registry: MustRegister entra en
// pánico ante un registro duplicado (configuración rota = no arrancar).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(eventsEmitted, eventsProcessed, consumerLag)
}
