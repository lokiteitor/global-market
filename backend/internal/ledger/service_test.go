package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

// Guardas de PostTransaction que no requieren BD: se comprueban antes de
// abrir ninguna transacción (el pool nil nunca se toca).
func TestPostTransactionGuards(t *testing.T) {
	svc := NewService(nil, DefaultOptions(), nil)
	ctx := context.Background()
	acc := uuid.New()

	if _, err := svc.PostTransaction(ctx, TransactionKindTransfer, 0, nil, "", nil); !errors.Is(err, ErrTooFewEntries) {
		t.Errorf("sin partidas: %v, esperado ErrTooFewEntries", err)
	}
	if _, err := svc.PostTransaction(ctx, TransactionKindTransfer, 0, nil, "",
		[]EntryInput{{AccountID: acc, Amount: 100}}); !errors.Is(err, ErrTooFewEntries) {
		t.Errorf("una sola partida: %v, esperado ErrTooFewEntries", err)
	}
	if _, err := svc.PostTransaction(ctx, TransactionKindTransfer, 0, nil, "",
		[]EntryInput{{AccountID: acc, Amount: 0}, {AccountID: acc, Amount: 100}}); !errors.Is(err, ErrZeroAmount) {
		t.Errorf("importe cero: %v, esperado ErrZeroAmount", err)
	}
}

// Las validaciones de filtros y cursores devuelven error antes de tocar la BD.
func TestListAccountsRejectsBadInputWithoutDB(t *testing.T) {
	svc := NewService(nil, DefaultOptions(), nil)
	ctx := context.Background()
	owner := uuid.New()

	if _, _, err := svc.ListAccounts(ctx, owner, AccountFilter{Kind: "bogus"}); err == nil {
		t.Error("kind inválido aceptado")
	}
	if _, _, err := svc.ListAccounts(ctx, owner, AccountFilter{Cursor: "###"}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("cursor inválido: %v, esperado ErrInvalidCursor", err)
	}
	if _, _, err := svc.ListEntries(ctx, owner, uuid.New(), EntryFilter{Cursor: "###"}); !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("cursor de partidas inválido: %v, esperado ErrInvalidCursor", err)
	}
}

func TestNormalizeLimit(t *testing.T) {
	cases := []struct {
		in   int
		want int32
	}{
		{0, DefaultPageLimit},
		{-3, DefaultPageLimit},
		{1, 1},
		{7, 7},
		{MaxPageLimit, MaxPageLimit},
		{MaxPageLimit + 1, MaxPageLimit},
		{100000, MaxPageLimit},
	}
	for _, c := range cases {
		if got := normalizeLimit(c.in); got != c.want {
			t.Errorf("normalizeLimit(%d) = %d, esperado %d", c.in, got, c.want)
		}
	}
}

// mapPostError traduce los SQLSTATE de las invariantes del ledger a errores
// tipados del módulo.
func TestMapPostError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{
			"no-negatividad",
			&pgconn.PgError{Code: "23514", ConstraintName: "ck_accounts_non_negative", Message: "check violado"},
			ErrInsufficientBalance,
		},
		{
			"doble entrada",
			&pgconn.PgError{Code: "P0001", Message: "ledger: transaccion abc no balanceada (doble entrada violada)"},
			ErrUnbalanced,
		},
		{
			"cuenta inexistente",
			&pgconn.PgError{Code: "23503", TableName: "entries", ConstraintName: "entries_account_id_fkey"},
			ErrAccountNotFound,
		},
	}
	for _, c := range cases {
		if got := mapPostError(c.in); !errors.Is(got, c.want) {
			t.Errorf("%s: mapPostError = %v, esperado %v", c.name, got, c.want)
		}
	}
	// Otros errores no se disfrazan de invariantes.
	plain := errors.New("conexión rechazada")
	got := mapPostError(plain)
	if errors.Is(got, ErrUnbalanced) || errors.Is(got, ErrInsufficientBalance) || errors.Is(got, ErrAccountNotFound) {
		t.Errorf("error genérico mapeado a invariante: %v", got)
	}
	if !errors.Is(got, plain) {
		t.Errorf("error genérico no envuelto: %v", got)
	}
}

func TestAccountKindValid(t *testing.T) {
	for _, k := range []AccountKind{
		AccountKindCash, AccountKindEscrow, AccountKindGuarantee, AccountKindStockFree,
		AccountKindStockReserved, AccountKindCustody, AccountKindSink, AccountKindEmission,
	} {
		if !k.Valid() {
			t.Errorf("%q debería ser válido", k)
		}
	}
	for _, k := range []AccountKind{"", "money", "CASH", "stock"} {
		if k.Valid() {
			t.Errorf("%q no debería ser válido", k)
		}
	}
}
