package market

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del módulo (prefijo II_, 12-factor).
const (
	// EnvOhlcBucketSimSeconds es el tamaño del bucket de agregación de velas,
	// en segundos de sim-time. Lo usa el agregador para calcular
	// bucket_start_sim = floor(settled_at_sim / bucket) * bucket. Default 3600
	// (1 hora de juego), coincide con el default del parámetro bucket_sim_secs
	// del contrato OpenAPI.
	EnvOhlcBucketSimSeconds = "II_OHLC_BUCKET_SIM_SECONDS"
	// EnvQueryTimeout acota la duración de cada operación de BD del lado de
	// lectura del módulo (GET /market/ohlc).
	EnvQueryTimeout = "II_MARKET_QUERY_TIMEOUT"
)

// Valores por defecto documentados.
const (
	// DefaultOhlcBucketSimSeconds es el bucket de agregación por defecto
	// (1 hora de sim-time).
	DefaultOhlcBucketSimSeconds int64 = 3600
	// DefaultQueryTimeout es el timeout por defecto de las lecturas de BD.
	DefaultQueryTimeout = 10 * time.Second
)

// Límites de paginación FIJADOS por el contrato OpenAPI (parámetro limit:
// default 50, maximum 200). No son configurables: cambiarlos rompería el
// contrato.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Options es la configuración del módulo market.
type Options struct {
	// OhlcBucketSimSeconds es el tamaño del bucket de agregación en sim-time
	// que aplica el agregador a los eventos contract.settled (> 0).
	OhlcBucketSimSeconds int64
	// QueryTimeout es la duración máxima de cada lectura contra la BD (> 0).
	QueryTimeout time.Duration
}

// DefaultOptions devuelve la configuración por defecto del módulo.
func DefaultOptions() Options {
	return Options{
		OhlcBucketSimSeconds: DefaultOhlcBucketSimSeconds,
		QueryTimeout:         DefaultQueryTimeout,
	}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvOhlcBucketSimSeconds)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Options{}, fmt.Errorf("market: %s inválido %q (entero de 64 bits): %w", EnvOhlcBucketSimSeconds, v, err)
		}
		opts.OhlcBucketSimSeconds = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("market: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		opts.QueryTimeout = d
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.OhlcBucketSimSeconds <= 0 {
		return fmt.Errorf("market: %s debe ser > 0 (actual %d)", EnvOhlcBucketSimSeconds, o.OhlcBucketSimSeconds)
	}
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("market: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	return nil
}
