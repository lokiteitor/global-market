package logistics

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del bounded context (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada acceso a BD de logistics/*
	// (lectura del grafo, pathfinding, CRUD de rutas). Default 10s.
	EnvQueryTimeout = "II_LOGISTICS_QUERY_TIMEOUT"

	// EnvFuelCostPerKm es el coste base de transporte por kilómetro (dinero de
	// punto fijo) del modelo de coste APROXIMADO del route-plan optimize=cost:
	// componente de combustible/operación por km, sobre el que se aplica la tasa
	// de aduanas de la región de cada segmento. Es una estimación informativa,
	// no un movimiento de valor. Default 100.
	EnvFuelCostPerKm = "II_LOGISTICS_FUEL_COST_PER_KM"
)

// Valores por defecto documentados.
const (
	DefaultQueryTimeout        = 10 * time.Second
	DefaultFuelCostPerKm int64 = 100
)

// Límites de paginación FIJADOS por el contrato OpenAPI (parámetro limit:
// default 50, maximum 200). No son configurables: cambiarlos rompería el
// contrato.
const (
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// Options es la configuración del bounded context logistics.
type Options struct {
	// QueryTimeout es la duración máxima de cada acceso a la BD (> 0).
	QueryTimeout time.Duration
	// FuelCostPerKm es el coste base de transporte por km del modelo de coste
	// aproximado (optimize=cost), en dinero de punto fijo (>= 0).
	FuelCostPerKm int64
}

// DefaultOptions devuelve la configuración por defecto del bounded context.
func DefaultOptions() Options {
	return Options{
		QueryTimeout:  DefaultQueryTimeout,
		FuelCostPerKm: DefaultFuelCostPerKm,
	}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("logistics: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		opts.QueryTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvFuelCostPerKm)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Options{}, fmt.Errorf("logistics: %s inválido %q (entero de punto fijo): %w", EnvFuelCostPerKm, v, err)
		}
		opts.FuelCostPerKm = n
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("logistics: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	if o.FuelCostPerKm < 0 {
		return fmt.Errorf("logistics: %s debe ser >= 0 (actual %d)", EnvFuelCostPerKm, o.FuelCostPerKm)
	}
	return nil
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case int32(limit) > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}
