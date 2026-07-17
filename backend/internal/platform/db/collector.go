package db

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// poolCollector exporta las estadísticas de pgxpool.Stat como métricas
// Prometheus (collector custom: lee el estado bajo demanda en cada scrape).
type poolCollector struct {
	pool *pgxpool.Pool

	acquireCount         *prometheus.Desc
	acquireDuration      *prometheus.Desc
	acquiredConns        *prometheus.Desc
	canceledAcquireCount *prometheus.Desc
	constructingConns    *prometheus.Desc
	emptyAcquireCount    *prometheus.Desc
	idleConns            *prometheus.Desc
	maxConns             *prometheus.Desc
	totalConns           *prometheus.Desc
	newConnsCount        *prometheus.Desc
	maxLifetimeDestroys  *prometheus.Desc
	maxIdleDestroys      *prometheus.Desc
}

// NewPoolCollector construye el collector Prometheus del pool. Registrarlo
// en el registry del servicio: reg.MustRegister(db.NewPoolCollector(pool)).
func NewPoolCollector(pool *pgxpool.Pool) prometheus.Collector {
	return &poolCollector{
		pool: pool,
		acquireCount: prometheus.NewDesc("ii_pgxpool_acquires_total",
			"Total de adquisiciones de conexión del pool.", nil, nil),
		acquireDuration: prometheus.NewDesc("ii_pgxpool_acquire_duration_seconds_total",
			"Tiempo acumulado esperando adquirir conexiones, en segundos.", nil, nil),
		acquiredConns: prometheus.NewDesc("ii_pgxpool_acquired_conns",
			"Conexiones actualmente adquiridas (en uso).", nil, nil),
		canceledAcquireCount: prometheus.NewDesc("ii_pgxpool_canceled_acquires_total",
			"Adquisiciones canceladas por contexto.", nil, nil),
		constructingConns: prometheus.NewDesc("ii_pgxpool_constructing_conns",
			"Conexiones en proceso de construcción.", nil, nil),
		emptyAcquireCount: prometheus.NewDesc("ii_pgxpool_empty_acquires_total",
			"Adquisiciones que esperaron porque el pool estaba vacío.", nil, nil),
		idleConns: prometheus.NewDesc("ii_pgxpool_idle_conns",
			"Conexiones abiertas ociosas.", nil, nil),
		maxConns: prometheus.NewDesc("ii_pgxpool_max_conns",
			"Tamaño máximo configurado del pool.", nil, nil),
		totalConns: prometheus.NewDesc("ii_pgxpool_total_conns",
			"Conexiones abiertas totales (adquiridas + ociosas + en construcción).", nil, nil),
		newConnsCount: prometheus.NewDesc("ii_pgxpool_new_conns_total",
			"Conexiones nuevas abiertas desde el arranque.", nil, nil),
		maxLifetimeDestroys: prometheus.NewDesc("ii_pgxpool_max_lifetime_destroys_total",
			"Conexiones destruidas por exceder MaxConnLifetime.", nil, nil),
		maxIdleDestroys: prometheus.NewDesc("ii_pgxpool_max_idle_destroys_total",
			"Conexiones destruidas por exceder MaxConnIdleTime.", nil, nil),
	}
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	prometheus.DescribeByCollect(c, ch)
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, s.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.constructingConns, prometheus.GaugeValue, float64(s.ConstructingConns()))
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(c.newConnsCount, prometheus.CounterValue, float64(s.NewConnsCount()))
	ch <- prometheus.MustNewConstMetric(c.maxLifetimeDestroys, prometheus.CounterValue, float64(s.MaxLifetimeDestroyCount()))
	ch <- prometheus.MustNewConstMetric(c.maxIdleDestroys, prometheus.CounterValue, float64(s.MaxIdleDestroyCount()))
}
