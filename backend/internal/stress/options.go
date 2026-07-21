package stress

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Variables de entorno del harness (prefijo II_STRESS_*, 12-factor). La
// salvaguarda vive en safety.go (II_STRESS_API_URL, II_ENV, II_STRESS_ALLOW_HOSTS).
const (
	// EnvAPIURL es la raíz de la API pública del ENTORNO DE PRUEBAS que se va a
	// cargar, incluido el prefijo de versión. OBLIGATORIA y SIN DEFAULT.
	EnvAPIURL = "II_STRESS_API_URL"
	// EnvBots es el número total de bots de la corrida. Default 200.
	EnvBots = "II_STRESS_BOTS"
	// EnvRamp es la rampa de arranque: los bots entran escalonados a lo largo
	// de esta ventana en lugar de golpe. Default 30s.
	EnvRamp = "II_STRESS_RAMP"
	// EnvDuration es la duración de la fase de carga (desde el primer bot).
	// Default 120s.
	EnvDuration = "II_STRESS_DURATION"
	// EnvTick es el periodo de acción de cada bot, jitterizado ±20%. Default 1s.
	EnvTick = "II_STRESS_TICK"
	// EnvMix es la mezcla de arquetipos en porcentaje. Default DefaultMixSpec.
	EnvMix = "II_STRESS_MIX"
	// EnvWriteRatio es la fracción de acciones que son de ESCRITURA (0..1).
	// Default 0.3.
	EnvWriteRatio = "II_STRESS_WRITE_RATIO"
	// EnvReport es la ruta del informe JSON final. Default stress-report.json.
	EnvReport = "II_STRESS_REPORT"
	// EnvAddr es la dirección del servidor de observabilidad propio del harness
	// (/healthz, /metrics). Default :8083.
	EnvAddr = "II_STRESS_ADDR"
	// EnvCleanup activa la limpieza final (marcar retiradas las cuentas del
	// run). Default true.
	EnvCleanup = "II_STRESS_CLEANUP"
	// EnvRunID identifica la corrida y forma el prefijo de las cuentas
	// ("stress-<run_id>-…"). Default: derivado de un UUIDv7.
	EnvRunID = "II_STRESS_RUN_ID"
	// EnvCapital es la capitalización de cada cuenta nueva del harness, en
	// unidades menores (emisión contabilizada del banco central). Default 500000.
	EnvCapital = "II_STRESS_CAPITAL"
	// EnvSecretSeed es la semilla de derivación de los secretos de las cuentas
	// del harness. Default "dev-stress-seed".
	EnvSecretSeed = "II_STRESS_SECRET_SEED"
	// EnvDatabaseURL es la BD del entorno de pruebas que usa el PROVISIONER
	// (admin del entorno, no gameplay). Default: II_DATABASE_URL y, en su
	// ausencia, la BD local de desarrollo.
	EnvDatabaseURL = "II_STRESS_DATABASE_URL"
	// EnvTargetMetrics son las URLs /metrics del sistema bajo prueba que el
	// harness raspa al terminar, separadas por comas. Default: derivadas del
	// host de la API en los puertos 8080 (gateway) y 8081 (engine).
	EnvTargetMetrics = "II_STRESS_TARGET_METRICS"
	// EnvLogInterval es el periodo del log estructurado de progreso.
	// Default 10s.
	EnvLogInterval = "II_STRESS_LOG_INTERVAL"
	// EnvMaxSamples es el tope de muestras de latencia por operación.
	// Default DefaultMaxSamples.
	EnvMaxSamples = "II_STRESS_MAX_SAMPLES"
	// EnvStockEndowment es la DOTACIÓN DE STOCK de cada cuenta del harness, en
	// unidades de producto (asiento production_output del provisioner, +stock_free
	// / −world_source, ADR-022). Sin ella el harness NO tiene lado vendedor: solo
	// puede publicar buy —para publicar sell o freight hay que tener la mercancía
	// en el almacén de origen, y para aceptar un buy hay que ser dueño de ese
	// almacén—, de modo que la operación de ACEPTACIÓN dependería de una oferta
	// sell ajena y finita, y se degradaría a cero justo al escalar la población.
	// 0 la desactiva (el harness vuelve a publicar solo buy). Default 10000.
	EnvStockEndowment = "II_STRESS_STOCK_ENDOWMENT"
	// EnvSellShare es la fracción de las publicaciones del harness que son sell
	// (0..1), con el resto en buy. Mantiene el tablón con contrapartes propias en
	// proporción a la población, que es lo que hace que la tasa de aceptación
	// escale con los bots. Default 0.5.
	EnvSellShare = "II_STRESS_SELL_SHARE"
)

// Defaults documentados del harness.
const (
	DefaultBots        = 200
	DefaultRamp        = 30 * time.Second
	DefaultDuration    = 120 * time.Second
	DefaultTick        = time.Second
	DefaultWriteRatio  = 0.3
	DefaultReportPath  = "stress-report.json"
	DefaultAddr        = ":8083"
	DefaultCleanup     = true
	DefaultCapital     = 500_000
	DefaultSecretSeed  = "dev-stress-seed"
	DefaultDatabaseURL = "postgres://imperio:imperio@localhost:5432/imperio?sslmode=disable"
	DefaultLogInterval = 10 * time.Second
	// DefaultStockEndowment cubre con holgura lo que un bot puede vender en una
	// corrida larga (cada sell mueve a lo sumo pubMaxQty unidades), de modo que
	// el lado vendedor nunca se agote a mitad de la medición.
	DefaultStockEndowment = 10_000
	DefaultSellShare      = 0.5
	// DefaultGatewayMetricsPort y DefaultEngineMetricsPort son los puertos de
	// /metrics del sistema bajo prueba (mismos defaults que II_HTTP_ADDR e
	// II_ENGINE_ADDR).
	DefaultGatewayMetricsPort = "8080"
	DefaultEngineMetricsPort  = "8081"
	// EnvPlatformDatabaseURL es la variable de BD compartida de la plataforma,
	// usada como respaldo de EnvDatabaseURL.
	EnvPlatformDatabaseURL = "II_DATABASE_URL"
	// maxBots acota la población de una corrida: por encima, el cuello de
	// botella es el propio harness y la medición deja de ser honesta en un solo
	// proceso (escala horizontalmente lanzando varias instancias, GDD §15.4).
	maxBots = 200_000
	// maxRunIDLen acota el identificador de corrida que entra en los nombres de
	// cuenta (auth.accounts.name admite 128 caracteres).
	maxRunIDLen = 24
)

// AccountPrefix es el prefijo RECONOCIBLE de toda cuenta creada por el harness:
// permite identificar y limpiar las cuentas de cualquier corrida de stress sin
// ambigüedad (`... WHERE name LIKE 'stress-%'`).
const AccountPrefix = "stress-"

// Options es la configuración de una corrida de stress.
type Options struct {
	// APIURL es la raíz de la API pública del entorno de pruebas.
	APIURL string
	// Env es el valor de II_ENV observado (la salvaguarda rehúsa prod).
	Env string
	// AllowHosts es la allowlist de hosts no productivos (vacía = la default).
	AllowHosts []string
	// AllowMatch es el patrón de la allowlist que autorizó el target.
	AllowMatch string
	// RunID identifica la corrida y forma el prefijo de las cuentas.
	RunID string
	// Bots es el número total de bots (1..maxBots).
	Bots int
	// Ramp es la rampa de arranque (>= 0).
	Ramp time.Duration
	// Duration es la duración de la fase de carga (> 0).
	Duration time.Duration
	// Tick es el periodo de acción por bot (> 0), jitterizado ±20%.
	Tick time.Duration
	// Mix es la mezcla de arquetipos.
	Mix Mix
	// WriteRatio es la fracción de acciones de escritura (0..1).
	WriteRatio float64
	// ReportPath es la ruta del informe JSON.
	ReportPath string
	// Addr es la dirección del servidor de métricas del harness.
	Addr string
	// Cleanup activa el retiro de las cuentas del run al terminar.
	Cleanup bool
	// Capital es la capitalización de cada cuenta nueva (> 0).
	Capital int64
	// StockEndowment es la dotación de stock de cada cuenta nueva, en unidades
	// de producto (>= 0; 0 desactiva el lado vendedor del harness).
	StockEndowment int64
	// SellShare es la fracción de publicaciones sell del harness (0..1).
	SellShare float64
	// SecretSeed es la semilla de derivación de secretos (no vacía).
	SecretSeed string
	// DatabaseURL es la BD del entorno de pruebas usada por el provisioner.
	DatabaseURL string
	// TargetMetrics son las URLs /metrics del sistema bajo prueba.
	TargetMetrics []string
	// LogInterval es el periodo del log de progreso (> 0).
	LogInterval time.Duration
	// MaxSamples es el tope de muestras de latencia por operación (> 0).
	MaxSamples int
}

// DefaultOptions devuelve la configuración por defecto SIN APIURL: el target
// nunca tiene valor por defecto (salvaguarda del GDD §13.4).
func DefaultOptions() Options {
	mix, err := ParseMix(DefaultMixSpec)
	if err != nil {
		// DefaultMixSpec es una constante válida por construcción.
		panic("stress: DefaultMixSpec inválida: " + err.Error())
	}
	return Options{
		Bots:           DefaultBots,
		Ramp:           DefaultRamp,
		Duration:       DefaultDuration,
		Tick:           DefaultTick,
		Mix:            mix,
		WriteRatio:     DefaultWriteRatio,
		ReportPath:     DefaultReportPath,
		Addr:           DefaultAddr,
		Cleanup:        DefaultCleanup,
		Capital:        DefaultCapital,
		StockEndowment: DefaultStockEndowment,
		SellShare:      DefaultSellShare,
		SecretSeed:     DefaultSecretSeed,
		DatabaseURL:    DefaultDatabaseURL,
		LogInterval:    DefaultLogInterval,
		MaxSamples:     DefaultMaxSamples,
	}
}

// OptionsFromEnv construye la configuración desde las variables II_STRESS_* y
// APLICA LA SALVAGUARDA (Validate). Cualquier valor inválido —o un target que
// huela a producción— impide el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	opts.APIURL = strings.TrimSpace(os.Getenv(EnvAPIURL))
	opts.Env = strings.TrimSpace(os.Getenv(EnvEnvironment))
	if v := strings.TrimSpace(os.Getenv(EnvAllowHosts)); v != "" {
		opts.AllowHosts = splitList(v)
	}
	var err error
	if opts.Bots, err = intFromEnv(EnvBots, opts.Bots); err != nil {
		return Options{}, err
	}
	if opts.Ramp, err = durationFromEnv(EnvRamp, opts.Ramp); err != nil {
		return Options{}, err
	}
	if opts.Duration, err = durationFromEnv(EnvDuration, opts.Duration); err != nil {
		return Options{}, err
	}
	if opts.Tick, err = durationFromEnv(EnvTick, opts.Tick); err != nil {
		return Options{}, err
	}
	if opts.LogInterval, err = durationFromEnv(EnvLogInterval, opts.LogInterval); err != nil {
		return Options{}, err
	}
	if v := strings.TrimSpace(os.Getenv(EnvMix)); v != "" {
		if opts.Mix, err = ParseMix(v); err != nil {
			return Options{}, err
		}
	}
	if v := strings.TrimSpace(os.Getenv(EnvWriteRatio)); v != "" {
		f, perr := strconv.ParseFloat(v, 64)
		if perr != nil {
			return Options{}, fmt.Errorf("stress: %s inválido %q (fracción 0..1): %w", EnvWriteRatio, v, perr)
		}
		opts.WriteRatio = f
	}
	if v := strings.TrimSpace(os.Getenv(EnvReport)); v != "" {
		opts.ReportPath = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvAddr)); v != "" {
		opts.Addr = v
	}
	if opts.Cleanup, err = boolFromEnv(EnvCleanup, opts.Cleanup); err != nil {
		return Options{}, err
	}
	if v := strings.TrimSpace(os.Getenv(EnvRunID)); v != "" {
		opts.RunID = strings.ToLower(v)
	} else {
		opts.RunID = NewRunID()
	}
	if v := strings.TrimSpace(os.Getenv(EnvCapital)); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return Options{}, fmt.Errorf("stress: %s inválido %q (entero de unidades menores): %w", EnvCapital, v, perr)
		}
		opts.Capital = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvStockEndowment)); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return Options{}, fmt.Errorf("stress: %s inválido %q (entero de unidades de producto): %w", EnvStockEndowment, v, perr)
		}
		opts.StockEndowment = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvSellShare)); v != "" {
		f, perr := strconv.ParseFloat(v, 64)
		if perr != nil {
			return Options{}, fmt.Errorf("stress: %s inválido %q (fracción 0..1): %w", EnvSellShare, v, perr)
		}
		opts.SellShare = f
	}
	if v := strings.TrimSpace(os.Getenv(EnvSecretSeed)); v != "" {
		opts.SecretSeed = v
	}
	switch {
	case strings.TrimSpace(os.Getenv(EnvDatabaseURL)) != "":
		opts.DatabaseURL = strings.TrimSpace(os.Getenv(EnvDatabaseURL))
	case strings.TrimSpace(os.Getenv(EnvPlatformDatabaseURL)) != "":
		opts.DatabaseURL = strings.TrimSpace(os.Getenv(EnvPlatformDatabaseURL))
	}
	if v := strings.TrimSpace(os.Getenv(EnvTargetMetrics)); v != "" {
		opts.TargetMetrics = splitList(v)
	}
	if opts.MaxSamples, err = intFromEnv(EnvMaxSamples, opts.MaxSamples); err != nil {
		return Options{}, err
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	if len(opts.TargetMetrics) == 0 {
		opts.TargetMetrics = DefaultTargetMetrics(opts.APIURL)
	}
	return opts, nil
}

// NewRunID genera un identificador de corrida corto y ordenable (12 hexos de un
// UUIDv7: prefijo temporal + entropía).
func NewRunID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	}
	return strings.ReplaceAll(id.String(), "-", "")[:12]
}

// RunAccountPrefix es el prefijo de TODAS las cuentas de esta corrida
// ("stress-<run_id>-"): identifica y permite limpiar el run sin ambigüedad.
func (o Options) RunAccountPrefix() string {
	return AccountPrefix + o.RunID + "-"
}

// AccountName construye el nombre de la cuenta i-ésima de un arquetipo
// ("stress-<run_id>-<arquetipo>-0001").
func (o Options) AccountName(a Archetype, index int) string {
	return fmt.Sprintf("%s%s-%04d", o.RunAccountPrefix(), a, index)
}

// Validate comprueba las invariantes de la configuración Y APLICA LA
// SALVAGUARDA sobre el target (API y BD del provisioner).
func (o *Options) Validate() error {
	matched, err := GuardTarget(o.APIURL, o.Env, o.AllowHosts)
	if err != nil {
		return err
	}
	o.AllowMatch = matched
	if _, err := GuardDatabaseURL(o.DatabaseURL, o.Env, o.AllowHosts); err != nil {
		return err
	}
	if err := validRunID(o.RunID); err != nil {
		return err
	}
	if o.Bots < 1 || o.Bots > maxBots {
		return fmt.Errorf("stress: %s debe estar entre 1 y %d (actual %d)", EnvBots, maxBots, o.Bots)
	}
	if o.Ramp < 0 {
		return fmt.Errorf("stress: %s no puede ser negativa (actual %s)", EnvRamp, o.Ramp)
	}
	if o.Duration <= 0 {
		return fmt.Errorf("stress: %s debe ser una duración positiva (actual %s)", EnvDuration, o.Duration)
	}
	if o.Tick <= 0 {
		return fmt.Errorf("stress: %s debe ser una duración positiva (actual %s)", EnvTick, o.Tick)
	}
	if o.LogInterval <= 0 {
		return fmt.Errorf("stress: %s debe ser una duración positiva (actual %s)", EnvLogInterval, o.LogInterval)
	}
	if len(o.Mix.Order) == 0 || o.Mix.TotalWeight() <= 0 {
		return fmt.Errorf("stress: %s no declara una mezcla válida (formato %q)", EnvMix, DefaultMixSpec)
	}
	if o.WriteRatio < 0 || o.WriteRatio > 1 {
		return fmt.Errorf("stress: %s debe estar entre 0 y 1 (actual %g)", EnvWriteRatio, o.WriteRatio)
	}
	if strings.TrimSpace(o.ReportPath) == "" {
		return fmt.Errorf("stress: %s no puede estar vacía", EnvReport)
	}
	if strings.TrimSpace(o.Addr) == "" {
		return fmt.Errorf("stress: %s no puede estar vacía", EnvAddr)
	}
	if o.Capital <= 0 {
		return fmt.Errorf("stress: %s debe ser > 0 (actual %d)", EnvCapital, o.Capital)
	}
	if o.StockEndowment < 0 {
		return fmt.Errorf("stress: %s no puede ser negativa (actual %d)", EnvStockEndowment, o.StockEndowment)
	}
	if o.SellShare < 0 || o.SellShare > 1 {
		return fmt.Errorf("stress: %s debe estar entre 0 y 1 (actual %g)", EnvSellShare, o.SellShare)
	}
	if strings.TrimSpace(o.SecretSeed) == "" {
		return fmt.Errorf("stress: %s no puede estar vacía", EnvSecretSeed)
	}
	if o.MaxSamples <= 0 {
		return fmt.Errorf("stress: %s debe ser > 0 (actual %d)", EnvMaxSamples, o.MaxSamples)
	}
	for _, u := range o.TargetMetrics {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("stress: %s contiene una URL inválida %q", EnvTargetMetrics, u)
		}
	}
	return nil
}

// validRunID acota el identificador de corrida a lo que puede vivir con
// seguridad dentro de un nombre de cuenta.
func validRunID(runID string) error {
	if runID == "" || len(runID) > maxRunIDLen {
		return fmt.Errorf("stress: %s debe tener entre 1 y %d caracteres (actual %q)", EnvRunID, maxRunIDLen, runID)
	}
	for _, r := range runID {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return fmt.Errorf("stress: %s solo admite [a-z0-9-] (actual %q)", EnvRunID, runID)
		}
	}
	return nil
}

// DefaultTargetMetrics deriva las URLs /metrics del sistema bajo prueba del host
// de la API: gateway en :8080 y engine en :8081 (defaults de la plataforma).
// Si no son accesibles, el informe lo registra sin fallar.
func DefaultTargetMetrics(apiURL string) []string {
	u, err := url.Parse(apiURL)
	if err != nil || u.Host == "" {
		return nil
	}
	host := u.Hostname()
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	return []string{
		u.Scheme + "://" + host + ":" + DefaultGatewayMetricsPort + "/metrics",
		u.Scheme + "://" + host + ":" + DefaultEngineMetricsPort + "/metrics",
	}
}

// splitList separa una lista separada por comas, recortando espacios y vacíos.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// intFromEnv lee un entero del entorno con default.
func intFromEnv(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("stress: %s inválido %q (entero): %w", key, v, err)
	}
	return n, nil
}

// durationFromEnv lee una duración Go del entorno con default.
func durationFromEnv(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("stress: %s inválido %q (duración Go, p. ej. 30s): %w", key, v, err)
	}
	return d, nil
}

// boolFromEnv lee un booleano del entorno con default.
func boolFromEnv(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("stress: %s inválido %q (booleano: true/false): %w", key, v, err)
	}
	return b, nil
}
