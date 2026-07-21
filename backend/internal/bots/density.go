package bots

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Población objetivo por arquetipo — modo "densidad dinámica" (GDD §13.4 modo
// 2) y VÁLVULA DE CARGA PRINCIPAL del techo de capacidad (GDD §19, ADR-009).
//
// El controlador NO retira bots (eso es exclusivo del RetirementJob, y solo por
// insolvencia): pausa y reanuda el bucle de Decide de bots ya aprovisionados,
// conservando su cuenta, su capital y su estado local. Es una decisión de
// LIFECYCLE, no de juego: por eso —y solo por eso— lee la BD directamente, en
// lugar de jugar por el SDK como los arquetipos (ADR-010/ADR-024).
//
// SEÑALES (una sola ida y vuelta a la BD por ciclo):
//
//  1. ACTIVIDAD HUMANA — sesiones humanas vivas (auth.sessions no expiradas de
//     cuentas kind='human') y comandos humanos recientes (publicaciones y
//     contratos con un humano en alguno de los lados dentro de la ventana
//     II_BOTS_DENSITY_ACTIVITY_WINDOW). Más humanos ⇒ se PERMITE más densidad,
//     porque hacen falta más contrapartes.
//  2. SATURACIÓN DEL SISTEMA — lag del outbox (eventos PENDIENTES del consumidor
//     más retrasado: para cada cursor de outbox.consumer_cursors, los eventos de
//     LOS TIPOS QUE ESE CONSUMIDOR DECLARA SUSCRITOS —event_types, migración
//     0016— con seq por encima de su cursor; barato sobre ix_outbox_type_seq,
//     transaccional y fiable) y profundidad de la cola de transbordo
//     (world.shipments encolados en terminal sin servir, índice parcial de la
//     migración 0015). El peor de los dos manda. La suscripción es
//     IMPRESCINDIBLE: un consumidor solo avanza su cursor con eventos de sus
//     tipos, así que compararlo con la cabecera global del outbox mide la
//     historia entera del mundo —no el retraso— y clava la válvula en el suelo
//     para siempre. Nota operativa: un consumidor PARADO sí se observa como
//     saturación creciente y recorta la población de bots — es el comportamiento
//     deseado (el mundo no está siguiendo el ritmo), no un falso positivo.
//  3. COBERTURA DE MERCADO — publicaciones vivas del tablón. Con pocas
//     contrapartes se sube la densidad de los arquetipos de backstop de
//     liquidez (productores y comerciantes, GDD §5.3.1).
//
// FÓRMULA (enteros en basis points; sin punto flotante):
//
//	base       = max(1, aprovisionados × II_BOTS_DENSITY_BASE_BP / 10000)
//	f_actividad= 1 + gain_act × min(1, max(sesiones/ref_ses, comandos/ref_cmd))
//	f_carga    = min(rampa(lag_outbox), rampa(cola_transbordo))   [1 → suelo]
//	f_cobertura= 1 + gain_cob × (cobertura_min − vivas)/cobertura_min  (solo backstop)
//
//	si f_carga < 1:  objetivo = clamp(round(base × f_carga), min, max)   ← la carga MANDA
//	si no:           objetivo = clamp(round(base × f_actividad × f_cobertura), min, max)
//
// La prioridad es explícita y auditable (GDD §19): en cuanto el sistema se
// degrada, los bonos de actividad y cobertura se DESCARTAN y solo actúa el
// recorte por carga — se reduce población de bots antes que degradar la
// experiencia humana. El techo nunca supera lo aprovisionado.
//
// SUAVIZADO: cada ciclo mueve como mucho II_BOTS_DENSITY_MAX_STEP bots por
// arquetipo, con banda muerta ASIMÉTRICA (II_BOTS_DENSITY_HYSTERESIS): las
// subidas exigen superar la banda (no se añade carga por ruido), las bajadas se
// aplican de inmediato (proteger al humano no espera). La banda muerta NO se
// aplica cuando el arquetipo está APAGADO (activos 0): la válvula que el recorte
// por carga cierra tiene que poder volver a abrirse aunque el objetivo sea un
// solo bot.

// DensitySignals son las señales OBSERVADAS en un ciclo de densidad. Se leen de
// la BD (lifecycle) y se registran en el log de cada ajuste: la decisión debe
// poder reproducirse a partir de ellas.
type DensitySignals struct {
	// HumanSessions son las sesiones humanas no expiradas.
	HumanSessions int64
	// HumanCommands son los comandos humanos (publicaciones + contratos) dentro
	// de la ventana de actividad.
	HumanCommands int64
	// OutboxLag son los eventos pendientes del consumidor más retrasado: los de
	// SUS tipos suscritos que quedan por encima de su cursor.
	OutboxLag int64
	// TransshipQueue es la profundidad de la cola de transbordo sin servir.
	TransshipQueue int64
	// LivePublications son las publicaciones vivas del tablón.
	LivePublications int64
}

// LogValue expone las señales como grupo estructurado del log (auditoría).
func (s DensitySignals) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("human_sessions", s.HumanSessions),
		slog.Int64("human_commands", s.HumanCommands),
		slog.Int64("outbox_lag", s.OutboxLag),
		slog.Int64("transship_queue", s.TransshipQueue),
		slog.Int64("live_publications", s.LivePublications),
	)
}

// DensityDecision es la decisión de densidad de UN arquetipo en UN ciclo, con
// todos sus intermedios: es el registro auditable de por qué la población se
// movió (o no).
type DensityDecision struct {
	// Archetype es el arquetipo gobernado.
	Archetype string
	// Provisioned es la población aprovisionada (techo físico).
	Provisioned int
	// Base es la población de referencia en un mundo tranquilo y sano.
	Base int
	// Min y Max son los límites efectivos aplicados.
	Min int
	Max int
	// ActivityBP, LoadBP y CoverageBP son los tres factores en basis points.
	ActivityBP int64
	LoadBP     int64
	CoverageBP int64
	// LoadGoverned indica que la señal de carga descartó los bonos de actividad
	// y cobertura (sistema degradado: GDD §19).
	LoadGoverned bool
	// Target es la población activa objetivo tras clamp.
	Target int
	// ActiveBefore y ActiveAfter son la población activa antes y después.
	ActiveBefore int
	ActiveAfter  int
	// Delta es el ajuste aplicado: positivo arranca, negativo para.
	Delta int
}

// LogValue expone la decisión como grupo estructurado del log.
func (d DensityDecision) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("archetype", d.Archetype),
		slog.Int("provisioned", d.Provisioned),
		slog.Int("base", d.Base),
		slog.Int("min", d.Min),
		slog.Int("max", d.Max),
		slog.Int64("activity_bp", d.ActivityBP),
		slog.Int64("load_bp", d.LoadBP),
		slog.Int64("coverage_bp", d.CoverageBP),
		slog.Bool("load_governed", d.LoadGoverned),
		slog.Int("target", d.Target),
		slog.Int("active_before", d.ActiveBefore),
		slog.Int("active_after", d.ActiveAfter),
		slog.Int("delta", d.Delta),
	)
}

// Population es la población ejecutable que la densidad gobierna: bots YA
// aprovisionados cuyo bucle de Decide se puede parar y arrancar en caliente,
// sin reiniciar el proceso y sin retirar la cuenta. La implementa el
// Orchestrator.
type Population interface {
	// Provisioned devuelve la población aprovisionada por arquetipo. Vacío
	// mientras el orquestador no haya aprovisionado.
	Provisioned() map[string]int
	// Active devuelve cuántos bots del arquetipo están ejecutando su bucle.
	Active(archetype string) int
	// Start arranca hasta n bots parados del arquetipo y devuelve cuántos
	// arrancó (menos si no quedan plazas libres).
	Start(archetype string, n int) int
	// Stop para hasta n bots activos del arquetipo, con cierre limpio de su
	// sesión, y devuelve cuántos paró.
	Stop(archetype string, n int) int
}

// DensityMetrics es la instrumentación Prometheus del controlador de densidad.
type DensityMetrics struct {
	// Target publica el objetivo vigente por arquetipo
	// (ii_bots_density_target{archetype}).
	Target *prometheus.GaugeVec
	// Adjustments cuenta los ajustes aplicados por dirección
	// (ii_bots_density_adjustments_total{direction}).
	Adjustments *prometheus.CounterVec
	// OutboxLag publica el lag de outbox observado (ii_outbox_lag_observed).
	OutboxLag prometheus.Gauge
	// Signal publica el resto de señales observadas
	// (ii_bots_density_signal{signal}).
	Signal *prometheus.GaugeVec
}

// NewDensityMetrics registra las métricas del controlador en el registry (nil
// las deja sin instrumentar, para tests).
func NewDensityMetrics(reg prometheus.Registerer) *DensityMetrics {
	m := &DensityMetrics{
		Target: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_bots_density_target",
			Help: "Población activa objetivo de cada arquetipo según la densidad dinámica.",
		}, []string{"archetype"}),
		Adjustments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_bots_density_adjustments_total",
			Help: "Total de bots arrancados (up) y parados (down) por la densidad dinámica.",
		}, []string{"direction"}),
		OutboxLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_outbox_lag_observed",
			Help: "Lag del outbox observado por la densidad: eventos pendientes (de sus tipos suscritos) del consumidor más retrasado.",
		}),
		Signal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "ii_bots_density_signal",
			Help: "Señales observadas por la densidad dinámica (sesiones y comandos humanos, cola de transbordo, publicaciones vivas).",
		}, []string{"signal"}),
	}
	if reg != nil {
		reg.MustRegister(m.Target, m.Adjustments, m.OutboxLag, m.Signal)
	}
	return m
}

// Direcciones de ajuste (etiqueta de ii_bots_density_adjustments_total).
const (
	densityDirectionUp   = "up"
	densityDirectionDown = "down"
)

// DensityController ajusta continuamente la población ACTIVA de bots por
// arquetipo (GDD §13.4 modo 2). Ver la nota de cabecera del fichero para las
// señales y la fórmula.
type DensityController struct {
	pool    *pgxpool.Pool
	opts    DensityOptions
	pop     Population
	logger  *slog.Logger
	metrics *DensityMetrics
}

// NewDensityController construye el controlador sobre el pool compartido (las
// señales son lecturas de lifecycle) y la población a gobernar. reg puede ser
// nil (tests sin instrumentar); logger nil usa slog.Default.
func NewDensityController(pool *pgxpool.Pool, opts DensityOptions, pop Population, logger *slog.Logger, reg prometheus.Registerer) (*DensityController, error) {
	if pool == nil {
		return nil, errors.New("bots: el controlador de densidad requiere un pool de BD")
	}
	if pop == nil {
		return nil, errors.New("bots: el controlador de densidad requiere una población")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &DensityController{
		pool:    pool,
		opts:    opts,
		pop:     pop,
		logger:  logger,
		metrics: NewDensityMetrics(reg),
	}, nil
}

// Run ejecuta el bucle de ajuste hasta que ctx se cancele (nil en el apagado
// limpio). La cadencia es EXACTA (sin jitter): es un lazo de control y su
// estabilidad depende de un periodo constante. El primer ciclo espera un
// intervalo, para dar tiempo al orquestador a aprovisionar y arrancar.
func (c *DensityController) Run(ctx context.Context) error {
	if !c.opts.Enabled {
		c.logger.Info("bots: densidad dinámica desactivada: la población arrancada queda fija",
			slog.String("env", EnvDensityEnabled))
		return nil
	}
	c.logger.Info("bots: densidad dinámica iniciada",
		slog.Duration("interval", c.opts.Interval),
		slog.Int64("base_bp", c.opts.BaseBP),
		slog.Int64("activity_gain_bp", c.opts.ActivityGainBP),
		slog.Int64("lag_low", c.opts.LagLow),
		slog.Int64("lag_high", c.opts.LagHigh),
		slog.Int64("queue_low", c.opts.QueueLow),
		slog.Int64("queue_high", c.opts.QueueHigh),
		slog.Int64("load_floor_bp", c.opts.LoadFloorBP),
		slog.Int64("coverage_min", c.opts.CoverageMin),
		slog.Int("max_step", c.opts.MaxStep),
		slog.Int("hysteresis", c.opts.Hysteresis))
	for {
		if err := sleepCtx(ctx, c.opts.Interval); err != nil {
			c.logger.Info("bots: densidad dinámica detenida")
			return nil
		}
		if _, err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
			c.logger.Warn("bots: ciclo de densidad con error", slog.Any("error", err))
		}
	}
}

// RunOnce ejecuta UN ciclo: observa las señales, calcula el objetivo de cada
// arquetipo y aplica el ajuste suavizado. Devuelve las decisiones tomadas (para
// tests y tooling de auditoría). Aislado del bucle para que los tests controlen
// el disparo.
func (c *DensityController) RunOnce(ctx context.Context) ([]DensityDecision, error) {
	signals, err := c.Observe(ctx)
	if err != nil {
		return nil, err
	}
	c.publishSignals(signals)

	provisioned := c.pop.Provisioned()
	if len(provisioned) == 0 {
		c.logger.Debug("bots: densidad sin población aprovisionada todavía", slog.Any("signals", signals))
		return nil, nil
	}

	decisions := make([]DensityDecision, 0, len(DensityArchetypes))
	for _, archetype := range DensityArchetypes {
		n, governed := provisioned[archetype]
		if !governed {
			continue
		}
		d := c.opts.TargetFor(archetype, n, signals)
		d.ActiveBefore = c.pop.Active(archetype)
		d.Delta = c.opts.Step(d.ActiveBefore, d.Target)

		switch {
		case d.Delta > 0:
			started := c.pop.Start(archetype, d.Delta)
			d.Delta = started
			c.metrics.Adjustments.WithLabelValues(densityDirectionUp).Add(float64(started))
		case d.Delta < 0:
			stopped := c.pop.Stop(archetype, -d.Delta)
			d.Delta = -stopped
			c.metrics.Adjustments.WithLabelValues(densityDirectionDown).Add(float64(stopped))
		}
		d.ActiveAfter = c.pop.Active(archetype)

		c.metrics.Target.WithLabelValues(archetype).Set(float64(d.Target))
		switch {
		case d.Delta != 0:
			// Cada AJUSTE se registra con sus señales: la decisión es auditable
			// y reproducible a partir del log (GDD §13.3).
			c.logger.Info("bots: densidad ajustada",
				slog.Any("decision", d), slog.Any("signals", signals))
		case d.ActiveAfter != d.Target:
			// Ciclo SIN ajuste con la población fuera del objetivo: la banda
			// muerta o el paso lo retuvieron. Se registra en Info para que el
			// estancamiento sea visible en la auditoría (no un silencio).
			c.logger.Info("bots: densidad retenida",
				slog.Any("decision", d), slog.Any("signals", signals))
		default:
			c.logger.Debug("bots: densidad estable",
				slog.Any("decision", d), slog.Any("signals", signals))
		}
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// publishSignals refleja las señales observadas en las métricas.
func (c *DensityController) publishSignals(s DensitySignals) {
	c.metrics.OutboxLag.Set(float64(s.OutboxLag))
	c.metrics.Signal.WithLabelValues("human_sessions").Set(float64(s.HumanSessions))
	c.metrics.Signal.WithLabelValues("human_commands").Set(float64(s.HumanCommands))
	c.metrics.Signal.WithLabelValues("transship_queue").Set(float64(s.TransshipQueue))
	c.metrics.Signal.WithLabelValues("live_publications").Set(float64(s.LivePublications))
}

// Observe lee las señales del ciclo en UNA sola consulta (barata: cuentas sobre
// índices existentes). Exportada para tooling de auditoría y tests.
func (c *DensityController) Observe(ctx context.Context) (DensitySignals, error) {
	var s DensitySignals
	windowSeconds := c.opts.ActivityWindow.Seconds()
	if err := c.pool.QueryRow(ctx, densitySignalsSQL, windowSeconds).Scan(
		&s.HumanSessions, &s.HumanCommands, &s.OutboxLag, &s.TransshipQueue, &s.LivePublications,
	); err != nil {
		return DensitySignals{}, fmt.Errorf("bots: leyendo las señales de densidad: %w", err)
	}
	return s, nil
}

// TargetFor calcula el objetivo de población activa de UN arquetipo a partir de
// las señales. Es una función PURA de (opciones, aprovisionados, señales): toda
// la política de densidad vive aquí y se prueba sin BD.
func (o DensityOptions) TargetFor(archetype string, provisioned int, s DensitySignals) DensityDecision {
	d := DensityDecision{
		Archetype:   archetype,
		Provisioned: provisioned,
		ActivityBP:  densityBP,
		LoadBP:      densityBP,
		CoverageBP:  densityBP,
	}
	if provisioned <= 0 {
		return d
	}
	d.Min, d.Max = o.bounds(archetype, provisioned)
	d.Base = o.baseFor(provisioned)
	d.ActivityBP = o.activityBP(s)
	d.LoadBP = o.loadBP(s)
	if liquidityBackstop[archetype] {
		d.CoverageBP = o.coverageBP(s)
	}

	var raw int64
	if d.LoadBP < densityBP {
		// Sistema degradado: la carga MANDA sobre la demanda (GDD §19). Los
		// bonos de actividad y cobertura se descartan: primero se protege la
		// experiencia humana, después se busca contraparte.
		d.LoadGoverned = true
		raw = scaleBP(int64(d.Base), d.LoadBP)
	} else {
		raw = scaleBP(scaleBP(int64(d.Base), d.ActivityBP), d.CoverageBP)
	}
	d.Target = clampInt(int(raw), d.Min, d.Max)
	return d
}

// Step decide el AJUSTE de un ciclo: suavizado (como mucho MaxStep bots) con
// banda muerta ASIMÉTRICA — subir exige superar Hysteresis (no se añade carga
// por ruido), bajar es inmediato (GDD §19).
//
// La banda muerta NO se aplica sobre el arranque desde CERO: con el arquetipo
// apagado no hay ruido que filtrar, hay ausencia de contrapartes. Filtrarlo
// dejaría la válvula cerrada para siempre en poblaciones pequeñas (objetivo 1 y
// activos 0 caen dentro de la banda con la histéresis por defecto), justo lo
// contrario de lo que exige el GDD §19: la válvula debe poder REABRIRSE en
// cuanto la carga cede.
func (o DensityOptions) Step(active, target int) int {
	diff := target - active
	switch {
	case diff > 0:
		if diff <= o.Hysteresis && active > 0 {
			return 0
		}
		return min(diff, o.MaxStep)
	case diff < 0:
		return -min(-diff, o.MaxStep)
	default:
		return 0
	}
}

// baseFor es la población de referencia del arquetipo en un mundo tranquilo y
// sano: una fracción de lo aprovisionado, con al menos un bot vivo (el modo
// "mundo vivo" del GDD §13.4 nunca deja el arquetipo a cero por defecto; el
// recorte por carga sí puede llevarlo a cero).
func (o DensityOptions) baseFor(provisioned int) int {
	base := int(scaleBP(int64(provisioned), o.BaseBP))
	return clampInt(base, 1, provisioned)
}

// activityBP es el factor de actividad humana: 1 sin humanos (la base mantiene
// el mundo vivo) y hasta 1+ActivityGainBP con actividad plena. Sesiones y
// comandos se comparan cada uno con su propia referencia y manda el mayor: son
// magnitudes distintas y no se suman.
func (o DensityOptions) activityBP(s DensitySignals) int64 {
	ratio := int64(0)
	if o.SessionsRef > 0 {
		ratio = max(ratio, s.HumanSessions*densityBP/o.SessionsRef)
	}
	if o.CommandsRef > 0 {
		ratio = max(ratio, s.HumanCommands*densityBP/o.CommandsRef)
	}
	ratio = min(max(ratio, 0), densityBP)
	return densityBP + o.ActivityGainBP*ratio/densityBP
}

// loadBP es el factor de carga: 1 con el sistema sano y LoadFloorBP con el
// sistema saturado, interpolado en la rampa. Manda la PEOR de las dos señales
// (lag de outbox y cola de transbordo).
func (o DensityOptions) loadBP(s DensitySignals) int64 {
	return min(
		rampDownBP(s.OutboxLag, o.LagLow, o.LagHigh, o.LoadFloorBP),
		rampDownBP(s.TransshipQueue, o.QueueLow, o.QueueHigh, o.LoadFloorBP),
	)
}

// coverageBP es el factor de cobertura de mercado: 1 con el tablón surtido y
// hasta 1+CoverageGainBP con el tablón vacío (backstop de liquidez, GDD §5.3.1).
func (o DensityOptions) coverageBP(s DensitySignals) int64 {
	if o.CoverageMin <= 0 || s.LivePublications >= o.CoverageMin {
		return densityBP
	}
	deficit := o.CoverageMin - max(s.LivePublications, 0)
	return densityBP + o.CoverageGainBP*deficit/o.CoverageMin
}

// rampDownBP interpola linealmente 10000 → floorBP conforme v recorre (low, high).
func rampDownBP(v, low, high, floorBP int64) int64 {
	if v <= low {
		return densityBP
	}
	if v >= high || high <= low {
		return floorBP
	}
	return densityBP - (densityBP-floorBP)*(v-low)/(high-low)
}

// scaleBP aplica un factor en basis points con redondeo al más cercano
// (medio hacia arriba): las poblaciones son enteras y el redondeo debe ser
// simétrico y reproducible.
func scaleBP(v, bp int64) int64 {
	return (v*bp + densityBP/2) / densityBP
}

// clampInt acota v al intervalo [lo, hi].
func clampInt(v, lo, hi int) int {
	return min(max(v, lo), hi)
}

// densitySignalsSQL lee de una sola pasada las cinco señales del ciclo:
// sesiones humanas vivas, comandos humanos de la ventana, lag del outbox
// (eventos pendientes del consumidor más retrasado), profundidad de la cola de
// transbordo y publicaciones vivas del tablón. $1 es la ventana de actividad en
// segundos.
//
// consumer_backlog mide el retraso de CADA consumidor contra los eventos que él
// mismo declara consumir (outbox.consumer_cursors.event_types, que el propio
// consumidor mantiene al día en cada polling; migración 0016): eventos de sus
// tipos con seq por encima de su cursor, resuelto por ix_outbox_type_seq. Un
// cursor sin suscripción declarada aporta 0 — no se mide lo que no se sabe.
const densitySignalsSQL = `
WITH consumer_backlog AS (
  SELECT (SELECT count(*)
            FROM outbox.events e
           WHERE e.seq > c.last_seq
             AND e.event_type = ANY (c.event_types)) AS pending
    FROM outbox.consumer_cursors c
)
SELECT
  (SELECT count(*) FROM auth.sessions s
     JOIN auth.accounts a ON a.id = s.account_id
    WHERE a.kind = 'human' AND s.expires_at > now())::bigint AS human_sessions,

  ((SELECT count(*) FROM ledger.publications p
      JOIN auth.accounts a ON a.id = p.publisher_account_id
     WHERE a.kind = 'human'
       AND p.created_at > now() - ($1::double precision * interval '1 second'))
   + (SELECT count(*) FROM ledger.contracts c
       WHERE c.created_at > now() - ($1::double precision * interval '1 second')
         AND EXISTS (SELECT 1 FROM auth.accounts a
                      WHERE a.id IN (c.buyer_account_id, c.seller_account_id)
                        AND a.kind = 'human')))::bigint AS human_commands,

  COALESCE((SELECT max(pending) FROM consumer_backlog), 0)::bigint AS outbox_lag,

  (SELECT count(*) FROM world.shipments
    WHERE status = 'at_terminal' AND transship_ready_at_sim IS NULL)::bigint AS transship_queue,

  (SELECT count(*) FROM ledger.publications
    WHERE status IN ('draw_window', 'open', 'micro_window'))::bigint AS live_publications
`
