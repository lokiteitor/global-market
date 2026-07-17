package contracts

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Variables de entorno del módulo (prefijo II_, 12-factor). Las ventanas de
// sorteo/micro-ventana y el cooldown anti-parpadeo son las únicas mecánicas
// del dominio en TIEMPO REAL (ADR-011) y se evalúan siempre contra now() de
// la BD; el TTL de publicación es sim-time puro.
const (
	// EnvDrawWindowSeconds es la duración de la ventana de sorteo inicial,
	// en segundos reales (GDD 5.3.1: 30-60 s). Default 45.
	EnvDrawWindowSeconds = "II_DRAW_WINDOW_SECONDS"
	// EnvMicroWindowSeconds es la duración de la micro-ventana abierta por
	// una aceptación sobre una publicación madura (15-30 s). Default 20.
	EnvMicroWindowSeconds = "II_MICRO_WINDOW_SECONDS"
	// EnvCancelCooldownSeconds es el cooldown anti-parpadeo tras publicar,
	// en segundos reales. Default 10.
	EnvCancelCooldownSeconds = "II_CANCEL_COOLDOWN_SECONDS"
	// EnvPublicationTTLSimSeconds es el plazo de vida de una publicación
	// abierta, en sim-time desde published_at_sim; al vencer sin agotarse
	// pasa a expired y su garantía restante se libera. Default 604800
	// (7 días de juego).
	EnvPublicationTTLSimSeconds = "II_PUBLICATION_TTL_SIM_SECONDS"
	// EnvCompensationBP es la parte de la garantía del vendedor que compensa
	// al comprador en un fallo de entrega, en puntos básicos (el resto va al
	// sink del banco central). Default 5000 (50/50).
	EnvCompensationBP = "II_COMPENSATION_BP"
)

// Valores por defecto documentados.
const (
	DefaultDrawWindowSeconds        int64 = 45
	DefaultMicroWindowSeconds       int64 = 20
	DefaultCancelCooldownSeconds    int64 = 10
	DefaultPublicationTTLSimSeconds int64 = 604_800
	DefaultCompensationBP                 = 5000
)

// Límites de paginación FIJADOS por el contrato OpenAPI (limit: default 50,
// maximum 200). No son configurables.
const (
	DefaultPageLimit = 50
	MaxPageLimit     = 200
)

// guaranteePercent es la garantía monetaria del vendedor: 10% fijo del valor
// del contrato, sin reputación (GDD 5.3 v1.2, decisión #27). Debe coincidir
// con las fórmulas de ledger.confirm_contract/settle_contract_prorata
// ((valor*10)/100).
const guaranteePercent = 10

// Options es la configuración del módulo contracts.
type Options struct {
	// DrawWindowSeconds es la ventana de sorteo inicial (segundos reales, > 0).
	DrawWindowSeconds int64
	// MicroWindowSeconds es la micro-ventana (segundos reales, > 0).
	MicroWindowSeconds int64
	// CancelCooldownSeconds es el cooldown anti-parpadeo (segundos reales,
	// >= 0; 0 lo desactiva).
	CancelCooldownSeconds int64
	// PublicationTTLSimSeconds es el TTL de publicación abierta (sim-time, > 0).
	PublicationTTLSimSeconds int64
	// CompensationBP es el reparto compensación/sink de la garantía en fallo
	// (0..10000 puntos básicos).
	CompensationBP int
}

// DefaultOptions devuelve la configuración por defecto del módulo.
func DefaultOptions() Options {
	return Options{
		DrawWindowSeconds:        DefaultDrawWindowSeconds,
		MicroWindowSeconds:       DefaultMicroWindowSeconds,
		CancelCooldownSeconds:    DefaultCancelCooldownSeconds,
		PublicationTTLSimSeconds: DefaultPublicationTTLSimSeconds,
		CompensationBP:           DefaultCompensationBP,
	}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if err := readInt64(EnvDrawWindowSeconds, &opts.DrawWindowSeconds); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvMicroWindowSeconds, &opts.MicroWindowSeconds); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvCancelCooldownSeconds, &opts.CancelCooldownSeconds); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvPublicationTTLSimSeconds, &opts.PublicationTTLSimSeconds); err != nil {
		return Options{}, err
	}
	if v := strings.TrimSpace(os.Getenv(EnvCompensationBP)); v != "" {
		bp, err := strconv.Atoi(v)
		if err != nil {
			return Options{}, fmt.Errorf("contracts: %s inválido %q (entero 0..10000): %w", EnvCompensationBP, v, err)
		}
		opts.CompensationBP = bp
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.DrawWindowSeconds <= 0 {
		return fmt.Errorf("contracts: %s debe ser > 0 (actual %d)", EnvDrawWindowSeconds, o.DrawWindowSeconds)
	}
	if o.MicroWindowSeconds <= 0 {
		return fmt.Errorf("contracts: %s debe ser > 0 (actual %d)", EnvMicroWindowSeconds, o.MicroWindowSeconds)
	}
	if o.CancelCooldownSeconds < 0 {
		return fmt.Errorf("contracts: %s debe ser >= 0 (actual %d)", EnvCancelCooldownSeconds, o.CancelCooldownSeconds)
	}
	if o.PublicationTTLSimSeconds <= 0 {
		return fmt.Errorf("contracts: %s debe ser > 0 (actual %d)", EnvPublicationTTLSimSeconds, o.PublicationTTLSimSeconds)
	}
	if o.CompensationBP < 0 || o.CompensationBP > 10000 {
		return fmt.Errorf("contracts: %s debe estar en 0..10000 (actual %d)", EnvCompensationBP, o.CompensationBP)
	}
	return nil
}

// readInt64 lee una variable entera del entorno sobre dst; ausente o en
// blanco conserva el default.
func readInt64(key string, dst *int64) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("contracts: %s inválido %q (entero de 64 bits): %w", key, v, err)
	}
	*dst = n
	return nil
}
