package bots

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// densityTestOptions devuelve la configuración por defecto con las señales
// "blandas" (actividad y cobertura) neutralizadas salvo que el caso las
// active: así cada test aísla la señal que quiere probar.
func densityTestOptions() DensityOptions {
	o := DefaultDensityOptions()
	o.CoverageMin = 0 // sin backstop de liquidez por defecto
	return o
}

func TestDensityArchetypeNames(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := NewMetrics(nil)
	cases := map[string]Behavior{
		ArchetypeCoalProducer:          NewCoalProducer(DefaultCoalProducerConfig(), "c", logger, metrics),
		ArchetypeIronProducer:          NewIronProducer(DefaultIronProducerConfig(), "i", logger, metrics),
		ArchetypeTrader:                NewTrader(DefaultTraderConfig(500_000), "t", logger, metrics),
		ArchetypeIndustrialTransformer: NewIndustrialTransformer(DefaultIndustrialTransformerConfig(2_500), "x", logger, metrics),
		ArchetypeFreighter:             NewFreighter(DefaultFreighterConfig(2_000, 500_000), "f", logger, metrics),
	}
	if len(cases) != len(DensityArchetypes) {
		t.Fatalf("DensityArchetypes tiene %d arquetipos, el test cubre %d", len(DensityArchetypes), len(cases))
	}
	for archetype, behavior := range cases {
		if behavior.Name() != archetype {
			t.Errorf("la clave de densidad %q no coincide con Behavior.Name() = %q", archetype, behavior.Name())
		}
	}
	for _, archetype := range DensityArchetypes {
		if _, ok := cases[archetype]; !ok {
			t.Errorf("DensityArchetypes declara %q sin arquetipo correspondiente", archetype)
		}
	}
}

// TestDensityGobiernaTodaLaPoblacion garantiza que ningún arquetipo de la
// población del orquestador queda fuera del control de densidad (si se añade
// uno nuevo, debe entrar en DensityArchetypes).
func TestDensityGobiernaTodaLaPoblacion(t *testing.T) {
	orch := &Orchestrator{
		opts: Options{
			CoalProducers: 1, IronProducers: 1, Traders: 1, Transformers: 1, Freighters: 1,
			TransformerMarginBP: DefaultTransformerMarginBP,
			FreighterMarginBP:   DefaultFreighterMarginBP,
			SecretSeed:          "test-seed",
			Capital:             DefaultCapital,
			Tick:                time.Second,
			Addr:                ":0",
			APIURL:              "http://localhost:0/api/v1",
		},
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		metrics: NewMetrics(nil),
	}
	governed := make(map[string]bool, len(DensityArchetypes))
	for _, archetype := range DensityArchetypes {
		governed[archetype] = true
	}
	seen := map[string]bool{}
	for _, bot := range orch.population() {
		archetype := bot.Behavior.Name()
		seen[archetype] = true
		if !governed[archetype] {
			t.Errorf("el arquetipo %q de la población no lo gobierna la densidad dinámica", archetype)
		}
	}
	for _, archetype := range DensityArchetypes {
		if !seen[archetype] {
			t.Errorf("la densidad declara %q pero el orquestador no lo aprovisiona", archetype)
		}
	}
}

func TestDensityTargetBaseYLimites(t *testing.T) {
	opts := densityTestOptions()

	// Mundo tranquilo y sano: el objetivo es la BASE (60% de lo aprovisionado).
	d := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{})
	if d.Base != 6 || d.Target != 6 {
		t.Fatalf("base/objetivo en calma = %d/%d, esperado 6/6", d.Base, d.Target)
	}
	if d.LoadGoverned {
		t.Fatal("sin carga observada la decisión no debe estar gobernada por la carga")
	}

	// Techo por arquetipo.
	capped := densityTestOptions()
	capped.Max[ArchetypeFreighter] = 4
	if got := capped.TargetFor(ArchetypeFreighter, 10, DensitySignals{}).Target; got != 4 {
		t.Fatalf("objetivo con techo 4 = %d, esperado 4", got)
	}

	// Suelo por arquetipo.
	floored := densityTestOptions()
	floored.Min[ArchetypeFreighter] = 8
	if got := floored.TargetFor(ArchetypeFreighter, 10, DensitySignals{}).Target; got != 8 {
		t.Fatalf("objetivo con suelo 8 = %d, esperado 8", got)
	}

	// El techo NUNCA supera lo aprovisionado: no se arrancan bots inexistentes.
	over := densityTestOptions()
	over.Max[ArchetypeFreighter] = 100
	over.Min[ArchetypeFreighter] = 50
	if got := over.TargetFor(ArchetypeFreighter, 3, DensitySignals{}).Target; got != 3 {
		t.Fatalf("objetivo con techo/suelo por encima de lo aprovisionado = %d, esperado 3", got)
	}

	// Sin población aprovisionada no hay objetivo.
	if got := opts.TargetFor(ArchetypeFreighter, 0, DensitySignals{}).Target; got != 0 {
		t.Fatalf("objetivo sin población = %d, esperado 0", got)
	}

	// Un único bot aprovisionado mantiene el mundo vivo (base >= 1).
	if got := opts.TargetFor(ArchetypeFreighter, 1, DensitySignals{}).Target; got != 1 {
		t.Fatalf("objetivo con 1 aprovisionado = %d, esperado 1", got)
	}
}

func TestDensityActividadHumanaSubeLaDensidad(t *testing.T) {
	opts := densityTestOptions()

	// Actividad plena por sesiones: base × 2, acotado por lo aprovisionado.
	full := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{HumanSessions: opts.SessionsRef})
	if full.ActivityBP != 20_000 || full.Target != 10 {
		t.Fatalf("actividad plena: factor %d objetivo %d, esperado 20000/10", full.ActivityBP, full.Target)
	}

	// Media actividad: factor 1,5 → 6 × 1,5 = 9.
	half := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{HumanSessions: opts.SessionsRef / 2})
	if half.ActivityBP != 15_000 || half.Target != 9 {
		t.Fatalf("media actividad: factor %d objetivo %d, esperado 15000/9", half.ActivityBP, half.Target)
	}

	// Los comandos humanos recientes sirven igual que las sesiones (manda el mayor).
	byCommands := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{HumanCommands: opts.CommandsRef})
	if byCommands.ActivityBP != full.ActivityBP {
		t.Fatalf("comandos en referencia: factor %d, esperado %d", byCommands.ActivityBP, full.ActivityBP)
	}

	// Por encima de la referencia el factor satura (no crece sin límite).
	sat := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{HumanSessions: opts.SessionsRef * 100})
	if sat.ActivityBP != full.ActivityBP {
		t.Fatalf("actividad saturada: factor %d, esperado %d", sat.ActivityBP, full.ActivityBP)
	}
}

func TestDensityCargaMandaSobreLaActividad(t *testing.T) {
	opts := densityTestOptions()

	// Muchos humanos Y sistema saturado: GDD §19 — se reduce la población de
	// bots ANTES que degradar la experiencia humana.
	saturated := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{
		HumanSessions: opts.SessionsRef * 10,
		OutboxLag:     opts.LagHigh + 1,
	})
	if !saturated.LoadGoverned {
		t.Fatal("con el sistema saturado la decisión debe estar gobernada por la carga")
	}
	if saturated.LoadBP != opts.LoadFloorBP {
		t.Fatalf("factor de carga saturado = %d, esperado el suelo %d", saturated.LoadBP, opts.LoadFloorBP)
	}
	if saturated.Target >= saturated.Base {
		t.Fatalf("con carga alta el objetivo (%d) debe bajar de la base (%d)", saturated.Target, saturated.Base)
	}
	if saturated.Target != 1 { // 6 × 0,2 = 1,2 → 1
		t.Fatalf("objetivo saturado = %d, esperado 1", saturated.Target)
	}
	healthy := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{HumanSessions: opts.SessionsRef * 10})
	if saturated.Target >= healthy.Target {
		t.Fatalf("el objetivo saturado (%d) debe ser menor que el sano (%d)", saturated.Target, healthy.Target)
	}

	// La cola de transbordo satura igual que el lag: manda la PEOR señal.
	byQueue := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{
		HumanSessions:  opts.SessionsRef * 10,
		TransshipQueue: opts.QueueHigh + 1,
	})
	if byQueue.Target != saturated.Target || !byQueue.LoadGoverned {
		t.Fatalf("cola saturada: objetivo %d (gobernado %v), esperado %d gobernado", byQueue.Target, byQueue.LoadGoverned, saturated.Target)
	}

	// La cobertura tampoco puede contrarrestar la carga.
	backstop := densityTestOptions()
	backstop.CoverageMin = 12
	covered := backstop.TargetFor(ArchetypeCoalProducer, 10, DensitySignals{
		HumanSessions:    backstop.SessionsRef * 10,
		LivePublications: 0,
		OutboxLag:        backstop.LagHigh + 1,
	})
	if covered.Target != 1 {
		t.Fatalf("objetivo saturado con tablón vacío = %d, esperado 1 (la carga manda)", covered.Target)
	}
}

func TestDensityRampaDeCarga(t *testing.T) {
	opts := densityTestOptions()

	// Justo en el umbral sano: sin recorte.
	if d := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{OutboxLag: opts.LagLow}); d.LoadBP != 10_000 || d.LoadGoverned {
		t.Fatalf("lag en el umbral bajo: factor %d gobernado %v, esperado 10000/false", d.LoadBP, d.LoadGoverned)
	}
	// Punto medio de la rampa (200..2000): factor 6000 → 6 × 0,6 = 3,6 → 4.
	mid := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{OutboxLag: 1_100})
	if mid.LoadBP != 6_000 || mid.Target != 4 {
		t.Fatalf("punto medio de la rampa: factor %d objetivo %d, esperado 6000/4", mid.LoadBP, mid.Target)
	}
	// La rampa es monótona no creciente.
	prev := int64(10_001)
	for lag := int64(0); lag <= opts.LagHigh+500; lag += 100 {
		got := opts.TargetFor(ArchetypeFreighter, 10, DensitySignals{OutboxLag: lag}).LoadBP
		if got > prev {
			t.Fatalf("la rampa de carga creció con lag %d: %d > %d", lag, got, prev)
		}
		prev = got
	}
}

func TestDensityCoberturaSoloEnBackstop(t *testing.T) {
	opts := densityTestOptions()
	opts.CoverageMin = 12

	empty := DensitySignals{LivePublications: 0}
	for _, archetype := range []string{ArchetypeCoalProducer, ArchetypeIronProducer, ArchetypeTrader} {
		d := opts.TargetFor(archetype, 10, empty)
		if d.CoverageBP != 15_000 || d.Target != 9 { // 6 × 1,5 = 9
			t.Errorf("%s con el tablón vacío: cobertura %d objetivo %d, esperado 15000/9", archetype, d.CoverageBP, d.Target)
		}
	}
	for _, archetype := range []string{ArchetypeIndustrialTransformer, ArchetypeFreighter} {
		d := opts.TargetFor(archetype, 10, empty)
		if d.CoverageBP != 10_000 || d.Target != 6 {
			t.Errorf("%s no es backstop de liquidez: cobertura %d objetivo %d, esperado 10000/6", archetype, d.CoverageBP, d.Target)
		}
	}
	// Tablón surtido: sin bono.
	if d := opts.TargetFor(ArchetypeTrader, 10, DensitySignals{LivePublications: opts.CoverageMin}); d.CoverageBP != 10_000 || d.Target != 6 {
		t.Fatalf("tablón surtido: cobertura %d objetivo %d, esperado 10000/6", d.CoverageBP, d.Target)
	}
	// Déficit parcial: bono proporcional (6/12 → +25% → 6 × 1,25 = 7,5 → 8).
	if d := opts.TargetFor(ArchetypeTrader, 10, DensitySignals{LivePublications: 6}); d.CoverageBP != 12_500 || d.Target != 8 {
		t.Fatalf("déficit parcial: cobertura %d objetivo %d, esperado 12500/8", d.CoverageBP, d.Target)
	}
	// CoverageMin = 0 desactiva la señal.
	off := densityTestOptions()
	if d := off.TargetFor(ArchetypeTrader, 10, empty); d.CoverageBP != 10_000 {
		t.Fatalf("cobertura desactivada: factor %d, esperado 10000", d.CoverageBP)
	}
}

func TestDensityStepSuavizadoEHisteresis(t *testing.T) {
	opts := densityTestOptions() // MaxStep 2, Hysteresis 1
	cases := []struct {
		name           string
		active, target int
		want           int
	}{
		{"objetivo alcanzado: no mueve", 5, 5, 0},
		{"desviación al alza dentro de la banda muerta: no mueve", 4, 5, 0},
		{"desviación al alza sobre la banda: sube acotado por el paso", 3, 5, 2},
		{"salto grande al alza: acotado por el paso", 0, 10, 2},
		// La válvula del GDD §19 debe poder REABRIRSE: con el arquetipo apagado
		// la banda muerta no filtra ruido, deja el mundo sin contrapartes.
		{"reapertura desde cero: la banda muerta no retiene el último bot", 0, 1, 1},
		{"bajada de un bot: inmediata, sin banda muerta", 5, 4, -1},
		{"bajada grande: acotada por el paso", 9, 0, -2},
		{"bajada a cero desde uno: inmediata", 1, 0, -1},
	}
	for _, tc := range cases {
		if got := opts.Step(tc.active, tc.target); got != tc.want {
			t.Errorf("%s: Step(%d, %d) = %d, esperado %d", tc.name, tc.active, tc.target, got, tc.want)
		}
	}

	// El suavizado converge sin oscilar: pasos sucesivos se acercan al objetivo
	// hasta quedar dentro de la banda muerta y ahí se detienen (no rebasan ni
	// van y vienen).
	active, target := 0, 5
	for range 10 {
		active += opts.Step(active, target)
	}
	if active > target || target-active > opts.Hysteresis {
		t.Fatalf("la convergencia al objetivo %d terminó en %d (fuera de la banda muerta %d)", target, active, opts.Hysteresis)
	}
	if got := opts.Step(active, target); got != 0 {
		t.Fatalf("dentro de la banda muerta el ajuste debe ser 0, fue %d", got)
	}

	// La válvula reabre SIEMPRE desde cero, por ancha que sea la banda muerta:
	// tras un recorte por carga (objetivo 0) el arquetipo tiene que volver a
	// arrancar en cuanto la carga cede, aunque el objetivo sea un solo bot
	// (caso normal en poblaciones pequeñas: baseFor acota la base a >= 1).
	wide := densityTestOptions()
	wide.Hysteresis = 5
	if got := wide.Step(0, 1); got != 1 {
		t.Fatalf("con banda muerta %d, Step(0, 1) = %d, esperado 1 (la válvula debe reabrir)", wide.Hysteresis, got)
	}
	if got := wide.Step(1, 5); got != 0 {
		t.Fatalf("con banda muerta %d y el arquetipo vivo, Step(1, 5) = %d, esperado 0", wide.Hysteresis, got)
	}

	// Sin banda muerta el objetivo se alcanza exacto.
	exact := densityTestOptions()
	exact.Hysteresis = 0
	active = 0
	for range 10 {
		active += exact.Step(active, target)
	}
	if active != target {
		t.Fatalf("sin banda muerta la convergencia terminó en %d, esperado %d", active, target)
	}
}

func TestDensityOptionsValidate(t *testing.T) {
	if err := DefaultDensityOptions().Validate(); err != nil {
		t.Fatalf("la configuración por defecto debe ser válida: %v", err)
	}
	cases := map[string]func(*DensityOptions){
		"intervalo no positivo":      func(o *DensityOptions) { o.Interval = 0 },
		"ventana no positiva":        func(o *DensityOptions) { o.ActivityWindow = 0 },
		"base no positiva":           func(o *DensityOptions) { o.BaseBP = 0 },
		"rampa de lag invertida":     func(o *DensityOptions) { o.LagHigh = o.LagLow - 1 },
		"rampa de cola invertida":    func(o *DensityOptions) { o.QueueHigh = o.QueueLow - 1 },
		"suelo de carga fuera de bp": func(o *DensityOptions) { o.LoadFloorBP = 10_001 },
		"paso menor que 1":           func(o *DensityOptions) { o.MaxStep = 0 },
		"histéresis negativa":        func(o *DensityOptions) { o.Hysteresis = -1 },
		"suelo mayor que techo":      func(o *DensityOptions) { o.Min[ArchetypeTrader] = 5; o.Max[ArchetypeTrader] = 2 },
	}
	for name, mutate := range cases {
		opts := DefaultDensityOptions()
		mutate(&opts)
		if err := opts.Validate(); err == nil {
			t.Errorf("%s: se esperaba error de validación", name)
		}
	}
}

func TestArchetypeCountsFromEnv(t *testing.T) {
	const key = "II_BOTS_DENSITY_TEST_SPEC"

	t.Run("vacío usa el default", func(t *testing.T) {
		got, err := archetypeCountsFromEnv(key, 3)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		for _, a := range DensityArchetypes {
			if got[a] != 3 {
				t.Fatalf("%s = %d, esperado 3", a, got[a])
			}
		}
	})

	t.Run("entero suelto se aplica a todos", func(t *testing.T) {
		t.Setenv(key, " 2 ")
		got, err := archetypeCountsFromEnv(key, 0)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		for _, a := range DensityArchetypes {
			if got[a] != 2 {
				t.Fatalf("%s = %d, esperado 2", a, got[a])
			}
		}
	})

	t.Run("lista por arquetipo", func(t *testing.T) {
		t.Setenv(key, "coal_producer=1, trader=4")
		got, err := archetypeCountsFromEnv(key, 0)
		if err != nil {
			t.Fatalf("error inesperado: %v", err)
		}
		if got[ArchetypeCoalProducer] != 1 || got[ArchetypeTrader] != 4 {
			t.Fatalf("lista mal interpretada: %v", got)
		}
		if got[ArchetypeFreighter] != 0 {
			t.Fatalf("el arquetipo no listado debe conservar el default, fue %d", got[ArchetypeFreighter])
		}
	})

	t.Run("arquetipo desconocido", func(t *testing.T) {
		t.Setenv(key, "minero=1")
		if _, err := archetypeCountsFromEnv(key, 0); err == nil {
			t.Fatal("se esperaba error con un arquetipo desconocido")
		}
	})

	t.Run("valor no entero", func(t *testing.T) {
		t.Setenv(key, "trader=muchos")
		if _, err := archetypeCountsFromEnv(key, 0); err == nil {
			t.Fatal("se esperaba error con un valor no entero")
		}
	})
}

// TestSupervisorArranqueYParadaEnCaliente ejercita el sustrato de la densidad:
// las plazas se paran y arrancan sin reiniciar el proceso, con cierre limpio y
// conservando el State local del bot.
func TestSupervisorArranqueYParadaEnCaliente(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := NewMetrics(nil)
	population := make([]ProvisionedBot, 0, 3)
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("Bot Transportista %02d", i)
		population = append(population, ProvisionedBot{
			Name:     name,
			Behavior: NewFreighter(DefaultFreighterConfig(2_000, 500_000), name, logger, metrics),
		})
	}

	var running, runs atomic.Int64
	sup := newSupervisor(ctx, population, func(ctx context.Context, _ ProvisionedBot, st *State) bool {
		if st == nil {
			t.Error("el supervisor debe entregar el State local de la plaza")
		}
		runs.Add(1)
		running.Add(1)
		defer running.Add(-1)
		<-ctx.Done()
		return false
	}, logger, metrics)

	if got := sup.provisioned()[ArchetypeFreighter]; got != 3 {
		t.Fatalf("aprovisionados = %d, esperado 3", got)
	}
	if got := sup.active(ArchetypeFreighter); got != 0 {
		t.Fatalf("las plazas nacen paradas, activas = %d", got)
	}

	if got := sup.start(ArchetypeFreighter, 2); got != 2 {
		t.Fatalf("arrancadas = %d, esperado 2", got)
	}
	waitFor(t, func() bool { return running.Load() == 2 }, "dos plazas en ejecución")
	if got := sup.active(ArchetypeFreighter); got != 2 {
		t.Fatalf("activas tras arrancar = %d, esperado 2", got)
	}

	// Parada en caliente: stop espera el cierre limpio, así que al volver la
	// goroutine ya terminó.
	if got := sup.stop(ArchetypeFreighter, 1); got != 1 {
		t.Fatalf("paradas = %d, esperado 1", got)
	}
	if got := running.Load(); got != 1 {
		t.Fatalf("stop debe esperar el cierre limpio: en ejecución %d, esperado 1", got)
	}
	if got := sup.active(ArchetypeFreighter); got != 1 {
		t.Fatalf("activas tras parar = %d, esperado 1", got)
	}

	// Reanudación: la plaza vuelve a arrancar sin re-aprovisionar.
	if got := sup.start(ArchetypeFreighter, 2); got != 2 {
		t.Fatalf("rearrancadas = %d, esperado 2", got)
	}
	waitFor(t, func() bool { return running.Load() == 3 }, "tres plazas en ejecución")
	if got := sup.start(ArchetypeFreighter, 5); got != 0 {
		t.Fatalf("sin plazas libres no debe arrancar nada, arrancó %d", got)
	}
	if runs.Load() < 4 {
		t.Fatalf("la plaza reanudada debe re-ejecutar su bucle: ejecuciones %d", runs.Load())
	}

	// Apagado: la cancelación del contexto raíz cierra toda la población.
	cancel()
	sup.close()
	sup.wait()
	if got := running.Load(); got != 0 {
		t.Fatalf("tras el apagado quedan %d plazas vivas", got)
	}
	if got := sup.start(ArchetypeFreighter, 1); got != 0 {
		t.Fatalf("tras el apagado no debe arrancarse ninguna plaza, arrancó %d", got)
	}
}

// TestSupervisorPlazaRetirada comprueba que una cuenta retirada libera su plaza
// del censo y no vuelve a arrancar (el retiro es del RetirementJob, no de la
// densidad).
func TestSupervisorPlazaRetirada(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	metrics := NewMetrics(nil)
	name := "Bot Mercader 01"
	population := []ProvisionedBot{{
		Name:     name,
		Behavior: NewTrader(DefaultTraderConfig(500_000), name, logger, metrics),
	}}

	sup := newSupervisor(ctx, population, func(context.Context, ProvisionedBot, *State) bool {
		return true // cuenta retirada
	}, logger, metrics)

	if got := sup.start(ArchetypeTrader, 1); got != 1 {
		t.Fatalf("arrancadas = %d, esperado 1", got)
	}
	waitFor(t, func() bool { return sup.provisioned()[ArchetypeTrader] == 0 }, "la plaza retirada sale del censo")
	if got := sup.active(ArchetypeTrader); got != 0 {
		t.Fatalf("activas tras el retiro = %d, esperado 0", got)
	}
	if got := sup.start(ArchetypeTrader, 1); got != 0 {
		t.Fatalf("una plaza retirada no debe volver a arrancar, arrancó %d", got)
	}
	sup.close()
	sup.wait()
}

// waitFor espera a que cond se cumpla dentro de un plazo corto (sincronización
// de goroutines en los tests del supervisor).
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("tiempo agotado esperando: %s", what)
}
