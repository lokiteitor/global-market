// Integración de la DENSIDAD DINÁMICA de bots (GDD §13.4 modo 2, §19) contra
// una BD real con el esquema migrado y el seed de los Incrementos 1-4. Ejercita
// el DensityController directamente (disparo por RunOnce) sobre una población
// gobernable, con las señales LEÍDAS DE LA BD — sin mocks de SQL:
//
//  1. SISTEMA SANO: el controlador sube la población activa hasta el objetivo,
//     como mucho MaxStep bots por ciclo (suavizado).
//  2. LAG DE OUTBOX SINTÉTICO ALTO (eventos insertados + cursor de consumidor
//     retrasado): el objetivo cae, la decisión queda gobernada por la carga y
//     el controlador PARA bots — antes que degradar la experiencia humana.
//  3. NORMALIZACIÓN (el cursor alcanza la cabecera): el objetivo vuelve a la
//     base y el controlador REANUDA los bots parados.
//  4. ACTIVIDAD HUMANA: una sesión humana viva se observa en las señales y
//     PERMITE más densidad.
//
// TestBotsDensityLagIgnoresUnsubscribedEvents añade la regresión de la señal de
// saturación con un consumidor REAL (sin tocar cursores a mano).
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package bots_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/seed"
)

// fakePopulation es una población gobernable en memoria: el controlador solo
// necesita contar plazas y ordenar arranques/paradas, así que el test observa
// exactamente esas órdenes sin levantar la API ni sesiones reales (el
// supervisor real se prueba en density_unit_test.go).
type fakePopulation struct {
	mu          sync.Mutex
	provisioned map[string]int
	active      map[string]int
	started     map[string]int
	stopped     map[string]int
}

func newFakePopulation(provisioned map[string]int) *fakePopulation {
	return &fakePopulation{
		provisioned: provisioned,
		active:      map[string]int{},
		started:     map[string]int{},
		stopped:     map[string]int{},
	}
}

func (p *fakePopulation) Provisioned() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]int, len(p.provisioned))
	for k, v := range p.provisioned {
		out[k] = v
	}
	return out
}

func (p *fakePopulation) Active(archetype string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active[archetype]
}

func (p *fakePopulation) Start(archetype string, n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	free := p.provisioned[archetype] - p.active[archetype]
	if n > free {
		n = free
	}
	if n < 0 {
		n = 0
	}
	p.active[archetype] += n
	p.started[archetype] += n
	return n
}

func (p *fakePopulation) Stop(archetype string, n int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n > p.active[archetype] {
		n = p.active[archetype]
	}
	if n < 0 {
		n = 0
	}
	p.active[archetype] -= n
	p.stopped[archetype] += n
	return n
}

func (p *fakePopulation) counters(archetype string) (started, stopped int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started[archetype], p.stopped[archetype]
}

const (
	densityProvisioned = 4
	// densityTestConsumer es el consumidor lógico cuyo cursor se retrasa a mano
	// para fabricar el lag sintético, y densityTestEvent el tipo que declara
	// consumir (el retraso se mide contra SUS eventos, no contra la cabecera).
	densityTestConsumer = "density_test_consumer"
	densityTestEvent    = "density.test"
)

func TestBotsDensityIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: itDemoName, DemoSecret: itDemoSecret,
		TraderName: itNorteName, TraderSecret: itNorteSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, itSimBase)

	pop := newFakePopulation(map[string]int{
		bots.ArchetypeCoalProducer: densityProvisioned,
		bots.ArchetypeFreighter:    densityProvisioned,
	})

	// Configuración del test: base = población aprovisionada, rampa de carga
	// corta (un puñado de eventos basta para saturar) y sin bonos de actividad
	// ni cobertura, para aislar la señal de carga. El paso máximo es 2: el
	// suavizado debe verse en los ciclos.
	opts := bots.DefaultDensityOptions()
	opts.BaseBP = 10_000
	opts.ActivityGainBP = 0
	opts.CoverageMin = 0
	opts.LagLow = 1
	opts.LagHigh = 10
	opts.LoadFloorBP = 0
	opts.MaxStep = 2
	opts.Hysteresis = 0
	if err := opts.Validate(); err != nil {
		t.Fatalf("configuración de densidad inválida: %v", err)
	}

	ctrl, err := bots.NewDensityController(pool, opts, pop, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewDensityController: %v", err)
	}

	// ── 1. Sistema sano: la densidad sube hasta el objetivo, 2 bots por ciclo ──
	decisions := runDensityCycle(t, ctx, ctrl)
	coal := decisionFor(t, decisions, bots.ArchetypeCoalProducer)
	if coal.Target != densityProvisioned {
		t.Fatalf("objetivo en calma = %d, esperado %d", coal.Target, densityProvisioned)
	}
	if coal.LoadGoverned {
		t.Fatal("sin lag observado la decisión no debe estar gobernada por la carga")
	}
	if coal.Delta != opts.MaxStep || pop.Active(bots.ArchetypeCoalProducer) != opts.MaxStep {
		t.Fatalf("primer ciclo: delta %d activos %d, esperado %d y %d",
			coal.Delta, pop.Active(bots.ArchetypeCoalProducer), opts.MaxStep, opts.MaxStep)
	}
	runDensityCycle(t, ctx, ctrl)
	for _, archetype := range []string{bots.ArchetypeCoalProducer, bots.ArchetypeFreighter} {
		if got := pop.Active(archetype); got != densityProvisioned {
			t.Fatalf("%s: activos tras dos ciclos = %d, esperado %d", archetype, got, densityProvisioned)
		}
	}

	// ── 2. Lag de outbox sintético alto: el objetivo cae y se paran bots ──────
	insertOutboxEvents(t, ctx, pool, densityTestEvent, 20)
	setConsumerCursor(t, ctx, pool, densityTestConsumer, densityTestEvent, 0)

	signals, err := ctrl.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if signals.OutboxLag != 20 {
		t.Fatalf("lag observado = %d, esperado 20", signals.OutboxLag)
	}

	decisions = runDensityCycle(t, ctx, ctrl)
	coal = decisionFor(t, decisions, bots.ArchetypeCoalProducer)
	if !coal.LoadGoverned {
		t.Fatal("con el lag por encima del umbral la decisión debe estar gobernada por la carga")
	}
	if coal.Target != 0 {
		t.Fatalf("objetivo saturado = %d, esperado 0", coal.Target)
	}
	if coal.Delta != -opts.MaxStep {
		t.Fatalf("ajuste a la baja = %d, esperado %d", coal.Delta, -opts.MaxStep)
	}
	if got := pop.Active(bots.ArchetypeCoalProducer); got != densityProvisioned-opts.MaxStep {
		t.Fatalf("activos tras el recorte = %d, esperado %d", got, densityProvisioned-opts.MaxStep)
	}
	runDensityCycle(t, ctx, ctrl)
	for _, archetype := range []string{bots.ArchetypeCoalProducer, bots.ArchetypeFreighter} {
		if got := pop.Active(archetype); got != 0 {
			t.Fatalf("%s: activos con el sistema saturado = %d, esperado 0", archetype, got)
		}
		if _, stopped := pop.counters(archetype); stopped != densityProvisioned {
			t.Fatalf("%s: parados = %d, esperado %d", archetype, stopped, densityProvisioned)
		}
	}

	// ── 3. Normalización: el cursor alcanza la cabecera y los bots se reanudan ─
	advanceConsumerCursor(t, ctx, pool, densityTestConsumer)
	signals, err = ctrl.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if signals.OutboxLag != 0 {
		t.Fatalf("lag tras normalizar = %d, esperado 0", signals.OutboxLag)
	}

	decisions = runDensityCycle(t, ctx, ctrl)
	coal = decisionFor(t, decisions, bots.ArchetypeCoalProducer)
	if coal.LoadGoverned || coal.Target != densityProvisioned {
		t.Fatalf("tras normalizar: gobernado %v objetivo %d, esperado false/%d",
			coal.LoadGoverned, coal.Target, densityProvisioned)
	}
	if coal.Delta != opts.MaxStep {
		t.Fatalf("reanudación: delta %d, esperado %d", coal.Delta, opts.MaxStep)
	}
	runDensityCycle(t, ctx, ctrl)
	for _, archetype := range []string{bots.ArchetypeCoalProducer, bots.ArchetypeFreighter} {
		if got := pop.Active(archetype); got != densityProvisioned {
			t.Fatalf("%s: activos tras normalizar = %d, esperado %d", archetype, got, densityProvisioned)
		}
		if started, _ := pop.counters(archetype); started != 2*densityProvisioned {
			t.Fatalf("%s: arrancados acumulados = %d, esperado %d", archetype, started, 2*densityProvisioned)
		}
	}

	// ── 4. Actividad humana: una sesión viva permite más densidad ─────────────
	activityOpts := opts
	activityOpts.BaseBP = 5_000 // base = 2 de 4 aprovisionados
	activityOpts.ActivityGainBP = 10_000
	activityOpts.SessionsRef = 1
	activityPop := newFakePopulation(map[string]int{bots.ArchetypeCoalProducer: densityProvisioned})
	activityCtrl, err := bots.NewDensityController(pool, activityOpts, activityPop, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewDensityController (actividad): %v", err)
	}

	decisions = runDensityCycle(t, ctx, activityCtrl)
	quiet := decisionFor(t, decisions, bots.ArchetypeCoalProducer)
	if quiet.Base != 2 || quiet.Target != 2 {
		t.Fatalf("sin humanos: base %d objetivo %d, esperado 2/2", quiet.Base, quiet.Target)
	}

	openHumanSession(t, ctx, pool, itDemoName)
	signals, err = activityCtrl.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if signals.HumanSessions != 1 {
		t.Fatalf("sesiones humanas observadas = %d, esperado 1", signals.HumanSessions)
	}
	decisions = runDensityCycle(t, ctx, activityCtrl)
	busy := decisionFor(t, decisions, bots.ArchetypeCoalProducer)
	if busy.Target <= quiet.Target {
		t.Fatalf("con actividad humana el objetivo (%d) debe superar al tranquilo (%d)", busy.Target, quiet.Target)
	}
	if busy.Target != densityProvisioned {
		t.Fatalf("objetivo con actividad plena = %d, esperado %d", busy.Target, densityProvisioned)
	}

	// ── 5. REGRESIÓN: la válvula reabre en poblaciones pequeñas ───────────────
	// Con la HISTÉRESIS POR DEFECTO (1) y un solo bot aprovisionado —el caso
	// normal del mundo de desarrollo—, el recorte por carga lleva el objetivo a
	// 0 y la reapertura pide subir exactamente 1 bot. Si la banda muerta se
	// aplicara también sobre ese último bot, el arquetipo se quedaría apagado
	// para siempre y el mundo sin contrapartes (GDD §19).
	tinyOpts := opts
	tinyOpts.Hysteresis = bots.DefaultDensityHysteresis
	tinyPop := newFakePopulation(map[string]int{bots.ArchetypeTrader: 1})
	tinyCtrl, err := bots.NewDensityController(pool, tinyOpts, tinyPop, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewDensityController (población pequeña): %v", err)
	}
	if decisions = runDensityCycle(t, ctx, tinyCtrl); decisionFor(t, decisions, bots.ArchetypeTrader).Target != 1 {
		t.Fatalf("población pequeña en calma: objetivo %d, esperado 1", decisionFor(t, decisions, bots.ArchetypeTrader).Target)
	}
	if got := tinyPop.Active(bots.ArchetypeTrader); got != 1 {
		t.Fatalf("población pequeña en calma: activos %d, esperado 1", got)
	}

	setConsumerCursor(t, ctx, pool, densityTestConsumer, densityTestEvent, 0) // lag sintético de nuevo
	decisions = runDensityCycle(t, ctx, tinyCtrl)
	cut := decisionFor(t, decisions, bots.ArchetypeTrader)
	if !cut.LoadGoverned || cut.Target != 0 || tinyPop.Active(bots.ArchetypeTrader) != 0 {
		t.Fatalf("población pequeña saturada: gobernado %v objetivo %d activos %d, esperado true/0/0",
			cut.LoadGoverned, cut.Target, tinyPop.Active(bots.ArchetypeTrader))
	}

	advanceConsumerCursor(t, ctx, pool, densityTestConsumer)
	decisions = runDensityCycle(t, ctx, tinyCtrl)
	reopen := decisionFor(t, decisions, bots.ArchetypeTrader)
	if reopen.Target != 1 || reopen.Delta != 1 {
		t.Fatalf("reapertura de la válvula: objetivo %d delta %d, esperado 1/1", reopen.Target, reopen.Delta)
	}
	if got := tinyPop.Active(bots.ArchetypeTrader); got != 1 {
		t.Fatalf("reapertura de la válvula: activos %d, esperado 1 (la banda muerta dejó el arquetipo apagado)", got)
	}
}

// TestBotsDensityLagIgnoresUnsubscribedEvents fija la REGRESIÓN de la señal de
// saturación: un consumidor REAL, sano y suscrito a un tipo de evento que casi
// nunca se emite (el caso de system_liquidator ← building.seized) NO puede
// observarse como saturación.
//
// Ningún cursor se toca a mano —ese era justo el punto ciego de los tests
// previos—: arranca un outbox.Consumer de verdad, se le deja registrar su
// cursor y se genera tráfico de OTROS tipos, que su filtro descarta y que por
// tanto NO le hacen avanzar el cursor (queda en 0 para siempre). Con el lag
// medido contra la cabecera global, ese 0 valía "tantos eventos como haya
// emitido el mundo en su historia": la válvula del GDD §19 se clavaba en el
// suelo sin retorno. Medido contra los eventos que ese consumidor SÍ consume,
// vale 0 y la densidad se mantiene en su base.
func TestBotsDensityLagIgnoresUnsubscribedEvents(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	const (
		idleConsumer = "density_idle_consumer"
		rareEvent    = "density.never_emitted" // nadie lo emite: el caso patológico
		trafficEvent = "density.traffic"       // el pulso del mundo, ajeno al consumidor
		trafficCount = 500                     // muy por encima de LagHigh
	)

	// Consumidor REAL en su bucle de polling. Su handler no debe invocarse
	// jamás: ningún evento de su tipo se emite en todo el test.
	consumer := outbox.NewConsumer(pool, idleConsumer, []string{rareEvent},
		outbox.WithLogger(logger))
	runCtx, stopConsumer := context.WithCancel(ctx)
	defer stopConsumer()
	done := make(chan error, 1)
	go func() {
		done <- consumer.Run(runCtx, 20*time.Millisecond, func(context.Context, pgx.Tx, outbox.Event) error {
			return errors.New("el consumidor recibió un evento de un tipo que nadie emite")
		})
	}()

	// El primer polling registra el cursor con su suscripción (sin alta manual).
	registered := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if countRows(t, ctx, pool,
			`SELECT count(*) FROM outbox.consumer_cursors WHERE consumer_name = $1 AND event_types = ARRAY[$2]`,
			idleConsumer, rareEvent) == 1 {
			registered = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !registered {
		t.Fatal("el consumidor real no registró su cursor con la suscripción declarada")
	}

	// El mundo late: tráfico abundante de tipos que ESE consumidor no consume.
	insertOutboxEvents(t, ctx, pool, trafficEvent, trafficCount)
	// Margen para varios ciclos de polling del consumidor: su cursor no puede
	// moverse (el filtro descarta todo) y aun así debe observarse al día.
	time.Sleep(200 * time.Millisecond)

	if cursor := cursorOf(t, ctx, pool, idleConsumer); cursor != 0 {
		t.Fatalf("cursor del consumidor sano = %d, esperado 0 (no hay eventos de su tipo)", cursor)
	}
	if head := countRows(t, ctx, pool, `SELECT COALESCE(max(seq), 0) FROM outbox.events`); head < trafficCount {
		t.Fatalf("cabecera del outbox = %d, esperada al menos %d", head, trafficCount)
	}

	opts := bots.DefaultDensityOptions()
	opts.BaseBP = 10_000
	opts.ActivityGainBP = 0
	opts.CoverageMin = 0
	opts.LoadFloorBP = 0
	opts.Hysteresis = 0
	opts.MaxStep = densityProvisioned
	pop := newFakePopulation(map[string]int{bots.ArchetypeTrader: densityProvisioned})
	ctrl, err := bots.NewDensityController(pool, opts, pop, logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewDensityController: %v", err)
	}

	signals, err := ctrl.Observe(ctx)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if signals.OutboxLag != 0 {
		t.Fatalf("lag observado = %d, esperado 0: un consumidor al día en SUS tipos no es saturación "+
			"(la resta a la cabecera global daba %d, la historia entera del mundo)",
			signals.OutboxLag, trafficCount)
	}

	decision := decisionFor(t, runDensityCycle(t, ctx, ctrl), bots.ArchetypeTrader)
	if decision.LoadGoverned {
		t.Fatal("sin retraso real la decisión no puede estar gobernada por la carga")
	}
	if decision.Target != densityProvisioned || pop.Active(bots.ArchetypeTrader) != densityProvisioned {
		t.Fatalf("objetivo %d y activos %d, esperado %d: la densidad debe mantenerse en su base",
			decision.Target, pop.Active(bots.ArchetypeTrader), densityProvisioned)
	}

	stopConsumer()
	if err := <-done; err != nil {
		t.Fatalf("el consumidor terminó con error: %v", err)
	}
}

// runDensityCycle dispara un ciclo del controlador y devuelve sus decisiones.
func runDensityCycle(t *testing.T, ctx context.Context, ctrl *bots.DensityController) []bots.DensityDecision {
	t.Helper()
	decisions, err := ctrl.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	return decisions
}

// decisionFor localiza la decisión de un arquetipo en un ciclo.
func decisionFor(t *testing.T, decisions []bots.DensityDecision, archetype string) bots.DensityDecision {
	t.Helper()
	for _, d := range decisions {
		if d.Archetype == archetype {
			return d
		}
	}
	t.Fatalf("el ciclo no decidió sobre el arquetipo %s", archetype)
	return bots.DensityDecision{}
}

// insertOutboxEvents inyecta n eventos sintéticos del tipo dado en el outbox
// (cabecera de seq).
func insertOutboxEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string, n int) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox.events (aggregate_type, aggregate_id, event_type, payload, sim_time_at)
		SELECT 'density_test', uuidv7(), $3, '{}'::jsonb, $2
		FROM generate_series(1, $1)`, n, itSimBase, eventType); err != nil {
		t.Fatalf("insertando eventos de outbox: %v", err)
	}
}

// setConsumerCursor fija el cursor de un consumidor lógico SUSCRITO a eventType
// (lag sintético: eventos de su tipo por encima de su cursor).
func setConsumerCursor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, eventType string, seq int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox.consumer_cursors (consumer_name, last_seq, event_types)
		VALUES ($1, $2, ARRAY[$3])
		ON CONFLICT (consumer_name) DO UPDATE
		   SET last_seq = EXCLUDED.last_seq, event_types = EXCLUDED.event_types`,
		name, seq, eventType); err != nil {
		t.Fatalf("fijando el cursor del consumidor: %v", err)
	}
}

// advanceConsumerCursor lleva el cursor a la cabecera del outbox (lag 0).
func advanceConsumerCursor(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		UPDATE outbox.consumer_cursors
		   SET last_seq = (SELECT COALESCE(max(seq), 0) FROM outbox.events)
		 WHERE consumer_name = $1`, name); err != nil {
		t.Fatalf("avanzando el cursor del consumidor: %v", err)
	}
}

// openHumanSession abre una sesión viva para una cuenta humana del seed.
func openHumanSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountName string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth.sessions (account_id, token_hash, expires_at)
		SELECT id, 'density-test-session', now() + interval '1 hour'
		  FROM auth.accounts WHERE name = $1 AND kind = 'human'`, accountName); err != nil {
		t.Fatalf("abriendo la sesión humana: %v", err)
	}
}
