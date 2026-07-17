package production

import "errors"

// Errores tipados del subpaquete, mapeables a los códigos estables del contrato
// OpenAPI. Los handlers los traducen con errors.Is/errors.As.
var (
	// ErrBatchNotFound: el lote no existe (→ 404 NOT_FOUND).
	ErrBatchNotFound = errors.New("world/production: el lote de producción no existe")

	// ErrBuildingNotFound: el edificio no existe (→ 404 NOT_FOUND).
	ErrBuildingNotFound = errors.New("world/production: el edificio no existe")

	// ErrForbidden: el edificio o el lote pertenecen a otra corporación
	// (autorización por propiedad, → 403 NOT_RESOURCE_OWNER).
	ErrForbidden = errors.New("world/production: el recurso pertenece a otra corporación")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los errores
	// concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("world/production: parámetros inválidos")

	// ErrBuildingNotOperational: el edificio no está operational y no puede
	// producir (→ 422 BUILDING_NOT_OPERATIONAL).
	ErrBuildingNotOperational = errors.New("world/production: el edificio no está operativo")

	// ErrRecipeNotSupported: la receta no pertenece al tipo del edificio
	// (→ 422 RECIPE_NOT_SUPPORTED).
	ErrRecipeNotSupported = errors.New("world/production: la receta no está soportada por el edificio")

	// ErrBatchNotCancellable: el lote ya está completed o cancelled
	// (→ 409 BATCH_NOT_CANCELLABLE).
	ErrBatchNotCancellable = errors.New("world/production: el lote ya está completado o cancelado")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/production: cursor de paginación inválido")
)
