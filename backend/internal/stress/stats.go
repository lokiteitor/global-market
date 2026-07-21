package stress

import (
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// Op es una operación medida del harness. El informe desglosa throughput,
// latencias y errores POR OPERACIÓN.
type Op string

// Catálogo de operaciones. Las de lectura consultan; las de escritura mutan
// estado del mundo por la API pública (mismos endpoints que un humano).
const (
	OpLogin         Op = "login"
	OpLogout        Op = "logout"
	OpBoardRead     Op = "board_read"
	OpWorldRead     Op = "world_read"
	OpNetworkRead   Op = "network_read"
	OpLedgerRead    Op = "ledger_read"
	OpContractsRead Op = "contracts_read"
	OpFleetRead     Op = "fleet_read"
	OpMarketRead    Op = "market_ohlc_read"
	OpPublish       Op = "publish"
	OpCancel        Op = "cancel"
	OpAccept        Op = "accept"
	OpRoutePlan     Op = "route_plan"
	// OpDrainCancel son las cancelaciones de HIGIENE del cierre: no forman parte
	// del perfil de carga, pero SÍ son peticiones del harness al sistema bajo
	// prueba. Se miden aparte —no contaminan las latencias de OpCancel, que sí
	// son perfil— porque una petición que el harness emite y no cuenta acaba
	// apareciendo en las métricas del sistema como un 5xx «de otro cliente».
	OpDrainCancel Op = "drain_cancel"
)

// opOrder fija el orden estable de las operaciones en el informe.
var opOrder = []Op{
	OpLogin, OpBoardRead, OpWorldRead, OpNetworkRead, OpLedgerRead,
	OpContractsRead, OpFleetRead, OpMarketRead,
	OpPublish, OpCancel, OpAccept, OpRoutePlan, OpDrainCancel, OpLogout,
}

// Outcome es el resultado de una operación.
type Outcome string

const (
	// OutcomeOK: la petición salió y el sistema respondió 2xx.
	OutcomeOK Outcome = "ok"
	// OutcomeSkipped: el bot decidió NO emitir la petición (no había trabajo:
	// tablón sin oferta compatible, cooldown de cancelación aún vivo, mundo sin
	// nodos…). No cuenta como error ni como carga emitida.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeError: la petición salió y falló (status no-2xx o error de red).
	OutcomeError Outcome = "error"
)

// Clases de error del informe (agrupación por status HTTP; "network" para los
// fallos de transporte y "canceled" para el corte por fin de corrida).
const (
	ClassRateLimited  = "429"
	ClassConflict     = "409"
	ClassValidation   = "422"
	ClassUnauthorized = "401"
	ClassClient       = "4xx"
	ClassServer       = "5xx"
	ClassNetwork      = "network"
	ClassCanceled     = "canceled"
)

// benignDomainCodes son los códigos de dominio que, bajo carga, son RESPUESTAS
// LEGÍTIMAS del sistema y no defectos: el informe los cuenta aparte para que el
// veredicto no los confunda con degradación. 429 es siempre benigno (es
// justamente la válvula de backpressure que el harness quiere medir).
var benignDomainCodes = map[string]bool{
	"RATE_LIMITED":           true, // 429: backpressure del rate limit por cuenta
	"CANCEL_COOLDOWN_ACTIVE": true, // 409: cooldown anti-parpadeo del CCRI
	"PUBLICATION_EXHAUSTED":  true, // 409: otra contraparte se llevó la publicación
	"BELOW_MIN_LOT":          true, // 422: el remanente bajó del lote mínimo
	"NO_ROUTE_FOUND":         true, // 422: no hay ruta ejecutable entre esos nodos
	"MAINTENANCE_WINDOW":     true, // 503: ventana de mantenimiento diaria
}

// Result es la observación de UNA operación.
type Result struct {
	Op         Op
	Outcome    Outcome
	Latency    time.Duration
	Class      string // clase de error (vacío si no hubo error)
	DomainCode string // código de dominio del envelope de error
	SkipReason string // motivo del skip (auditable)
	Benign     bool   // el error es una respuesta legítima bajo carga
}

// classify construye el Result de un error del SDK.
func classify(op Op, d time.Duration, err error) Result {
	r := Result{Op: op, Outcome: OutcomeError, Latency: d}
	apiErr, ok := botsdk.AsAPIError(err)
	if !ok {
		r.Class = ClassNetwork
		if strings.Contains(err.Error(), "context canceled") || strings.Contains(err.Error(), "context deadline exceeded") {
			r.Class = ClassCanceled
			r.Benign = true
		}
		return r
	}
	r.DomainCode = apiErr.Code
	switch {
	case apiErr.Status == http.StatusTooManyRequests:
		r.Class = ClassRateLimited
	case apiErr.Status == http.StatusConflict:
		r.Class = ClassConflict
	case apiErr.Status == http.StatusUnprocessableEntity:
		r.Class = ClassValidation
	case apiErr.Status == http.StatusUnauthorized:
		r.Class = ClassUnauthorized
	case apiErr.Status >= 500:
		r.Class = ClassServer
	case apiErr.Status >= 400:
		r.Class = ClassClient
	default:
		r.Class = ClassClient
	}
	r.Benign = apiErr.Status == http.StatusTooManyRequests || benignDomainCodes[apiErr.Code]
	return r
}

// okResult construye el Result de una operación exitosa.
func okResult(op Op, d time.Duration) Result {
	return Result{Op: op, Outcome: OutcomeOK, Latency: d}
}

// skippedResult construye el Result de una operación no emitida.
func skippedResult(op Op, reason string) Result {
	return Result{Op: op, Outcome: OutcomeSkipped, SkipReason: reason}
}

// DefaultMaxSamples es el tope de muestras de latencia conservadas por
// operación. Por debajo del tope los percentiles son EXACTOS; por encima, el
// colector pasa a muestreo de reservorio (algoritmo R, uniforme) para que la
// memoria no crezca con corridas de cientos de miles de bots.
const DefaultMaxSamples = 200_000

// opStats acumula las observaciones de una operación.
type opStats struct {
	ok         int64
	skipped    int64
	failed     int64
	benign     int64
	totalNanos int64
	minNanos   int64
	maxNanos   int64
	samples    []time.Duration
	seen       int64 // latencias observadas (para el reservorio)
	byClass    map[string]int64
	byCode     map[string]int64
	bySkip     map[string]int64
}

// Collector acumula los Result de todos los bots de la corrida. Es seguro para
// uso concurrente.
type Collector struct {
	mu         sync.Mutex
	ops        map[Op]*opStats
	maxSamples int
	rng        *rand.Rand
}

// NewCollector construye el colector. maxSamples <= 0 usa DefaultMaxSamples;
// seed hace reproducible el muestreo de reservorio de una corrida.
func NewCollector(maxSamples int, seed uint64) *Collector {
	if maxSamples <= 0 {
		maxSamples = DefaultMaxSamples
	}
	return &Collector{
		ops:        map[Op]*opStats{},
		maxSamples: maxSamples,
		rng:        rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)),
	}
}

// Record acumula una observación.
func (c *Collector) Record(r Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.ops[r.Op]
	if st == nil {
		st = &opStats{
			byClass: map[string]int64{},
			byCode:  map[string]int64{},
			bySkip:  map[string]int64{},
		}
		c.ops[r.Op] = st
	}
	switch r.Outcome {
	case OutcomeOK:
		st.ok++
	case OutcomeSkipped:
		st.skipped++
		reason := r.SkipReason
		if reason == "" {
			reason = "sin motivo"
		}
		st.bySkip[reason]++
		return // un skip no emite petición: no aporta latencia
	case OutcomeError:
		st.failed++
		if r.Benign {
			st.benign++
		}
		if r.Class != "" {
			st.byClass[r.Class]++
		}
		if r.DomainCode != "" {
			st.byCode[r.DomainCode]++
		}
	}
	c.observe(st, r.Latency)
}

// observe acumula la latencia de una petición efectivamente emitida.
func (c *Collector) observe(st *opStats, d time.Duration) {
	n := int64(d)
	st.seen++
	st.totalNanos += n
	if st.seen == 1 || n < st.minNanos {
		st.minNanos = n
	}
	if n > st.maxNanos {
		st.maxNanos = n
	}
	if len(st.samples) < c.maxSamples {
		st.samples = append(st.samples, d)
		return
	}
	// Reservorio (algoritmo R): cada muestra nueva entra con probabilidad
	// maxSamples/seen, manteniendo una muestra uniforme del total.
	j := c.rng.Int64N(st.seen)
	if j < int64(c.maxSamples) {
		st.samples[j] = d
	}
}

// LatencyStats son los percentiles y agregados de latencia de una operación.
type LatencyStats struct {
	MinMs float64 `json:"min_ms"`
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`
	// Samples es el número de muestras conservadas (== peticiones emitidas
	// mientras no se alcance el tope del reservorio).
	Samples int `json:"samples"`
	// Exact indica si los percentiles son exactos (sin reservorio).
	Exact bool `json:"exact"`
}

// OpReport es el resumen de una operación en el informe.
type OpReport struct {
	Op           Op               `json:"op"`
	Requests     int64            `json:"requests"`
	OK           int64            `json:"ok"`
	Errors       int64            `json:"errors"`
	BenignErrors int64            `json:"benign_errors"`
	Skipped      int64            `json:"skipped"`
	OpsPerSecond float64          `json:"ops_per_second"`
	ErrorRate    float64          `json:"error_rate"`
	Latency      LatencyStats     `json:"latency"`
	ErrorsBySt   map[string]int64 `json:"errors_by_status_class,omitempty"`
	ErrorsByCode map[string]int64 `json:"errors_by_domain_code,omitempty"`
	SkipReasons  map[string]int64 `json:"skip_reasons,omitempty"`
}

// Totals son los agregados de toda la corrida.
type Totals struct {
	Requests        int64            `json:"requests"`
	OK              int64            `json:"ok"`
	Errors          int64            `json:"errors"`
	BenignErrors    int64            `json:"benign_errors"`
	UnexpectedError int64            `json:"unexpected_errors"`
	Skipped         int64            `json:"skipped"`
	OpsPerSecond    float64          `json:"ops_per_second"`
	ErrorRate       float64          `json:"error_rate"`
	ErrorsBySt      map[string]int64 `json:"errors_by_status_class,omitempty"`
	ErrorsByCode    map[string]int64 `json:"errors_by_domain_code,omitempty"`
}

// Snapshot resume el estado actual del colector para el intervalo elapsed
// (throughput = peticiones emitidas / elapsed). Es seguro llamarlo durante la
// corrida (log periódico) y al final (informe).
func (c *Collector) Snapshot(elapsed time.Duration) ([]OpReport, Totals) {
	c.mu.Lock()
	defer c.mu.Unlock()

	secs := elapsed.Seconds()
	reports := make([]OpReport, 0, len(c.ops))
	totals := Totals{ErrorsBySt: map[string]int64{}, ErrorsByCode: map[string]int64{}}

	for _, op := range c.opsInOrder() {
		st := c.ops[op]
		r := OpReport{
			Op:           op,
			Requests:     st.seen,
			OK:           st.ok,
			Errors:       st.failed,
			BenignErrors: st.benign,
			Skipped:      st.skipped,
			Latency:      latencyOf(st),
			ErrorsBySt:   copyCounts(st.byClass),
			ErrorsByCode: copyCounts(st.byCode),
			SkipReasons:  copyCounts(st.bySkip),
		}
		if secs > 0 {
			r.OpsPerSecond = float64(st.seen) / secs
		}
		if st.seen > 0 {
			r.ErrorRate = float64(st.failed) / float64(st.seen)
		}
		reports = append(reports, r)

		totals.Requests += st.seen
		totals.OK += st.ok
		totals.Errors += st.failed
		totals.BenignErrors += st.benign
		totals.Skipped += st.skipped
		for k, v := range st.byClass {
			totals.ErrorsBySt[k] += v
		}
		for k, v := range st.byCode {
			totals.ErrorsByCode[k] += v
		}
	}
	totals.UnexpectedError = totals.Errors - totals.BenignErrors
	if secs > 0 {
		totals.OpsPerSecond = float64(totals.Requests) / secs
	}
	if totals.Requests > 0 {
		totals.ErrorRate = float64(totals.Errors) / float64(totals.Requests)
	}
	return reports, totals
}

// opsInOrder devuelve las operaciones con datos en el orden estable del informe
// (las no catalogadas van al final, ordenadas alfabéticamente).
func (c *Collector) opsInOrder() []Op {
	out := make([]Op, 0, len(c.ops))
	known := map[Op]bool{}
	for _, op := range opOrder {
		known[op] = true
		if _, ok := c.ops[op]; ok {
			out = append(out, op)
		}
	}
	var extra []Op
	for op := range c.ops {
		if !known[op] {
			extra = append(extra, op)
		}
	}
	sort.Slice(extra, func(i, j int) bool { return extra[i] < extra[j] })
	return append(out, extra...)
}

// latencyOf calcula los percentiles de una operación.
func latencyOf(st *opStats) LatencyStats {
	ls := LatencyStats{Samples: len(st.samples), Exact: st.seen == int64(len(st.samples))}
	if st.seen == 0 {
		return ls
	}
	ls.MinMs = millis(time.Duration(st.minNanos))
	ls.MaxMs = millis(time.Duration(st.maxNanos))
	ls.AvgMs = millis(time.Duration(st.totalNanos / st.seen))
	if len(st.samples) == 0 {
		return ls
	}
	sorted := make([]time.Duration, len(st.samples))
	copy(sorted, st.samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	ls.P50Ms = millis(Percentile(sorted, 50))
	ls.P95Ms = millis(Percentile(sorted, 95))
	ls.P99Ms = millis(Percentile(sorted, 99))
	return ls
}

// Percentile devuelve el percentil p (0..100) de una muestra YA ORDENADA de
// menor a mayor, por RANGO MÁS CERCANO (nearest-rank): el valor en la posición
// ceil(p/100 · n). Sin interpolación: el resultado es siempre un valor
// realmente observado. Devuelve 0 con muestra vacía.
func Percentile(sorted []time.Duration, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[n-1]
	}
	// rank = ceil(p/100 · n) con aritmética entera sobre milésimas de percentil
	// (evita el redondeo binario de ceil sobre float en los casos exactos).
	scaled := int64(p*1000+0.5) * int64(n)
	rank := scaled / 100_000
	if scaled%100_000 != 0 {
		rank++
	}
	if rank < 1 {
		rank = 1
	}
	if rank > int64(n) {
		rank = int64(n)
	}
	return sorted[rank-1]
}

// millis convierte una duración a milisegundos con tres decimales.
func millis(d time.Duration) float64 {
	return float64(d.Microseconds()) / 1000.0
}

// copyCounts copia un mapa de contadores (nil si está vacío: el JSON lo omite).
func copyCounts(in map[string]int64) map[string]int64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
