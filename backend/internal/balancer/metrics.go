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

	// Mercado spot eléctrico (Fase 3, ADR-025).
	powerSpotPrice      *prometheus.GaugeVec   // ii_power_spot_price{region}
	powerSupplied       *prometheus.CounterVec // ii_power_supplied_units_total{region}
	powerCurtailedUnits *prometheus.CounterVec // ii_power_curtailed_units_total{region}
	powerCurtailments   *prometheus.CounterVec // ii_power_curtailments_total{region}
	powerFuelBurned     *prometheus.CounterVec // ii_power_fuel_burned_total{product}
	powerTickDuration   prometheus.Histogram   // ii_power_spot_tick_duration_seconds
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
		powerSpotPrice: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_power_spot_price",
			Help: "Precio de cierre del último tick del mercado spot eléctrico, por región (0 = sin despacho).",
		}, []string{"region"}),
		powerSupplied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_power_supplied_units_total",
			Help: "Unidades de energía despachadas por el mercado spot, por región.",
		}, []string{"region"}),
		powerCurtailedUnits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_power_curtailed_units_total",
			Help: "Unidades de energía demandadas y NO servidas (déficit + insolvencia), por región.",
		}, []string{"region"}),
		powerCurtailments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_power_curtailments_total",
			Help: "Edificios recortados por tick del spot (recorte rotatorio, GDD 5.8), por región.",
		}, []string{"region"}),
		powerFuelBurned: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_power_fuel_burned_total",
			Help: "Combustible físico quemado por las térmicas despachadas, por producto (ADR-022).",
		}, []string{"product"}),
		powerTickDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ii_power_spot_tick_duration_seconds",
			Help:    "Duración de la liquidación de cada tick del mercado spot (por región).",
			Buckets: prometheus.DefBuckets,
		}),
	}
	if reg != nil {
		reg.MustRegister(m.buysPublished, m.cityEmission, m.consumed, m.cityLevel,
			m.levelChanges, m.recalcDuration, m.moneySupply,
			m.analyticsDuration, m.macroMoneySupply, m.simulatedGDP, m.globalDepletionRate, m.taxRateBP,
			m.powerSpotPrice, m.powerSupplied, m.powerCurtailedUnits, m.powerCurtailments,
			m.powerFuelBurned, m.powerTickDuration)
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

func (m *Metrics) setPowerSpotPrice(region string, price int64) {
	if m != nil {
		m.powerSpotPrice.WithLabelValues(region).Set(float64(price))
	}
}

func (m *Metrics) addPowerSupplied(region string, units int64) {
	if m != nil && units > 0 {
		m.powerSupplied.WithLabelValues(region).Add(float64(units))
	}
}

func (m *Metrics) addPowerCurtailed(region string, units int64, buildings int) {
	if m != nil {
		if units > 0 {
			m.powerCurtailedUnits.WithLabelValues(region).Add(float64(units))
		}
		if buildings > 0 {
			m.powerCurtailments.WithLabelValues(region).Add(float64(buildings))
		}
	}
}

func (m *Metrics) addPowerFuelBurned(product string, units int64) {
	if m != nil && units > 0 {
		m.powerFuelBurned.WithLabelValues(product).Add(float64(units))
	}
}

func (m *Metrics) observePowerTick(seconds float64) {
	if m != nil {
		m.powerTickDuration.Observe(seconds)
	}
}
