package land

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del subpaquete (prefijo II_, 12-factor).
const (
	// EnvQueryTimeout acota la duración de cada operación de BD de los
	// endpoints world/concessions*. Default 10s. Se comparte el nombre con el
	// resto del contexto world (misma semántica).
	EnvQueryTimeout = "II_WORLD_QUERY_TIMEOUT"

	// EnvTransferFeeBP es la tasa del sistema en un traspaso de concesión, en
	// puntos básicos sobre el precio (sink). Default 500 (5%).
	EnvTransferFeeBP = "II_CONCESSION_TRANSFER_FEE_BP"
)

// Defaults documentados del subpaquete.
const (
	// DefaultQueryTimeout es el timeout por defecto de las operaciones de BD.
	DefaultQueryTimeout = 10 * time.Second

	// DefaultTransferFeeBP es la tasa de traspaso por defecto (5% en bp).
	DefaultTransferFeeBP int64 = 500

	// maxBP es el 100% en puntos básicos (cota superior de la tasa).
	maxBP int64 = 10000
)

// ConcessionPeriodDays es el plazo de referencia de una concesión, en días de
// juego (GDD 11.1). El primer canon y cada renovación cubren un periodo.
const ConcessionPeriodDays int32 = 90

// CanonBaseMultiplier es el multiplicador simple del canon sobre el canon_base
// regional (decisión del incremento: multiplicador de ubicación fijo ×1;
// extensible por el Balancer). Documentado y aplicado tal cual.
const CanonBaseMultiplier int64 = 1

// Límites de paginación FIJADOS por el contrato OpenAPI (parámetro limit:
// default 50, maximum 200). No son configurables.
const (
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// Options es la configuración del subpaquete de suelo.
type Options struct {
	// QueryTimeout es la duración máxima de cada operación contra la BD (> 0).
	QueryTimeout time.Duration
	// TransferFeeBP es la tasa del sistema en un traspaso, en bp (0..10000).
	TransferFeeBP int64
}

// DefaultOptions devuelve la configuración por defecto.
func DefaultOptions() Options {
	return Options{QueryTimeout: DefaultQueryTimeout, TransferFeeBP: DefaultTransferFeeBP}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvQueryTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("world/land: %s inválido %q (duración Go, p. ej. 10s): %w", EnvQueryTimeout, v, err)
		}
		opts.QueryTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvTransferFeeBP)); v != "" {
		bp, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return Options{}, fmt.Errorf("world/land: %s inválido %q (entero de bp): %w", EnvTransferFeeBP, v, err)
		}
		opts.TransferFeeBP = bp
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.QueryTimeout <= 0 {
		return fmt.Errorf("world/land: %s debe ser > 0 (actual %s)", EnvQueryTimeout, o.QueryTimeout)
	}
	if o.TransferFeeBP < 0 || o.TransferFeeBP > maxBP {
		return fmt.Errorf("world/land: %s debe estar entre 0 y %d (actual %d)", EnvTransferFeeBP, maxBP, o.TransferFeeBP)
	}
	return nil
}
