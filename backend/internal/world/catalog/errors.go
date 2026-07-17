package catalog

import "errors"

// Errores tipados del subpaquete. Los handlers los mapean a los códigos del
// contrato (NOT_FOUND, VALIDATION_ERROR).
var (
	// ErrRegionNotFound: la región no existe (→ 404 NOT_FOUND).
	ErrRegionNotFound = errors.New("world/catalog: la región no existe")

	// ErrCityNotFound: la ciudad no existe (→ 404 NOT_FOUND).
	ErrCityNotFound = errors.New("world/catalog: la ciudad no existe")

	// ErrInvalidCursor: el cursor de paginación no es uno emitido por este
	// listado (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/catalog: cursor de paginación inválido")
)
