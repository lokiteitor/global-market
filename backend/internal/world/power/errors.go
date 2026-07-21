package power

import (
	"errors"
	"fmt"
)

// Errores tipados del subpaquete, mapeables a los códigos estables del contrato
// OpenAPI (los handlers los traducen con errors.Is/errors.As).
var (
	// ErrNotFound: la línea o el edificio no existen (→ 404 NOT_FOUND).
	ErrNotFound = errors.New("world/power: el recurso no existe")

	// ErrForbidden: el edificio pertenece a otra corporación (→ 403
	// NOT_RESOURCE_OWNER).
	ErrForbidden = errors.New("world/power: el recurso pertenece a otra corporación")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR).
	ErrValidation = errors.New("world/power: parámetros inválidos")

	// ErrPlacementInvalid: el trazado no cumple los requisitos de emplazamiento
	// (→ 422 PLACEMENT_INVALID). Con errors.As se recupera el PlacementError.
	ErrPlacementInvalid = errors.New("world/power: emplazamiento inválido")

	// ErrInsufficientFunds: la caja no cubre el coste (→ 422 INSUFFICIENT_FUNDS).
	// Con errors.As se recupera el FundsError con {required, available}.
	ErrInsufficientFunds = errors.New("world/power: fondos insuficientes")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/power: cursor de paginación inválido")
)

// PlacementError detalla un requisito de emplazamiento incumplido del trazado.
type PlacementError struct {
	Rule    string
	Message string
	Details map[string]any
}

func (e *PlacementError) Error() string {
	return fmt.Sprintf("world/power: emplazamiento inválido (%s): %s", e.Rule, e.Message)
}

func (e *PlacementError) Unwrap() error { return ErrPlacementInvalid }

// FundsError detalla fondos insuficientes con los importes del contrato.
type FundsError struct {
	Required  int64
	Available int64
}

func (e *FundsError) Error() string {
	return fmt.Sprintf("world/power: fondos insuficientes (necesarios %d, disponibles %d)", e.Required, e.Available)
}

func (e *FundsError) Unwrap() error { return ErrInsufficientFunds }
