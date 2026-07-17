package buildings

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada operación de BD de los
	// endpoints world/buildings*. Default 10s (compartido con el resto del
	// contexto world).
	EnvQueryTimeout = "II_WORLD_QUERY_TIMEOUT"

	// EnvBuildSimSeconds es el tiempo fijo de construcción en sim-time desde el
	// alta (under_construction) hasta operational; lo CONSUME EL MOTOR (engine)
	// para completar la construcción emitiendo building.constructed, no estos
	// handlers. Se documenta aquí por cohesión de la configuración del contexto.
	EnvBuildSimSeconds = "II_BUILD_SIM_SECONDS"
)

// Defaults documentados del subpaquete.
const (
	// DefaultQueryTimeout es el timeout por defecto de las operaciones de BD.
	DefaultQueryTimeout = 10 * time.Second

	// DefaultBuildSimSeconds es el tiempo fijo de construcción por defecto (1h
	// de sim-time). Marca de referencia para el motor; ver EnvBuildSimSeconds.
	DefaultBuildSimSeconds int64 = 3600
)

// Límites de paginación FIJADOS por el contrato OpenAPI (limit: default 50,
// maximum 200). No son configurables.
const (
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// Options es la configuración del subpaquete de edificios.
type Options struct {
	// QueryTimeout es la duración máxima de cada operación contra la BD (> 0).
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
			return Options{}, fmt.Errorf("world/buildings: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
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
		return fmt.Errorf("world/buildings: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	return nil
}
