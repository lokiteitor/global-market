package stress

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Report es el INFORME FINAL de una corrida de stress: configuración, carga
// generada y medida, y el estado del sistema bajo prueba con los disparadores
// medidos del SAD §13. Se emite en JSON (II_STRESS_REPORT) y en consola.
type Report struct {
	RunID           string        `json:"run_id"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	DurationSeconds float64       `json:"duration_seconds"`
	Config          ConfigReport  `json:"config"`
	Totals          Totals        `json:"totals"`
	Operations      []OpReport    `json:"operations"`
	System          SystemReport  `json:"system"`
	Cleanup         CleanupResult `json:"cleanup"`
	Verdict         Verdict       `json:"verdict"`
}

// ConfigReport es la configuración efectiva de la corrida tal y como queda
// registrada en el informe. NUNCA incluye secretos: ni la semilla de derivación
// ni la cadena de conexión (solo el host de la BD).
type ConfigReport struct {
	APIURL          string   `json:"api_url"`
	Env             string   `json:"env"`
	AllowMatch      string   `json:"allowlist_match"`
	AccountPrefix   string   `json:"account_prefix"`
	Bots            int      `json:"bots"`
	Mix             string   `json:"mix"`
	Archetypes      []string `json:"archetypes"`
	Counts          []int    `json:"archetype_counts"`
	RampSeconds     float64  `json:"ramp_seconds"`
	DurationSeconds float64  `json:"duration_seconds"`
	TickSeconds     float64  `json:"tick_seconds"`
	WriteRatio      float64  `json:"write_ratio"`
	SellShare       float64  `json:"sell_share"`
	Capital         int64    `json:"capital"`
	StockEndowment  int64    `json:"stock_endowment"`
	Cleanup         bool     `json:"cleanup"`
	DatabaseHost    string   `json:"database_host"`
	TargetMetrics   []string `json:"target_metrics"`
}

// SystemReport es el estado MEDIDO del sistema bajo prueba al terminar.
type SystemReport struct {
	Targets  []TargetMetrics `json:"targets"`
	Database DatabaseProbe   `json:"database"`
}

// Verdict es la lectura simple del informe: qué respondió el sistema bajo N
// bots y si algo pide atención.
type Verdict struct {
	// OK es false si hubo 5xx o errores no benignos.
	OK bool `json:"ok"`
	// Summary es la frase de cabecera del veredicto.
	Summary string `json:"summary"`
	// Lines son las observaciones sobre los disparadores medidos del SAD §13.
	Lines []string `json:"lines"`
	// ServerErrors, RateLimited y UnexpectedErrors son los recuentos que
	// sostienen el veredicto, MEDIDOS POR EL HARNESS en sus propias respuestas.
	ServerErrors     int64 `json:"server_errors"`
	RateLimited      int64 `json:"rate_limited"`
	UnexpectedErrors int64 `json:"unexpected_errors"`
	// TargetServerErrors son las respuestas 5xx que el SISTEMA BAJO PRUEBA
	// registró en sus propias métricas DURANTE la corrida (delta contra la línea
	// base). Un 5xx puede no aparecer en ServerErrors —lo recibió otro cliente o
	// una ruta que los bots no ejercitan— y aun así el sistema rompió: cuenta
	// igual para el veredicto.
	TargetServerErrors int64 `json:"target_server_errors"`
	// TargetTxRetries y TargetTxExhausted son la contención SERIALIZABLE que el
	// SISTEMA BAJO PRUEBA registró DURANTE la corrida (delta contra la línea
	// base, sumado sobre sus procesos). Los reintentos son ruido normal bajo
	// carga; cada presupuesto agotado es una transacción revertida entera —una
	// operación de usuario devuelta como reintentable o un trabajo de fondo que
	// se cayó—, y por eso degrada el veredicto a ADVERTENCIA explícita: es el
	// disparador MEDIDO de contención del SAD §13, no un detalle del log.
	TargetTxRetries   int64 `json:"target_tx_serialization_retries"`
	TargetTxExhausted int64 `json:"target_tx_serialization_exhausted"`
	// Unexercised son las operaciones del mix que tuvieron ocasión de salir y no
	// emitieron NI UNA petición (100% omitidas). Un informe con caminos así NO
	// midió lo que su mezcla declara —típicamente la aceptación, que es la
	// escritura cara— y no puede leerse como sano por lo que sí midió.
	Unexercised []string `json:"unexercised_paths,omitempty"`
}

// skipDegradedRatio es la fracción de ocasiones omitidas a partir de la cual una
// operación deja de medirse en proporción a la población y el veredicto lo dice.
const skipDegradedRatio = 0.5

// NewConfigReport proyecta las Options en la configuración del informe.
func NewConfigReport(o Options) ConfigReport {
	counts := o.Mix.Counts(o.Bots)
	archetypes := make([]string, 0, len(o.Mix.Order))
	values := make([]int, 0, len(o.Mix.Order))
	for _, a := range o.Mix.Order {
		archetypes = append(archetypes, string(a))
		values = append(values, counts[a])
	}
	host, _ := databaseHost(o.DatabaseURL)
	if host == "" {
		host = "local"
	}
	return ConfigReport{
		APIURL:          o.APIURL,
		Env:             o.Env,
		AllowMatch:      o.AllowMatch,
		AccountPrefix:   o.RunAccountPrefix(),
		Bots:            o.Bots,
		Mix:             o.Mix.String(),
		Archetypes:      archetypes,
		Counts:          values,
		RampSeconds:     o.Ramp.Seconds(),
		DurationSeconds: o.Duration.Seconds(),
		TickSeconds:     o.Tick.Seconds(),
		WriteRatio:      o.WriteRatio,
		SellShare:       o.SellShare,
		Capital:         o.Capital,
		StockEndowment:  o.StockEndowment,
		Cleanup:         o.Cleanup,
		DatabaseHost:    host,
		TargetMetrics:   o.TargetMetrics,
	}
}

// buildVerdict deriva el veredicto de los totales y del estado del sistema. El
// veredicto cruza las DOS fuentes del informe: lo que el harness recibió y lo
// que el propio sistema registró durante la corrida (delta de sus contadores
// contra la línea base). Basta que una de ellas vea 5xx para que la corrida sea
// negativa (código de salida 2).
func buildVerdict(r *Report) Verdict {
	v := Verdict{
		ServerErrors:       r.Totals.ErrorsBySt[ClassServer],
		RateLimited:        r.Totals.ErrorsBySt[ClassRateLimited],
		UnexpectedErrors:   r.Totals.UnexpectedError,
		TargetServerErrors: targetServerErrors(r.System.Targets),
	}
	v.TargetTxRetries, v.TargetTxExhausted = targetTxContention(r.System.Targets)
	v.OK = v.ServerErrors == 0 && v.TargetServerErrors == 0 && v.UnexpectedErrors == 0

	board := findOp(r.Operations, OpBoardRead)
	if board != nil && board.Requests > 0 {
		v.Summary = fmt.Sprintf("el tablón respondió p95 %.1f ms (p99 %.1f ms) bajo %d bots, con %.1f ops/s totales",
			board.Latency.P95Ms, board.Latency.P99Ms, r.Config.Bots, r.Totals.OpsPerSecond)
	} else {
		v.Summary = fmt.Sprintf("%d bots generaron %.1f ops/s durante %.1f s", r.Config.Bots, r.Totals.OpsPerSecond, r.DurationSeconds)
	}

	// Disparadores medidos (SAD §13).
	if board != nil && board.Requests > 0 {
		v.Lines = append(v.Lines, fmt.Sprintf(
			"latencia de consulta del tablón (disparador del motor de búsqueda dedicado): p50 %.1f ms · p95 %.1f ms · p99 %.1f ms sobre %d consultas",
			board.Latency.P50Ms, board.Latency.P95Ms, board.Latency.P99Ms, board.Requests))
	}
	for _, t := range r.System.Targets {
		if !t.Reachable {
			v.Lines = append(v.Lines, fmt.Sprintf("métricas del target %s no accesibles (%s): el informe se apoya solo en la medición del harness", t.URL, t.Error))
			continue
		}
		name := t.Service
		if name == "" {
			name = t.URL
		}
		served := fmt.Sprintf("%.0f peticiones servidas DURANTE la corrida (%.0f 5xx) · acumulado del proceso %.0f (%.0f 5xx)",
			t.HTTPRequestsDelta, t.HTTPErrors5xxDelta, t.HTTPRequests, t.HTTPErrors5xx)
		if !t.BaselineTaken {
			// Sin línea base no hay delta: el acumulado incluye tráfico anterior a
			// la corrida y NO puede sostener el veredicto.
			served = fmt.Sprintf("%.0f peticiones acumuladas desde el arranque del proceso (%.0f 5xx) · SIN línea base previa: no atribuibles a esta corrida",
				t.HTTPRequests, t.HTTPErrors5xx)
		}
		v.Lines = append(v.Lines, fmt.Sprintf(
			"carga del proceso %s (disparador de la extracción de shards): %.0f goroutines · %.1f s de CPU · %.0f MiB residentes · %s",
			name, t.GoGoroutines, t.ProcessCPUSeconds, t.ResidentBytes/(1024*1024), served))
		if t.BoardRequests > 0 {
			// La etiqueta route es el PATRÓN del mux, que puede agrupar todo el
			// subárbol del tablón: se nombra para que la cifra no se lea como si
			// fuese solo GET /contracts/board.
			v.Lines = append(v.Lines, fmt.Sprintf(
				"ruta del tablón servida por %s (route %q): %.0f peticiones con p95 %.1f ms medidos POR EL PROPIO SISTEMA",
				name, t.BoardRoute, t.BoardRequests, t.BoardP95Ms))
		}
		if len(t.OutboxLagByConsumer) > 0 {
			// NO es el máximo de la corrida: es UN raspado al terminar la carga, y
			// dentro de él el peor de los consumidores DE ESTE PROCESO con el valor
			// que dejó su ÚLTIMO polling. Un consumidor parado o en backoff congela
			// su gauge (publica el último valor, típicamente 0) mientras la fuente
			// acumula: por eso la cifra de referencia es la del sondeo de BD.
			v.Lines = append(v.Lines, fmt.Sprintf(
				"lag de la outbox publicado por %s (disparador de Kafka): %.0f eventos en el peor de sus %d consumidores, INSTANTÁNEO al terminar la carga y con el valor del último polling de cada uno (no es el máximo de la corrida; un consumidor parado congela su gauge)",
				name, t.OutboxLag, len(t.OutboxLagByConsumer)))
		}
		switch {
		case !t.TxMetricsPublished:
			// El proceso no registró la familia: no hay lectura de contención, y
			// callarlo se leería como «no hubo».
			v.Lines = append(v.Lines, fmt.Sprintf(
				"contención SERIALIZABLE en %s: SIN lectura (el proceso no publica %s; se registra con db.RegisterTxMetrics)", name, txRetriesMetric))
		case !t.BaselineTaken:
			v.Lines = append(v.Lines, fmt.Sprintf(
				"contención SERIALIZABLE en %s (disparador del techo de escritura): %.0f reintentos y %.0f presupuestos agotados ACUMULADOS desde el arranque del proceso · SIN línea base previa: no atribuibles a esta corrida",
				name, t.TxRetries, t.TxExhausted))
		default:
			v.Lines = append(v.Lines, fmt.Sprintf(
				"contención SERIALIZABLE en %s (disparador del techo de escritura): %.0f reintentos y %.0f presupuestos agotados durante la corrida · acumulado del proceso %.0f/%.0f",
				name, t.TxRetriesDelta, t.TxExhaustedDelta, t.TxRetries, t.TxExhausted))
		}
	}
	if r.System.Database.Reachable {
		db := r.System.Database
		v.Lines = append(v.Lines, fmt.Sprintf(
			"outbox en BD (disparador de Kafka, MEDIDA DE REFERENCIA): %d eventos pendientes del consumidor más rezagado, INSTANTÁNEO al terminar la carga y sobre TODOS los cursores (también los de consumidores que no corren en los procesos raspados) · %d emitidos durante la corrida",
			db.OutboxPending, db.OutboxEmittedDuringRun))
		// Las dos cifras del MISMO disparador se leen juntas: se miden en el mismo
		// instante y con la misma definición (eventos de los tipos suscritos por
		// encima del cursor), así que una divergencia NO es del muestreo — es la
		// señal de que algún consumidor no está publicando su retraso.
		if lag, published := maxTargetOutboxLag(r.System.Targets); published && db.OutboxPending > int64(math.Round(lag)) {
			v.Lines = append(v.Lines, fmt.Sprintf(
				"lectura del lag: la fuente (%d) supera lo que publican los targets (%.0f) — manda la de BD: el gauge solo se refresca en el polling de cada consumidor y no cubre los cursores de consumidores ajenos a esos procesos (uno parado o en backoff publica su último valor)",
				db.OutboxPending, lag))
		}
		v.Lines = append(v.Lines, fmt.Sprintf(
			"tablón en BD: %d publicaciones vivas al terminar (%d creadas durante la corrida) · %d contratos confirmados (%.2f/s)",
			db.LivePublications, db.PublicationsCreatedDuringRun, db.ContractsCreatedDuringRun, db.ContractsPerSecond))
		v.Lines = append(v.Lines, fmt.Sprintf(
			"cuentas del run con prefijo %q: %d (%d activas tras la limpieza)",
			r.Config.AccountPrefix, db.StressAccounts, db.StressAccountsActive))
	} else if r.System.Database.Error != "" {
		v.Lines = append(v.Lines, "sondeo de BD no disponible: "+r.System.Database.Error)
	}

	switch {
	case v.ServerErrors > 0:
		line := fmt.Sprintf("ATENCIÓN: %d respuestas 5xx recibidas por el harness — el sistema rompió bajo esta carga", v.ServerErrors)
		if v.TargetServerErrors > 0 {
			line += fmt.Sprintf(" (el propio sistema registró %d durante la corrida)", v.TargetServerErrors)
		}
		// DÓNDE rompió importa tanto como que rompió: la higiene de cierre
		// (drain_cancel) descarga de golpe todas las publicaciones vivas de todos
		// los bots y NO es el perfil de carga. Atribuirle a la carga medida unos
		// 5xx que solo produjo esa ráfaga sería tan engañoso como no contarlos.
		if drain := findOp(r.Operations, OpDrainCancel); drain != nil {
			if n := drain.ErrorsBySt[ClassServer]; n > 0 && n == v.ServerErrors {
				line += fmt.Sprintf(" — todas (%d) salieron de la higiene de cierre %q (cancelación en ráfaga de %d publicaciones al terminar, FUERA del perfil de carga): el perfil medido no produjo ninguna",
					n, OpDrainCancel, drain.Requests)
			}
		}
		v.Lines = append(v.Lines, line)
	case v.TargetServerErrors > 0:
		// El harness no recibió ningún 5xx, pero el sistema los sirvió: otra ruta
		// u otro cliente. La corrida es NEGATIVA igual.
		v.Lines = append(v.Lines, fmt.Sprintf(
			"ATENCIÓN: el sistema registró %d respuestas 5xx en sus propias métricas durante la corrida (ninguna la recibió el harness: otra ruta u otro cliente) — el sistema rompió bajo esta carga",
			v.TargetServerErrors))
	case v.UnexpectedErrors > 0:
		v.Lines = append(v.Lines, fmt.Sprintf("ATENCIÓN: %d errores no esperados (fuera de 429/cooldown/sin-ruta)", v.UnexpectedErrors))
	default:
		// Sin 5xx ni errores inesperados la corrida NO es automáticamente limpia:
		// el 429 es la válvula funcionando, pero una transacción que agota su
		// presupuesto de reintentos se revirtió entera y nadie la recibió como
		// error —un trabajo de fondo caído no llega a ningún cliente—, así que
		// solo se puede afirmar «absorbió la carga» cuando tampoco hubo eso.
		if v.RateLimited > 0 {
			v.Lines = append(v.Lines, fmt.Sprintf("%d respuestas 429: el backpressure del rate limit actuó como válvula (GDD §19: la densidad de bots se reduce antes que degradar la experiencia humana)", v.RateLimited))
		}
		switch {
		case v.TargetTxExhausted > 0:
			v.Lines = append(v.Lines, fmt.Sprintf(
				"ADVERTENCIA: contención SERIALIZABLE — el sistema agotó %d veces su presupuesto de reintentos (%d reintentos en total) durante la corrida: cada una es una transacción revertida entera (una operación devuelta como reintentable o un trabajo de fondo que se cayó hasta el barrido siguiente). Es el disparador MEDIDO del techo de escritura del SAD §13, no un detalle del log",
				v.TargetTxExhausted, v.TargetTxRetries))
		case v.RateLimited == 0:
			v.Lines = append(v.Lines, "sin 5xx (ni recibidos por el harness ni registrados por el sistema), sin errores inesperados y sin presupuestos de reintento agotados: el sistema absorbió la carga dentro de su techo")
		}
	}

	// La REPRESENTATIVIDAD va PRIMERA: un camino que no se ejercitó invalida la
	// lectura del resto del informe, así que no puede quedar sepultado al final.
	coverage, unexercised := coverageLines(r.Operations)
	v.Unexercised = unexercised
	v.Lines = append(coverage, v.Lines...)
	return v
}

// coverageLines audita la COBERTURA del perfil de carga: qué operaciones de la
// mezcla se quedaron sin emitir pese a haber tenido ocasión. Devuelve las líneas
// del veredicto y los nombres de las operaciones 100% omitidas.
func coverageLines(ops []OpReport) ([]string, []string) {
	var lines, unexercised []string
	for i := range ops {
		op := ops[i]
		chances := op.Requests + op.Skipped
		if op.Skipped == 0 || chances == 0 {
			continue
		}
		reason := dominantSkipReason(op.SkipReasons)
		switch {
		case op.Requests == 0:
			unexercised = append(unexercised, string(op.Op))
			lines = append(lines, fmt.Sprintf(
				"ATENCIÓN: el camino %q NO SE EJERCITÓ en esta corrida — las %d ocasiones se omitieron (100%%), motivo dominante %q. El informe NO mide ese camino y el resto de sus cifras no pueden leerse como si lo hiciera",
				op.Op, op.Skipped, reason))
		case float64(op.Skipped) >= skipDegradedRatio*float64(chances):
			lines = append(lines, fmt.Sprintf(
				"ATENCIÓN: el camino %q se ejercitó a medias — %d de %d ocasiones omitidas (%.0f%%), motivo dominante %q: su medición no es proporcional a la población de la corrida",
				op.Op, op.Skipped, chances, 100*float64(op.Skipped)/float64(chances), reason))
		}
	}
	return lines, unexercised
}

// dominantSkipReason devuelve el motivo de omisión más frecuente (desempate
// alfabético, para que el informe sea reproducible).
func dominantSkipReason(reasons map[string]int64) string {
	best, bestN := "", int64(-1)
	for reason, n := range reasons {
		if n > bestN || (n == bestN && reason < best) {
			best, bestN = reason, n
		}
	}
	if best == "" {
		return "sin motivo"
	}
	return best
}

// targetServerErrors suma las respuestas 5xx que los targets registraron en sus
// PROPIAS métricas DURANTE la corrida. Solo cuentan los targets accesibles con
// línea base: sin ella el contador es el acumulado desde el arranque del proceso
// (incluye tráfico anterior a la corrida) y no es atribuible a esta carga, así
// que no puede sostener un veredicto negativo.
func targetServerErrors(targets []TargetMetrics) int64 {
	var total int64
	for _, t := range targets {
		if !t.Reachable || !t.BaselineTaken {
			continue
		}
		if d := t.HTTPErrors5xxDelta; d > 0 {
			total += int64(math.Round(d))
		}
	}
	return total
}

// targetTxContention suma la contención SERIALIZABLE que los targets
// registraron en sus PROPIAS métricas DURANTE la corrida: reintentos y
// presupuestos agotados. Igual que con los 5xx, solo cuentan los targets
// accesibles CON línea base: sin ella el contador es el acumulado desde el
// arranque del proceso y no es atribuible a esta carga.
func targetTxContention(targets []TargetMetrics) (retries, exhausted int64) {
	for _, t := range targets {
		if !t.Reachable || !t.BaselineTaken || !t.TxMetricsPublished {
			continue
		}
		if d := t.TxRetriesDelta; d > 0 {
			retries += int64(math.Round(d))
		}
		if d := t.TxExhaustedDelta; d > 0 {
			exhausted += int64(math.Round(d))
		}
	}
	return retries, exhausted
}

// maxTargetOutboxLag devuelve el mayor retraso de outbox publicado por los
// targets accesibles y si ALGUNO lo publicó. Sin ese segundo valor no se puede
// distinguir «ningún target publica la métrica» de «todos publican 0», que es
// justo la diferencia entre no tener lectura y tener una lectura tranquilizadora.
func maxTargetOutboxLag(targets []TargetMetrics) (float64, bool) {
	lag, published := 0.0, false
	for _, t := range targets {
		if !t.Reachable || len(t.OutboxLagByConsumer) == 0 {
			continue
		}
		published = true
		lag = max(lag, t.OutboxLag)
	}
	return lag, published
}

// findOp localiza el resumen de una operación en el informe (nil si no la hubo).
func findOp(ops []OpReport, op Op) *OpReport {
	for i := range ops {
		if ops[i].Op == op {
			return &ops[i]
		}
	}
	return nil
}

// WriteJSON escribe el informe en path (creando los directorios intermedios).
func (r *Report) WriteJSON(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("stress: creando el directorio del informe %q: %w", dir, err)
		}
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("stress: serializando el informe: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("stress: escribiendo el informe en %q: %w", path, err)
	}
	return nil
}

// Console renderiza el informe legible por consola.
func (r *Report) Console() string {
	var b strings.Builder
	line := strings.Repeat("─", 96)
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "INFORME DE STRESS · run %s · %s\n", r.RunID, r.FinishedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "Target        : %s (II_ENV=%s, autorizado por la allowlist con %q)\n",
		r.Config.APIURL, envLabel(r.Config.Env), r.Config.AllowMatch)
	fmt.Fprintf(&b, "Cuentas       : prefijo %q · capital %d · dotación de stock %d · limpieza %t\n",
		r.Config.AccountPrefix, r.Config.Capital, r.Config.StockEndowment, r.Config.Cleanup)
	fmt.Fprintf(&b, "Perfil        : %d bots [%s] · rampa %.0fs · duración %.0fs · tick %.2fs · write_ratio %.2f · sell_share %.2f\n",
		r.Config.Bots, r.Config.Mix, r.Config.RampSeconds, r.Config.DurationSeconds, r.Config.TickSeconds, r.Config.WriteRatio, r.Config.SellShare)
	fmt.Fprintf(&b, "Reparto       : %s\n", formatCounts(r.Config))
	fmt.Fprintf(&b, "Duración real : %.2f s\n", r.DurationSeconds)
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "%-18s %9s %9s %9s %9s %9s %9s %9s %9s\n",
		"OPERACIÓN", "PETIC.", "OK", "ERRORES", "OMITIDAS", "OPS/S", "p50 ms", "p95 ms", "p99 ms")
	for _, op := range r.Operations {
		fmt.Fprintf(&b, "%-18s %9d %9d %9d %9d %9.2f %9.1f %9.1f %9.1f\n",
			op.Op, op.Requests, op.OK, op.Errors, op.Skipped, op.OpsPerSecond,
			op.Latency.P50Ms, op.Latency.P95Ms, op.Latency.P99Ms)
	}
	fmt.Fprintf(&b, "%-18s %9d %9d %9d %9d %9.2f %9s %9s %9s\n",
		"TOTAL", r.Totals.Requests, r.Totals.OK, r.Totals.Errors, r.Totals.Skipped, r.Totals.OpsPerSecond, "-", "-", "-")
	fmt.Fprintf(&b, "%s\n", line)
	if len(r.Totals.ErrorsBySt) > 0 {
		fmt.Fprintf(&b, "Errores por status : %s\n", formatCounts64(r.Totals.ErrorsBySt))
	}
	if len(r.Totals.ErrorsByCode) > 0 {
		fmt.Fprintf(&b, "Errores por código : %s\n", formatCounts64(r.Totals.ErrorsByCode))
	}
	fmt.Fprintf(&b, "Errores benignos   : %d (429/cooldown/sin-ruta) · inesperados: %d\n",
		r.Totals.BenignErrors, r.Totals.UnexpectedError)
	fmt.Fprintf(&b, "Limpieza           : %d retiradas · %d ya inactivas · %d fallidas%s\n",
		r.Cleanup.Retired, r.Cleanup.AlreadyInactive, r.Cleanup.Failed, cleanupSuffix(r.Cleanup))
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "VEREDICTO: %s\n", r.Verdict.Summary)
	for _, l := range r.Verdict.Lines {
		fmt.Fprintf(&b, "  · %s\n", l)
	}
	fmt.Fprintf(&b, "%s\n", line)
	return b.String()
}

// envLabel formatea el valor observado de II_ENV para la consola.
func envLabel(env string) string {
	if strings.TrimSpace(env) == "" {
		return "<sin definir>"
	}
	return fmt.Sprintf("%q", env)
}

// cleanupSuffix anota la limpieza desactivada.
func cleanupSuffix(c CleanupResult) string {
	if c.Skipped {
		return " (desactivada por II_STRESS_CLEANUP=false)"
	}
	return ""
}

// formatCounts formatea el reparto de bots por arquetipo.
func formatCounts(c ConfigReport) string {
	parts := make([]string, 0, len(c.Archetypes))
	for i, a := range c.Archetypes {
		n := 0
		if i < len(c.Counts) {
			n = c.Counts[i]
		}
		parts = append(parts, fmt.Sprintf("%s=%d", a, n))
	}
	return strings.Join(parts, " · ")
}

// formatCounts64 formatea un mapa de contadores en orden estable.
func formatCounts64(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, " · ")
}
