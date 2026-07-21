package stress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// El PROBE mide el SISTEMA BAJO PRUEBA al terminar la corrida, para que el
// informe incluya los DISPARADORES DE EVOLUCIÓN MEDIDOS del SAD §13:
//
//   - carga sostenida del proceso del motor → extracción de shards;
//   - volumen/lag de la outbox → Kafka;
//   - latencia de consulta del tablón → motor de búsqueda dedicado;
//   - contención SERIALIZABLE (reintentos y presupuestos agotados) → techo de
//     escritura del PostgreSQL único.
//
// Las fuentes son las propias métricas Prometheus del target (:8080 gateway,
// :8081 engine) y consultas de solo lectura a la BD del entorno de pruebas.
// Ninguna es obligatoria: si no son accesibles, el informe lo deja registrado y
// la corrida sigue siendo válida (la medición del harness es independiente).

// scrapeTimeout acota cada raspado de /metrics del target.
const scrapeTimeout = 10 * time.Second

// dbProbeTimeout acota las consultas de sondeo a la BD.
const dbProbeTimeout = 15 * time.Second

// boardRouteHints son las pistas, en orden de preferencia, con las que el probe
// localiza la etiqueta route del tablón dentro de las métricas HTTP del target.
// La etiqueta es el PATRÓN casado por el mux del gateway y depende de cómo esté
// montado el árbol de rutas ("/contracts/board" si el patrón es exacto,
// "/api/v1/contracts/" si el subárbol se monta con StripPrefix): el probe no
// asume ninguna forma concreta y deja registrado el valor que midió.
var boardRouteHints = []string{"board", "contracts"}

// Familias de la contención SERIALIZABLE que publican los binarios del sistema
// bajo prueba (internal/platform/db.RegisterTxMetrics, registradas en gateway,
// engine y bots).
const (
	txRetriesMetric   = "ii_tx_serialization_retries_total"
	txExhaustedMetric = "ii_tx_serialization_exhausted_total"
)

// TargetMetrics es el resultado de raspar un /metrics del sistema bajo prueba.
type TargetMetrics struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
	// Service es la etiqueta service de las métricas HTTP (gateway|engine).
	Service string `json:"service,omitempty"`
	// HTTPRequests es el total de peticiones HTTP servidas por el proceso.
	// ATENCIÓN: es un contador ACUMULADO desde el arranque del proceso, no lo
	// servido durante la corrida (contra un sistema de vida larga incluye
	// tráfico anterior). Lo atribuible a la corrida es HTTPRequestsDelta.
	HTTPRequests float64 `json:"http_requests_total,omitempty"`
	// HTTPErrors5xx es el total ACUMULADO de respuestas 5xx servidas. Lo
	// atribuible a la corrida es HTTPErrors5xxDelta.
	HTTPErrors5xx float64 `json:"http_5xx_total,omitempty"`
	// BaselineTaken indica que se raspó una LÍNEA BASE de este target antes de
	// la carga: sin ella los deltas no son atribuibles a esta corrida y el
	// veredicto no puede apoyarse en los contadores del sistema.
	BaselineTaken bool `json:"baseline_taken"`
	// HTTPRequestsDelta y HTTPErrors5xxDelta son las peticiones y las respuestas
	// 5xx que el sistema registró DURANTE la corrida (contador final − línea
	// base). Son las cifras que sostienen el veredicto; valen 0 si no hubo línea
	// base.
	HTTPRequestsDelta  float64 `json:"http_requests_delta"`
	HTTPErrors5xxDelta float64 `json:"http_5xx_delta"`
	// HTTPAvgMs es la latencia media servida (suma/cuenta del histograma).
	HTTPAvgMs float64 `json:"http_avg_ms,omitempty"`
	// BoardRoute es la etiqueta route sobre la que se midió el tablón.
	BoardRoute string `json:"board_route,omitempty"`
	// BoardRequests y BoardP95Ms son la carga y la latencia p95 SERVIDAS del
	// tablón (SAD §13: la latencia de consulta del tablón es el disparador del
	// motor de búsqueda dedicado).
	BoardRequests float64 `json:"board_requests_total,omitempty"`
	BoardP95Ms    float64 `json:"board_p95_ms,omitempty"`
	// TxMetricsPublished indica que el proceso publica la familia
	// ii_tx_serialization_* (la registra con db.RegisterTxMetrics). Sin este
	// distintivo un 0 sería ambiguo: «no hubo contención» y «no hay lectura» son
	// cosas distintas y el informe no puede confundirlas.
	TxMetricsPublished bool `json:"tx_metrics_published"`
	// TxRetries y TxExhausted son los contadores ACUMULADOS de la contención
	// SERIALIZABLE del proceso: reintentos ejecutados y transacciones que
	// agotaron su presupuesto. Lo atribuible a la corrida son los deltas.
	TxRetries   float64 `json:"tx_serialization_retries_total,omitempty"`
	TxExhausted float64 `json:"tx_serialization_exhausted_total,omitempty"`
	// TxRetriesDelta y TxExhaustedDelta son la contención MEDIDA DURANTE la
	// corrida (contador final − línea base). El agotamiento es el disparador
	// medido del SAD §13: cada incremento es una operación —del usuario o de un
	// trabajo de fondo— que NO se aplicó por contención de escritura.
	TxRetriesDelta   float64 `json:"tx_serialization_retries_delta"`
	TxExhaustedDelta float64 `json:"tx_serialization_exhausted_delta"`
	// OutboxLag es el mayor lag de consumidor de la outbox publicado por el
	// proceso (SAD §13: disparador de Kafka).
	OutboxLag float64 `json:"outbox_consumer_lag_max,omitempty"`
	// OutboxLagByConsumer detalla el lag por consumidor.
	OutboxLagByConsumer map[string]float64 `json:"outbox_consumer_lag,omitempty"`
	// EngineEventsProcessed es el total de eventos de outbox procesados.
	EngineEventsProcessed float64 `json:"outbox_events_processed_total,omitempty"`
	// GoGoroutines y ProcessCPUSeconds describen la carga del proceso (SAD §13:
	// disparador de la extracción de shards).
	GoGoroutines      float64 `json:"go_goroutines,omitempty"`
	ProcessCPUSeconds float64 `json:"process_cpu_seconds_total,omitempty"`
	ResidentBytes     float64 `json:"process_resident_memory_bytes,omitempty"`
}

// DatabaseProbe es el sondeo de solo lectura a la BD del entorno de pruebas.
type DatabaseProbe struct {
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
	// OutboxPending son los eventos de outbox aún no consumidos por el
	// consumidor más rezagado (lag real, medido en la fuente): eventos de SUS
	// tipos suscritos por encima de su cursor, nunca la cabecera global.
	OutboxPending int64 `json:"outbox_pending"`
	// OutboxEmittedDuringRun son los eventos emitidos durante la corrida.
	OutboxEmittedDuringRun int64 `json:"outbox_emitted_during_run"`
	// LivePublications son las publicaciones vivas del tablón al terminar.
	LivePublications int64 `json:"live_publications"`
	// PublicationsCreatedDuringRun son las publicaciones creadas durante la corrida.
	PublicationsCreatedDuringRun int64 `json:"publications_created_during_run"`
	// ContractsCreatedDuringRun son los contratos CCRI confirmados durante la corrida.
	ContractsCreatedDuringRun int64 `json:"contracts_created_during_run"`
	// ContractsPerSecond es el ritmo de confirmación de contratos de la corrida.
	ContractsPerSecond float64 `json:"contracts_per_second"`
	// StressAccounts son las cuentas del run (prefijo reconocible).
	StressAccounts int64 `json:"stress_accounts"`
	// StressAccountsActive son las que siguen activas (0 tras la limpieza).
	StressAccountsActive int64 `json:"stress_accounts_active"`
}

// ScrapeTargets raspa las métricas Prometheus del sistema bajo prueba. Nunca
// devuelve error: la inaccesibilidad de un target se refleja en el resultado.
func ScrapeTargets(ctx context.Context, httpc *http.Client, urls []string) []TargetMetrics {
	out := make([]TargetMetrics, 0, len(urls))
	for _, u := range urls {
		out = append(out, scrapeTarget(ctx, httpc, u))
	}
	return out
}

// ApplyBaseline convierte los contadores ACUMULADOS del raspado final en el
// DELTA DE LA CORRIDA restando la línea base raspada ANTES de la carga. Las
// métricas de Prometheus son contadores monótonos desde el arranque del
// proceso: contra un sistema de vida larga solo el delta describe lo que
// ocurrió durante la corrida, y es ese delta —no el acumulado— el que sostiene
// el veredicto. Si el contador retrocede (el proceso bajo prueba se reinició a
// mitad de corrida) el delta es el valor actual completo, que es lo servido
// desde el reinicio.
func ApplyBaseline(targets, baseline []TargetMetrics) {
	base := make(map[string]TargetMetrics, len(baseline))
	for _, b := range baseline {
		if b.Reachable {
			base[b.URL] = b
		}
	}
	for i := range targets {
		t := &targets[i]
		if !t.Reachable {
			continue
		}
		b, ok := base[t.URL]
		if !ok {
			continue
		}
		t.BaselineTaken = true
		t.HTTPRequestsDelta = counterDelta(t.HTTPRequests, b.HTTPRequests)
		t.HTTPErrors5xxDelta = counterDelta(t.HTTPErrors5xx, b.HTTPErrors5xx)
		t.TxRetriesDelta = counterDelta(t.TxRetries, b.TxRetries)
		t.TxExhaustedDelta = counterDelta(t.TxExhausted, b.TxExhausted)
	}
}

// counterDelta resta la línea base a un contador acumulado tratando el
// retroceso como reinicio del proceso (todo el valor actual es posterior).
func counterDelta(current, base float64) float64 {
	if d := current - base; d >= 0 {
		return d
	}
	return current
}

// scrapeTarget raspa e interpreta un /metrics del target.
func scrapeTarget(ctx context.Context, httpc *http.Client, target string) TargetMetrics {
	tm := TargetMetrics{URL: target}
	ctx, cancel := context.WithTimeout(ctx, scrapeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		tm.Error = err.Error()
		return tm
	}
	resp, err := httpc.Do(req)
	if err != nil {
		tm.Error = err.Error()
		return tm
	}
	defer resp.Body.Close() //nolint:errcheck // lectura de sondeo
	if resp.StatusCode != http.StatusOK {
		tm.Error = fmt.Sprintf("status %d", resp.StatusCode)
		return tm
	}
	samples, err := ParsePrometheusText(resp.Body)
	if err != nil {
		tm.Error = err.Error()
		return tm
	}
	tm.Reachable = true
	fillTargetMetrics(&tm, samples)
	return tm
}

// fillTargetMetrics proyecta las muestras raspadas en el resumen del informe.
func fillTargetMetrics(tm *TargetMetrics, samples PromSamples) {
	tm.Service = samples.AnyLabel("ii_http_requests_total", "service")
	tm.HTTPRequests = samples.Sum("ii_http_requests_total", nil)
	tm.HTTPErrors5xx = samples.SumWhere("ii_http_requests_total", func(labels map[string]string) bool {
		return strings.HasPrefix(labels["status"], "5")
	})
	if count := samples.Sum("ii_http_request_duration_seconds_count", nil); count > 0 {
		tm.HTTPAvgMs = samples.Sum("ii_http_request_duration_seconds_sum", nil) / count * 1000
	}
	if route := pickBoardRoute(samples); route != "" {
		boardFilter := map[string]string{"route": route}
		tm.BoardRoute = route
		tm.BoardRequests = samples.Sum("ii_http_request_duration_seconds_count", boardFilter)
		if tm.BoardRequests > 0 {
			tm.BoardP95Ms = samples.HistogramQuantile("ii_http_request_duration_seconds", boardFilter, 0.95) * 1000
		}
	}
	// Contención SERIALIZABLE del proceso (SAD §13). Los reintentos son ruido
	// normal bajo carga; el agotamiento del presupuesto NO: es una transacción
	// que se revirtió entera. La familia solo existe si el binario la registró,
	// así que se distingue «no publica» de «publica 0».
	if _, ok := samples[txRetriesMetric]; ok {
		tm.TxMetricsPublished = true
	}
	tm.TxRetries = samples.Sum(txRetriesMetric, nil)
	tm.TxExhausted = samples.Sum(txExhaustedMetric, nil)
	if lag := samples.ByLabel("ii_outbox_consumer_lag", "consumer"); len(lag) > 0 {
		tm.OutboxLagByConsumer = lag
		for _, v := range lag {
			tm.OutboxLag = max(tm.OutboxLag, v)
		}
	}
	tm.EngineEventsProcessed = samples.Sum("ii_outbox_events_processed_total", nil)
	tm.GoGoroutines = samples.Sum("go_goroutines", nil)
	tm.ProcessCPUSeconds = samples.Sum("process_cpu_seconds_total", nil)
	tm.ResidentBytes = samples.Sum("process_resident_memory_bytes", nil)
}

// pickBoardRoute localiza la etiqueta route bajo la que el target sirvió el
// tablón, probando las pistas en orden de preferencia. Devuelve "" si el target
// no publica ninguna ruta reconocible (p. ej. el proceso del engine, que no
// sirve la API del contrato).
func pickBoardRoute(samples PromSamples) string {
	routes := map[string]bool{}
	for _, s := range samples["ii_http_request_duration_seconds_count"] {
		if r := s.Labels["route"]; r != "" {
			routes[r] = true
		}
	}
	for _, hint := range boardRouteHints {
		best := ""
		for route := range routes {
			if !strings.Contains(route, hint) {
				continue
			}
			// Desempate estable: la ruta más específica (más larga) y, a igual
			// longitud, la menor lexicográficamente.
			if best == "" || len(route) > len(best) || (len(route) == len(best) && route < best) {
				best = route
			}
		}
		if best != "" {
			return best
		}
	}
	return ""
}

// ProbeDatabase sondea la BD del entorno de pruebas (solo lectura) para las
// magnitudes que las métricas no dan: lag real de la outbox, publicaciones
// vivas y contratos confirmados durante la corrida.
func ProbeDatabase(ctx context.Context, pool *pgxpool.Pool, prefix string, since time.Time, elapsed time.Duration) DatabaseProbe {
	p := DatabaseProbe{}
	if pool == nil {
		p.Error = "sin pool de base de datos"
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, dbProbeTimeout)
	defer cancel()

	queries := []struct {
		sql  string
		args []any
		dst  *int64
	}{
		// Retraso REAL del consumidor más rezagado: eventos de LOS TIPOS QUE ESE
		// CONSUMIDOR DECLARA SUSCRITOS (outbox.consumer_cursors.event_types,
		// migración 0016) por encima de su cursor. Restar el cursor de la
		// cabecera global mediría la historia entera del outbox: un consumidor
		// de eventos raros nunca alcanza max(seq) aunque esté al día.
		{`SELECT COALESCE(max(pending), 0) FROM (
		    SELECT (SELECT count(*)
		              FROM outbox.events e
		             WHERE e.seq > c.last_seq
		               AND e.event_type = ANY (c.event_types)) AS pending
		      FROM outbox.consumer_cursors c) backlog`, nil, &p.OutboxPending},
		{`SELECT count(*) FROM outbox.events WHERE created_at >= $1`, []any{since}, &p.OutboxEmittedDuringRun},
		{`SELECT count(*) FROM ledger.publications
		   WHERE status IN ('draw_window', 'open', 'micro_window')`, nil, &p.LivePublications},
		{`SELECT count(*) FROM ledger.publications WHERE created_at >= $1`, []any{since}, &p.PublicationsCreatedDuringRun},
		{`SELECT count(*) FROM ledger.contracts WHERE created_at >= $1`, []any{since}, &p.ContractsCreatedDuringRun},
		{`SELECT count(*) FROM auth.accounts WHERE name LIKE $1`, []any{prefix + "%"}, &p.StressAccounts},
		{`SELECT count(*) FROM auth.accounts WHERE name LIKE $1 AND status = 'active'`, []any{prefix + "%"}, &p.StressAccountsActive},
	}
	for _, q := range queries {
		if err := pool.QueryRow(ctx, q.sql, q.args...).Scan(q.dst); err != nil {
			p.Error = err.Error()
			return p
		}
	}
	p.Reachable = true
	if secs := elapsed.Seconds(); secs > 0 {
		p.ContractsPerSecond = float64(p.ContractsCreatedDuringRun) / secs
	}
	if p.OutboxPending < 0 {
		p.OutboxPending = 0
	}
	return p
}

// ─── Lector mínimo del formato de texto de Prometheus ────────────────────────
//
// El harness NO añade dependencias: interpreta el exposition format con un
// lector propio, suficiente para las familias que consulta (contadores, gauges
// e histogramas con sufijos _bucket/_sum/_count).

// PromSample es una muestra del exposition format: nombre, etiquetas y valor.
type PromSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// PromSamples es un conjunto de muestras indexado por nombre de métrica.
type PromSamples map[string][]PromSample

// ParsePrometheusText interpreta el exposition format de texto (v0.0.4).
// Ignora comentarios (# HELP/# TYPE), líneas vacías y timestamps opcionales.
func ParsePrometheusText(r io.Reader) (PromSamples, error) {
	out := PromSamples{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sample, ok := parsePromLine(line)
		if !ok {
			continue
		}
		out[sample.Name] = append(out[sample.Name], sample)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("stress: leyendo /metrics del target: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("stress: /metrics del target no devolvió ninguna métrica")
	}
	return out, nil
}

// parsePromLine interpreta una línea `nombre{etiquetas} valor [timestamp]`.
func parsePromLine(line string) (PromSample, bool) {
	name := line
	labels := map[string]string{}
	if i := strings.IndexByte(line, '{'); i >= 0 {
		j := strings.LastIndexByte(line, '}')
		if j < i {
			return PromSample{}, false
		}
		name = strings.TrimSpace(line[:i])
		labels = parsePromLabels(line[i+1 : j])
		line = strings.TrimSpace(line[j+1:])
	} else {
		k := strings.IndexAny(line, " \t")
		if k < 0 {
			return PromSample{}, false
		}
		name = strings.TrimSpace(line[:k])
		line = strings.TrimSpace(line[k:])
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return PromSample{}, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return PromSample{}, false
	}
	return PromSample{Name: name, Labels: labels, Value: v}, true
}

// parsePromLabels interpreta el bloque de etiquetas `a="1",b="2"` respetando
// las comas dentro de los valores entrecomillados y los escapes.
func parsePromLabels(block string) map[string]string {
	labels := map[string]string{}
	i := 0
	for i < len(block) {
		eq := strings.IndexByte(block[i:], '=')
		if eq < 0 {
			break
		}
		key := strings.TrimSpace(strings.Trim(block[i:i+eq], ", "))
		i += eq + 1
		if i >= len(block) || block[i] != '"' {
			break
		}
		i++ // abre comillas
		var sb strings.Builder
		for i < len(block) {
			c := block[i]
			if c == '\\' && i+1 < len(block) {
				i++
				switch block[i] {
				case 'n':
					sb.WriteByte('\n')
				default:
					sb.WriteByte(block[i])
				}
				i++
				continue
			}
			if c == '"' {
				i++
				break
			}
			sb.WriteByte(c)
			i++
		}
		if key != "" {
			labels[key] = sb.String()
		}
		for i < len(block) && (block[i] == ',' || block[i] == ' ') {
			i++
		}
	}
	return labels
}

// matches informa de si las etiquetas de la muestra contienen todos los pares
// del filtro (filtro nil o vacío = casa siempre).
func (s PromSample) matches(filter map[string]string) bool {
	for k, v := range filter {
		if s.Labels[k] != v {
			return false
		}
	}
	return true
}

// Sum suma el valor de todas las muestras de una métrica que casan el filtro.
func (p PromSamples) Sum(name string, filter map[string]string) float64 {
	total := 0.0
	for _, s := range p[name] {
		if s.matches(filter) {
			total += s.Value
		}
	}
	return total
}

// SumWhere suma el valor de las muestras que satisfacen un predicado sobre sus
// etiquetas (para filtros que no son igualdad, p. ej. status 5xx).
func (p PromSamples) SumWhere(name string, pred func(labels map[string]string) bool) float64 {
	total := 0.0
	for _, s := range p[name] {
		if pred(s.Labels) {
			total += s.Value
		}
	}
	return total
}

// ByLabel agrega el valor de una métrica por el valor de una etiqueta.
func (p PromSamples) ByLabel(name, label string) map[string]float64 {
	out := map[string]float64{}
	for _, s := range p[name] {
		out[s.Labels[label]] += s.Value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AnyLabel devuelve el valor de una etiqueta en la primera muestra de la
// métrica ("" si no hay muestras).
func (p PromSamples) AnyLabel(name, label string) string {
	for _, s := range p[name] {
		if v := s.Labels[label]; v != "" {
			return v
		}
	}
	return ""
}

// HistogramQuantile estima el cuantil q (0..1) de un histograma Prometheus
// (familias name_bucket/name_count) agregando los buckets que casan el filtro,
// con interpolación lineal dentro del bucket que contiene el cuantil — la misma
// aproximación que histogram_quantile() de PromQL. Devuelve 0 si no hay
// observaciones y +Inf acotado al último bucket finito.
func (p PromSamples) HistogramQuantile(name string, filter map[string]string, q float64) float64 {
	type bucket struct {
		le    float64
		count float64
	}
	agg := map[float64]float64{}
	for _, s := range p[name+"_bucket"] {
		if !s.matches(filter) {
			continue
		}
		le, err := strconv.ParseFloat(s.Labels["le"], 64)
		if err != nil {
			continue
		}
		agg[le] += s.Value
	}
	if len(agg) == 0 {
		return 0
	}
	buckets := make([]bucket, 0, len(agg))
	for le, c := range agg {
		buckets = append(buckets, bucket{le: le, count: c})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].le < buckets[j].le })
	total := buckets[len(buckets)-1].count // el bucket +Inf es acumulativo total
	if total <= 0 {
		return 0
	}
	rank := q * total
	prevLe, prevCount := 0.0, 0.0
	for _, b := range buckets {
		if b.count >= rank {
			if b.le > 1e307 { // +Inf: el cuantil cae en la cola abierta
				return prevLe
			}
			span := b.count - prevCount
			if span <= 0 {
				return b.le
			}
			return prevLe + (b.le-prevLe)*((rank-prevCount)/span)
		}
		prevLe, prevCount = b.le, b.count
	}
	return buckets[len(buckets)-1].le
}
