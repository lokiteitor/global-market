package contracts

import (
	"errors"
	"fmt"
	"time"
)

// Errores tipados del módulo, mapeables a los códigos estables del contrato
// OpenAPI. Los handlers los traducen con errors.Is/errors.As; los errores con
// details estructurados (CollateralError, CooldownError, MinLotError) se
// recuperan con errors.As y siguen respondiendo a errors.Is de su sentinela.
var (
	// ErrPublicationNotFound: la publicación no existe (→ 404 NOT_FOUND).
	ErrPublicationNotFound = errors.New("contracts: la publicación no existe")

	// ErrAcceptanceNotFound: la aceptación no existe (→ 404 NOT_FOUND).
	ErrAcceptanceNotFound = errors.New("contracts: la aceptación no existe")

	// ErrNotPublisher: solo el publicador puede cancelar su publicación
	// (→ 403 NOT_RESOURCE_OWNER).
	ErrNotPublisher = errors.New("contracts: la publicación pertenece a otra corporación")

	// ErrNotParty: una publicación privada solo es visible y aceptable por
	// sus partes (publicador y counterparty) (→ 403 NOT_RESOURCE_OWNER).
	ErrNotParty = errors.New("contracts: la publicación privada solo es visible para sus partes")

	// ErrNotAcceptor: una aceptación solo es visible para el aceptante
	// (→ 403 NOT_RESOURCE_OWNER).
	ErrNotAcceptor = errors.New("contracts: la aceptación pertenece a otra corporación")

	// ErrNotNodeOwner: el origin_node_id aportado al aceptar una publicación
	// buy debe ser un almacén propio del aceptante (→ 403 NOT_RESOURCE_OWNER).
	ErrNotNodeOwner = errors.New("contracts: el nodo de origen no pertenece al aceptante")

	// ErrValidation: parámetros inválidos (→ 422 VALIDATION_ERROR). Los
	// errores concretos lo envuelven con el detalle legible.
	ErrValidation = errors.New("contracts: parámetros inválidos")

	// ErrInsufficientCollateral: la garantía disponible (stock o dinero) no
	// cubre el bloqueo requerido (→ 422 INSUFFICIENT_COLLATERAL). Todo-o-nada:
	// un bloqueo rechazado no deja NINGÚN efecto. Con errors.As se recupera
	// el CollateralError con los details {required, available}.
	ErrInsufficientCollateral = errors.New("contracts: garantía insuficiente")

	// ErrPublicationExhausted: la publicación está agotada, cancelada o
	// expirada — no admite aceptaciones ni cancelación
	// (→ 409 PUBLICATION_EXHAUSTED).
	ErrPublicationExhausted = errors.New("contracts: la publicación está agotada, cancelada o expirada")

	// ErrCancelCooldownActive: cancelación dentro del cooldown anti-parpadeo
	// (→ 409 CANCEL_COOLDOWN_ACTIVE). Con errors.As se recupera el
	// CooldownError con details {cancel_cooldown_until}.
	ErrCancelCooldownActive = errors.New("contracts: la publicación no puede cancelarse durante el cooldown anti-parpadeo")

	// ErrBelowMinLot: la cantidad aceptada no cumple
	// min(min_lot, quantity_remaining) <= qty <= quantity_remaining
	// (→ 422 BELOW_MIN_LOT). Con errors.As se recupera el MinLotError.
	ErrBelowMinLot = errors.New("contracts: la cantidad aceptada no cumple el lote mínimo de la publicación")

	// ErrInvalidCursor: el cursor de paginación no fue emitido por este módulo
	// para este orden (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("contracts: cursor de paginación inválido")

	// ErrContractNotFound: el contrato no existe (→ 404 NOT_FOUND).
	ErrContractNotFound = errors.New("contracts: el contrato no existe")

	// ErrNotContractParty: un contrato solo es visible para sus partes
	// (comprador y vendedor) (→ 403 NOT_RESOURCE_OWNER).
	ErrNotContractParty = errors.New("contracts: el contrato pertenece a otras partes")
)

// Errores de validación concretos: envuelven ErrValidation (mismo código 422
// VALIDATION_ERROR) conservando identidad propia para errors.Is.
var (
	// ErrFreightPhase2: el CCRI-Flete no está activo en la Fase 0. El mensaje
	// es parte del contrato de este incremento.
	ErrFreightPhase2 = fmt.Errorf("%w: CCRI-Flete se activa en Fase 2", ErrValidation)

	// ErrOverflow: quantity*unit_price (con el margen ×10 que exigen las
	// fórmulas de garantía en SQL) desborda int64.
	ErrOverflow = fmt.Errorf("%w: quantity*unit_price desborda la capacidad de int64", ErrValidation)
)

// CollateralError detalla una garantía insuficiente: el recurso que faltó
// ("stock" o "cash") y los importes requerido/disponible en punto fijo
// (details {required, available} del contrato, serializados como string).
type CollateralError struct {
	Resource  string // "stock" | "cash"
	Required  int64
	Available int64
}

func (e *CollateralError) Error() string {
	return fmt.Sprintf("%v: %s requerido %d, disponible %d",
		ErrInsufficientCollateral, e.Resource, e.Required, e.Available)
}

// Unwrap hace que errors.Is(err, ErrInsufficientCollateral) sea verdadero.
func (e *CollateralError) Unwrap() error { return ErrInsufficientCollateral }

// CooldownError detalla un intento de cancelación dentro del cooldown
// anti-parpadeo (details {cancel_cooldown_until}).
type CooldownError struct {
	Until time.Time
}

func (e *CooldownError) Error() string {
	return fmt.Sprintf("%v (hasta %s)", ErrCancelCooldownActive, e.Until.UTC().Format(time.RFC3339))
}

// Unwrap hace que errors.Is(err, ErrCancelCooldownActive) sea verdadero.
func (e *CooldownError) Unwrap() error { return ErrCancelCooldownActive }

// MinLotError detalla una cantidad de aceptación fuera de rango: el lote
// mínimo efectivo min(min_lot, quantity_remaining) y la cantidad restante.
type MinLotError struct {
	MinLot            int64 // lote mínimo efectivo
	QuantityRemaining int64
}

func (e *MinLotError) Error() string {
	return fmt.Sprintf("%v (mínimo efectivo %d, restante %d)",
		ErrBelowMinLot, e.MinLot, e.QuantityRemaining)
}

// Unwrap hace que errors.Is(err, ErrBelowMinLot) sea verdadero.
func (e *MinLotError) Unwrap() error { return ErrBelowMinLot }
