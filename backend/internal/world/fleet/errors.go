package fleet

import (
	"errors"
	"fmt"
)

// Errores tipados del subpaquete, mapeables a los códigos estables del contrato
// OpenAPI. Los handlers los traducen con errors.Is/errors.As.
var (
	// ErrVehicleNotFound: el vehículo no existe (→ 404 NOT_FOUND).
	ErrVehicleNotFound = errors.New("world/fleet: el vehículo no existe")

	// ErrShipmentNotFound: el cargamento no existe (→ 404 NOT_FOUND).
	ErrShipmentNotFound = errors.New("world/fleet: el cargamento no existe")

	// ErrNotFound: un recurso referenciado no existe (tipo de vehículo, nodo de
	// entrega, ruta) (→ 404 NOT_FOUND).
	ErrNotFound = errors.New("world/fleet: recurso no encontrado")

	// ErrForbidden: el recurso pertenece a otra corporación (autorización por
	// propiedad, → 403 NOT_RESOURCE_OWNER).
	ErrForbidden = errors.New("world/fleet: el recurso pertenece a otra corporación")

	// ErrVehicleSealed: el vehículo está SELLADO durante un handoff entre shards
	// (→ 403 VEHICLE_SEALED). Solo aplica tras la extracción multiproceso.
	ErrVehicleSealed = errors.New("world/fleet: el vehículo está sellado (handoff entre shards)")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los errores
	// concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("world/fleet: parámetros inválidos")

	// ErrInsufficientFunds: la caja no cubre el coste (→ 422 INSUFFICIENT_FUNDS).
	// Con errors.As se recupera el FundsError con {required, available}.
	ErrInsufficientFunds = errors.New("world/fleet: fondos insuficientes")

	// ErrVehicleNotIdle: el vehículo no está idle para el despacho (→ 409).
	ErrVehicleNotIdle = errors.New("world/fleet: el vehículo no está idle")

	// ErrShipmentNotDispatchable: el cargamento no es despachable (ni in_warehouse
	// ni at_terminal) (→ 409).
	ErrShipmentNotDispatchable = errors.New("world/fleet: el cargamento no es despachable (in_warehouse o at_terminal)")

	// ErrWrongVehicleMode: la ruta contiene tramos de un modo distinto al del
	// vehículo (un vehículo solo circula por enlaces de SU modo; una ruta multimodal
	// se despacha por tramos de un solo modo) (→ 422 VALIDATION_ERROR).
	ErrWrongVehicleMode = fmt.Errorf("%w: la ruta contiene tramos de un modo distinto al del vehículo", ErrValidation)

	// ErrTransshipmentPending: el cargamento at_terminal aún no consumió su tiempo
	// de transbordo en la terminal y no puede despacharse todavía (→ 409). Con
	// errors.As se recupera el TransshipmentPendingError con {ready_at_sim}.
	ErrTransshipmentPending = errors.New("world/fleet: el transbordo en la terminal aún no ha terminado")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este listado
	// (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("world/fleet: cursor de paginación inválido")

	// ErrOverflow: una multiplicación de dominio (volumen/combustible) desborda
	// int64.
	ErrOverflow = fmt.Errorf("%w: la operación desborda la capacidad de int64", ErrValidation)
)

// FundsError detalla un saldo insuficiente para el precio de compra de un
// vehículo: importes requerido/disponible en punto fijo (details {required,
// available}). Mapea a 422 INSUFFICIENT_FUNDS.
type FundsError struct {
	Required  int64
	Available int64
}

func (e *FundsError) Error() string {
	return fmt.Sprintf("world/fleet: fondos insuficientes: requerido %d, disponible %d", e.Required, e.Available)
}

// Unwrap hace que errors.Is(err, ErrInsufficientFunds) sea verdadero.
func (e *FundsError) Unwrap() error { return ErrInsufficientFunds }

// TransshipmentPendingError detalla que un cargamento at_terminal todavía no ha
// consumido su tiempo de transbordo: ReadyAtSim es el sim-time a partir del cual el
// siguiente despacho es admisible. Mapea a 409 (conflicto de estado temporal).
type TransshipmentPendingError struct {
	ReadyAtSim int64
	NowSim     int64
}

func (e *TransshipmentPendingError) Error() string {
	return fmt.Sprintf("world/fleet: transbordo en curso: listo en sim %d (ahora %d)", e.ReadyAtSim, e.NowSim)
}

// Unwrap hace que errors.Is(err, ErrTransshipmentPending) sea verdadero.
func (e *TransshipmentPendingError) Unwrap() error { return ErrTransshipmentPending }
