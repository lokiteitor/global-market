package land

import (
	"errors"
	"fmt"
)

// Errores tipados del subpaquete, mapeables a los códigos estables del contrato
// OpenAPI. Los handlers los traducen con errors.Is/errors.As.
var (
	// ErrConcessionNotFound: la concesión no existe (→ 404 NOT_FOUND).
	ErrConcessionNotFound = errors.New("world/land: la concesión no existe")

	// ErrNotHolder: la concesión pertenece a otra corporación
	// (→ 403 NOT_RESOURCE_OWNER).
	ErrNotHolder = errors.New("world/land: la concesión pertenece a otra corporación")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los
	// errores concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("world/land: parámetros inválidos")

	// ErrParcelOverlap: la parcela solicitada se solapa con una concesión
	// vigente (→ 409 CONCESSION_OVERLAP).
	ErrParcelOverlap = errors.New("world/land: la parcela se solapa con una concesión vigente")

	// ErrConcessionReverted: la concesión está revertida al sistema; no admite
	// renovación ni traspaso (→ 409 CONCESSION_REVERTED).
	ErrConcessionReverted = errors.New("world/land: la concesión está revertida al sistema")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/land: cursor de paginación inválido")

	// ErrOverflow: price*fee/bp desborda la capacidad de int64.
	ErrOverflow = fmt.Errorf("%w: el importe del traspaso desborda la capacidad de int64", ErrValidation)
)

// FundsError detalla un saldo insuficiente para un cargo obligatorio (canon,
// precio o tasa de traspaso): importes requerido/disponible en punto fijo
// (details {required, available} del contrato, serializados como string).
// Mapea a 422 INSUFFICIENT_FUNDS.
type FundsError struct {
	Required  int64
	Available int64
}

func (e *FundsError) Error() string {
	return fmt.Sprintf("world/land: fondos insuficientes: requerido %d, disponible %d", e.Required, e.Available)
}

// ErrInsufficientFunds es la sentinela de FundsError (→ 422 INSUFFICIENT_FUNDS).
var ErrInsufficientFunds = errors.New("world/land: fondos insuficientes")

// Unwrap hace que errors.Is(err, ErrInsufficientFunds) sea verdadero.
func (e *FundsError) Unwrap() error { return ErrInsufficientFunds }
