package stress

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
)

// Parámetros del transporte HTTP del harness. Un cliente compartido con pool
// generoso: con cientos de bots, el default de MaxIdleConnsPerHost (2) mediría
// el coste de abrir sockets en vez de la respuesta del sistema.
const (
	httpRequestTimeout  = 30 * time.Second
	httpDialTimeout     = 10 * time.Second
	httpIdleConnTimeout = 90 * time.Second
	httpMinIdlePerHost  = 64
	httpTLSHandshake    = 10 * time.Second
	drainTimeout        = 30 * time.Second
	probeTimeout        = 45 * time.Second
	cleanupTimeout      = 60 * time.Second
	// loadGrace es la holgura sobre rampa+duración de la ventana de carga: deja
	// terminar la última petición en vuelo en lugar de cancelarla (una petición
	// cancelada por el propio harness no mide nada del sistema).
	loadGrace = 2 * httpRequestTimeout
)

// ErrNoDatabase señala que el harness no recibió pool de BD: el provisioning de
// cuentas es obligatorio porque el contrato no expone endpoint de registro.
var ErrNoDatabase = errors.New("stress: el harness necesita el pool de la BD del entorno de pruebas para aprovisionar cuentas (el contrato no expone endpoint de registro)")

// Runner ejecuta una corrida completa: aprovisiona la población, la arranca con
// rampa, genera carga durante la duración configurada, mide el sistema bajo
// prueba y limpia.
type Runner struct {
	opts        Options
	logger      *slog.Logger
	metrics     *Metrics
	collector   *Collector
	provisioner *Provisioner
	pool        *pgxpool.Pool
	httpc       *http.Client

	activeBots atomic.Int64
}

// NewRunner construye el harness sobre el pool del ENTORNO DE PRUEBAS (usado
// solo por el provisioner y por el sondeo de BD; el gameplay va por la API).
// reg puede ser nil (tests sin instrumentar).
func NewRunner(pool *pgxpool.Pool, opts Options, ledgerOpts ledger.Options, logger *slog.Logger, reg prometheus.Registerer) (*Runner, error) {
	if pool == nil {
		return nil, ErrNoDatabase
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	prov, err := NewProvisioner(pool, opts, ledgerOpts, logger)
	if err != nil {
		return nil, err
	}
	return &Runner{
		opts:        opts,
		logger:      logger,
		metrics:     NewMetrics(reg),
		collector:   NewCollector(opts.MaxSamples, collectorSeed(opts.RunID)),
		provisioner: prov,
		pool:        pool,
		httpc:       newHTTPClient(opts.Bots),
	}, nil
}

// newHTTPClient construye el cliente HTTP compartido del harness.
func newHTTPClient(bots int) *http.Client {
	perHost := max(bots, httpMinIdlePerHost)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   httpDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        perHost * 2,
		MaxIdleConnsPerHost: perHost,
		MaxConnsPerHost:     0, // sin tope: el tope real lo pone el sistema bajo prueba
		IdleConnTimeout:     httpIdleConnTimeout,
		TLSHandshakeTimeout: httpTLSHandshake,
		ForceAttemptHTTP2:   true,
	}
	return &http.Client{Transport: transport, Timeout: httpRequestTimeout}
}

// collectorSeed deriva una semilla determinista del identificador de corrida
// (FNV-1a): el muestreo de reservorio es reproducible para un mismo run_id.
func collectorSeed(runID string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(runID); i++ {
		h ^= uint64(runID[i])
		h *= 1099511628211
	}
	return h
}

// Run ejecuta la corrida completa y devuelve el informe. Siempre devuelve
// informe salvo que el provisioning falle: la carga puede interrumpirse (señal,
// cancelación) y lo medido hasta ese momento sigue siendo válido.
func (r *Runner) Run(ctx context.Context) (*Report, error) {
	r.logger.Info("corrida de stress arrancando",
		slog.String("run_id", r.opts.RunID),
		slog.String("api_url", r.opts.APIURL),
		slog.String("env", r.opts.Env),
		slog.String("allowlist_match", r.opts.AllowMatch),
		slog.String("account_prefix", r.opts.RunAccountPrefix()),
		slog.Int("bots", r.opts.Bots),
		slog.String("mix", r.opts.Mix.String()),
		slog.Duration("ramp", r.opts.Ramp),
		slog.Duration("duration", r.opts.Duration),
		slog.Duration("tick", r.opts.Tick),
		slog.Float64("write_ratio", r.opts.WriteRatio))

	bots, err := r.provisioner.Provision(ctx)
	if err != nil {
		return nil, err
	}
	r.metrics.ProvisionedBots.Set(float64(len(bots)))

	// LÍNEA BASE del sistema bajo prueba ANTES de la carga: sus métricas son
	// contadores acumulados desde el arranque del proceso, así que solo el delta
	// contra esta lectura es atribuible a la corrida (y es el que juzga el
	// veredicto). Si no es accesible, el informe lo deja registrado.
	baselineCtx, cancelBaseline := context.WithTimeout(ctx, probeTimeout)
	baseline := ScrapeTargets(baselineCtx, r.httpc, r.opts.TargetMetrics)
	cancelBaseline()

	started := time.Now()
	// La ventana de carga lleva una holgura sobre rampa+duración: los bots paran
	// por RELOJ DE PARED al llegar al final, y la holgura evita que la última
	// petición en vuelo muera cancelada y ensucie la medición. Si un bot se
	// quedase colgado, la holgura sigue acotando la corrida.
	loadCtx, stopLoad := context.WithTimeout(ctx, r.opts.Ramp+r.opts.Duration+loadGrace)
	defer stopLoad()

	progressCtx, stopProgress := context.WithCancel(loadCtx)
	defer stopProgress()
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		r.logProgress(progressCtx, started)
	}()

	var wg sync.WaitGroup
	for i, bot := range bots {
		wg.Add(1)
		go func(i int, bot StressBot) {
			defer wg.Done()
			r.runBot(loadCtx, i, len(bots), bot, started)
		}(i, bot)
	}
	wg.Wait()
	elapsed := time.Since(started)
	stopProgress()
	<-progressDone
	stopLoad()

	// El sondeo y la limpieza usan un contexto propio: deben completarse aunque
	// la corrida se haya interrumpido por señal.
	report := r.buildReport(started, elapsed, bots, baseline)
	if err := report.WriteJSON(r.opts.ReportPath); err != nil {
		// El informe en consola y el veredicto siguen siendo válidos: un fallo
		// de escritura no invalida lo medido.
		r.logger.Error("no se pudo escribir el informe JSON",
			slog.String("path", r.opts.ReportPath), slog.Any("error", err))
	} else {
		r.logger.Info("informe de stress escrito",
			slog.String("path", r.opts.ReportPath), slog.String("run_id", r.opts.RunID))
	}
	r.logger.Info("corrida de stress terminada",
		slog.String("run_id", r.opts.RunID),
		slog.Duration("elapsed", elapsed),
		slog.Int64("requests", report.Totals.Requests),
		slog.Float64("ops_per_second", report.Totals.OpsPerSecond),
		slog.Int64("errors", report.Totals.Errors),
		slog.Bool("verdict_ok", report.Verdict.OK))
	return report, nil
}

// buildReport ensambla el informe final: agregados del colector, medición del
// sistema bajo prueba (en delta contra la línea base previa a la carga) y
// limpieza.
func (r *Runner) buildReport(started time.Time, elapsed time.Duration, bots []StressBot, baseline []TargetMetrics) *Report {
	ops, totals := r.collector.Snapshot(elapsed)

	probeCtx, cancelProbe := context.WithTimeout(context.Background(), probeTimeout)
	defer cancelProbe()
	targets := ScrapeTargets(probeCtx, r.httpc, r.opts.TargetMetrics)
	ApplyBaseline(targets, baseline)
	system := SystemReport{
		Targets:  targets,
		Database: ProbeDatabase(probeCtx, r.pool, r.opts.RunAccountPrefix(), started, elapsed),
	}

	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancelCleanup()
	cleanup := r.provisioner.Cleanup(cleanupCtx, bots)

	// La limpieza cambia el recuento de cuentas activas: SOLO ese recuento se
	// vuelve a leer tras retirar, para que el informe muestre el estado REAL.
	// El resto del sondeo se queda en el instante del FIN DE LA CARGA, el mismo
	// en el que se raspó ScrapeTargets milisegundos antes: si se sustituyese el
	// sondeo entero, el lag de la outbox en la fuente quedaría medido decenas de
	// segundos DESPUÉS que el publicado por las métricas y las dos lecturas del
	// mismo disparador (SAD §13) dejarían de ser comparables.
	if system.Database.Reachable {
		post := ProbeDatabase(cleanupCtx, r.pool, r.opts.RunAccountPrefix(), started, elapsed)
		if post.Reachable {
			system.Database.StressAccounts = post.StressAccounts
			system.Database.StressAccountsActive = post.StressAccountsActive
		}
	}

	report := &Report{
		RunID:           r.opts.RunID,
		StartedAt:       started,
		FinishedAt:      started.Add(elapsed),
		DurationSeconds: elapsed.Seconds(),
		Config:          NewConfigReport(r.opts),
		Totals:          totals,
		Operations:      ops,
		System:          system,
		Cleanup:         cleanup,
	}
	report.Verdict = buildVerdict(report)
	return report
}

// runBot ejecuta el ciclo de vida de un bot: espera su turno de la rampa, entra,
// cachea el mundo, genera carga hasta el final y sale.
func (r *Runner) runBot(ctx context.Context, index, total int, bot StressBot, started time.Time) {
	if err := sleepCtx(ctx, rampDelay(r.opts.Ramp, index, total)); err != nil {
		return
	}
	sess, err := newSession(bot, r.opts.APIURL, r.httpc, r.opts.Capital, r.opts.SellShare, r.logger, collectorSeed(bot.Name), r.record)
	if err != nil {
		r.logger.Error("no se pudo construir el bot de stress",
			slog.String("bot", describeSession(bot)), slog.Any("error", err))
		return
	}
	if err := sess.Login(ctx); err != nil {
		if ctx.Err() == nil {
			r.logger.Warn("login de bot de stress fallido",
				slog.String("bot", describeSession(bot)), slog.Any("error", err))
		}
		return
	}
	r.activeBots.Add(1)
	r.metrics.ActiveBots.Set(float64(r.activeBots.Load()))
	defer func() {
		r.activeBots.Add(-1)
		r.metrics.ActiveBots.Set(float64(r.activeBots.Load()))
	}()

	sess.Bootstrap(ctx)

	deadline := started.Add(r.opts.Ramp + r.opts.Duration)
	for ctx.Err() == nil && time.Now().Before(deadline) {
		sess.Act(ctx, r.opts.WriteRatio)
		if err := sleepCtx(ctx, jitter(sess.rng, r.opts.Tick)); err != nil {
			break
		}
	}

	// Cierre: contexto propio para que la higiene se complete aunque la carga
	// se haya cortado por señal.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drainTimeout)
	defer cancel()
	cancelled, left := sess.Drain(closeCtx)
	if cancelled > 0 || left > 0 {
		r.logger.Debug("publicaciones del bot al cerrar",
			slog.String("bot", bot.Name), slog.Int("cancelled", cancelled), slog.Int("left", left))
	}
	sess.Logout(closeCtx)
}

// rampDelay reparte los arranques de forma uniforme a lo largo de la rampa: el
// bot i entra en ramp·i/total (rampa 0 ⇒ todos a la vez).
func rampDelay(ramp time.Duration, index, total int) time.Duration {
	if ramp <= 0 || total <= 1 {
		return 0
	}
	return time.Duration(int64(ramp) * int64(index) / int64(total))
}

// record acumula un resultado en el colector y en las métricas del harness.
func (r *Runner) record(res Result) {
	r.collector.Record(res)
	r.metrics.observe(res)
}

// logProgress emite el log estructurado periódico de la corrida (throughput
// acumulado, latencia del tablón y errores) hasta que la carga termina.
func (r *Runner) logProgress(ctx context.Context, started time.Time) {
	ticker := time.NewTicker(r.opts.LogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		elapsed := time.Since(started)
		ops, totals := r.collector.Snapshot(elapsed)
		attrs := []any{
			slog.String("run_id", r.opts.RunID),
			slog.Duration("elapsed", elapsed.Round(time.Second)),
			slog.Int64("active_bots", r.activeBots.Load()),
			slog.Int64("requests", totals.Requests),
			slog.Float64("ops_per_second", round2(totals.OpsPerSecond)),
			slog.Int64("errors", totals.Errors),
			slog.Int64("unexpected_errors", totals.UnexpectedError),
			slog.Int64("rate_limited", totals.ErrorsBySt[ClassRateLimited]),
			slog.Int64("server_errors", totals.ErrorsBySt[ClassServer]),
		}
		if board := findOp(ops, OpBoardRead); board != nil {
			attrs = append(attrs,
				slog.Float64("board_p50_ms", round2(board.Latency.P50Ms)),
				slog.Float64("board_p95_ms", round2(board.Latency.P95Ms)),
				slog.Float64("board_p99_ms", round2(board.Latency.P99Ms)))
		}
		r.logger.Info("progreso de la corrida de stress", attrs...)
	}
}

// round2 redondea a dos decimales para los logs.
func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}
