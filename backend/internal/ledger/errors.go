package ledger

import "errors"

// Errores tipados del módulo. Los handlers los mapean a los códigos del
// contrato (NOT_FOUND, NOT_RESOURCE_OWNER, VALIDATION_ERROR…); otros módulos
// los comprueban con errors.Is.
var (
	// ErrAccountNotFound: la cuenta del ledger no existe (→ 404 NOT_FOUND).
	ErrAccountNotFound = errors.New("ledger: la cuenta no existe")

	// ErrNotOwner: la cuenta existe pero pertenece a otra corporación
	// (autorización por propiedad → 403 NOT_RESOURCE_OWNER).
	ErrNotOwner = errors.New("ledger: la cuenta pertenece a otra corporación")

	// ErrInvalidCursor: el cursor de paginación no es un cursor emitido por
	// este módulo (→ 400 VALIDATION_ERROR).
	ErrInvalidCursor = errors.New("ledger: cursor de paginación inválido")

	// ErrUnbalanced: el asiento viola la doble entrada — la suma de partidas
	// por activo no es cero. Lo detecta el trigger diferido de la BD en el
	// COMMIT; la transacción entera se revierte sin dejar rastro.
	ErrUnbalanced = errors.New("ledger: asiento no balanceado (doble entrada violada)")

	// ErrInsufficientBalance: el asiento dejaría un saldo negativo en una
	// cuenta no-emission (constraint ck_accounts_non_negative). Todo-o-nada:
	// no queda ninguna partida asentada.
	ErrInsufficientBalance = errors.New("ledger: saldo insuficiente (el asiento dejaría un saldo negativo)")

	// ErrTooFewEntries: un asiento de doble entrada requiere al menos dos
	// partidas (una sola, con importe no nulo, nunca balancea).
	ErrTooFewEntries = errors.New("ledger: un asiento requiere al menos dos partidas")

	// ErrZeroAmount: las partidas no pueden tener importe cero (CHECK de
	// ledger.entries; se valida antes de tocar la BD).
	ErrZeroAmount = errors.New("ledger: las partidas no pueden tener importe cero")
)
