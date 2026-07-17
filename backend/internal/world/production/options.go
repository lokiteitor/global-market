package production

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada operación de BD de los handlers
	// world/*production-batches. Default 10s (compartido con el resto del
	// contexto world).
	EnvQueryTimeout = "II_WORLD_QUERY_TIMEOUT"

	// EnvSweepInterval es el periodo del barrido del motor (construcción +
	// producción), en formato time.ParseDuration. Default 2s wall-clock.
	EnvSweepInterval = "II_PRODUCTION_SWEEP_INTERVAL"

	// EnvSweepBatchSize acota cuántos edificios/lotes procesa cada barrido por
	// iteración (cada uno en su propia transacción). Default 100.
	EnvSweepBatchSize = "II_PRODUCTION_SWEEP_BATCH_SIZE"

	// EnvBuildSimSeconds es el tiempo fijo de construcción en sim-time desde el
	// alta (under_construction) hasta operational. Default 3600 sim.
	EnvBuildSimSeconds = "II_BUILD_SIM_SECONDS"

	// EnvReconcileInterval es el periodo del job de reconciliación
	// física↔contable (ADR-004), en formato time.ParseDuration. Default 300s.
	EnvReconcileInterval = "II_RECONCILE_INTERVAL"
)

// Defaults documentados del subpaquete.
const (
	// DefaultQueryTimeout es el timeout por defecto de las operaciones de BD.
	DefaultQueryTimeout = 10 * time.Second

	// DefaultSweepInterval es el periodo por defecto del barrido del motor.
	DefaultSweepInterval = 2 * time.Second

	// DefaultSweepBatchSize es el tamaño de lote por defecto del barrido.
	DefaultSweepBatchSize = 100

	// DefaultBuildSimSeconds es el tiempo fijo de construcción por defecto (1h
	// de sim-time).
	DefaultBuildSimSeconds int64 = 3600

	// DefaultReconcileInterval es el periodo por defecto de la reconciliación.
	DefaultReconcileInterval = 300 * time.Second

	// DefaultExtractionRadiusM es el radio de influencia por defecto para
	// localizar el yacimiento de una mina cuando su tipo no declara
	// placement_rules.max_distance_m (metros de mundo, SRID 0).
	DefaultExtractionRadiusM float64 = 5000
)

// Límites de paginación FIJADOS por el contrato OpenAPI (limit: default 50,
// maximum 200). No son configurables.
const (
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// Options es la configuración de la capa de servicio (handlers).
type Options struct {
	// QueryTimeout es la duración máxima de cada operación contra la BD (> 0).
	QueryTimeout time.Duration
}

// DefaultOptions devuelve la configuración por defecto del servicio.
func DefaultOptions() Options { return Options{QueryTimeout: DefaultQueryTimeout} }

// OptionsFromEnv construye las Options del servicio desde las variables II_*.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/production: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		opts.QueryTimeout = d
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del servicio.
func (o Options) Validate() error {
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("world/production: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	return nil
}

// WorkerOptions es la configuración del motor (barridos + reconciliación). Se
// separa de Options porque sus periodos son wall-clock, no invariantes de
// dominio.
type WorkerOptions struct {
	// SweepInterval es el periodo entre barridos de construcción/producción (con
	// jitter). > 0.
	SweepInterval time.Duration
	// BatchSize es el máximo de elementos que cada barrido toma por iteración. > 0.
	BatchSize int
	// BuildSimSeconds es el tiempo fijo de construcción en sim-time. >= 0.
	BuildSimSeconds int64
	// ReconcileInterval es el periodo del job de reconciliación. > 0.
	ReconcileInterval time.Duration
}

// DefaultWorkerOptions devuelve la configuración por defecto del motor.
func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{
		SweepInterval:     DefaultSweepInterval,
		BatchSize:         DefaultSweepBatchSize,
		BuildSimSeconds:   DefaultBuildSimSeconds,
		ReconcileInterval: DefaultReconcileInterval,
	}
}

// WorkerOptionsFromEnv construye las opciones del motor desde el entorno; un
// valor inválido devuelve error (la configuración rota impide el arranque).
func WorkerOptionsFromEnv() (WorkerOptions, error) {
	opts := DefaultWorkerOptions()
	if v := strings.TrimSpace(os.Getenv(EnvSweepInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/production: %s inválido %q (time.ParseDuration): %w", EnvSweepInterval, v, err)
		}
		opts.SweepInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvSweepBatchSize)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/production: %s inválido %q (entero): %w", EnvSweepBatchSize, v, err)
		}
		opts.BatchSize = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvBuildSimSeconds)); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/production: %s inválido %q (entero): %w", EnvBuildSimSeconds, v, err)
		}
		opts.BuildSimSeconds = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvReconcileInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/production: %s inválido %q (time.ParseDuration): %w", EnvReconcileInterval, v, err)
		}
		opts.ReconcileInterval = d
	}
	if err := opts.Validate(); err != nil {
		return WorkerOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del motor.
func (o WorkerOptions) Validate() error {
	if o.SweepInterval <= 0 {
		return fmt.Errorf("world/production: %s debe ser una duración positiva (actual %s)", EnvSweepInterval, o.SweepInterval)
	}
	if o.BatchSize <= 0 {
		return fmt.Errorf("world/production: %s debe ser > 0 (actual %d)", EnvSweepBatchSize, o.BatchSize)
	}
	if o.BuildSimSeconds < 0 {
		return fmt.Errorf("world/production: %s debe ser >= 0 (actual %d)", EnvBuildSimSeconds, o.BuildSimSeconds)
	}
	if o.ReconcileInterval <= 0 {
		return fmt.Errorf("world/production: %s debe ser una duración positiva (actual %s)", EnvReconcileInterval, o.ReconcileInterval)
	}
	return nil
}
