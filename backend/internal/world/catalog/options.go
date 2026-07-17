package catalog

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada lectura de BD de los endpoints
	// world/* de catálogo. Default 10s.
	EnvQueryTimeout = "II_WORLD_QUERY_TIMEOUT"
)

// DefaultQueryTimeout es el timeout por defecto de las lecturas de BD.
const DefaultQueryTimeout = 10 * time.Second

// Límites de paginación FIJADOS por el contrato OpenAPI (parámetro limit:
// default 50, maximum 200). No son configurables: cambiarlos rompería el
// contrato.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Options es la configuración del subpaquete de catálogos.
type Options struct {
	// QueryTimeout es la duración máxima de cada lectura contra la BD (> 0).
	QueryTimeout time.Duration
}

// DefaultOptions devuelve la configuración por defecto.
func DefaultOptions() Options {
	return Options{QueryTimeout: DefaultQueryTimeout}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/catalog: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
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
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("world/catalog: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	return nil
}
