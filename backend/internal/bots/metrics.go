package bots

import "github.com/prometheus/client_golang/prometheus"

// Metrics es la instrumentación Prometheus del Bot Orchestration Service.
type Metrics struct {
	// Decisions cuenta las decisiones tomadas por cada bot
	// (ii_bot_decisions_total{bot,decision}).
	Decisions *prometheus.CounterVec
	// Errors cuenta los fallos de Decide (incluidos pánicos recuperados) por
	// bot (ii_bot_errors_total{bot}).
	Errors *prometheus.CounterVec
	// Cash publica la caja de cada bot en unidades menores, actualizada al
	// decidir (ii_bot_cash{bot}).
	Cash *prometheus.GaugeVec
	// Active publica cuántos bots de cada arquetipo están ejecutando su bucle
	// de decisión (ii_bots_active{archetype}): la población que la densidad
	// dinámica gobierna, no la aprovisionada.
	Active *prometheus.GaugeVec
}

// NewMetrics registra las métricas del módulo en el registry (nil = sin
// instrumentar, para tests).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_bot_decisions_total",
			Help: "Total de decisiones tomadas por cada bot, por tipo de decisión.",
		}, []string{"bot", "decision"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_bot_errors_total",
			Help: "Total de errores de la pasada de decisión de cada bot (incluye pánicos recuperados).",
		}, []string{"bot"}),
		Cash: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_bot_cash",
			Help: "Caja de cada bot en unidades menores de dinero, actualizada al decidir.",
		}, []string{"bot"}),
		Active: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_bots_active",
			Help: "Bots de cada arquetipo ejecutando su bucle de decisión (población activa, válvula de carga del GDD §19).",
		}, []string{"archetype"}),
	}
	if reg != nil {
		reg.MustRegister(m.Decisions, m.Errors, m.Cash, m.Active)
	}
	return m
}
