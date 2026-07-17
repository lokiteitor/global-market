package clock

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del módulo (prefijo II_, 12-factor como el resto de la
// plataforma; este módulo no toca internal/platform/config).
const (
	// EnvPersistInterval es el intervalo de re-anclaje del Clock en la BD.
	EnvPersistInterval = "II_CLOCK_PERSIST_INTERVAL"
	// EnvRefreshInterval es el intervalo de relectura del ancla por el Clock
	// (detecta cambios de otro proceso, p. ej. frozen o ratio).
	EnvRefreshInterval = "II_CLOCK_REFRESH_INTERVAL"
	// EnvReaderCacheTTL es el TTL de la caché del Reader.
	EnvReaderCacheTTL = "II_SIMCLOCK_CACHE_TTL"
)

// Valores por defecto documentados de las variables de entorno.
const (
	DefaultPersistInterval = 60 * time.Second
	DefaultRefreshInterval = 5 * time.Second
	DefaultReaderCacheTTL  = 5 * time.Second
)

// Options es la configuración del Clock del motor.
type Options struct {
	// PersistInterval es cada cuánto se re-persiste el ancla derivada
	// (II_CLOCK_PERSIST_INTERVAL, por defecto 60s).
	PersistInterval time.Duration
	// RefreshInterval es cada cuánto se relee el ancla de la BD
	// (II_CLOCK_REFRESH_INTERVAL, por defecto 5s).
	RefreshInterval time.Duration
}

// OptionsFromEnv construye las opciones del Clock desde el entorno y las
// valida: ambos intervalos deben ser duraciones positivas en el formato de
// time.ParseDuration (p. ej. "60s", "1m30s").
func OptionsFromEnv() (Options, error) {
	persist, err := durationFromEnv(EnvPersistInterval, DefaultPersistInterval)
	if err != nil {
		return Options{}, err
	}
	refresh, err := durationFromEnv(EnvRefreshInterval, DefaultRefreshInterval)
	if err != nil {
		return Options{}, err
	}
	opts := Options{PersistInterval: persist, RefreshInterval: refresh}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de las opciones del Clock.
func (o Options) Validate() error {
	if o.PersistInterval <= 0 {
		return fmt.Errorf("clock: %s debe ser una duración positiva, obtenido %s",
			EnvPersistInterval, o.PersistInterval)
	}
	if o.RefreshInterval <= 0 {
		return fmt.Errorf("clock: %s debe ser una duración positiva, obtenido %s",
			EnvRefreshInterval, o.RefreshInterval)
	}
	return nil
}

// ReaderOptions es la configuración del Reader ligero.
type ReaderOptions struct {
	// CacheTTL es la vigencia del ancla cacheada (II_SIMCLOCK_CACHE_TTL,
	// por defecto 5s). Cero fuerza una relectura en cada consulta.
	CacheTTL time.Duration
}

// ReaderOptionsFromEnv construye las opciones del Reader desde el entorno.
func ReaderOptionsFromEnv() (ReaderOptions, error) {
	ttl, err := durationFromEnv(EnvReaderCacheTTL, DefaultReaderCacheTTL)
	if err != nil {
		return ReaderOptions{}, err
	}
	if ttl < 0 {
		return ReaderOptions{}, fmt.Errorf("clock: %s no puede ser negativa, obtenido %s",
			EnvReaderCacheTTL, ttl)
	}
	return ReaderOptions{CacheTTL: ttl}, nil
}

// durationFromEnv lee una duración del entorno con valor por defecto si la
// variable está ausente o en blanco.
func durationFromEnv(name string, def time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("clock: %s inválida %q (formato de time.ParseDuration, p. ej. \"60s\"): %w",
			name, raw, err)
	}
	return d, nil
}
