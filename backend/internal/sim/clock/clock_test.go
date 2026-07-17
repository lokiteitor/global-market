package clock

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// gaugeValue recoge el valor actual de un gauge (evaluado en el momento).
func gaugeValue(t *testing.T, g prometheus.Metric) float64 {
	t.Helper()
	var pb dto.Metric
	if err := g.Write(&pb); err != nil {
		t.Fatalf("leyendo el gauge: %v", err)
	}
	return pb.GetGauge().GetValue()
}

// fakeStore es un AnchorStore en memoria que replica la semántica del real:
// PersistAnchor no toca el ancla si está congelada.
type fakeStore struct {
	mu          sync.Mutex
	anchor      Anchor
	ensureCalls int
	loadCalls   int
	persisted   []simtime.SimTime
	ensureErr   error
	loadErr     error
	persistErr  error
}

func (f *fakeStore) EnsureExists(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureCalls++
	return f.ensureErr
}

func (f *fakeStore) Load(context.Context) (Anchor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls++
	if f.loadErr != nil {
		return Anchor{}, f.loadErr
	}
	return f.anchor, nil
}

func (f *fakeStore) PersistAnchor(_ context.Context, derived simtime.SimTime) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.persistErr != nil {
		return f.persistErr
	}
	if f.anchor.Frozen {
		return nil // WHERE id = 1 AND NOT frozen: 0 filas, sin error
	}
	f.anchor.SimTimeAt = derived
	f.anchor.WallAnchor = time.Now()
	f.persisted = append(f.persisted, derived)
	return nil
}

func (f *fakeStore) setFrozen(frozen bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.anchor.Frozen = frozen
}

func (f *fakeStore) setLoadErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadErr = err
}

func (f *fakeStore) loads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadCalls
}

func (f *fakeStore) persists() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.persisted)
}

func (f *fakeStore) snapshot() Anchor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.anchor
}

// testLogger devuelve un logger JSON sobre un buffer para inspeccionar avisos.
func testLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

// eventually sondea cond hasta que se cumpla o expire el plazo.
func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal(msg)
}

// slowOpts mantiene los tickers fuera de juego en tests que no los ejercitan.
var slowOpts = Options{PersistInterval: time.Hour, RefreshInterval: time.Hour}

func startedClock(t *testing.T, fs *fakeStore, opts Options) *Clock {
	t.Helper()
	logger, _ := testLogger()
	c := New(fs, opts, logger, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		c.Stop()
	})
	return c
}

func TestOptionsFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvPersistInterval, "")
	t.Setenv(EnvRefreshInterval, "")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.PersistInterval != DefaultPersistInterval || opts.RefreshInterval != DefaultRefreshInterval {
		t.Fatalf("defaults incorrectos: %+v", opts)
	}
}

func TestOptionsFromEnvOverrides(t *testing.T) {
	t.Setenv(EnvPersistInterval, "2m")
	t.Setenv(EnvRefreshInterval, "500ms")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.PersistInterval != 2*time.Minute || opts.RefreshInterval != 500*time.Millisecond {
		t.Fatalf("overrides incorrectos: %+v", opts)
	}
}

func TestOptionsFromEnvInvalid(t *testing.T) {
	cases := []struct{ persist, refresh string }{
		{"abc", ""},
		{"", "abc"},
		{"0s", ""},
		{"-5s", ""},
		{"", "-1s"},
	}
	for _, tc := range cases {
		t.Setenv(EnvPersistInterval, tc.persist)
		t.Setenv(EnvRefreshInterval, tc.refresh)
		if _, err := OptionsFromEnv(); err == nil {
			t.Errorf("OptionsFromEnv(persist=%q, refresh=%q) sin error", tc.persist, tc.refresh)
		}
	}
}

func TestReaderOptionsFromEnv(t *testing.T) {
	t.Setenv(EnvReaderCacheTTL, "")
	opts, err := ReaderOptionsFromEnv()
	if err != nil || opts.CacheTTL != DefaultReaderCacheTTL {
		t.Fatalf("default: %+v, %v", opts, err)
	}
	t.Setenv(EnvReaderCacheTTL, "0s")
	if opts, err = ReaderOptionsFromEnv(); err != nil || opts.CacheTTL != 0 {
		t.Fatalf("TTL cero debería ser válido: %+v, %v", opts, err)
	}
	t.Setenv(EnvReaderCacheTTL, "-1s")
	if _, err = ReaderOptionsFromEnv(); err == nil {
		t.Fatal("TTL negativo sin error")
	}
}

func TestClockNowDerivesFromCachedAnchor(t *testing.T) {
	anchorWall := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 1000, WallAnchor: anchorWall, Ratio: 24}}
	c := startedClock(t, fs, slowOpts)
	c.nowFn = func() time.Time { return anchorWall.Add(90 * time.Second) }

	if got, want := c.Now(), simtime.SimTime(1000+90*24); got != want {
		t.Fatalf("Now() = %d, esperado %d", got, want)
	}
	if fs.ensureCalls != 1 {
		t.Fatalf("EnsureExists llamado %d veces, esperado 1", fs.ensureCalls)
	}
	if got, want := gaugeValue(t, c.simGauge), float64(1000+90*24); got != want {
		t.Fatalf("ii_sim_time_seconds = %v, esperado %v", got, want)
	}
	if got := gaugeValue(t, c.frozenGauge); got != 0 {
		t.Fatalf("ii_sim_clock_frozen = %v, esperado 0", got)
	}
}

func TestClockNowFrozenReturnsBase(t *testing.T) {
	anchorWall := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 5000, WallAnchor: anchorWall, Ratio: 24, Frozen: true}}
	c := startedClock(t, fs, slowOpts)
	c.nowFn = func() time.Time { return anchorWall.Add(time.Hour) }

	if got := c.Now(); got != 5000 {
		t.Fatalf("Now() congelado = %d, esperado 5000", got)
	}
	if !c.Frozen() {
		t.Fatal("Frozen() = false con ancla congelada")
	}
	if got := gaugeValue(t, c.frozenGauge); got != 1 {
		t.Fatalf("ii_sim_clock_frozen = %v, esperado 1", got)
	}
}

func TestClockNowNeverNegative(t *testing.T) {
	// Un sesgo de reloj (wall_anchor de la BD por delante del reloj local)
	// no puede producir sim-time negativo: el dominio sim_time lo prohíbe.
	anchorWall := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 10, WallAnchor: anchorWall, Ratio: 24}}
	c := startedClock(t, fs, slowOpts)
	c.nowFn = func() time.Time { return anchorWall.Add(-time.Minute) }

	if got := c.Now(); got != 0 {
		t.Fatalf("Now() con sesgo negativo = %d, esperado 0", got)
	}
}

func TestClockNowBeforeStartIsGenesis(t *testing.T) {
	logger, _ := testLogger()
	c := New(&fakeStore{}, slowOpts, logger, nil)
	if got := c.Now(); got != 0 {
		t.Fatalf("Now() antes de Start = %d, esperado 0", got)
	}
	c.Stop() // segura sin Start
}

func TestClockStartFailsWhenEnsureFails(t *testing.T) {
	logger, _ := testLogger()
	fs := &fakeStore{ensureErr: errors.New("bd caída")}
	c := New(fs, slowOpts, logger, nil)
	if err := c.Start(context.Background()); err == nil {
		t.Fatal("Start sin error con EnsureExists roto")
	}
	c.Stop() // no debe bloquear tras un Start fallido
}

func TestClockRegistersGauges(t *testing.T) {
	reg := prometheus.NewRegistry()
	logger, _ := testLogger()
	New(&fakeStore{}, slowOpts, logger, reg)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := map[string]bool{}
	for _, mf := range families {
		found[mf.GetName()] = true
	}
	for _, name := range []string{"ii_sim_time_seconds", "ii_sim_clock_frozen"} {
		if !found[name] {
			t.Errorf("gauge %s no registrado", name)
		}
	}
}

func TestClockPersistLoopAdvancesAnchor(t *testing.T) {
	// Ratio enorme para que milisegundos reales sean sim-time observable.
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 0, WallAnchor: time.Now(), Ratio: 1_000_000}}
	c := startedClock(t, fs, Options{PersistInterval: 20 * time.Millisecond, RefreshInterval: 10 * time.Millisecond})

	eventually(t, 2*time.Second, func() bool { return fs.persists() >= 2 },
		"el bucle no persistió el ancla")
	if got := fs.snapshot().SimTimeAt; got <= 0 {
		t.Fatalf("sim_time_at persistido = %d, esperado > 0", got)
	}
	if c.Now() <= 0 {
		t.Fatal("Now() no avanza")
	}
}

func TestClockRefreshPicksUpFrozenFromStore(t *testing.T) {
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 100, WallAnchor: time.Now(), Ratio: 24}}
	c := startedClock(t, fs, Options{PersistInterval: time.Hour, RefreshInterval: 5 * time.Millisecond})

	if c.Frozen() {
		t.Fatal("Frozen() = true al arrancar")
	}
	fs.setFrozen(true) // otro proceso congela el mundo
	eventually(t, 2*time.Second, c.Frozen, "el refresco no detectó frozen=true")

	base := fs.snapshot().SimTimeAt
	if got := c.Now(); got != base {
		t.Fatalf("Now() congelado = %d, esperado la base %d", got, base)
	}
}

func TestClockShutdownPersistsFinalAnchor(t *testing.T) {
	logger, _ := testLogger()
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 0, WallAnchor: time.Now(), Ratio: 1_000_000}}
	c := New(fs, slowOpts, logger, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	cancel()
	c.Stop()
	if fs.persists() != 1 {
		t.Fatalf("persistencias en el apagado = %d, esperado 1", fs.persists())
	}
	if got := fs.snapshot().SimTimeAt; got <= 0 {
		t.Fatalf("ancla final = %d, esperado > 0", got)
	}
}

func TestReaderCachesWithinTTL(t *testing.T) {
	t0 := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 100, WallAnchor: t0, Ratio: 24}}
	logger, _ := testLogger()
	r := NewReader(fs, ReaderOptions{CacheTTL: 10 * time.Second}, logger)
	cur := t0
	r.nowFn = func() time.Time { return cur }
	ctx := context.Background()

	if got := r.Now(ctx); got != 100 {
		t.Fatalf("Now() inicial = %d, esperado 100", got)
	}
	cur = t0.Add(2 * time.Second)
	if got, want := r.Now(ctx), simtime.SimTime(100+2*24); got != want {
		t.Fatalf("Now() dentro del TTL = %d, esperado %d", got, want)
	}
	if fs.loads() != 1 {
		t.Fatalf("Load llamado %d veces dentro del TTL, esperado 1", fs.loads())
	}

	cur = t0.Add(20 * time.Second) // TTL expirado: recarga
	if got, want := r.Now(ctx), simtime.SimTime(100+20*24); got != want {
		t.Fatalf("Now() tras el TTL = %d, esperado %d", got, want)
	}
	if fs.loads() != 2 {
		t.Fatalf("Load llamado %d veces tras el TTL, esperado 2", fs.loads())
	}
}

func TestReaderZeroTTLReloadsEveryCall(t *testing.T) {
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 0, WallAnchor: time.Now(), Ratio: 24}}
	logger, _ := testLogger()
	r := NewReader(fs, ReaderOptions{CacheTTL: 0}, logger)
	ctx := context.Background()
	for range 3 {
		r.Now(ctx)
	}
	if fs.loads() != 3 {
		t.Fatalf("Load llamado %d veces con TTL 0, esperado 3", fs.loads())
	}
}

func TestReaderKeepsDerivingWhenReloadFails(t *testing.T) {
	t0 := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 100, WallAnchor: t0, Ratio: 24}}
	logger, buf := testLogger()
	r := NewReader(fs, ReaderOptions{CacheTTL: 5 * time.Second}, logger)
	cur := t0
	r.nowFn = func() time.Time { return cur }
	ctx := context.Background()

	if got := r.Now(ctx); got != 100 {
		t.Fatalf("Now() inicial = %d, esperado 100", got)
	}
	fs.setLoadErr(errors.New("bd caída"))

	cur = t0.Add(10 * time.Second) // TTL expirado, la recarga falla
	if got, want := r.Now(ctx), simtime.SimTime(100+10*24); got != want {
		t.Fatalf("Now() con recarga fallida = %d, esperado %d (derivado del ancla vieja)", got, want)
	}
	if !strings.Contains(buf.String(), "no se pudo recargar el ancla") {
		t.Fatal("el fallo de recarga no quedó logueado como warning")
	}

	// El fallo consume un TTL: la siguiente llamada inmediata no recarga.
	loads := fs.loads()
	cur = cur.Add(time.Second)
	r.Now(ctx)
	if fs.loads() != loads {
		t.Fatalf("Load reintentado dentro del TTL tras un fallo: %d -> %d", loads, fs.loads())
	}
}

func TestReaderNeverLoadedReturnsGenesis(t *testing.T) {
	fs := &fakeStore{loadErr: errors.New("bd caída")}
	logger, buf := testLogger()
	r := NewReader(fs, ReaderOptions{CacheTTL: time.Second}, logger)
	if got := r.Now(context.Background()); got != 0 {
		t.Fatalf("Now() sin ancla = %d, esperado 0", got)
	}
	if !strings.Contains(buf.String(), "no se pudo recargar el ancla") {
		t.Fatal("el fallo de carga inicial no quedó logueado")
	}
}

func TestReaderFrozenAnchorDoesNotAdvance(t *testing.T) {
	t0 := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{anchor: Anchor{SimTimeAt: 500, WallAnchor: t0, Ratio: 24, Frozen: true}}
	logger, _ := testLogger()
	r := NewReader(fs, ReaderOptions{CacheTTL: time.Hour}, logger)
	cur := t0
	r.nowFn = func() time.Time { return cur }
	ctx := context.Background()

	if got := r.Now(ctx); got != 500 {
		t.Fatalf("Now() congelado = %d, esperado 500", got)
	}
	cur = t0.Add(time.Hour)
	if got := r.Now(ctx); got != 500 {
		t.Fatalf("Now() congelado tras 1h = %d, esperado 500", got)
	}
}
