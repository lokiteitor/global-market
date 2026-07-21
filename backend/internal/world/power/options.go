package power

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Variables de entorno del subpaquete (12-factor, prefijo II_).
const (
	// EnvLineCostPerKm es el coste de construcción de una línea de transmisión
	// por kilómetro de trazado (sink, como el build_cost de un edificio).
	EnvLineCostPerKm = "II_POWER_LINE_COST_PER_KM"
)

// Defaults documentados.
const (
	// DefaultLineCostPerKm: 5.000/km — una línea corta (~2 km) cuesta como una
	// fracción de una mina; cruzar una región entera (~50 km) es una inversión
	// mayor que un alto horno.
	DefaultLineCostPerKm int64 = 5_000

	// Paginación fija del contrato (como el resto de listados world/*).
	DefaultPageLimit int32 = 50
	MaxPageLimit     int32 = 200
)

// Options es la configuración del subpaquete.
type Options struct {
	// LineCostPerKm es el coste de construcción por km de línea (>= 0).
	LineCostPerKm int64
}

// DefaultOptions devuelve la configuración por defecto documentada.
func DefaultOptions() Options {
	return Options{LineCostPerKm: DefaultLineCostPerKm}
}

// OptionsFromEnv lee la configuración del entorno; un valor inválido devuelve
// error (la configuración rota debe impedir el arranque).
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if err := readInt64(EnvLineCostPerKm, &opts.LineCostPerKm); err != nil {
		return Options{}, err
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba los invariantes de configuración.
func (o Options) Validate() error {
	if o.LineCostPerKm < 0 {
		return fmt.Errorf("world/power: %s debe ser >= 0 (actual %d)", EnvLineCostPerKm, o.LineCostPerKm)
	}
	return nil
}

func readInt64(key string, dst *int64) error {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("world/power: %s debe ser un entero (actual %q)", key, raw)
	}
	*dst = v
	return nil
}
