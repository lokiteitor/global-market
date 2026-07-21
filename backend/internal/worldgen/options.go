package worldgen

// Configuración de la generación procedural (12-factor, prefijo II_). Los
// defaults reproducen el mundo canónico de desarrollo: semilla 42, grilla 3×3
// centrada en Askadia (0,0), regiones de 50 km de lado.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
)

// Nombres de las variables de entorno propias del generador.
const (
	EnvWorldSeed       = "II_WORLD_SEED"
	EnvWorldGrid       = "II_WORLD_GRID"
	EnvWorldRegionSize = "II_WORLD_REGION_SIZE_M"
)

// Valores por defecto documentados (mandato del Incremento 7).
const (
	DefaultSeed        int64 = 42
	DefaultGrid              = 3
	DefaultRegionSizeM int64 = 50_000
)

// Options es la configuración tipada y validada del generador.
type Options struct {
	// Seed es la semilla del mundo (II_WORLD_SEED). Misma semilla ⇒ mismo mundo.
	Seed int64
	// Grid es el lado de la grilla de macro-regiones, impar y ≥ 1, centrada en
	// (0,0) (II_WORLD_GRID). Un valor 3 genera las 8 regiones que rodean a Askadia.
	Grid int
	// RegionSizeM es el lado en metros de cada región cuadrada (II_WORLD_REGION_SIZE_M).
	RegionSizeM int64
	// Ledger es la configuración del módulo ledger (para prefondear las ciudades).
	Ledger ledger.Options
}

// DefaultOptions devuelve la configuración canónica de desarrollo.
func DefaultOptions() Options {
	return Options{
		Seed:        DefaultSeed,
		Grid:        DefaultGrid,
		RegionSizeM: DefaultRegionSizeM,
		Ledger:      ledger.DefaultOptions(),
	}
}

// OptionsFromEnv construye las Options desde el entorno con los defaults
// documentados, incluida la configuración del módulo ledger (II_LEDGER_*).
func OptionsFromEnv() (Options, error) {
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	opts := Options{
		Seed:        DefaultSeed,
		Grid:        DefaultGrid,
		RegionSizeM: DefaultRegionSizeM,
		Ledger:      ledgerOpts,
	}
	if v := strings.TrimSpace(os.Getenv(EnvWorldSeed)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Options{}, fmt.Errorf("worldgen: %s inválida %q: %w", EnvWorldSeed, v, err)
		}
		opts.Seed = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvWorldGrid)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Options{}, fmt.Errorf("worldgen: %s inválida %q: %w", EnvWorldGrid, v, err)
		}
		opts.Grid = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvWorldRegionSize)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Options{}, fmt.Errorf("worldgen: %s inválida %q: %w", EnvWorldRegionSize, v, err)
		}
		opts.RegionSizeM = n
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración: la grilla debe ser
// impar y positiva (para quedar centrada en (0,0)) y el lado de región positivo.
func (o Options) Validate() error {
	if o.Grid < 1 {
		return fmt.Errorf("worldgen: %s debe ser >= 1 (es %d)", EnvWorldGrid, o.Grid)
	}
	if o.Grid%2 == 0 {
		return fmt.Errorf("worldgen: %s debe ser impar para centrar la grilla en (0,0) (es %d)", EnvWorldGrid, o.Grid)
	}
	if o.RegionSizeM <= 0 {
		return fmt.Errorf("worldgen: %s debe ser > 0 (es %d)", EnvWorldRegionSize, o.RegionSizeM)
	}
	return nil
}

// half devuelve el semilado de la grilla: los índices recorren [-half, half].
func (o Options) half() int {
	return (o.Grid - 1) / 2
}
