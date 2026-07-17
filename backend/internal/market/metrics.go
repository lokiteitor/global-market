package market

import "github.com/prometheus/client_golang/prometheus"

// Metrics agrupa la instrumentación Prometheus del módulo. Se registra en el
// registry del binario que hospeda el agregador (patrón del resto de la
// plataforma: sin registry global). nil las deja sin instrumentar (tests).
type Metrics struct {
	// candlesUpserted cuenta las velas OHLC insertadas o actualizadas por el
	// agregador. Cuenta upserts EJECUTADOS dentro de la transacción del lote:
	// un lote revertido y reintentado vuelve a contar (misma convención que
	// ii_outbox_events_emitted_total, que cuenta intentos).
	candlesUpserted prometheus.Counter
}

// NewMetrics construye las métricas del módulo y las registra en reg (nil las
// deja sin registrar). MustRegister entra en pánico ante un registro
// duplicado: llamar una sola vez por registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		candlesUpserted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_ohlc_candles_upserted_total",
			Help: "Total de velas OHLC insertadas o actualizadas por el agregador a partir de contratos liquidados.",
		}),
	}
	if reg != nil {
		reg.MustRegister(m.candlesUpserted)
	}
	return m
}

// incCandleUpserted incrementa el contador de velas si las métricas están
// activas.
func (m *Metrics) incCandleUpserted() {
	if m != nil {
		m.candlesUpserted.Inc()
	}
}
