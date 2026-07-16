// Package ledger encapsula los asientos de doble entrada y las funciones
// todo-o-nada de la base (ledger.confirm_contract / settle_contract_prorata).
// Regla de oro (ADR-005): la base garantiza (triggers de balance por activo,
// no-negatividad); este paquete solo orquesta.
package ledger

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"imperio/engine/internal/core"
)

// Entry es una partida (cuenta, importe con signo) de un asiento.
type Entry struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostTx inserta una cabecera de asiento + sus partidas. Los triggers
// diferidos abortan el COMMIT si el asiento no balancea por activo.
func PostTx(ctx context.Context, q core.Querier, kind string, simTime int64, referenceID *uuid.UUID, description string, entries []Entry) (uuid.UUID, error) {
	txID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO ledger.transactions (id, kind, sim_time_at, reference_id, description)
		 VALUES ($1, $2, $3, $4, $5)`,
		txID, kind, simTime, referenceID, description); err != nil {
		return uuid.Nil, fmt.Errorf("ledger: transaction %s: %w", kind, err)
	}
	for _, e := range entries {
		if e.Amount == 0 {
			continue // el CHECK amount <> 0 prohíbe partidas nulas
		}
		if _, err := q.Exec(ctx,
			`INSERT INTO ledger.entries (transaction_id, account_id, amount) VALUES ($1, $2, $3)`,
			txID, e.AccountID, e.Amount); err != nil {
			return uuid.Nil, fmt.Errorf("ledger: entry %s: %w", kind, err)
		}
	}
	return txID, nil
}

// CashAccount devuelve la cuenta de caja de una corporación (debe existir).
func CashAccount(ctx context.Context, q core.Querier, owner uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM ledger.accounts WHERE kind = 'cash' AND owner_account_id = $1`, owner).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: cash de %s: %w", owner, err)
	}
	return id, nil
}

// Balance devuelve el saldo actual de una cuenta.
func Balance(ctx context.Context, q core.Querier, id uuid.UUID) (int64, error) {
	var b int64
	err := q.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE id = $1`, id).Scan(&b)
	return b, err
}

// EnsureStockFree devuelve (creando bajo demanda) la cuenta stock_free por
// (dueño, producto, almacén). warehouse puede ser nil (p. ej. compradores
// ciudad en un city_gate sin edificio).
func EnsureStockFree(ctx context.Context, q core.Querier, owner, product uuid.UUID, warehouse *uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM ledger.accounts
		  WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2
		    AND warehouse_building_id IS NOT DISTINCT FROM $3`,
		owner, product, warehouse).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	err = q.QueryRow(ctx,
		`INSERT INTO ledger.accounts (kind, owner_account_id, product_id, warehouse_building_id)
		 VALUES ('stock_free', $1, $2, $3) RETURNING id`,
		owner, product, warehouse).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: crear stock_free: %w", err)
	}
	return id, nil
}

// EnsureEmissionStock devuelve (creando bajo demanda) la cuenta de génesis de
// stock del producto (única por producto, owner = Banco Central; ADR-IMPL-12).
func EnsureEmissionStock(ctx context.Context, q core.Querier, bancoCentral, product uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT id FROM ledger.accounts WHERE kind = 'emission' AND product_id = $1`, product).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	err = q.QueryRow(ctx,
		`INSERT INTO ledger.accounts (kind, owner_account_id, product_id)
		 VALUES ('emission', $1, $2) RETURNING id`,
		bancoCentral, product).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: crear emission de stock: %w", err)
	}
	return id, nil
}

// NewMirrorAccount crea una cuenta espejo (stock_reserved / guarantee /
// escrow) con reference_id = uuid de la entidad que la motiva.
func NewMirrorAccount(ctx context.Context, q core.Querier, kind string, owner uuid.UUID, product *uuid.UUID, warehouse *uuid.UUID, reference uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := q.QueryRow(ctx,
		`INSERT INTO ledger.accounts (kind, owner_account_id, product_id, warehouse_building_id, reference_id)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		kind, owner, product, warehouse, reference).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: crear espejo %s: %w", kind, err)
	}
	return id, nil
}

// ConfirmContract invoca ledger.confirm_contract (bloqueo triple atómico).
func ConfirmContract(ctx context.Context, q core.Querier, contractID uuid.UUID, simTime, quantity, unitPrice int64,
	fromStock, fromGuarantee, fromEscrow, toStock, toGuarantee, toEscrow uuid.UUID) error {
	txID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx,
		`SELECT ledger.confirm_contract($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		txID, contractID, simTime, quantity, unitPrice,
		fromStock, fromGuarantee, fromEscrow, toStock, toGuarantee, toEscrow)
	if err != nil {
		return fmt.Errorf("ledger: confirm_contract %s: %w", contractID, err)
	}
	return nil
}

// SettleContractProrata invoca ledger.settle_contract_prorata.
func SettleContractProrata(ctx context.Context, q core.Querier, contractID uuid.UUID, simTime int64,
	sellerCash, buyerCash, buyerStock, sink, sellerStockRelease uuid.UUID, compensationBp int) error {
	txID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx,
		`SELECT ledger.settle_contract_prorata($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		txID, contractID, simTime, sellerCash, buyerCash, buyerStock, sink, sellerStockRelease, compensationBp)
	if err != nil {
		return fmt.Errorf("ledger: settle_contract_prorata %s: %w", contractID, err)
	}
	return nil
}

// ProrataResult replica en Go la aritmética de ledger.settle_contract_prorata
// (para tests de proporcionalidad; la ejecución real vive en la base).
type ProrataResult struct {
	ValueTotal, ValueFilled, ValueMissing int64
	GuarTotal, GuarFilled, GuarMissing    int64
	Compensation, SinkPart, QtyMissing    int64
}

func Prorata(agreed, delivered, unitPrice int64, compensationBp int64) ProrataResult {
	r := ProrataResult{}
	r.ValueTotal = agreed * unitPrice
	r.ValueFilled = delivered * unitPrice
	r.ValueMissing = r.ValueTotal - r.ValueFilled
	r.GuarTotal = (r.ValueTotal * 10) / 100
	r.GuarFilled = (r.GuarTotal * delivered) / agreed
	r.GuarMissing = r.GuarTotal - r.GuarFilled
	r.Compensation = (r.GuarMissing * compensationBp) / 10000
	r.SinkPart = r.GuarMissing - r.Compensation
	r.QtyMissing = agreed - delivered
	return r
}
