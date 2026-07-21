package enforcement

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvMaintenanceInterval es el periodo (wall-clock) del barrido de
	// mantenimiento + canon, en formato time.ParseDuration. Default 30s.
	EnvMaintenanceInterval = "II_MAINTENANCE_INTERVAL"

	// EnvEnforcementInterval es el periodo (wall-clock) del barrido de embargo,
	// en formato time.ParseDuration. Default 15s.
	EnvEnforcementInterval = "II_ENFORCEMENT_INTERVAL"

	// EnvBatchSize acota cuántas entidades procesa cada barrido por iteración
	// (cada una en su propia transacción). Default 100.
	EnvBatchSize = "II_ENFORCEMENT_BATCH_SIZE"

	// EnvDegradePctPerSimDay es la condición (0..100) que pierde un edificio por
	// día-sim con mantenimiento impagado. Default 5.
	EnvDegradePctPerSimDay = "II_DEGRADE_PCT_PER_SIM_DAY"

	// EnvAbandonConditionPct es el umbral de condición por debajo del cual (≤) un
	// edificio degradado pasa a 'abandoned'. Default 20.
	EnvAbandonConditionPct = "II_ABANDON_CONDITION_PCT"

	// EnvSeizeGraceSimSeconds es el periodo de gracia en sim-time (semanas reales
	// → sim-time) antes del embargo, tanto para el canon impagado (concesión
	// delinquent → grace) como para el edificio abandonado. Default 1209600
	// (14 días-sim).
	EnvSeizeGraceSimSeconds = "II_SEIZE_GRACE_SIM_SECONDS"
)

// Defaults documentados del subpaquete.
const (
	DefaultMaintenanceInterval        = 30 * time.Second
	DefaultEnforcementInterval        = 15 * time.Second
	DefaultBatchSize                  = 100
	DefaultDegradePctPerSimDay  int32 = 5
	DefaultAbandonConditionPct  int32 = 20
	DefaultSeizeGraceSimSeconds int64 = 1_209_600 // 14 días-sim
)

// recoverPctPerSimDay es la condición que RECUPERA un edificio por día-sim con
// el mantenimiento al día (GDD 11.2, recuperación lenta). Fijo, no configurable.
const recoverPctPerSimDay int32 = 2

// WorkerOptions es la configuración del motor de enforcement.
type WorkerOptions struct {
	// MaintenanceInterval es el periodo entre barridos de mantenimiento + canon
	// (con jitter). > 0.
	MaintenanceInterval time.Duration
	// EnforcementInterval es el periodo entre barridos de embargo (con jitter). > 0.
	EnforcementInterval time.Duration
	// BatchSize es el máximo de entidades que cada barrido toma por iteración. > 0.
	BatchSize int
	// DegradePctPerSimDay es la condición perdida por día-sim impagado. 1..100.
	DegradePctPerSimDay int32
	// AbandonConditionPct es el umbral (≤) de abandono. 0..100.
	AbandonConditionPct int32
	// SeizeGraceSimSeconds es el periodo de gracia en sim-time antes del embargo. >= 0.
	SeizeGraceSimSeconds int64
}

// DefaultWorkerOptions devuelve la configuración por defecto del motor.
func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{
		MaintenanceInterval:  DefaultMaintenanceInterval,
		EnforcementInterval:  DefaultEnforcementInterval,
		BatchSize:            DefaultBatchSize,
		DegradePctPerSimDay:  DefaultDegradePctPerSimDay,
		AbandonConditionPct:  DefaultAbandonConditionPct,
		SeizeGraceSimSeconds: DefaultSeizeGraceSimSeconds,
	}
}

// WorkerOptionsFromEnv construye las opciones del motor desde el entorno; un
// valor inválido devuelve error (la configuración rota impide el arranque).
func WorkerOptionsFromEnv() (WorkerOptions, error) {
	opts := DefaultWorkerOptions()
	if v := strings.TrimSpace(os.Getenv(EnvMaintenanceInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (time.ParseDuration): %w", EnvMaintenanceInterval, v, err)
		}
		opts.MaintenanceInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvEnforcementInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (time.ParseDuration): %w", EnvEnforcementInterval, v, err)
		}
		opts.EnforcementInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvBatchSize)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (entero): %w", EnvBatchSize, v, err)
		}
		opts.BatchSize = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvDegradePctPerSimDay)); v != "" {
		var n int32
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (entero): %w", EnvDegradePctPerSimDay, v, err)
		}
		opts.DegradePctPerSimDay = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvAbandonConditionPct)); v != "" {
		var n int32
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (entero): %w", EnvAbandonConditionPct, v, err)
		}
		opts.AbandonConditionPct = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvSeizeGraceSimSeconds)); v != "" {
		var n int64
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
			return WorkerOptions{}, fmt.Errorf("world/enforcement: %s inválido %q (entero): %w", EnvSeizeGraceSimSeconds, v, err)
		}
		opts.SeizeGraceSimSeconds = n
	}
	if err := opts.Validate(); err != nil {
		return WorkerOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del motor.
func (o WorkerOptions) Validate() error {
	if o.MaintenanceInterval <= 0 {
		return fmt.Errorf("world/enforcement: %s debe ser una duración positiva (actual %s)", EnvMaintenanceInterval, o.MaintenanceInterval)
	}
	if o.EnforcementInterval <= 0 {
		return fmt.Errorf("world/enforcement: %s debe ser una duración positiva (actual %s)", EnvEnforcementInterval, o.EnforcementInterval)
	}
	if o.BatchSize <= 0 {
		return fmt.Errorf("world/enforcement: %s debe ser > 0 (actual %d)", EnvBatchSize, o.BatchSize)
	}
	if o.DegradePctPerSimDay < 1 || o.DegradePctPerSimDay > 100 {
		return fmt.Errorf("world/enforcement: %s debe estar en 1..100 (actual %d)", EnvDegradePctPerSimDay, o.DegradePctPerSimDay)
	}
	if o.AbandonConditionPct < 0 || o.AbandonConditionPct > 100 {
		return fmt.Errorf("world/enforcement: %s debe estar en 0..100 (actual %d)", EnvAbandonConditionPct, o.AbandonConditionPct)
	}
	if o.SeizeGraceSimSeconds < 0 {
		return fmt.Errorf("world/enforcement: %s debe ser >= 0 (actual %d)", EnvSeizeGraceSimSeconds, o.SeizeGraceSimSeconds)
	}
	return nil
}
