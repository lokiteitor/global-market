// Tests unitarios del harness de stress: la SALVAGUARDA (que es lo que impide
// que una corrida toque producción), el cálculo de percentiles, el parseo de la
// mezcla de arquetipos, la configuración por entorno y el lector del exposition
// format de Prometheus. Sin BD ni red.
package stress

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// ─── Salvaguarda (GDD §13.4) ─────────────────────────────────────────────────

func TestGuardTargetRefusesWithoutAPIURL(t *testing.T) {
	_, err := GuardTarget("", "dev", nil)
	if !errors.Is(err, ErrAPIURLRequired) {
		t.Fatalf("GuardTarget(\"\") = %v, esperado ErrAPIURLRequired", err)
	}
	if !strings.Contains(err.Error(), "13.4") {
		t.Errorf("el error debe citar el GDD §13.4: %q", err.Error())
	}
}

func TestGuardTargetRefusesProduction(t *testing.T) {
	for _, env := range []string{"prod", "PROD", "production", "Live", "prd"} {
		_, err := GuardTarget("http://localhost:8080/api/v1", env, nil)
		if !errors.Is(err, ErrProductionEnv) {
			t.Fatalf("GuardTarget con II_ENV=%q = %v, esperado ErrProductionEnv", env, err)
		}
		if !strings.Contains(err.Error(), "nunca toca el mundo de producción") {
			t.Errorf("el error debe citar la salvaguarda del GDD: %q", err.Error())
		}
	}
}

func TestGuardTargetRefusesProductionEvenWithPermissiveAllowlist(t *testing.T) {
	// La allowlist es un override del OPERADOR sobre el host; nunca relaja la
	// negativa por entorno de producción.
	if _, err := GuardTarget("http://localhost:8080/api/v1", "prod", []string{"*"}); !errors.Is(err, ErrProductionEnv) {
		t.Fatalf("una allowlist permisiva no puede autorizar II_ENV=prod: %v", err)
	}
}

func TestGuardTargetRefusesHostOutsideAllowlist(t *testing.T) {
	for _, target := range []string{
		"https://api.imperio-industrial.com/api/v1",
		"http://10.0.0.7:8080/api/v1",
		"http://gateway.internal/api/v1",
		"http://notstress.example.com/api/v1",
	} {
		_, err := GuardTarget(target, "dev", nil)
		if !errors.Is(err, ErrHostNotAllowed) {
			t.Fatalf("GuardTarget(%q) = %v, esperado ErrHostNotAllowed", target, err)
		}
		if !strings.Contains(err.Error(), "13.4") {
			t.Errorf("el error debe citar el GDD §13.4: %q", err.Error())
		}
	}
}

func TestGuardTargetAcceptsNonProductionHosts(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080/api/v1":               "localhost",
		"http://127.0.0.1:8080/api/v1":               "127.0.0.1",
		"http://[::1]:8080/api/v1":                   "::1",
		"http://gw.stress.imperio.lan:8080/api/v1":   "*.stress.*",
		"https://staging.imperio-industrial.com/api": "staging.*",
		"http://api.staging.imperio.dev/api/v1":      "*.staging.*",
	}
	for target, wantPattern := range cases {
		matched, err := GuardTarget(target, "dev", nil)
		if err != nil {
			t.Fatalf("GuardTarget(%q) devolvió error: %v", target, err)
		}
		if matched != wantPattern {
			t.Errorf("GuardTarget(%q) casó %q, esperado %q", target, matched, wantPattern)
		}
	}
}

func TestGuardTargetHonoursCustomAllowlist(t *testing.T) {
	const target = "http://qa-gateway.corp.lan:8080/api/v1"
	if _, err := GuardTarget(target, "qa", nil); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("sin allowlist propia el host debía rechazarse: %v", err)
	}
	matched, err := GuardTarget(target, "qa", []string{"qa-*.corp.lan"})
	if err != nil {
		t.Fatalf("con allowlist propia el host debía aceptarse: %v", err)
	}
	if matched != "qa-*.corp.lan" {
		t.Errorf("patrón casado %q, esperado %q", matched, "qa-*.corp.lan")
	}
}

func TestGuardTargetRejectsMalformedURL(t *testing.T) {
	for _, target := range []string{"localhost:8080", "ftp://localhost/api", "://roto"} {
		if _, err := GuardTarget(target, "dev", nil); err == nil {
			t.Errorf("GuardTarget(%q) debía fallar", target)
		}
	}
}

func TestGuardDatabaseURL(t *testing.T) {
	ok := []string{
		"postgres://imperio:imperio@localhost:5432/imperio?sslmode=disable",
		"postgres://imperio@127.0.0.1:5432/imperio",
		"host=localhost port=5432 dbname=imperio",
		"host=/var/run/postgresql dbname=imperio",
	}
	for _, dsn := range ok {
		if _, err := GuardDatabaseURL(dsn, "dev", nil); err != nil {
			t.Errorf("GuardDatabaseURL(%q) devolvió error: %v", dsn, err)
		}
	}
	bad := []string{
		"postgres://imperio:imperio@db.imperio-industrial.com:5432/imperio",
		"host=db-prod-01 dbname=imperio",
	}
	for _, dsn := range bad {
		if _, err := GuardDatabaseURL(dsn, "dev", nil); !errors.Is(err, ErrHostNotAllowed) {
			t.Errorf("GuardDatabaseURL(%q) = %v, esperado ErrHostNotAllowed", dsn, err)
		}
	}
	if _, err := GuardDatabaseURL("postgres://x@localhost/db", "production", nil); !errors.Is(err, ErrProductionEnv) {
		t.Errorf("GuardDatabaseURL con II_ENV=production debía rehusar: %v", err)
	}
	if _, err := GuardDatabaseURL("", "dev", nil); err == nil {
		t.Error("GuardDatabaseURL(\"\") debía fallar")
	}
}

func TestMatchHostPattern(t *testing.T) {
	cases := []struct {
		pattern, host string
		want          bool
	}{
		{"localhost", "localhost", true},
		{"localhost", "LOCALHOST", true},
		{"localhost", "localhost.evil.com", false},
		{"*.localhost", "api.localhost", true},
		{"*.localhost", "localhost", false},
		{"*.stress.*", "gw.stress.lan", true},
		{"*.stress.*", "stress.lan", false},
		{"*.stress.*", "gw.notstress.lan", false},
		{"staging.*", "staging.example.com", true},
		{"staging.*", "pre-staging.example.com", false},
		{"qa-*.corp.lan", "qa-gateway.corp.lan", true},
		{"qa-*.corp.lan", "qa-gateway.other.lan", false},
		{"*", "cualquiera.example.com", true},
		{"localhost", "", false},
		{"", "localhost", false},
	}
	for _, c := range cases {
		if got := matchHostPattern(c.pattern, c.host); got != c.want {
			t.Errorf("matchHostPattern(%q, %q) = %t, esperado %t", c.pattern, c.host, got, c.want)
		}
	}
}

// ─── Percentiles ─────────────────────────────────────────────────────────────

func TestPercentileNearestRank(t *testing.T) {
	sorted := make([]time.Duration, 0, 100)
	for i := 1; i <= 100; i++ {
		sorted = append(sorted, time.Duration(i)*time.Millisecond)
	}
	cases := map[float64]time.Duration{
		0:   1 * time.Millisecond,
		50:  50 * time.Millisecond,
		95:  95 * time.Millisecond,
		99:  99 * time.Millisecond,
		100: 100 * time.Millisecond,
	}
	for p, want := range cases {
		if got := Percentile(sorted, p); got != want {
			t.Errorf("Percentile(p%v) = %s, esperado %s", p, got, want)
		}
	}
}

func TestPercentileSmallSamples(t *testing.T) {
	if got := Percentile(nil, 95); got != 0 {
		t.Errorf("Percentile(muestra vacía) = %s, esperado 0", got)
	}
	one := []time.Duration{7 * time.Millisecond}
	for _, p := range []float64{0, 50, 95, 100} {
		if got := Percentile(one, p); got != 7*time.Millisecond {
			t.Errorf("Percentile(muestra de 1, p%v) = %s, esperado 7ms", p, got)
		}
	}
	ten := make([]time.Duration, 0, 10)
	for i := 1; i <= 10; i++ {
		ten = append(ten, time.Duration(i)*time.Millisecond)
	}
	// nearest-rank: p50 → ceil(0.5·10) = 5 → 5ms; p95 → ceil(9.5) = 10 → 10ms.
	if got := Percentile(ten, 50); got != 5*time.Millisecond {
		t.Errorf("Percentile(10 muestras, p50) = %s, esperado 5ms", got)
	}
	if got := Percentile(ten, 95); got != 10*time.Millisecond {
		t.Errorf("Percentile(10 muestras, p95) = %s, esperado 10ms", got)
	}
}

func TestCollectorSnapshotExactPercentiles(t *testing.T) {
	c := NewCollector(0, 1)
	for i := 1; i <= 100; i++ {
		c.Record(okResult(OpBoardRead, time.Duration(i)*time.Millisecond))
	}
	c.Record(Result{Op: OpBoardRead, Outcome: OutcomeError, Latency: 5 * time.Millisecond, Class: ClassRateLimited, DomainCode: "RATE_LIMITED", Benign: true})
	c.Record(skippedResult(OpAccept, "sin oferta sell asequible en caché"))

	ops, totals := c.Snapshot(10 * time.Second)
	board := findOp(ops, OpBoardRead)
	if board == nil {
		t.Fatal("el informe no incluye board_read")
	}
	if board.Requests != 101 || board.OK != 100 || board.Errors != 1 {
		t.Fatalf("board_read: requests=%d ok=%d errors=%d, esperado 101/100/1", board.Requests, board.OK, board.Errors)
	}
	if !board.Latency.Exact {
		t.Error("con 101 muestras bajo el tope los percentiles deben ser exactos")
	}
	if board.Latency.P95Ms != 95 {
		t.Errorf("p95 = %.3f ms, esperado 95", board.Latency.P95Ms)
	}
	if board.OpsPerSecond != 10.1 {
		t.Errorf("ops/s = %v, esperado 10.1", board.OpsPerSecond)
	}
	accept := findOp(ops, OpAccept)
	if accept == nil || accept.Skipped != 1 || accept.Requests != 0 {
		t.Fatalf("accept: %+v — un skip no emite petición", accept)
	}
	if totals.Requests != 101 || totals.Errors != 1 || totals.BenignErrors != 1 || totals.UnexpectedError != 0 {
		t.Fatalf("totales inesperados: %+v", totals)
	}
	if totals.ErrorsBySt[ClassRateLimited] != 1 {
		t.Errorf("errores 429 = %d, esperado 1", totals.ErrorsBySt[ClassRateLimited])
	}
}

func TestCollectorReservoirBoundsMemory(t *testing.T) {
	c := NewCollector(16, 42)
	for i := 0; i < 1_000; i++ {
		c.Record(okResult(OpPublish, time.Duration(i)*time.Microsecond))
	}
	ops, _ := c.Snapshot(time.Second)
	pub := findOp(ops, OpPublish)
	if pub == nil {
		t.Fatal("el informe no incluye publish")
	}
	if pub.Requests != 1_000 {
		t.Errorf("requests = %d, esperado 1000", pub.Requests)
	}
	if pub.Latency.Samples != 16 {
		t.Errorf("muestras conservadas = %d, esperado 16 (tope del reservorio)", pub.Latency.Samples)
	}
	if pub.Latency.Exact {
		t.Error("con reservorio los percentiles no pueden declararse exactos")
	}
	if pub.Latency.MaxMs != 0.999 {
		t.Errorf("máximo = %.3f ms, esperado 0.999 (el máximo NO se muestrea)", pub.Latency.MaxMs)
	}
}

// ─── Mezcla de arquetipos ────────────────────────────────────────────────────

func TestParseMixDefault(t *testing.T) {
	m, err := ParseMix(DefaultMixSpec)
	if err != nil {
		t.Fatalf("ParseMix(default): %v", err)
	}
	if got := m.String(); got != DefaultMixSpec {
		t.Errorf("String() = %q, esperado %q", got, DefaultMixSpec)
	}
	if m.TotalWeight() != 100 {
		t.Errorf("TotalWeight() = %d, esperado 100", m.TotalWeight())
	}
	want := map[Archetype]int{
		ArchetypeProducer: 50, ArchetypeTrader: 30, ArchetypeFreighter: 10, ArchetypeTransformer: 10,
	}
	for a, w := range want {
		if m.Weights[a] != w {
			t.Errorf("peso de %s = %d, esperado %d", a, m.Weights[a], w)
		}
	}
}

func TestParseMixTolerantAndStrict(t *testing.T) {
	if _, err := ParseMix(" producer = 1 , trader = 3 "); err != nil {
		t.Errorf("la mezcla con espacios debía aceptarse: %v", err)
	}
	if _, err := ParseMix("PRODUCER=1"); err != nil {
		t.Errorf("el arquetipo en mayúsculas debía aceptarse: %v", err)
	}
	bad := []string{
		"",
		"producer",
		"minero=50",
		"producer=50,producer=50",
		"producer=-1",
		"producer=0,trader=0",
		"producer=x",
	}
	for _, spec := range bad {
		if _, err := ParseMix(spec); err == nil {
			t.Errorf("ParseMix(%q) debía fallar", spec)
		}
	}
}

func TestMixCountsLargestRemainder(t *testing.T) {
	m, err := ParseMix(DefaultMixSpec)
	if err != nil {
		t.Fatalf("ParseMix: %v", err)
	}
	cases := []struct {
		total int
		want  map[Archetype]int
	}{
		{200, map[Archetype]int{ArchetypeProducer: 100, ArchetypeTrader: 60, ArchetypeFreighter: 20, ArchetypeTransformer: 20}},
		{10, map[Archetype]int{ArchetypeProducer: 5, ArchetypeTrader: 3, ArchetypeFreighter: 1, ArchetypeTransformer: 1}},
		{1, map[Archetype]int{ArchetypeProducer: 1, ArchetypeTrader: 0, ArchetypeFreighter: 0, ArchetypeTransformer: 0}},
		{3, map[Archetype]int{ArchetypeProducer: 2, ArchetypeTrader: 1, ArchetypeFreighter: 0, ArchetypeTransformer: 0}},
	}
	for _, c := range cases {
		got := m.Counts(c.total)
		sum := 0
		for a, want := range c.want {
			if got[a] != want {
				t.Errorf("Counts(%d)[%s] = %d, esperado %d", c.total, a, got[a], want)
			}
		}
		for _, n := range got {
			sum += n
		}
		if sum != c.total {
			t.Errorf("Counts(%d) suma %d: el reparto SIEMPRE debe sumar el total", c.total, sum)
		}
	}
}

func TestMixCountsWithZeroWeightArchetype(t *testing.T) {
	m, err := ParseMix("producer=1,freighter=0")
	if err != nil {
		t.Fatalf("ParseMix: %v", err)
	}
	counts := m.Counts(5)
	if counts[ArchetypeProducer] != 5 || counts[ArchetypeFreighter] != 0 {
		t.Fatalf("un arquetipo de peso 0 no debe recibir bots: %+v", counts)
	}
}

func TestMixAllocateInterleaves(t *testing.T) {
	m, err := ParseMix("producer=2,trader=1,freighter=1")
	if err != nil {
		t.Fatalf("ParseMix: %v", err)
	}
	got := m.Allocate(8)
	if len(got) != 8 {
		t.Fatalf("Allocate(8) devolvió %d elementos", len(got))
	}
	want := []Archetype{
		ArchetypeProducer, ArchetypeTrader, ArchetypeFreighter,
		ArchetypeProducer, ArchetypeTrader, ArchetypeFreighter,
		ArchetypeProducer, ArchetypeProducer,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Allocate(8) = %v, esperado intercalado %v", got, want)
		}
	}
}

func TestArchetypeBotArchetypeMapping(t *testing.T) {
	want := map[Archetype]string{
		ArchetypeProducer:    "primary_producer",
		ArchetypeTrader:      "arbitrageur",
		ArchetypeFreighter:   "freighter",
		ArchetypeTransformer: "industrial_transformer",
	}
	for a, w := range want {
		if got := a.BotArchetype(); got != w {
			t.Errorf("%s.BotArchetype() = %q, esperado %q", a, got, w)
		}
	}
	if Archetype("minero").Valid() {
		t.Error("un arquetipo desconocido no puede ser válido")
	}
}

// ─── Configuración ───────────────────────────────────────────────────────────

func TestOptionsFromEnvDefaultsAndSafeguard(t *testing.T) {
	clearStressEnv(t)
	if _, err := OptionsFromEnv(); !errors.Is(err, ErrAPIURLRequired) {
		t.Fatalf("sin %s el harness debe rehusar: %v", EnvAPIURL, err)
	}

	t.Setenv(EnvAPIURL, "http://localhost:8080/api/v1")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.Bots != DefaultBots || opts.Ramp != DefaultRamp || opts.Duration != DefaultDuration || opts.Tick != DefaultTick {
		t.Errorf("defaults del perfil de carga incorrectos: %+v", opts)
	}
	if opts.WriteRatio != DefaultWriteRatio || opts.ReportPath != DefaultReportPath || opts.Addr != DefaultAddr || !opts.Cleanup {
		t.Errorf("defaults de informe/limpieza incorrectos: %+v", opts)
	}
	// El lado VENDEDOR viene activado por defecto: sin él la operación de
	// aceptación no puede escalar con la población (solo publicaría buy).
	if opts.StockEndowment != DefaultStockEndowment || opts.SellShare != DefaultSellShare {
		t.Errorf("defaults del lado vendedor incorrectos: dotación %d / sell_share %g", opts.StockEndowment, opts.SellShare)
	}
	if opts.Mix.String() != DefaultMixSpec {
		t.Errorf("mezcla por defecto = %q, esperado %q", opts.Mix.String(), DefaultMixSpec)
	}
	if !strings.HasPrefix(opts.RunAccountPrefix(), AccountPrefix) {
		t.Errorf("prefijo de cuentas %q, esperado que empiece por %q", opts.RunAccountPrefix(), AccountPrefix)
	}
	want := []string{"http://localhost:8080/metrics", "http://localhost:8081/metrics"}
	if len(opts.TargetMetrics) != 2 || opts.TargetMetrics[0] != want[0] || opts.TargetMetrics[1] != want[1] {
		t.Errorf("targets de métricas = %v, esperado %v", opts.TargetMetrics, want)
	}

	t.Setenv(EnvEnvironment, "prod")
	if _, err := OptionsFromEnv(); !errors.Is(err, ErrProductionEnv) {
		t.Fatalf("con II_ENV=prod el harness debe rehusar: %v", err)
	}
}

func TestOptionsFromEnvOverrides(t *testing.T) {
	clearStressEnv(t)
	t.Setenv(EnvAPIURL, "http://gw.stress.lan:9000/api/v1")
	t.Setenv(EnvBots, "12")
	t.Setenv(EnvRamp, "2s")
	t.Setenv(EnvDuration, "5s")
	t.Setenv(EnvTick, "250ms")
	t.Setenv(EnvMix, "trader=1,freighter=1")
	t.Setenv(EnvWriteRatio, "0.75")
	t.Setenv(EnvReport, "/tmp/informe.json")
	t.Setenv(EnvCleanup, "false")
	t.Setenv(EnvRunID, "corrida-01")
	t.Setenv(EnvStockEndowment, "250")
	t.Setenv(EnvSellShare, "0.25")

	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.Bots != 12 || opts.Ramp != 2*time.Second || opts.Duration != 5*time.Second || opts.Tick != 250*time.Millisecond {
		t.Errorf("perfil de carga no aplicado: %+v", opts)
	}
	if opts.WriteRatio != 0.75 || opts.ReportPath != "/tmp/informe.json" || opts.Cleanup {
		t.Errorf("informe/limpieza no aplicados: %+v", opts)
	}
	if opts.Mix.String() != "trader=1,freighter=1" {
		t.Errorf("mezcla = %q", opts.Mix.String())
	}
	if got := opts.AccountName(ArchetypeTrader, 7); got != "stress-corrida-01-trader-0007" {
		t.Errorf("AccountName = %q, esperado stress-corrida-01-trader-0007", got)
	}
	if opts.AllowMatch != "*.stress.*" {
		t.Errorf("patrón de la allowlist = %q, esperado *.stress.*", opts.AllowMatch)
	}
	if opts.StockEndowment != 250 || opts.SellShare != 0.25 {
		t.Errorf("lado vendedor no aplicado: dotación %d / sell_share %g", opts.StockEndowment, opts.SellShare)
	}
}

func TestOptionsValidateRejectsBadValues(t *testing.T) {
	base := func() Options {
		o := DefaultOptions()
		o.APIURL = "http://localhost:8080/api/v1"
		o.RunID = "abc"
		return o
	}
	cases := map[string]func(*Options){
		"bots 0":            func(o *Options) { o.Bots = 0 },
		"bots desbordado":   func(o *Options) { o.Bots = maxBots + 1 },
		"rampa negativa":    func(o *Options) { o.Ramp = -time.Second },
		"duración 0":        func(o *Options) { o.Duration = 0 },
		"tick 0":            func(o *Options) { o.Tick = 0 },
		"write ratio > 1":   func(o *Options) { o.WriteRatio = 1.5 },
		"write ratio < 0":   func(o *Options) { o.WriteRatio = -0.1 },
		"capital 0":         func(o *Options) { o.Capital = 0 },
		"dotación negativa": func(o *Options) { o.StockEndowment = -1 },
		"sell share > 1":    func(o *Options) { o.SellShare = 1.5 },
		"sell share < 0":    func(o *Options) { o.SellShare = -0.1 },
		"informe vacío":     func(o *Options) { o.ReportPath = "" },
		"addr vacía":        func(o *Options) { o.Addr = "" },
		"semilla vacía":     func(o *Options) { o.SecretSeed = " " },
		"run id inválido":   func(o *Options) { o.RunID = "Corrida 01" },
		"run id vacío":      func(o *Options) { o.RunID = "" },
		"target inválido":   func(o *Options) { o.TargetMetrics = []string{"no-es-url"} },
		"log interval 0":    func(o *Options) { o.LogInterval = 0 },
		"muestras 0":        func(o *Options) { o.MaxSamples = 0 },
		"bd de producción":  func(o *Options) { o.DatabaseURL = "postgres://u@db.imperio.com/imperio" },
		"api de producción": func(o *Options) { o.APIURL = "https://api.imperio.com/api/v1" },
	}
	for name, mutate := range cases {
		o := base()
		mutate(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("Validate() debía fallar con %s", name)
		}
	}
	o := base()
	if err := o.Validate(); err != nil {
		t.Fatalf("la configuración base debía ser válida: %v", err)
	}
}

// clearStressEnv deja el entorno del test sin variables del harness.
func clearStressEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		EnvAPIURL, EnvEnvironment, EnvAllowHosts, EnvBots, EnvRamp, EnvDuration, EnvTick,
		EnvMix, EnvWriteRatio, EnvReport, EnvAddr, EnvCleanup, EnvRunID, EnvCapital,
		EnvSecretSeed, EnvDatabaseURL, EnvPlatformDatabaseURL, EnvTargetMetrics,
		EnvLogInterval, EnvMaxSamples, EnvStockEndowment, EnvSellShare,
	} {
		t.Setenv(k, "")
	}
}

// ─── Lector del exposition format de Prometheus ──────────────────────────────

const sampleExposition = `# HELP ii_http_requests_total Total de peticiones HTTP servidas.
# TYPE ii_http_requests_total counter
ii_http_requests_total{method="GET",route="/contracts/board",service="gateway",status="200"} 120
ii_http_requests_total{method="POST",route="/contracts/publications",service="gateway",status="500"} 3
# TYPE ii_http_request_duration_seconds histogram
ii_http_request_duration_seconds_bucket{method="GET",route="/contracts/board",service="gateway",status="200",le="0.005"} 0
ii_http_request_duration_seconds_bucket{method="GET",route="/contracts/board",service="gateway",status="200",le="0.01"} 50
ii_http_request_duration_seconds_bucket{method="GET",route="/contracts/board",service="gateway",status="200",le="0.025"} 100
ii_http_request_duration_seconds_bucket{method="GET",route="/contracts/board",service="gateway",status="200",le="+Inf"} 100
ii_http_request_duration_seconds_sum{method="GET",route="/contracts/board",service="gateway",status="200"} 1.5
ii_http_request_duration_seconds_count{method="GET",route="/contracts/board",service="gateway",status="200"} 100
ii_outbox_consumer_lag{consumer="notifier"} 7
ii_outbox_consumer_lag{consumer="settler"} 2
# TYPE ii_tx_serialization_retries_total counter
ii_tx_serialization_retries_total 4213
# TYPE ii_tx_serialization_exhausted_total counter
ii_tx_serialization_exhausted_total 9
go_goroutines 143
process_resident_memory_bytes 1.048576e+08
`

func TestParsePrometheusText(t *testing.T) {
	samples, err := ParsePrometheusText(strings.NewReader(sampleExposition))
	if err != nil {
		t.Fatalf("ParsePrometheusText: %v", err)
	}
	if got := samples.Sum("ii_http_requests_total", nil); got != 123 {
		t.Errorf("suma de peticiones = %v, esperado 123", got)
	}
	if got := samples.Sum("ii_http_requests_total", map[string]string{"route": "/contracts/board"}); got != 120 {
		t.Errorf("peticiones del tablón = %v, esperado 120", got)
	}
	if got := samples.SumWhere("ii_http_requests_total", func(l map[string]string) bool {
		return strings.HasPrefix(l["status"], "5")
	}); got != 3 {
		t.Errorf("peticiones 5xx = %v, esperado 3", got)
	}
	if got := samples.AnyLabel("ii_http_requests_total", "service"); got != "gateway" {
		t.Errorf("etiqueta service = %q, esperado gateway", got)
	}
	lag := samples.ByLabel("ii_outbox_consumer_lag", "consumer")
	if lag["notifier"] != 7 || lag["settler"] != 2 {
		t.Errorf("lag por consumidor = %v", lag)
	}
	if got := samples.Sum("go_goroutines", nil); got != 143 {
		t.Errorf("go_goroutines = %v, esperado 143", got)
	}
	if got := samples.Sum("process_resident_memory_bytes", nil); got != 1.048576e+08 {
		t.Errorf("memoria residente = %v", got)
	}
	if got := samples.Sum(txRetriesMetric, nil); got != 4213 {
		t.Errorf("reintentos serializables = %v, esperado 4213", got)
	}
	if got := samples.Sum(txExhaustedMetric, nil); got != 9 {
		t.Errorf("presupuestos agotados = %v, esperado 9", got)
	}
}

// La contención SERIALIZABLE es un disparador MEDIDO del SAD §13: el probe debe
// proyectarla en el informe (no basta con que el proceso la publique) y
// distinguir «no publica la familia» de «publica 0».
func TestFillTargetMetricsReadsSerializationContention(t *testing.T) {
	samples, err := ParsePrometheusText(strings.NewReader(sampleExposition))
	if err != nil {
		t.Fatalf("ParsePrometheusText: %v", err)
	}
	var tm TargetMetrics
	fillTargetMetrics(&tm, samples)
	if !tm.TxMetricsPublished {
		t.Fatal("el target publica ii_tx_serialization_*: debe quedar marcado como leído")
	}
	if tm.TxRetries != 4213 || tm.TxExhausted != 9 {
		t.Errorf("contención raspada = %v/%v, esperado 4213/9", tm.TxRetries, tm.TxExhausted)
	}

	// Un proceso que no registra la familia no tiene lectura: no puede quedar
	// como «0 contención», que sería una lectura tranquilizadora inventada.
	bare, err := ParsePrometheusText(strings.NewReader("go_goroutines 12\n"))
	if err != nil {
		t.Fatalf("ParsePrometheusText: %v", err)
	}
	var sin TargetMetrics
	fillTargetMetrics(&sin, bare)
	if sin.TxMetricsPublished {
		t.Error("sin la familia publicada no hay lectura de contención")
	}
}

func TestHistogramQuantile(t *testing.T) {
	samples, err := ParsePrometheusText(strings.NewReader(sampleExposition))
	if err != nil {
		t.Fatalf("ParsePrometheusText: %v", err)
	}
	filter := map[string]string{"route": "/contracts/board"}
	if got := pickBoardRoute(samples); got != "/contracts/board" {
		t.Fatalf("pickBoardRoute = %q, esperado /contracts/board", got)
	}
	// 100 observaciones: 50 en (0.005, 0.01] y 50 en (0.01, 0.025]. El p95
	// (rango 95) cae en el segundo bucket, interpolado: 0.01 + 0.015·(45/50).
	got := samples.HistogramQuantile("ii_http_request_duration_seconds", filter, 0.95)
	want := 0.01 + 0.015*(45.0/50.0)
	if diff := got - want; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("HistogramQuantile(p95) = %v, esperado %v", got, want)
	}
	if got := samples.HistogramQuantile("no_existe", filter, 0.95); got != 0 {
		t.Errorf("un histograma inexistente debe dar 0, dio %v", got)
	}
}

func TestClassifyErrorWithoutAPIError(t *testing.T) {
	r := classify(OpBoardRead, 3*time.Millisecond, errors.New("dial tcp: connection refused"))
	if r.Outcome != OutcomeError || r.Class != ClassNetwork || r.Benign {
		t.Fatalf("error de red mal clasificado: %+v", r)
	}
	r = classify(OpBoardRead, time.Millisecond, errors.New("botsdk: GET /x: context canceled"))
	if r.Class != ClassCanceled || !r.Benign {
		t.Fatalf("la cancelación de fin de corrida debe ser benigna: %+v", r)
	}
}

// ─── Veredicto (puerta de calidad del binario) ───────────────────────────────

// syntheticReport arma un informe con carga sana y sin errores del harness, y
// deja que el llamante decida qué registró el sistema bajo prueba.
func syntheticReport(targets []TargetMetrics) *Report {
	r := &Report{
		DurationSeconds: 60,
		Config:          ConfigReport{Bots: 150},
		Totals: Totals{
			Requests:   1000,
			OK:         1000,
			ErrorsBySt: map[string]int64{},
		},
		System: SystemReport{Targets: targets},
	}
	r.Verdict = buildVerdict(r)
	return r
}

// Una corrida que no llega a emitir la operación MÁS CARA de su mezcla no midió
// el techo de escritura, por muy limpias que salgan las demás cifras. El
// veredicto tiene que decirlo, y decirlo LO PRIMERO.
func TestVerdictDenunciaCaminoNoEjercitado(t *testing.T) {
	r := syntheticReport(nil)
	r.Operations = []OpReport{
		{Op: OpBoardRead, Requests: 3896, OK: 3896},
		{Op: OpPublish, Requests: 1781, OK: 1781},
		{Op: OpAccept, Requests: 0, Skipped: 519, SkipReasons: map[string]int64{
			"sin oferta sell asequible tras consultar el tablón": 519,
		}},
	}
	r.Verdict = buildVerdict(r)

	if len(r.Verdict.Unexercised) != 1 || r.Verdict.Unexercised[0] != string(OpAccept) {
		t.Fatalf("caminos no ejercitados = %v, esperado [accept]", r.Verdict.Unexercised)
	}
	first := r.Verdict.Lines[0]
	if !strings.Contains(first, "NO SE EJERCITÓ") || !strings.Contains(first, string(OpAccept)) {
		t.Fatalf("la primera línea del veredicto debe denunciar el camino no ejercitado, fue %q", first)
	}
	if !strings.Contains(first, "519") || !strings.Contains(first, "sin oferta sell asequible") {
		t.Errorf("la línea debe llevar el recuento y el motivo dominante: %q", first)
	}
	if !strings.Contains(r.Console(), "NO SE EJERCITÓ") {
		t.Error("la lectura por consola tiene que arrastrar la denuncia del camino no ejercitado")
	}
}

// Los 5xx cuentan siempre, pero el veredicto tiene que decir DE DÓNDE salieron:
// la ráfaga de cancelación del cierre no es el perfil de carga, y atribuirle a
// la carga medida lo que rompió en la higiene es tan engañoso como no contarlo.
func TestVerdictLocalizaLos5xxDeLaHigieneDeCierre(t *testing.T) {
	r := syntheticReport(nil)
	r.Totals.ErrorsBySt = map[string]int64{ClassServer: 2}
	r.Operations = []OpReport{
		{Op: OpCancel, Requests: 1118, OK: 1109, Errors: 9, BenignErrors: 9,
			ErrorsBySt: map[string]int64{ClassConflict: 9}},
		{Op: OpDrainCancel, Requests: 1213, OK: 1197, Errors: 16, BenignErrors: 14,
			ErrorsBySt: map[string]int64{ClassConflict: 14, ClassServer: 2}},
	}
	r.Verdict = buildVerdict(r)

	if r.Verdict.OK {
		t.Fatal("dos 5xx son dos 5xx: el veredicto no puede seguir siendo positivo")
	}
	joined := strings.Join(r.Verdict.Lines, "\n")
	if !strings.Contains(joined, string(OpDrainCancel)) || !strings.Contains(joined, "FUERA del perfil de carga") {
		t.Fatalf("el veredicto no localiza los 5xx en la higiene de cierre: %q", joined)
	}
}

// Una operación que se emite solo a ratos tampoco escala con la población: el
// informe lo dice sin llegar a declararla no ejercitada.
func TestVerdictDenunciaCaminoDegradado(t *testing.T) {
	r := syntheticReport(nil)
	r.Operations = []OpReport{
		{Op: OpAccept, Requests: 40, OK: 40, Skipped: 460, SkipReasons: map[string]int64{
			"sin oferta sell asequible tras consultar el tablón": 460,
		}},
		{Op: OpCancel, Requests: 1625, OK: 1625, Skipped: 462, SkipReasons: map[string]int64{
			"cooldown anti-parpadeo aún activo": 283, "sin publicaciones propias vivas": 179,
		}},
	}
	r.Verdict = buildVerdict(r)

	if len(r.Verdict.Unexercised) != 0 {
		t.Fatalf("una operación emitida no puede declararse no ejercitada: %v", r.Verdict.Unexercised)
	}
	joined := strings.Join(r.Verdict.Lines, "\n")
	if !strings.Contains(joined, "se ejercitó a medias") || !strings.Contains(joined, string(OpAccept)) {
		t.Fatalf("el veredicto debe señalar la operación degradada: %q", joined)
	}
	if strings.Contains(joined, string(OpCancel)) {
		t.Errorf("cancel se omitió por debajo del umbral (%.0f%%) y no debe señalarse: %q",
			100*skipDegradedRatio, joined)
	}
	if got := dominantSkipReason(map[string]int64{"b": 2, "a": 2, "c": 1}); got != "a" {
		t.Errorf("motivo dominante = %q, esperado el desempate alfabético %q", got, "a")
	}
}

func TestVerdictNegativeWhenOnlyTargetRegistered5xx(t *testing.T) {
	r := syntheticReport([]TargetMetrics{{
		URL:                "http://localhost:8080/metrics",
		Service:            "gateway",
		Reachable:          true,
		BaselineTaken:      true,
		HTTPRequests:       65103,
		HTTPErrors5xx:      2,
		HTTPRequestsDelta:  65103,
		HTTPErrors5xxDelta: 2,
	}})
	if r.Verdict.TargetServerErrors != 2 {
		t.Fatalf("5xx del sistema = %d, esperado 2", r.Verdict.TargetServerErrors)
	}
	if r.Verdict.OK {
		t.Fatalf("veredicto OK con 2 5xx registrados por el sistema: %v", r.Verdict.Lines)
	}
	last := r.Verdict.Lines[len(r.Verdict.Lines)-1]
	if !strings.Contains(last, "ATENCIÓN") || strings.Contains(last, "sin 5xx") {
		t.Fatalf("la última línea contradice el veredicto: %q", last)
	}
}

func TestVerdictIgnoresTargetCountersWithoutBaseline(t *testing.T) {
	// Sin línea base el acumulado incluye tráfico anterior a la corrida: no es
	// atribuible y no puede tumbar el veredicto.
	r := syntheticReport([]TargetMetrics{{
		URL:           "http://localhost:8080/metrics",
		Reachable:     true,
		BaselineTaken: false,
		HTTPRequests:  65103,
		HTTPErrors5xx: 2,
	}})
	if r.Verdict.TargetServerErrors != 0 {
		t.Fatalf("sin línea base los 5xx acumulados no cuentan, contó %d", r.Verdict.TargetServerErrors)
	}
	if !r.Verdict.OK {
		t.Fatalf("veredicto negativo sin evidencia atribuible: %v", r.Verdict.Lines)
	}
}

func TestVerdictOKWithoutErrors(t *testing.T) {
	r := syntheticReport([]TargetMetrics{{
		URL:                "http://localhost:8080/metrics",
		Reachable:          true,
		BaselineTaken:      true,
		HTTPRequestsDelta:  65103,
		HTTPErrors5xxDelta: 0,
	}})
	if !r.Verdict.OK {
		t.Fatalf("veredicto negativo sin 5xx: %v", r.Verdict.Lines)
	}
	last := r.Verdict.Lines[len(r.Verdict.Lines)-1]
	if !strings.Contains(last, "absorbió la carga") {
		t.Fatalf("última línea inesperada: %q", last)
	}
}

// Un trabajo de fondo que se cae por contención no lo recibe ningún cliente: si
// el informe no lo saca de las métricas del sistema, la única forma de enterarse
// es leer el log del engine a mano — justo lo que el informe existe para evitar.
func TestVerdictReportsSerializationContention(t *testing.T) {
	r := syntheticReport([]TargetMetrics{{
		URL:                "http://localhost:8081/metrics",
		Service:            "engine",
		Reachable:          true,
		BaselineTaken:      true,
		TxMetricsPublished: true,
		TxRetries:          4213,
		TxExhausted:        9,
		TxRetriesDelta:     4100,
		TxExhaustedDelta:   9,
	}})
	if r.Verdict.TargetTxExhausted != 9 || r.Verdict.TargetTxRetries != 4100 {
		t.Fatalf("contención del veredicto = %d/%d, esperado 4100/9",
			r.Verdict.TargetTxRetries, r.Verdict.TargetTxExhausted)
	}
	joined := strings.Join(r.Verdict.Lines, "\n")
	if !strings.Contains(joined, "contención SERIALIZABLE en engine") {
		t.Errorf("falta la línea de contención por proceso: %q", joined)
	}
	if strings.Contains(joined, "absorbió la carga") {
		t.Errorf("con 9 presupuestos agotados el informe no puede decir que absorbió la carga: %q", joined)
	}
	last := r.Verdict.Lines[len(r.Verdict.Lines)-1]
	if !strings.Contains(last, "ADVERTENCIA") || !strings.Contains(last, "9 veces") {
		t.Errorf("el agotamiento debe cerrar el veredicto como advertencia explícita: %q", last)
	}
	// Es una señal de techo, no una rotura: el veredicto sigue siendo OK (salida
	// 0), que es lo que separa «encontramos el techo» de «el sistema rompió».
	if !r.Verdict.OK {
		t.Errorf("la contención advierte, no tumba la corrida: %v", r.Verdict.Lines)
	}

	// Solo reintentos (ruido normal bajo carga): se publican, pero no advierten.
	sano := syntheticReport([]TargetMetrics{{
		URL: "http://localhost:8081/metrics", Service: "engine", Reachable: true,
		BaselineTaken: true, TxMetricsPublished: true, TxRetriesDelta: 512,
	}})
	joined = strings.Join(sano.Verdict.Lines, "\n")
	if strings.Contains(joined, "ADVERTENCIA") || !strings.Contains(joined, "absorbió la carga") {
		t.Errorf("sin presupuestos agotados no hay advertencia: %q", joined)
	}

	// Un proceso que no publica la familia deja constancia de que NO hay lectura.
	sin := syntheticReport([]TargetMetrics{{
		URL: "http://localhost:8080/metrics", Service: "gateway", Reachable: true, BaselineTaken: true,
	}})
	if !strings.Contains(strings.Join(sin.Verdict.Lines, "\n"), "SIN lectura") {
		t.Errorf("sin la familia publicada el informe debe declararlo: %v", sin.Verdict.Lines)
	}
}

func TestApplyBaselineDeltaAndProcessRestart(t *testing.T) {
	targets := []TargetMetrics{
		{URL: "a", Reachable: true, HTTPRequests: 65103, HTTPErrors5xx: 5, TxMetricsPublished: true, TxRetries: 4213, TxExhausted: 9},
		{URL: "b", Reachable: true, HTTPRequests: 40, HTTPErrors5xx: 1, TxMetricsPublished: true, TxRetries: 12, TxExhausted: 1},
		{URL: "c", Reachable: true, HTTPRequests: 10, HTTPErrors5xx: 0},
	}
	baseline := []TargetMetrics{
		{URL: "a", Reachable: true, HTTPRequests: 100, HTTPErrors5xx: 3, TxRetries: 113, TxExhausted: 0},
		// Reinicio del proceso a mitad de corrida: el contador retrocede.
		{URL: "b", Reachable: true, HTTPRequests: 900, HTTPErrors5xx: 7, TxRetries: 4000, TxExhausted: 8},
		// Target no accesible en la línea base: sin delta atribuible.
		{URL: "c", Reachable: false},
	}
	ApplyBaseline(targets, baseline)
	if targets[0].TxRetriesDelta != 4100 || targets[0].TxExhaustedDelta != 9 {
		t.Errorf("contención de a = %v/%v, esperado 4100/9", targets[0].TxRetriesDelta, targets[0].TxExhaustedDelta)
	}
	if targets[1].TxRetriesDelta != 12 || targets[1].TxExhaustedDelta != 1 {
		t.Errorf("tras reinicio la contención es el valor actual, dio %v/%v", targets[1].TxRetriesDelta, targets[1].TxExhaustedDelta)
	}
	if retries, exhausted := targetTxContention(targets); retries != 4112 || exhausted != 10 {
		t.Errorf("contención agregada = %d/%d, esperado 4112/10", retries, exhausted)
	}
	if targets[0].HTTPRequestsDelta != 65003 || targets[0].HTTPErrors5xxDelta != 2 {
		t.Errorf("delta de a = %v/%v, esperado 65003/2", targets[0].HTTPRequestsDelta, targets[0].HTTPErrors5xxDelta)
	}
	if targets[1].HTTPRequestsDelta != 40 || targets[1].HTTPErrors5xxDelta != 1 {
		t.Errorf("tras reinicio el delta es el valor actual, dio %v/%v", targets[1].HTTPRequestsDelta, targets[1].HTTPErrors5xxDelta)
	}
	if targets[2].BaselineTaken {
		t.Error("sin línea base accesible no debe marcarse BaselineTaken")
	}
	if got := targetServerErrors(targets); got != 3 {
		t.Errorf("5xx del sistema = %d, esperado 3 (2 de a + 1 de b)", got)
	}
}

// El informe cruza DOS lecturas del mismo disparador de Kafka: el gauge que
// publica cada proceso y el sondeo a la fuente. Ninguna puede presentarse como
// «el máximo de la corrida» —las dos son un instante— y, cuando divergen, el
// informe debe decir cuál manda en lugar de dejar dos cifras contradictorias.
func TestVerdictOutboxLagLinesAreReconciled(t *testing.T) {
	r := syntheticReport([]TargetMetrics{{
		URL:                 "http://localhost:8080/metrics",
		Service:             "gateway",
		Reachable:           true,
		BaselineTaken:       true,
		OutboxLag:           0,
		OutboxLagByConsumer: map[string]float64{"notification_gateway": 0},
	}})
	r.System.Database = DatabaseProbe{Reachable: true, OutboxPending: 1441, OutboxEmittedDuringRun: 9000}
	r.Verdict = buildVerdict(r)

	joined := strings.Join(r.Verdict.Lines, "\n")
	if strings.Contains(joined, "máximo 0 eventos") {
		t.Errorf("un raspado único no es el máximo de la corrida: %q", joined)
	}
	if !strings.Contains(joined, "INSTANTÁNEO al terminar la carga") {
		t.Errorf("las dos lecturas deben declararse instantáneas: %q", joined)
	}
	if !strings.Contains(joined, "manda la de BD") {
		t.Errorf("la divergencia 0 vs 1441 debe quedar explicada: %q", joined)
	}
	if lag, published := maxTargetOutboxLag(r.System.Targets); !published || lag != 0 {
		t.Errorf("maxTargetOutboxLag = %v/%v, esperado 0/true (el target publica 0)", lag, published)
	}

	// Sin la métrica no hay nada que reconciliar: no se inventa una divergencia.
	r2 := syntheticReport([]TargetMetrics{{URL: "http://localhost:8081/metrics", Reachable: true, BaselineTaken: true}})
	r2.System.Database = DatabaseProbe{Reachable: true, OutboxPending: 1441}
	r2.Verdict = buildVerdict(r2)
	if strings.Contains(strings.Join(r2.Verdict.Lines, "\n"), "manda la de BD") {
		t.Error("sin gauge publicado no hay dos lecturas que contrastar")
	}
	if _, published := maxTargetOutboxLag(r2.System.Targets); published {
		t.Error("un target sin la métrica no publica lag")
	}
}
