package balancer

import "github.com/prometheus/client_golang/prometheus"

// Metrics reúne los contadores/gauges del Balancer. reg nil los deja sin
// registrar (tests, herramientas).
type Metrics struct {
	buysPublished  *prometheus.CounterVec // ii_city_buys_published_total{product}
	cityEmission   prometheus.Counter     // ii_city_emission_total (faucet)
	consumed       *prometheus.CounterVec // ii_city_consumed_total{product}
	cityLevel      *prometheus.GaugeVec   // ii_city_level{city}
	levelChanges   *prometheus.CounterVec // ii_city_level_changes_total{direction}
	recalcDuration prometheus.Histogram   // ii_balancer_recalc_duration_seconds
	moneySupply    prometheus.Gauge       // ii_balancer_money_supply (masa monetaria)

	// Espejo macro (Incremento 6b): gauges del job de analítica y del lazo fiscal.
	analyticsDuration   prometheus.Histogram // ii_balancer_analytics_duration_seconds
	macroMoneySupply    prometheus.Gauge     // ii_money_supply
	simulatedGDP        prometheus.Gauge     // ii_simulated_gdp
	globalDepletionRate prometheus.Gauge     // ii_global_depletion_rate
	taxRateBP           *prometheus.GaugeVec // ii_tax_rate_bp{region}
}

// NewMetrics construye e (si reg != nil) registra las métricas del Balancer.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		buysPublished: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_city_buys_published_total",
			Help: "Total de solicitudes de compra de ciudad publicadas en el tablón, por producto.",
		}, []string{"product"}),
		cityEmission: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_city_emission_total",
			Help: "Total de dinero emitido para pre-fondear a las ciudades (faucet, GDD 5.5).",
		}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_city_consumed_total",
			Help: "Total de unidades consumidas por las ciudades (sumidero final), por producto.",
		}, []string{"product"}),
		cityLevel: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_city_level",
			Help: "Nivel de desarrollo actual de cada ciudad.",
		}, []string{"city"}),
		levelChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_city_level_changes_total",
			Help: "Total de cambios de nivel de ciudad, por dirección (up|down).",
		}, []string{"direction"}),
		recalcDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ii_balancer_recalc_duration_seconds",
			Help:    "Duración de cada barrido de recálculo de demanda del Balancer.",
			Buckets: prometheus.DefBuckets,
		}),
		moneySupply: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_balancer_money_supply",
			Help: "Masa monetaria total (suma de cuentas cash/escrow/guarantee) observada en el refresco macro.",
		}),
		analyticsDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ii_balancer_analytics_duration_seconds",
			Help:    "Duración de cada barrido macro del Balancer (analítica + fórmula laboral + ajuste fiscal).",
			Buckets: prometheus.DefBuckets,
		}),
		macroMoneySupply: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_money_supply",
			Help: "Masa monetaria total del último bucket de analítica (cash+escrow+guarantee).",
		}),
		simulatedGDP: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_simulated_gdp",
			Help: "PIB simulado del último bucket de analítica (valor de contratos liquidados en el bucket).",
		}),
		globalDepletionRate: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_global_depletion_rate",
			Help: "Ritmo global de agotamiento de yacimientos finitos (unidades por día de juego).",
		}),
		taxRateBP: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_tax_rate_bp",
			Help: "Tipo impositivo vigente por región tras el ajuste fiscal, en puntos básicos.",
		}, []string{"region"}),
	}
	if reg != nil {
		reg.MustRegister(m.buysPublished, m.cityEmission, m.consumed, m.cityLevel,
			m.levelChanges, m.recalcDuration, m.moneySupply,
			m.analyticsDuration, m.macroMoneySupply, m.simulatedGDP, m.globalDepletionRate, m.taxRateBP)
	}
	return m
}

func (m *Metrics) incBuyPublished(product string) {
	if m != nil {
		m.buysPublished.WithLabelValues(product).Inc()
	}
}

func (m *Metrics) addEmission(amount int64) {
	if m != nil {
		m.cityEmission.Add(float64(amount))
	}
}

func (m *Metrics) addConsumed(product string, qty int64) {
	if m != nil {
		m.consumed.WithLabelValues(product).Add(float64(qty))
	}
}

func (m *Metrics) setCityLevel(city string, level int32) {
	if m != nil {
		m.cityLevel.WithLabelValues(city).Set(float64(level))
	}
}

func (m *Metrics) incLevelChange(direction string) {
	if m != nil {
		m.levelChanges.WithLabelValues(direction).Inc()
	}
}

func (m *Metrics) observeRecalc(seconds float64) {
	if m != nil {
		m.recalcDuration.Observe(seconds)
	}
}

func (m *Metrics) setMoneySupply(v int64) {
	if m != nil {
		m.moneySupply.Set(float64(v))
	}
}

func (m *Metrics) observeAnalytics(seconds float64) {
	if m != nil {
		m.analyticsDuration.Observe(seconds)
	}
}

func (m *Metrics) setMacroMoneySupply(v int64) {
	if m != nil {
		m.macroMoneySupply.Set(float64(v))
	}
}

func (m *Metrics) setSimulatedGDP(v int64) {
	if m != nil {
		m.simulatedGDP.Set(float64(v))
	}
}

func (m *Metrics) setGlobalDepletionRate(v float64) {
	if m != nil {
		m.globalDepletionRate.Set(v)
	}
}

func (m *Metrics) setTaxRateBP(region string, bp int32) {
	if m != nil {
		m.taxRateBP.WithLabelValues(region).Set(float64(bp))
	}
}
