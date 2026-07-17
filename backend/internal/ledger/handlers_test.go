package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// fakeReader implementa Reader capturando los argumentos recibidos.
type fakeReader struct {
	accounts []Account
	entries  []Entry
	next     string
	err      error

	gotOwner     uuid.UUID
	gotAccFilter AccountFilter
	gotRequester uuid.UUID
	gotAccountID uuid.UUID
	gotEntFilter EntryFilter
	calls        int
}

func (f *fakeReader) ListAccounts(_ context.Context, owner uuid.UUID, filter AccountFilter) ([]Account, string, error) {
	f.calls++
	f.gotOwner, f.gotAccFilter = owner, filter
	return f.accounts, f.next, f.err
}

func (f *fakeReader) ListEntries(_ context.Context, requester, accountID uuid.UUID, filter EntryFilter) ([]Entry, string, error) {
	f.calls++
	f.gotRequester, f.gotAccountID, f.gotEntFilter = requester, accountID, filter
	return f.entries, f.next, f.err
}

// fixedIdentity implementa Identity con una cuenta fija (o ninguna).
type fixedIdentity struct {
	id uuid.UUID
	ok bool
}

func (f fixedIdentity) AccountID(context.Context) (uuid.UUID, bool) { return f.id, f.ok }

// fixedMeta implementa MetaSource con un sim-time fijo.
type fixedMeta struct{}

func (fixedMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{
		SimTime:        simtime.Format(3600),
		SimTimeSeconds: 3600,
		ServerTime:     time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

func newTestServer(t *testing.T, reader Reader, identity Identity) *httptest.Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	NewHandlers(reader, identity, fixedMeta{}, logger).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// get realiza la petición y decodifica el cuerpo JSON en un mapa genérico.
func get(t *testing.T, srv *httptest.Server, path string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decodificando la respuesta de %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// errorCode extrae error.code del envelope de error.
func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	e, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("respuesta sin envelope de error: %v", body)
	}
	code, _ := e["code"].(string)
	return code
}

func TestListAccountsUnauthorized(t *testing.T) {
	srv := newTestServer(t, &fakeReader{}, fixedIdentity{ok: false})
	status, body := get(t, srv, "/ledger/accounts")
	if status != http.StatusUnauthorized || errorCode(t, body) != "UNAUTHORIZED" {
		t.Fatalf("status %d code %s, esperado 401 UNAUTHORIZED", status, errorCode(t, body))
	}
}

func TestListAccountsValidation(t *testing.T) {
	owner := uuid.New()
	cases := []struct {
		name string
		path string
	}{
		{"kind desconocido", "/ledger/accounts?kind=money"},
		{"product_id no uuid", "/ledger/accounts?product_id=abc"},
		{"limit no numérico", "/ledger/accounts?limit=abc"},
		{"limit cero", "/ledger/accounts?limit=0"},
		{"limit sobre el máximo", "/ledger/accounts?limit=201"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reader := &fakeReader{}
			srv := newTestServer(t, reader, fixedIdentity{id: owner, ok: true})
			status, body := get(t, srv, c.path)
			if status != http.StatusBadRequest || errorCode(t, body) != httpx.CodeValidationError {
				t.Fatalf("status %d code %s, esperado 400 VALIDATION_ERROR", status, errorCode(t, body))
			}
			if reader.calls != 0 {
				t.Fatal("el reader no debe invocarse con parámetros inválidos")
			}
		})
	}
}

func TestListAccountsPassesFilters(t *testing.T) {
	owner := uuid.New()
	product := uuid.New()
	reader := &fakeReader{accounts: []Account{}}
	srv := newTestServer(t, reader, fixedIdentity{id: owner, ok: true})

	status, _ := get(t, srv, "/ledger/accounts?kind=stock_free&product_id="+product.String()+"&limit=7&cursor=abc")
	if status != http.StatusOK {
		t.Fatalf("status %d, esperado 200", status)
	}
	if reader.gotOwner != owner {
		t.Errorf("owner %s, esperado %s", reader.gotOwner, owner)
	}
	f := reader.gotAccFilter
	if f.Kind != AccountKindStockFree || f.ProductID == nil || *f.ProductID != product ||
		f.Limit != 7 || f.Cursor != "abc" {
		t.Errorf("filtro inesperado: %+v", f)
	}
}

func TestListAccountsResponseShape(t *testing.T) {
	owner := uuid.New()
	product := uuid.New()
	ref := uuid.New()
	createdAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		accounts: []Account{
			{ // caja: sin producto/almacén/referencia — claves ausentes
				ID: uuid.New(), Kind: AccountKindCash, OwnerAccountID: &owner,
				Balance: 1000, CreatedAt: createdAt, UpdatedAt: createdAt,
			},
			{ // stock con todos los opcionales
				ID: uuid.New(), Kind: AccountKindStockReserved, OwnerAccountID: &owner,
				ProductID: &product, ReferenceID: &ref,
				Balance: -5, CreatedAt: createdAt, UpdatedAt: createdAt,
			},
		},
		next: "CURSOR-NEXT",
	}
	srv := newTestServer(t, reader, fixedIdentity{id: owner, ok: true})

	status, body := get(t, srv, "/ledger/accounts")
	if status != http.StatusOK {
		t.Fatalf("status %d, esperado 200", status)
	}
	data, ok := body["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("data inesperado: %v", body["data"])
	}

	cash := data[0].(map[string]any)
	if cash["balance"] != "1000" {
		t.Errorf("balance debe ser string \"1000\", obtenido %v (%T)", cash["balance"], cash["balance"])
	}
	if cash["kind"] != "cash" || cash["owner_account_id"] != owner.String() {
		t.Errorf("cuenta cash inesperada: %v", cash)
	}
	for _, absent := range []string{"product_id", "warehouse_building_id", "reference_id"} {
		if _, present := cash[absent]; present {
			t.Errorf("clave %s presente en cuenta monetaria sin valor", absent)
		}
	}

	stock := data[1].(map[string]any)
	if stock["balance"] != "-5" {
		t.Errorf("balance negativo como string: %v", stock["balance"])
	}
	if stock["product_id"] != product.String() || stock["reference_id"] != ref.String() {
		t.Errorf("opcionales de stock: %v", stock)
	}

	meta := body["meta"].(map[string]any)
	if meta["next_cursor"] != "CURSOR-NEXT" {
		t.Errorf("meta.next_cursor: %v", meta["next_cursor"])
	}
	if meta["sim_time"] != simtime.Format(3600) {
		t.Errorf("meta.sim_time: %v", meta["sim_time"])
	}
}

func TestListAccountsEmptyDataIsArray(t *testing.T) {
	owner := uuid.New()
	srv := newTestServer(t, &fakeReader{accounts: []Account{}}, fixedIdentity{id: owner, ok: true})
	status, body := get(t, srv, "/ledger/accounts")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	if data, ok := body["data"].([]any); !ok || data == nil {
		t.Fatalf("data debe ser [] y nunca null: %v", body["data"])
	}
	if meta := body["meta"].(map[string]any); meta["next_cursor"] != nil {
		t.Errorf("next_cursor debe estar ausente sin más páginas: %v", meta["next_cursor"])
	}
}

func TestListAccountsServiceErrors(t *testing.T) {
	owner := uuid.New()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"cursor inválido", ErrInvalidCursor, http.StatusBadRequest, httpx.CodeValidationError},
		{"error interno", errors.New("bd caída"), http.StatusInternalServerError, httpx.CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeReader{err: c.err}, fixedIdentity{id: owner, ok: true})
			status, body := get(t, srv, "/ledger/accounts")
			if status != c.wantStatus || errorCode(t, body) != c.wantCode {
				t.Fatalf("status %d code %s, esperado %d %s", status, errorCode(t, body), c.wantStatus, c.wantCode)
			}
		})
	}
}

func TestListEntriesPathAndParams(t *testing.T) {
	owner := uuid.New()
	account := uuid.New()

	t.Run("sin sesión", func(t *testing.T) {
		srv := newTestServer(t, &fakeReader{}, fixedIdentity{ok: false})
		status, body := get(t, srv, "/ledger/accounts/"+account.String()+"/entries")
		if status != http.StatusUnauthorized || errorCode(t, body) != "UNAUTHORIZED" {
			t.Fatalf("status %d code %s", status, errorCode(t, body))
		}
	})

	t.Run("id de ruta no uuid es 404", func(t *testing.T) {
		srv := newTestServer(t, &fakeReader{}, fixedIdentity{id: owner, ok: true})
		status, body := get(t, srv, "/ledger/accounts/not-a-uuid/entries")
		if status != http.StatusNotFound || errorCode(t, body) != httpx.CodeNotFound {
			t.Fatalf("status %d code %s, esperado 404 NOT_FOUND", status, errorCode(t, body))
		}
	})

	t.Run("from_sim inválido", func(t *testing.T) {
		srv := newTestServer(t, &fakeReader{}, fixedIdentity{id: owner, ok: true})
		for _, qs := range []string{"from_sim=abc", "from_sim=-1", "to_sim=x"} {
			status, body := get(t, srv, "/ledger/accounts/"+account.String()+"/entries?"+qs)
			if status != http.StatusBadRequest || errorCode(t, body) != httpx.CodeValidationError {
				t.Fatalf("%s: status %d code %s", qs, status, errorCode(t, body))
			}
		}
	})

	t.Run("filtros correctos llegan al servicio", func(t *testing.T) {
		reader := &fakeReader{entries: []Entry{}}
		srv := newTestServer(t, reader, fixedIdentity{id: owner, ok: true})
		status, _ := get(t, srv, "/ledger/accounts/"+account.String()+"/entries?from_sim=100&to_sim=200&limit=3&cursor=zzz")
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		if reader.gotRequester != owner || reader.gotAccountID != account {
			t.Errorf("requester/cuenta: %s/%s", reader.gotRequester, reader.gotAccountID)
		}
		f := reader.gotEntFilter
		if f.FromSim == nil || *f.FromSim != 100 || f.ToSim == nil || *f.ToSim != 200 ||
			f.Limit != 3 || f.Cursor != "zzz" {
			t.Errorf("filtro inesperado: %+v", f)
		}
	})
}

func TestListEntriesServiceErrors(t *testing.T) {
	owner := uuid.New()
	account := uuid.New()
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"cuenta inexistente", ErrAccountNotFound, http.StatusNotFound, httpx.CodeNotFound},
		{"cuenta ajena", ErrNotOwner, http.StatusForbidden, "NOT_RESOURCE_OWNER"},
		{"cursor inválido", ErrInvalidCursor, http.StatusBadRequest, httpx.CodeValidationError},
		{"error interno", errors.New("bd caída"), http.StatusInternalServerError, httpx.CodeInternal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newTestServer(t, &fakeReader{err: c.err}, fixedIdentity{id: owner, ok: true})
			status, body := get(t, srv, "/ledger/accounts/"+account.String()+"/entries")
			if status != c.wantStatus || errorCode(t, body) != c.wantCode {
				t.Fatalf("status %d code %s, esperado %d %s", status, errorCode(t, body), c.wantStatus, c.wantCode)
			}
		})
	}
}

func TestListEntriesResponseShape(t *testing.T) {
	owner := uuid.New()
	account := uuid.New()
	ref := uuid.New()
	desc := "capital semilla"
	createdAt := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	reader := &fakeReader{
		entries: []Entry{
			{
				ID: uuid.New(), TransactionID: uuid.New(), AccountID: account,
				Amount: -125000, TransactionKind: TransactionKindSeedCapital,
				ReferenceID: &ref, Description: &desc,
				SimTimeAt: 31104000, CreatedAt: createdAt,
			},
			{ // sin opcionales
				ID: uuid.New(), TransactionID: uuid.New(), AccountID: account,
				Amount: 42, TransactionKind: TransactionKindTransfer,
				SimTimeAt: 0, CreatedAt: createdAt,
			},
		},
		next: "MORE",
	}
	srv := newTestServer(t, reader, fixedIdentity{id: owner, ok: true})

	status, body := get(t, srv, "/ledger/accounts/"+account.String()+"/entries")
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	data := body["data"].([]any)
	first := data[0].(map[string]any)
	if first["amount"] != "-125000" {
		t.Errorf("amount debe ser string: %v (%T)", first["amount"], first["amount"])
	}
	if first["transaction_kind"] != "seed_capital" {
		t.Errorf("transaction_kind: %v", first["transaction_kind"])
	}
	// SimTime es integer en el contrato (no string).
	if simAt, ok := first["sim_time_at"].(float64); !ok || int64(simAt) != 31104000 {
		t.Errorf("sim_time_at debe ser numérico: %v (%T)", first["sim_time_at"], first["sim_time_at"])
	}
	if first["reference_id"] != ref.String() || first["description"] != desc {
		t.Errorf("opcionales: %v", first)
	}
	second := data[1].(map[string]any)
	for _, absent := range []string{"reference_id", "description"} {
		if _, present := second[absent]; present {
			t.Errorf("clave %s presente sin valor", absent)
		}
	}
	if meta := body["meta"].(map[string]any); meta["next_cursor"] != "MORE" {
		t.Errorf("next_cursor: %v", meta["next_cursor"])
	}
}
