package buildings

import (
	"errors"
	"fmt"
)

// Errores tipados del subpaquete, mapeables a los códigos estables del contrato
// OpenAPI. Los handlers los traducen con errors.Is/errors.As.
var (
	// ErrBuildingNotFound: el edificio no existe (→ 404 NOT_FOUND).
	ErrBuildingNotFound = errors.New("world/buildings: el edificio no existe")

	// ErrForbidden: el edificio o la concesión pertenecen a otra corporación
	// (autorización por propiedad, → 403 NOT_RESOURCE_OWNER).
	ErrForbidden = errors.New("world/buildings: el recurso pertenece a otra corporación")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los errores
	// concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("world/buildings: parámetros inválidos")

	// ErrMaxLevelReached: el edificio ya está en su nivel máximo
	// (→ 409 MAX_LEVEL_REACHED).
	ErrMaxLevelReached = errors.New("world/buildings: el edificio ya está en su nivel máximo")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/buildings: cursor de paginación inválido")

	// ErrPlacementInvalid: los requisitos de emplazamiento no se cumplen
	// (→ 422 PLACEMENT_INVALID). Con errors.As se recupera el PlacementError.
	ErrPlacementInvalid = errors.New("world/buildings: emplazamiento inválido")

	// ErrInsufficientFunds: la caja no cubre el coste (→ 422 INSUFFICIENT_FUNDS).
	// Con errors.As se recupera el FundsError con {required, available}.
	ErrInsufficientFunds = errors.New("world/buildings: fondos insuficientes")

	// ErrOverflow: el coste de mejora (build_cost * factor) desborda int64.
	ErrOverflow = fmt.Errorf("%w: el coste de mejora desborda la capacidad de int64", ErrValidation)
)

// PlacementError detalla una regla de emplazamiento incumplida: la regla y un
// contexto estructurado (details del contrato). Mapea a 422 PLACEMENT_INVALID.
type PlacementError struct {
	Rule    string         // p. ej. "footprint_within_parcel", "near_resource"
	Message string         // descripción legible
	Details map[string]any // contexto adicional (product, max_distance_m, ...)
}

func (e *PlacementError) Error() string {
	return fmt.Sprintf("world/buildings: emplazamiento inválido (%s): %s", e.Rule, e.Message)
}

// Unwrap hace que errors.Is(err, ErrPlacementInvalid) sea verdadero.
func (e *PlacementError) Unwrap() error { return ErrPlacementInvalid }

// FundsError detalla un saldo insuficiente para un coste obligatorio (build_cost,
// upgrade_cost): importes requerido/disponible en punto fijo (details {required,
// available}). Mapea a 422 INSUFFICIENT_FUNDS.
type FundsError struct {
	Required  int64
	Available int64
}

func (e *FundsError) Error() string {
	return fmt.Sprintf("world/buildings: fondos insuficientes: requerido %d, disponible %d", e.Required, e.Available)
}

// Unwrap hace que errors.Is(err, ErrInsufficientFunds) sea verdadero.
func (e *FundsError) Unwrap() error { return ErrInsufficientFunds }
