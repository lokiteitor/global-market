package bots

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Arquetipos gobernados por la densidad dinámica (GDD §13.2 completo). Cada
// constante DEBE coincidir con el Behavior.Name() del arquetipo homónimo:
// TestDensityArchetypeNames lo verifica, porque estas claves etiquetan las
// métricas y las plazas de ejecución del supervisor.
const (
	ArchetypeCoalProducer          = "coal_producer"
	ArchetypeIronProducer          = "iron_producer"
	ArchetypeTrader                = "trader"
	ArchetypeIndustrialTransformer = "industrial_transformer"
	ArchetypeFreighter             = "freighter"
)

// DensityArchetypes es el orden estable en que la densidad evalúa, ajusta y
// loguea los arquetipos.
var DensityArchetypes = []string{
	ArchetypeCoalProducer,
	ArchetypeIronProducer,
	ArchetypeTrader,
	ArchetypeIndustrialTransformer,
	ArchetypeFreighter,
}

// liquidityBackstop marca los arquetipos que actúan de BACKSTOP DE LIQUIDEZ
// (GDD §5.3.1): con el tablón vacío se sube su densidad para que siempre haya
// contrapartes. El transformador y el transportista no lo son: su oferta
// depende de que ya exista mercado, no lo crean.
var liquidityBackstop = map[string]bool{
	ArchetypeCoalProducer: true,
	ArchetypeIronProducer: true,
	ArchetypeTrader:       true,
}

// Variables de entorno de la densidad dinámica (prefijo II_BOTS_DENSITY_*,
// 12-factor). Es la VÁLVULA DE CARGA PRINCIPAL del GDD §19: ante saturación se
// reduce la población de bots antes que degradar la experiencia humana.
const (
	// EnvDensityEnabled activa el controlador de densidad (true/false).
	// Default true; en false la población arrancada queda fija (modo "mundo
	// vivo" puro, GDD §13.4 modo 1).
	EnvDensityEnabled = "II_BOTS_DENSITY_ENABLED"

	// EnvDensityInterval es la cadencia (wall-clock) del ciclo de ajuste, en
	// formato time.ParseDuration. Default 30s.
	EnvDensityInterval = "II_BOTS_DENSITY_INTERVAL"

	// EnvDensityMin es el suelo de población activa POR ARQUETIPO. Admite un
	// entero suelto ("1", aplicado a todos) o una lista por arquetipo
	// ("coal_producer=1,trader=2"). Default 0 (la carga puede pausarlos todos).
	EnvDensityMin = "II_BOTS_DENSITY_MIN"

	// EnvDensityMax es el techo de población activa POR ARQUETIPO, con la misma
	// sintaxis que EnvDensityMin. Default 0 = la población APROVISIONADA del
	// arquetipo (el controlador jamás arranca bots que no existan).
	EnvDensityMax = "II_BOTS_DENSITY_MAX"

	// EnvDensityBaseBP es la fracción (basis points) de la población
	// aprovisionada que forma la BASE del objetivo, es decir la densidad en un
	// mundo tranquilo y sano. Default 6000 (60%): deja margen para que la
	// actividad humana suba la densidad hasta el 100% aprovisionado.
	EnvDensityBaseBP = "II_BOTS_DENSITY_BASE_BP"

	// EnvDensityActivityGainBP es la ganancia máxima (basis points sobre la
	// base) del factor de ACTIVIDAD HUMANA. Default 10000 (+100%: con actividad
	// humana plena se permite el doble de la base, acotado por el techo).
	EnvDensityActivityGainBP = "II_BOTS_DENSITY_ACTIVITY_GAIN_BP"

	// EnvDensityActivityWindow es la ventana (wall-clock) de comandos humanos
	// recientes que cuenta como actividad. Default 15m.
	EnvDensityActivityWindow = "II_BOTS_DENSITY_ACTIVITY_WINDOW"

	// EnvDensitySessionsRef es el número de sesiones humanas activas que satura
	// el factor de actividad. Default 8.
	EnvDensitySessionsRef = "II_BOTS_DENSITY_SESSIONS_REF"

	// EnvDensityCommandsRef es el número de comandos humanos en la ventana que
	// satura el factor de actividad. Default 32.
	EnvDensityCommandsRef = "II_BOTS_DENSITY_COMMANDS_REF"

	// EnvDensityLagLow es el lag de outbox (eventos) hasta el cual el sistema se
	// considera SANO (factor de carga 1). Default 200.
	EnvDensityLagLow = "II_BOTS_DENSITY_LAG_LOW"

	// EnvDensityLagHigh es el lag de outbox a partir del cual el sistema se
	// considera SATURADO (factor de carga al suelo). Default 2000.
	EnvDensityLagHigh = "II_BOTS_DENSITY_LAG_HIGH"

	// EnvDensityQueueLow es la profundidad de la cola de transbordo
	// (cargamentos encolados sin servir) hasta la cual el sistema se considera
	// sano. Default 100.
	EnvDensityQueueLow = "II_BOTS_DENSITY_QUEUE_LOW"

	// EnvDensityQueueHigh es la profundidad de la cola de transbordo a partir de
	// la cual el sistema se considera saturado. Default 1000.
	EnvDensityQueueHigh = "II_BOTS_DENSITY_QUEUE_HIGH"

	// EnvDensityLoadFloorBP es el SUELO del factor de carga en basis points:
	// cuánto de la base sobrevive con el sistema plenamente saturado.
	// Default 2000 (20%).
	EnvDensityLoadFloorBP = "II_BOTS_DENSITY_LOAD_FLOOR_BP"

	// EnvDensityCoverageMin es el número de publicaciones vivas por debajo del
	// cual el tablón se considera escaso de contrapartes. Default 12; 0 desactiva
	// la señal de cobertura.
	EnvDensityCoverageMin = "II_BOTS_DENSITY_COVERAGE_MIN"

	// EnvDensityCoverageGainBP es la ganancia máxima (basis points) del factor
	// de cobertura sobre los arquetipos de backstop de liquidez, con el tablón
	// completamente vacío. Default 5000 (+50%).
	EnvDensityCoverageGainBP = "II_BOTS_DENSITY_COVERAGE_GAIN_BP"

	// EnvDensityMaxStep es el suavizado: máximo de bots arrancados o parados por
	// arquetipo y ciclo. Default 2.
	EnvDensityMaxStep = "II_BOTS_DENSITY_MAX_STEP"

	// EnvDensityHysteresis es la banda muerta AL ALZA (en bots): una desviación
	// positiva menor o igual no mueve la población. Las bajadas NO tienen banda
	// muerta (GDD §19: proteger al humano es inmediato). Default 1.
	EnvDensityHysteresis = "II_BOTS_DENSITY_HYSTERESIS"
)

// Defaults documentados de la densidad dinámica.
const (
	DefaultDensityEnabled              = true
	DefaultDensityInterval             = 30 * time.Second
	DefaultDensityMin            int   = 0
	DefaultDensityMax            int   = 0 // 0 = población aprovisionada
	DefaultDensityBaseBP         int64 = 6_000
	DefaultDensityActivityGainBP int64 = 10_000
	DefaultDensityActivityWindow       = 15 * time.Minute
	DefaultDensitySessionsRef    int64 = 8
	DefaultDensityCommandsRef    int64 = 32
	DefaultDensityLagLow         int64 = 200
	DefaultDensityLagHigh        int64 = 2_000
	DefaultDensityQueueLow       int64 = 100
	DefaultDensityQueueHigh      int64 = 1_000
	DefaultDensityLoadFloorBP    int64 = 2_000
	DefaultDensityCoverageMin    int64 = 12
	DefaultDensityCoverageGainBP int64 = 5_000
	DefaultDensityMaxStep        int   = 2
	DefaultDensityHysteresis     int   = 1
	densityBP                    int64 = 10_000 // 100% en basis points
)

// DensityOptions es la configuración del controlador de densidad dinámica
// (GDD §13.4 modo 2). Todos los factores son enteros en basis points: la
// densidad es una decisión auditable, sin aritmética de punto flotante.
type DensityOptions struct {
	// Enabled activa el controlador. Con false la población no se ajusta.
	Enabled bool
	// Interval es la cadencia del ciclo de ajuste (> 0).
	Interval time.Duration
	// Min es el suelo de población activa por arquetipo (>= 0). Un mapa nil
	// equivale a 0 en todos; copiar las opciones NO clona el mapa.
	Min map[string]int
	// Max es el techo de población activa por arquetipo (>= 0; 0 = la población
	// aprovisionada). Siempre se acota además a lo aprovisionado. Un mapa nil
	// equivale a 0 en todos; copiar las opciones NO clona el mapa.
	Max map[string]int
	// BaseBP es la fracción de la población aprovisionada que forma la base (> 0).
	BaseBP int64
	// ActivityGainBP es la ganancia máxima del factor de actividad (>= 0).
	ActivityGainBP int64
	// ActivityWindow es la ventana de comandos humanos recientes (> 0).
	ActivityWindow time.Duration
	// SessionsRef satura el factor de actividad por sesiones humanas (>= 0).
	SessionsRef int64
	// CommandsRef satura el factor de actividad por comandos humanos (>= 0).
	CommandsRef int64
	// LagLow / LagHigh delimitan la rampa del lag de outbox (0 <= low <= high).
	LagLow  int64
	LagHigh int64
	// QueueLow / QueueHigh delimitan la rampa de la cola de transbordo.
	QueueLow  int64
	QueueHigh int64
	// LoadFloorBP es el suelo del factor de carga (0 <= x <= 10000).
	LoadFloorBP int64
	// CoverageMin es el mínimo de publicaciones vivas deseadas (>= 0; 0 desactiva).
	CoverageMin int64
	// CoverageGainBP es la ganancia máxima del factor de cobertura (>= 0).
	CoverageGainBP int64
	// MaxStep acota el ajuste por arquetipo y ciclo (>= 1).
	MaxStep int
	// Hysteresis es la banda muerta al alza, en bots (>= 0).
	Hysteresis int
}

// DefaultDensityOptions devuelve la configuración por defecto del controlador.
func DefaultDensityOptions() DensityOptions {
	return DensityOptions{
		Enabled:        DefaultDensityEnabled,
		Interval:       DefaultDensityInterval,
		Min:            defaultArchetypeCounts(DefaultDensityMin),
		Max:            defaultArchetypeCounts(DefaultDensityMax),
		BaseBP:         DefaultDensityBaseBP,
		ActivityGainBP: DefaultDensityActivityGainBP,
		ActivityWindow: DefaultDensityActivityWindow,
		SessionsRef:    DefaultDensitySessionsRef,
		CommandsRef:    DefaultDensityCommandsRef,
		LagLow:         DefaultDensityLagLow,
		LagHigh:        DefaultDensityLagHigh,
		QueueLow:       DefaultDensityQueueLow,
		QueueHigh:      DefaultDensityQueueHigh,
		LoadFloorBP:    DefaultDensityLoadFloorBP,
		CoverageMin:    DefaultDensityCoverageMin,
		CoverageGainBP: DefaultDensityCoverageGainBP,
		MaxStep:        DefaultDensityMaxStep,
		Hysteresis:     DefaultDensityHysteresis,
	}
}

// DensityOptionsFromEnv construye la configuración desde las variables
// II_BOTS_DENSITY_*. Un valor inválido devuelve error: la configuración rota
// impide el arranque.
func DensityOptionsFromEnv() (DensityOptions, error) {
	opts := DefaultDensityOptions()
	var err error

	if v := strings.TrimSpace(os.Getenv(EnvDensityEnabled)); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			return DensityOptions{}, fmt.Errorf("bots: %s inválido %q (booleano): %w", EnvDensityEnabled, v, perr)
		}
		opts.Enabled = b
	}
	if opts.Interval, err = densityDuration(EnvDensityInterval, opts.Interval); err != nil {
		return DensityOptions{}, err
	}
	if opts.ActivityWindow, err = densityDuration(EnvDensityActivityWindow, opts.ActivityWindow); err != nil {
		return DensityOptions{}, err
	}
	if opts.Min, err = archetypeCountsFromEnv(EnvDensityMin, DefaultDensityMin); err != nil {
		return DensityOptions{}, err
	}
	if opts.Max, err = archetypeCountsFromEnv(EnvDensityMax, DefaultDensityMax); err != nil {
		return DensityOptions{}, err
	}
	for _, spec := range []struct {
		key string
		dst *int64
	}{
		{EnvDensityBaseBP, &opts.BaseBP},
		{EnvDensityActivityGainBP, &opts.ActivityGainBP},
		{EnvDensitySessionsRef, &opts.SessionsRef},
		{EnvDensityCommandsRef, &opts.CommandsRef},
		{EnvDensityLagLow, &opts.LagLow},
		{EnvDensityLagHigh, &opts.LagHigh},
		{EnvDensityQueueLow, &opts.QueueLow},
		{EnvDensityQueueHigh, &opts.QueueHigh},
		{EnvDensityLoadFloorBP, &opts.LoadFloorBP},
		{EnvDensityCoverageMin, &opts.CoverageMin},
		{EnvDensityCoverageGainBP, &opts.CoverageGainBP},
	} {
		if *spec.dst, err = int64FromEnv(spec.key, *spec.dst); err != nil {
			return DensityOptions{}, err
		}
	}
	if opts.MaxStep, err = intFromEnv(EnvDensityMaxStep, opts.MaxStep); err != nil {
		return DensityOptions{}, err
	}
	if opts.Hysteresis, err = intFromEnv(EnvDensityHysteresis, opts.Hysteresis); err != nil {
		return DensityOptions{}, err
	}
	if err := opts.Validate(); err != nil {
		return DensityOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración de densidad.
func (o DensityOptions) Validate() error {
	if o.Interval <= 0 {
		return fmt.Errorf("bots: %s debe ser una duración positiva (actual %s)", EnvDensityInterval, o.Interval)
	}
	if o.ActivityWindow <= 0 {
		return fmt.Errorf("bots: %s debe ser una duración positiva (actual %s)", EnvDensityActivityWindow, o.ActivityWindow)
	}
	if o.BaseBP <= 0 {
		return fmt.Errorf("bots: %s debe ser > 0 (actual %d)", EnvDensityBaseBP, o.BaseBP)
	}
	if o.ActivityGainBP < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvDensityActivityGainBP, o.ActivityGainBP)
	}
	if o.SessionsRef < 0 || o.CommandsRef < 0 {
		return fmt.Errorf("bots: %s y %s no pueden ser negativos (actual %d, %d)",
			EnvDensitySessionsRef, EnvDensityCommandsRef, o.SessionsRef, o.CommandsRef)
	}
	if o.LagLow < 0 || o.LagHigh < o.LagLow {
		return fmt.Errorf("bots: se exige 0 <= %s <= %s (actual %d, %d)",
			EnvDensityLagLow, EnvDensityLagHigh, o.LagLow, o.LagHigh)
	}
	if o.QueueLow < 0 || o.QueueHigh < o.QueueLow {
		return fmt.Errorf("bots: se exige 0 <= %s <= %s (actual %d, %d)",
			EnvDensityQueueLow, EnvDensityQueueHigh, o.QueueLow, o.QueueHigh)
	}
	if o.LoadFloorBP < 0 || o.LoadFloorBP > densityBP {
		return fmt.Errorf("bots: %s debe estar entre 0 y %d (actual %d)", EnvDensityLoadFloorBP, densityBP, o.LoadFloorBP)
	}
	if o.CoverageMin < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvDensityCoverageMin, o.CoverageMin)
	}
	if o.CoverageGainBP < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvDensityCoverageGainBP, o.CoverageGainBP)
	}
	if o.MaxStep < 1 {
		return fmt.Errorf("bots: %s debe ser >= 1 (actual %d)", EnvDensityMaxStep, o.MaxStep)
	}
	if o.Hysteresis < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvDensityHysteresis, o.Hysteresis)
	}
	for _, a := range DensityArchetypes {
		lo, hi := o.Min[a], o.Max[a]
		if lo < 0 {
			return fmt.Errorf("bots: %s[%s] no puede ser negativo (actual %d)", EnvDensityMin, a, lo)
		}
		if hi < 0 {
			return fmt.Errorf("bots: %s[%s] no puede ser negativo (actual %d)", EnvDensityMax, a, hi)
		}
		if hi > 0 && lo > hi {
			return fmt.Errorf("bots: %s[%s]=%d supera %s[%s]=%d", EnvDensityMin, a, lo, EnvDensityMax, a, hi)
		}
	}
	return nil
}

// bounds resuelve el suelo y el techo efectivos de un arquetipo. El techo
// nunca supera lo APROVISIONADO (el controlador pausa y reanuda bots que ya
// existen; no crea población) y el suelo nunca supera el techo.
func (o DensityOptions) bounds(archetype string, provisioned int) (int, int) {
	hi := o.Max[archetype]
	if hi <= 0 || hi > provisioned {
		hi = provisioned
	}
	lo := o.Min[archetype]
	if lo > hi {
		lo = hi
	}
	if lo < 0 {
		lo = 0
	}
	return lo, hi
}

// defaultArchetypeCounts construye el mapa por arquetipo con un valor único.
func defaultArchetypeCounts(v int) map[string]int {
	m := make(map[string]int, len(DensityArchetypes))
	for _, a := range DensityArchetypes {
		m[a] = v
	}
	return m
}

// archetypeCountsFromEnv lee una especificación por arquetipo: un entero suelto
// ("2") se aplica a TODOS los arquetipos; una lista ("coal_producer=0,trader=2")
// fija solo los indicados y el resto conserva def. Un arquetipo desconocido o un
// valor no entero devuelve error.
func archetypeCountsFromEnv(key string, def int) (map[string]int, error) {
	counts := defaultArchetypeCounts(def)
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return counts, nil
	}
	if !strings.ContainsAny(raw, "=,") {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("bots: %s inválido %q (entero o lista arquetipo=n): %w", key, raw, err)
		}
		return defaultArchetypeCounts(n), nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("bots: %s inválido %q (se espera arquetipo=n)", key, part)
		}
		name = strings.TrimSpace(name)
		if _, known := counts[name]; !known {
			return nil, fmt.Errorf("bots: %s referencia el arquetipo desconocido %q (conocidos: %s)",
				key, name, strings.Join(DensityArchetypes, ", "))
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("bots: %s inválido %q (entero): %w", key, part, err)
		}
		counts[name] = n
	}
	return counts, nil
}

// densityDuration lee una duración del entorno con default.
func densityDuration(key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("bots: %s inválido %q (duración Go, p. ej. 30s): %w", key, v, err)
	}
	return d, nil
}
