package ledger

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno propias del módulo (prefijo II_LEDGER_).
const (
	// EnvQueryTimeout acota la duración de cada operación de BD del módulo.
	EnvQueryTimeout = "II_LEDGER_QUERY_TIMEOUT"
)

// Valores por defecto documentados.
const (
	// DefaultQueryTimeout es el timeout por defecto de las operaciones de BD.
	DefaultQueryTimeout = 10 * time.Second
)

// Límites de paginación FIJADOS por el contrato OpenAPI (parámetro limit:
// default 50, maximum 200). No son configurables: cambiarlos rompería el
// contrato.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// Options es la configuración del módulo ledger.
type Options struct {
	// QueryTimeout es la duración máxima de cada operación contra la BD
	// (lecturas y asientos). <= 0 no es válido.
	QueryTimeout time.Duration
}

// DefaultOptions devuelve la configuración por defecto del módulo.
func DefaultOptions() Options {
	return Options{QueryTimeout: DefaultQueryTimeout}
}

// OptionsFromEnv construye las Options desde las variables II_LEDGER_* con
// sus valores por defecto. Un valor inválido devuelve error: la configuración
// rota debe impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("ledger: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		if d <= 0 {
			return Options{}, fmt.Errorf("ledger: %s inválido %q (debe ser > 0)", EnvQueryTimeout, v)
		}
		opts.QueryTimeout = d
	}
	return opts, nil
}
