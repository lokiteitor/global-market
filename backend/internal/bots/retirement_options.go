package bots

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del barrido de retiro (prefijo II_BOTS_RETIRE_* /
// II_BOT_RETIRE_*, 12-factor). El retiro de bots insolventes-inactivos cierra la
// cascada de insolvencia del lado de los bots (ADR-024, GDD 5.9): sin edificios
// operativos, sin contratos ni publicaciones vivas y con la caja bajo el piso.
const (
	// EnvRetireInterval es la cadencia (wall-clock) del barrido de retiro, en
	// formato time.ParseDuration. Default 60s.
	EnvRetireInterval = "II_BOTS_RETIRE_INTERVAL"

	// EnvRetireCashFloor es el piso de caja (unidades menores) por debajo del
	// cual un bot sin actividad se considera insolvente. Default 1000.
	EnvRetireCashFloor = "II_BOTS_RETIRE_CASH_FLOOR"

	// EnvRetireIdleSimSeconds es el tiempo de sim que un bot debe permanecer
	// insolvente-inactivo de forma sostenida antes de retirarse (se rastrea con
	// una marca en bot_profiles.behavior). Default 604800 (7 días-sim).
	EnvRetireIdleSimSeconds = "II_BOT_RETIRE_IDLE_SIM_SECONDS"
)

// Defaults documentados del barrido de retiro.
const (
	DefaultRetireInterval             = 60 * time.Second
	DefaultRetireCashFloor      int64 = 1000
	DefaultRetireIdleSimSeconds int64 = 604_800 // 7 días-sim
)

// RetirementOptions es la configuración del barrido de retiro de bots.
type RetirementOptions struct {
	// Interval es la cadencia (wall-clock) del barrido, jitterizada. > 0.
	Interval time.Duration
	// CashFloor es el piso de caja bajo el cual un bot inactivo es insolvente. >= 0.
	CashFloor int64
	// IdleSimSeconds es la ventana de insolvencia sostenida (sim-time) previa al
	// retiro. >= 0 (0 retira en cuanto se cumple la condición instantánea).
	IdleSimSeconds int64
}

// DefaultRetirementOptions devuelve la configuración por defecto del barrido.
func DefaultRetirementOptions() RetirementOptions {
	return RetirementOptions{
		Interval:       DefaultRetireInterval,
		CashFloor:      DefaultRetireCashFloor,
		IdleSimSeconds: DefaultRetireIdleSimSeconds,
	}
}

// RetirementOptionsFromEnv construye la configuración del barrido desde el
// entorno; un valor inválido devuelve error (la configuración rota impide el
// arranque).
func RetirementOptionsFromEnv() (RetirementOptions, error) {
	opts := DefaultRetirementOptions()
	if v := strings.TrimSpace(os.Getenv(EnvRetireInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return RetirementOptions{}, fmt.Errorf("bots: %s inválido %q (duración Go, p. ej. 60s): %w", EnvRetireInterval, v, err)
		}
		opts.Interval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvRetireCashFloor)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return RetirementOptions{}, fmt.Errorf("bots: %s inválido %q (entero de unidades menores): %w", EnvRetireCashFloor, v, err)
		}
		opts.CashFloor = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvRetireIdleSimSeconds)); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return RetirementOptions{}, fmt.Errorf("bots: %s inválido %q (segundos de sim): %w", EnvRetireIdleSimSeconds, v, err)
		}
		opts.IdleSimSeconds = n
	}
	if err := opts.Validate(); err != nil {
		return RetirementOptions{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración del barrido.
func (o RetirementOptions) Validate() error {
	if o.Interval <= 0 {
		return fmt.Errorf("bots: %s debe ser una duración positiva (actual %s)", EnvRetireInterval, o.Interval)
	}
	if o.CashFloor < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvRetireCashFloor, o.CashFloor)
	}
	if o.IdleSimSeconds < 0 {
		return fmt.Errorf("bots: %s no puede ser negativo (actual %d)", EnvRetireIdleSimSeconds, o.IdleSimSeconds)
	}
	return nil
}
