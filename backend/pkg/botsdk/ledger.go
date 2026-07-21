package botsdk

import (
	"context"
	"net/url"
	"strconv"
)

// LedgerAccountsQuery filtra GET /ledger/accounts.
type LedgerAccountsQuery struct {
	Kind      LedgerAccountKind
	ProductID string
	PageQuery
}

// values serializa la query.
func (q LedgerAccountsQuery) values() url.Values {
	v := url.Values{}
	if q.Kind != "" {
		v.Set("kind", string(q.Kind))
	}
	if q.ProductID != "" {
		v.Set("product_id", q.ProductID)
	}
	q.apply(v)
	return v
}

// ListAccounts devuelve las cuentas de valor de la corporación autenticada
// (GET /ledger/accounts). Solo lectura: el valor solo se mueve mediante
// operaciones de dominio.
func (c *Client) ListAccounts(ctx context.Context, q LedgerAccountsQuery) (Page[LedgerAccount], error) {
	return getList[LedgerAccount](ctx, c, "/ledger/accounts", q.values())
}

// LedgerEntriesQuery filtra GET /ledger/accounts/{id}/entries.
type LedgerEntriesQuery struct {
	// FromSim y ToSim acotan el sim-time del asiento (0 = sin cota).
	FromSim SimTime
	ToSim   SimTime
	PageQuery
}

// values serializa la query.
func (q LedgerEntriesQuery) values() url.Values {
	v := url.Values{}
	if q.FromSim > 0 {
		v.Set("from_sim", strconv.FormatInt(q.FromSim, 10))
	}
	if q.ToSim > 0 {
		v.Set("to_sim", strconv.FormatInt(q.ToSim, 10))
	}
	q.apply(v)
	return v
}

// ListEntries devuelve el extracto append-only de una cuenta propia del
// ledger, de más reciente a más antigua (GET /ledger/accounts/{id}/entries).
func (c *Client) ListEntries(ctx context.Context, ledgerAccountID string, q LedgerEntriesQuery) (Page[LedgerEntry], error) {
	return getList[LedgerEntry](ctx, c, "/ledger/accounts/"+pathID(ledgerAccountID)+"/entries", q.values())
}
