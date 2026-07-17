package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestLimiter construye un limiter con reloj falso.
func newTestLimiter(ratePerSec float64, burst int) (*Limiter, *fakeClock) {
	l := NewLimiter(ratePerSec, burst)
	clock := newFakeClock()
	l.now = clock.now
	l.lastSweep = clock.now()
	return l, clock
}

func TestLimiterBurstThenDeny(t *testing.T) {
	l, _ := newTestLimiter(1, 3)
	for i := range 3 {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatalf("petición %d de la ráfaga rechazada", i+1)
		}
	}
	ok, retryAfter := l.Allow("k")
	if ok {
		t.Fatal("cuarta petición admitida con burst=3")
	}
	if retryAfter <= 0 || retryAfter > time.Second {
		t.Errorf("retryAfter = %v, esperado en (0, 1s]", retryAfter)
	}
}

func TestLimiterRefill(t *testing.T) {
	l, clock := newTestLimiter(1, 2)
	l.Allow("k")
	l.Allow("k")
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("bucket agotado admitió una petición")
	}
	// Un segundo después hay exactamente un token.
	clock.advance(time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("token recargado no admitido")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("token extra admitido tras recargar solo uno")
	}
	// Una recarga larga no supera el burst.
	clock.advance(time.Hour)
	allowed := 0
	for range 5 {
		if ok, _ := l.Allow("k"); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("tras recarga larga se admitieron %d, esperado burst=2", allowed)
	}
}

func TestLimiterRetryAfterMatchesRate(t *testing.T) {
	l, _ := newTestLimiter(0.5, 1) // 1 token cada 2s
	l.Allow("k")
	ok, retryAfter := l.Allow("k")
	if ok {
		t.Fatal("petición admitida sin tokens")
	}
	if retryAfter <= 0 || retryAfter > 2*time.Second {
		t.Errorf("retryAfter = %v, esperado en (0, 2s]", retryAfter)
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(1, 1)
	if ok, _ := l.Allow("a"); !ok {
		t.Fatal("primera petición de 'a' rechazada")
	}
	if ok, _ := l.Allow("a"); ok {
		t.Fatal("'a' agotado admitió otra petición")
	}
	if ok, _ := l.Allow("b"); !ok {
		t.Fatal("el agotamiento de 'a' afectó a 'b'")
	}
}

func TestLimiterSweepRemovesIdleKeys(t *testing.T) {
	l, clock := newTestLimiter(1, 2)
	for _, k := range []string{"a", "b", "c"} {
		l.Allow(k)
	}
	if got := l.size(); got != 3 {
		t.Fatalf("size = %d, esperado 3", got)
	}
	// Tras el tiempo de rellenado completo (2s) + sweepInterval, el próximo
	// Allow barre las claves inactivas.
	clock.advance(2*time.Second + sweepInterval)
	l.Allow("d")
	if got := l.size(); got != 1 {
		t.Fatalf("size tras el barrido = %d, esperado 1 (solo 'd')", got)
	}
}

func TestLimiterSweepKeepsActiveKeys(t *testing.T) {
	l, clock := newTestLimiter(1, 100)
	l.Allow("vieja")
	clock.advance(sweepInterval + time.Second)
	l.Allow("activa") // recién usada: no puede barrerse
	l.Allow("otra")
	if got := l.size(); got < 2 {
		t.Fatalf("el barrido eliminó claves activas: size = %d", got)
	}
}

func TestLimiterConcurrency(t *testing.T) {
	// Reloj real y recarga despreciable: el total admitido debe ser
	// exactamente el burst, sin carreras (go test -race).
	l := NewLimiter(0.0001, 50)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				if ok, _ := l.Allow("compartida"); ok {
					allowed.Add(1)
				}
				// Claves propias en paralelo para ejercitar el mapa.
				l.Allow("otra-clave")
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 50 {
		t.Fatalf("admitidas %d peticiones de la clave compartida, esperado 50", got)
	}
}

func TestOptionsFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvRateLoginPerMin, "")
	t.Setenv(EnvRateAPIRPS, "")
	t.Setenv(EnvRateAPIBurst, "")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.LoginPerMin != DefaultRateLoginPerMin || opts.APIRPS != DefaultRateAPIRPS || opts.APIBurst != DefaultRateAPIBurst {
		t.Errorf("defaults inesperados: %+v", opts)
	}
}

func TestOptionsFromEnvOverrides(t *testing.T) {
	t.Setenv(EnvRateLoginPerMin, "10")
	t.Setenv(EnvRateAPIRPS, "2.5")
	t.Setenv(EnvRateAPIBurst, "7")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.LoginPerMin != 10 || opts.APIRPS != 2.5 || opts.APIBurst != 7 {
		t.Errorf("overrides no aplicados: %+v", opts)
	}
}

func TestOptionsFromEnvInvalid(t *testing.T) {
	cases := []struct{ env, val string }{
		{EnvRateLoginPerMin, "0"},
		{EnvRateLoginPerMin, "abc"},
		{EnvRateAPIRPS, "-1"},
		{EnvRateAPIRPS, "x"},
		{EnvRateAPIBurst, "0"},
		{EnvRateAPIBurst, "1.5"},
	}
	for _, c := range cases {
		t.Run(c.env+"="+c.val, func(t *testing.T) {
			t.Setenv(EnvRateLoginPerMin, "")
			t.Setenv(EnvRateAPIRPS, "")
			t.Setenv(EnvRateAPIBurst, "")
			t.Setenv(c.env, c.val)
			if _, err := OptionsFromEnv(); err == nil {
				t.Errorf("%s=%q no devolvió error", c.env, c.val)
			}
		})
	}
}
