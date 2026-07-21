package fleet

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada operación de BD de los handlers
	// world/vehicles|shipments. Default 10s (compartido con el resto de world).
	EnvQueryTimeout = "II_WORLD_QUERY_TIMEOUT"

	// EnvMaintenanceSimSeconds es la duración en sim-time de un mantenimiento
	// programado (in_maintenance → idle). Default 600 sim.
	EnvMaintenanceSimSeconds = "II_MAINTENANCE_SIM_SECONDS"

	// EnvSlotValiditySim es la vigencia en sim-time de un slot de prioridad de
	// terminal comprado (GDD 7.3). Default 2592000 (30 días de sim-time).
	EnvSlotValiditySim = "II_SLOT_VALIDITY_SIM"

	// EnvSweepInterval es el periodo del barrido del motor de tránsito, en
	// formato time.ParseDuration. Default 1s wall-clock.
	EnvSweepInterval = "II_TRANSIT_SWEEP_INTERVAL"

	// EnvSweepBatchSize acota cuántos vehículos procesa cada barrido por
	// iteración (cada uno en su propia transacción). Default 100.
	EnvSweepBatchSize = "II_TRANSIT_SWEEP_BATCH_SIZE"

	// EnvRepairSimSeconds es el tiempo de reparación de una avería en sim-time
	// (broken → in_transit re-entrando al mismo segmento). Default 1800 sim.
	EnvRepairSimSeconds = "II_REPAIR_SIM_SECONDS"

	// EnvCongestionInterval es el periodo del job de congestión, en formato
	// time.ParseDuration. Default 30s wall-clock.
	EnvCongestionInterval = "II_CONGESTION_INTERVAL"

	// EnvCongestionCapacityRef es la capacidad de referencia de vehículos por
	// segmento para normalizar la carga de congestión. Default 5.
	EnvCongestionCapacityRef = "II_CONGESTION_CAPACITY_REF"

	// EnvWearPerSegment es el desgaste (wear_pct) que suma cada segmento
	// recorrido. Default 1.
	EnvWearPerSegment = "II_WEAR_PER_SEGMENT"
)

// Defaults documentados del subpaquete.
const (
	DefaultQueryTimeout                = 10 * time.Second
	DefaultMaintenanceSimSeconds int64 = 600
	DefaultSlotValiditySim       int64 = 2_592_000
	DefaultSweepInterval               = time.Second
	DefaultSweepBatchSize              = 100
	DefaultRepairSimSeconds      int64 = 1800
	DefaultCongestionInterval          = 30 * time.Second
	DefaultCongestionCapacityRef       = 5.0
	DefaultWearPerSegment        int32 = 1
)

// Límites de paginación FIJADOS por el contrato OpenAPI (limit: default 50,
// maximum 200). No son configurables.
const (
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// ─── Options del servicio (handlers) ──────────────────────────────────────────

// Options es la configuración de la capa de servicio (handlers).
type Options struct {
	// QueryTimeout es la duración máxima de cada operación contra la BD (> 0).
	QueryTimeout time.Duration
	// MaintenanceSimSeconds es la duración de un mantenimiento programado (>= 0).
	MaintenanceSimSeconds int64
	// SlotValiditySim es la vigencia en sim-time de un slot de prioridad comprado (> 0).
	SlotValiditySim int64
}

// DefaultOptions devuelve la configuración por defecto del servicio.
func DefaultOptions() Options {
	return Options{
		QueryTimeout:          DefaultQueryTimeout,
		MaintenanceSimSeconds: DefaultMaintenanceSimSeconds,
		SlotValiditySim:       DefaultSlotValiditySim,
	}
}

// OptionsFromEnv construye las Options del servicio desde las variables II_*.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/fleet: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		opts.QueryTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvMaintenanceSimSeconds)); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/fleet: %s inválido %q (entero): %w", EnvMaintenanceSimSeconds, v, err)
		}
		opts.MaintenanceSimSeconds = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvSlotValiditySim)); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/fleet: %s inválido %q (entero): %w", EnvSlotValiditySim, v, err)
		}
		opts.SlotValiditySim = n
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del servicio.
func (o Options) Validate() error {
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	if o.MaintenanceSimSeconds < 0 {
		return fmt.Errorf("world/fleet: %s debe ser >= 0 (actual %d)", EnvMaintenanceSimSeconds, o.MaintenanceSimSeconds)
	}
	if o.SlotValiditySim <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser > 0 (actual %d)", EnvSlotValiditySim, o.SlotValiditySim)
	}
	return nil
}

// ─── WorkerOptions del motor de tránsito ──────────────────────────────────────

// WorkerOptions es la configuración del motor de tránsito y del job de
// congestión. Se separa de Options porque sus periodos son wall-clock, no
// invariantes de dominio.
type WorkerOptions struct {
	// SweepInterval es el periodo entre barridos de segmentos vencidos (> 0).
	SweepInterval time.Duration
	// BatchSize es el máximo de vehículos que cada barrido toma por iteración (> 0).
	BatchSize int
	// RepairSimSeconds es el tiempo de reparación de una avería en sim-time (>= 0).
	RepairSimSeconds int64
	// CongestionInterval es el periodo del job de congestión (> 0).
	CongestionInterval time.Duration
	// CongestionCapacityRef es la capacidad de referencia por segmento (> 0).
	CongestionCapacityRef float64
	// WearPerSegment es el desgaste que suma cada segmento recorrido (>= 0).
	WearPerSegment int32
	// Roll produce un número uniforme en [0,1) para la probabilidad de avería. No
	// se carga del entorno: nil deja el default basado en crypto/rand; los tests
	// lo inyectan para forzar/evitar averías de forma determinista.
	Roll func() float64
}

// DefaultWorkerOptions devuelve la configuración por defecto del motor.
func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{
		SweepInterval:         DefaultSweepInterval,
		BatchSize:             DefaultSweepBatchSize,
		RepairSimSeconds:      DefaultRepairSimSeconds,
		CongestionInterval:    DefaultCongestionInterval,
		CongestionCapacityRef: DefaultCongestionCapacityRef,
		WearPerSegment:        DefaultWearPerSegment,
	}
}

// WorkerOptionsFromEnv construye las opciones del motor desde el entorno; un
// valor inválido devuelve error (la configuración rota impide el arranque).
func WorkerOptionsFromEnv() (WorkerOptions, error) {
	opts := DefaultWorkerOptions()
	if v := strings.TrimSpace(os.Getenv(EnvSweepInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (time.ParseDuration): %w", EnvSweepInterval, v, err)
		}
		opts.SweepInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvSweepBatchSize)); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (entero): %w", EnvSweepBatchSize, v, err)
		}
		opts.BatchSize = int(n)
	}
	if v := strings.TrimSpace(os.Getenv(EnvRepairSimSeconds)); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (entero): %w", EnvRepairSimSeconds, v, err)
		}
		opts.RepairSimSeconds = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvCongestionInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (time.ParseDuration): %w", EnvCongestionInterval, v, err)
		}
		opts.CongestionInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvCongestionCapacityRef)); v != "" {
		f, err := parseFloat(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (número): %w", EnvCongestionCapacityRef, v, err)
		}
		opts.CongestionCapacityRef = f
	}
	if v := strings.TrimSpace(os.Getenv(EnvWearPerSegment)); v != "" {
		n, err := parseInt64(v)
		if err != nil {
			return WorkerOptions{}, fmt.Errorf("world/fleet: %s inválido %q (entero): %w", EnvWearPerSegment, v, err)
		}
		opts.WearPerSegment = int32(n)
	}
	if err := opts.Validate(); err != nil {
		return WorkerOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del motor.
func (o WorkerOptions) Validate() error {
	if o.SweepInterval <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser una duración positiva (actual %s)", EnvSweepInterval, o.SweepInterval)
	}
	if o.BatchSize <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser > 0 (actual %d)", EnvSweepBatchSize, o.BatchSize)
	}
	if o.RepairSimSeconds < 0 {
		return fmt.Errorf("world/fleet: %s debe ser >= 0 (actual %d)", EnvRepairSimSeconds, o.RepairSimSeconds)
	}
	if o.CongestionInterval <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser una duración positiva (actual %s)", EnvCongestionInterval, o.CongestionInterval)
	}
	if o.CongestionCapacityRef <= 0 {
		return fmt.Errorf("world/fleet: %s debe ser > 0 (actual %g)", EnvCongestionCapacityRef, o.CongestionCapacityRef)
	}
	if o.WearPerSegment < 0 {
		return fmt.Errorf("world/fleet: %s debe ser >= 0 (actual %d)", EnvWearPerSegment, o.WearPerSegment)
	}
	return nil
}

func parseInt64(v string) (int64, error) {
	var n int64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return 0, err
	}
	return n, nil
}

func parseFloat(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscanf(v, "%g", &f); err != nil {
		return 0, err
	}
	return f, nil
}
