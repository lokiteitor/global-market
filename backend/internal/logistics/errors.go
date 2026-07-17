package logistics

import (
	"errors"
	"fmt"
)

// Errores tipados del bounded context, mapeables a los códigos estables del
// contrato OpenAPI. Los handlers los traducen con errors.Is/errors.As.
var (
	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los
	// errores concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("logistics: parámetros inválidos")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// para este orden (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("logistics: cursor de paginación inválido")

	// ErrNodeNotFound: el nodo de origen o destino de un route-plan no existe en
	// el grafo (→ 404 NOT_FOUND).
	ErrNodeNotFound = errors.New("logistics: el nodo del grafo no existe")

	// ErrNoRoute: no existe ruta ejecutable entre origen y destino con los modos
	// indicados — el grafo está desconectado para esa consulta
	// (→ 422 NO_ROUTE_FOUND).
	ErrNoRoute = errors.New("logistics: no existe ruta ejecutable entre los nodos")

	// ErrRouteNotFound: la ruta no existe (→ 404 NOT_FOUND).
	ErrRouteNotFound = errors.New("logistics: la ruta no existe")

	// ErrNotRouteOwner: la ruta pertenece a otra corporación
	// (→ 403 NOT_RESOURCE_OWNER).
	ErrNotRouteOwner = errors.New("logistics: la ruta pertenece a otra corporación")

	// ErrLinkNotFound: un enlace referenciado por un leg de la ruta no existe
	// (→ 422 VALIDATION_ERROR).
	ErrLinkNotFound = errors.New("logistics: un enlace de la ruta no existe")

	// ErrDiscontiguousLegs: la secuencia de enlaces no es contigua
	// (to_node[i] != from_node[i+1]) (→ 422 VALIDATION_ERROR).
	ErrDiscontiguousLegs = errors.New("logistics: la secuencia de enlaces no es contigua")

	// ErrMultimodalNoTerminal: un cambio de modo ocurre en un nodo sin terminal
	// intermodal (→ 422 VALIDATION_ERROR).
	ErrMultimodalNoTerminal = errors.New("logistics: el cambio de modo requiere una terminal intermodal en el nodo de transbordo")

	// ErrOverflow: el coste estimado del plan desborda la capacidad de int64
	// (→ 422 VALIDATION_ERROR). Se detecta con math/big; nunca se serializa un
	// importe como float.
	ErrOverflow = fmt.Errorf("%w: el coste estimado desborda la capacidad de int64", ErrValidation)
)
