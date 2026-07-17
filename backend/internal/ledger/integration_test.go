package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// TestLedgerIntegration ejercita el módulo contra una BD real con el esquema
// completo: EnsureCashAccount idempotente, asiento de emisión balanceado,
// invariantes de los triggers (doble entrada, no-negatividad), autorización
// por propiedad, paginación keyset y los endpoints HTTP del contrato.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales de db/migrations y la
// destruye al terminar (mismo patrón que platform/migrate).
func TestLedgerIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)

	reg := prometheus.NewRegistry()
	svc := ledger.NewService(pool, ledger.DefaultOptions(), reg)

	owner := mustAuthAccount(t, ctx, pool, "human", "Ledger Test Corp")
	stranger := mustAuthAccount(t, ctx, pool, "human", "Otra Corp")

	// ── EnsureCashAccount: idempotente y a prueba de repetición ─────────────
	cash, err := svc.EnsureCashAccount(ctx, owner)
	if err != nil {
		t.Fatalf("EnsureCashAccount: %v", err)
	}
	if cash.Kind != ledger.AccountKindCash || cash.OwnerAccountID == nil || *cash.OwnerAccountID != owner || cash.Balance != 0 {
		t.Fatalf("caja recién creada inesperada: %+v", cash)
	}
	again, err := svc.EnsureCashAccount(ctx, owner)
	if err != nil {
		t.Fatalf("EnsureCashAccount (2ª vez): %v", err)
	}
	if again.ID != cash.ID {
		t.Fatalf("EnsureCashAccount no es idempotente: %s != %s", again.ID, cash.ID)
	}

	// ── Cuenta emission de sistema (owner NULL) ─────────────────────────────
	emission, err := svc.CreateAccount(ctx, ledger.AccountKindEmission, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateAccount(emission): %v", err)
	}
	if emission.OwnerAccountID != nil {
		t.Fatalf("emission debería carecer de titular: %+v", emission)
	}

	// ── Asiento de emisión balanceado: +1000 caja / -1000 emission ──────────
	ref := owner
	txID, err := svc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, 50, &ref, "capital de prueba",
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: 1000},
			{AccountID: emission.ID, Amount: -1000},
		})
	if err != nil {
		t.Fatalf("PostTransaction balanceada: %v", err)
	}

	// Saldos vía ListAccounts (nunca recalculados: los aplica el trigger).
	accounts, next, err := svc.ListAccounts(ctx, owner, ledger.AccountFilter{})
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 || next != "" {
		t.Fatalf("cuentas de owner: %d (next %q), esperado 1", len(accounts), next)
	}
	if accounts[0].ID != cash.ID || accounts[0].Balance != 1000 {
		t.Fatalf("saldo de caja: %+v, esperado 1000", accounts[0])
	}

	// Extracto con los campos de cabecera del contrato (LedgerEntry).
	entries, _, err := svc.ListEntries(ctx, owner, cash.ID, ledger.EntryFilter{})
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("extracto: %d partidas, esperado 1", len(entries))
	}
	e := entries[0]
	if e.TransactionID != txID || e.AccountID != cash.ID || e.Amount != 1000 ||
		e.TransactionKind != ledger.TransactionKindSeedCapital || e.SimTimeAt != 50 ||
		e.ReferenceID == nil || *e.ReferenceID != owner ||
		e.Description == nil || *e.Description != "capital de prueba" {
		t.Fatalf("partida inesperada: %+v", e)
	}

	// ── Autorización por propiedad ──────────────────────────────────────────
	if _, _, err := svc.ListEntries(ctx, stranger, cash.ID, ledger.EntryFilter{}); !errors.Is(err, ledger.ErrNotOwner) {
		t.Fatalf("cuenta ajena: %v, esperado ErrNotOwner", err)
	}
	if _, _, err := svc.ListEntries(ctx, owner, emission.ID, ledger.EntryFilter{}); !errors.Is(err, ledger.ErrNotOwner) {
		t.Fatalf("cuenta sin titular: %v, esperado ErrNotOwner", err)
	}
	if _, _, err := svc.ListEntries(ctx, owner, uuid.New(), ledger.EntryFilter{}); !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Fatalf("cuenta inexistente: %v, esperado ErrAccountNotFound", err)
	}

	// ── Asiento desbalanceado: el trigger diferido lo rechaza en el COMMIT
	//    y NO queda ninguna fila (todo-o-nada) ───────────────────────────────
	txsBefore := countRows(t, ctx, pool, "SELECT count(*) FROM ledger.transactions")
	entriesBefore := countRows(t, ctx, pool, "SELECT count(*) FROM ledger.entries")
	_, err = svc.PostTransaction(ctx, ledger.TransactionKindTransfer, 60, nil, "",
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: 500},
			{AccountID: emission.ID, Amount: -400},
		})
	if !errors.Is(err, ledger.ErrUnbalanced) {
		t.Fatalf("asiento desbalanceado: %v, esperado ErrUnbalanced", err)
	}
	assertLedgerUntouched(t, ctx, pool, svc, owner, cash.ID, txsBefore, entriesBefore)

	// ── No-negatividad: la caja no puede quedar en negativo ─────────────────
	_, err = svc.PostTransaction(ctx, ledger.TransactionKindTransfer, 61, nil, "",
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: -5000},
			{AccountID: emission.ID, Amount: 5000},
		})
	if !errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Fatalf("saldo insuficiente: %v, esperado ErrInsufficientBalance", err)
	}
	assertLedgerUntouched(t, ctx, pool, svc, owner, cash.ID, txsBefore, entriesBefore)

	// ── Partida contra cuenta inexistente ───────────────────────────────────
	_, err = svc.PostTransaction(ctx, ledger.TransactionKindTransfer, 62, nil, "",
		[]ledger.EntryInput{
			{AccountID: uuid.New(), Amount: 100},
			{AccountID: emission.ID, Amount: -100},
		})
	if !errors.Is(err, ledger.ErrAccountNotFound) {
		t.Fatalf("cuenta inexistente en partida: %v, esperado ErrAccountNotFound", err)
	}
	assertLedgerUntouched(t, ctx, pool, svc, owner, cash.ID, txsBefore, entriesBefore)

	// ── Paginación keyset del extracto y filtros de sim-time ────────────────
	for _, sim := range []simtime.SimTime{100, 200, 300} {
		if _, err := svc.PostTransaction(ctx, ledger.TransactionKindTransfer, sim, nil, "",
			[]ledger.EntryInput{
				{AccountID: cash.ID, Amount: 100},
				{AccountID: emission.ID, Amount: -100},
			}); err != nil {
			t.Fatalf("PostTransaction sim=%d: %v", sim, err)
		}
	}

	page1, cur, err := svc.ListEntries(ctx, owner, cash.ID, ledger.EntryFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListEntries página 1: %v", err)
	}
	if len(page1) != 2 || cur == "" {
		t.Fatalf("página 1: %d partidas, cursor %q", len(page1), cur)
	}
	page2, cur2, err := svc.ListEntries(ctx, owner, cash.ID, ledger.EntryFilter{Limit: 2, Cursor: cur})
	if err != nil {
		t.Fatalf("ListEntries página 2: %v", err)
	}
	if len(page2) != 2 || cur2 != "" {
		t.Fatalf("página 2: %d partidas, cursor %q (esperado fin)", len(page2), cur2)
	}
	all := append(append([]ledger.Entry{}, page1...), page2...)
	seen := map[uuid.UUID]bool{}
	for i, en := range all {
		if seen[en.ID] {
			t.Fatalf("partida repetida entre páginas: %s", en.ID)
		}
		seen[en.ID] = true
		if i > 0 && en.CreatedAt.After(all[i-1].CreatedAt) {
			t.Fatalf("orden DESC roto en la posición %d", i)
		}
	}
	if all[0].SimTimeAt != 300 {
		t.Fatalf("la primera partida debería ser la más reciente (sim 300): %+v", all[0])
	}

	from, to := simtime.SimTime(150), simtime.SimTime(250)
	ranged, _, err := svc.ListEntries(ctx, owner, cash.ID, ledger.EntryFilter{FromSim: &from, ToSim: &to})
	if err != nil {
		t.Fatalf("ListEntries con rango: %v", err)
	}
	if len(ranged) != 1 || ranged[0].SimTimeAt != 200 {
		t.Fatalf("rango [150,250]: %+v, esperada solo la partida de sim 200", ranged)
	}

	// ── Paginación y filtros de ListAccounts ────────────────────────────────
	for range 3 {
		if _, err := svc.CreateAccount(ctx, ledger.AccountKindEscrow, &owner, nil, nil, nil); err != nil {
			t.Fatalf("CreateAccount(escrow): %v", err)
		}
	}
	accPage1, accCur, err := svc.ListAccounts(ctx, owner, ledger.AccountFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListAccounts página 1: %v", err)
	}
	if len(accPage1) != 2 || accCur == "" {
		t.Fatalf("cuentas página 1: %d (cursor %q)", len(accPage1), accCur)
	}
	accPage2, accCur2, err := svc.ListAccounts(ctx, owner, ledger.AccountFilter{Limit: 2, Cursor: accCur})
	if err != nil {
		t.Fatalf("ListAccounts página 2: %v", err)
	}
	if len(accPage2) != 2 || accCur2 != "" {
		t.Fatalf("cuentas página 2: %d (cursor %q, esperado fin)", len(accPage2), accCur2)
	}
	accSeen := map[uuid.UUID]bool{}
	for _, a := range append(append([]ledger.Account{}, accPage1...), accPage2...) {
		if accSeen[a.ID] {
			t.Fatalf("cuenta repetida entre páginas: %s", a.ID)
		}
		accSeen[a.ID] = true
	}
	onlyCash, _, err := svc.ListAccounts(ctx, owner, ledger.AccountFilter{Kind: ledger.AccountKindCash})
	if err != nil {
		t.Fatalf("ListAccounts kind=cash: %v", err)
	}
	if len(onlyCash) != 1 || onlyCash[0].ID != cash.ID {
		t.Fatalf("filtro kind=cash: %+v", onlyCash)
	}

	// ── Métrica de asientos ─────────────────────────────────────────────────
	if got := metricSum(t, reg, "ii_ledger_transactions_posted_total", "outcome", "committed"); got < 4 {
		t.Errorf("asientos committed en la métrica: %v, esperado >= 4", got)
	}
	if got := metricSum(t, reg, "ii_ledger_transactions_posted_total", "outcome", "rejected"); got < 3 {
		t.Errorf("asientos rejected en la métrica: %v, esperado >= 3", got)
	}

	// ── Endpoints HTTP del contrato sobre el servicio real ──────────────────
	testHTTPEndpoints(t, svc, owner, cash.ID, emission.ID)
}

// assertLedgerUntouched verifica que un asiento rechazado no dejó NINGÚN
// rastro: mismas filas y mismo saldo de caja (regla todo-o-nada).
func assertLedgerUntouched(t *testing.T, ctx context.Context, pool *pgxpool.Pool, svc *ledger.Service, owner, cashID uuid.UUID, wantTxs, wantEntries int) {
	t.Helper()
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM ledger.transactions"); got != wantTxs {
		t.Fatalf("transactions: %d filas, esperado %d (asiento rechazado dejó rastro)", got, wantTxs)
	}
	if got := countRows(t, ctx, pool, "SELECT count(*) FROM ledger.entries"); got != wantEntries {
		t.Fatalf("entries: %d filas, esperado %d (asiento rechazado dejó rastro)", got, wantEntries)
	}
	accounts, _, err := svc.ListAccounts(ctx, owner, ledger.AccountFilter{Kind: ledger.AccountKindCash})
	if err != nil {
		t.Fatalf("ListAccounts tras rechazo: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != cashID || accounts[0].Balance != 1000 {
		t.Fatalf("saldo de caja alterado por un asiento rechazado: %+v", accounts)
	}
}

// testHTTPEndpoints ejercita GET /ledger/accounts y .../entries end-to-end
// (envelopes, dinero como string, cursor en meta, 403 por propiedad).
func testHTTPEndpoints(t *testing.T, svc *ledger.Service, owner, cashID, emissionID uuid.UUID) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	ledger.NewHandlers(svc, identStub{id: owner}, metaStub{}, logger).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Cuentas: balance serializado como string.
	body := getJSON(t, srv, "/ledger/accounts?kind=cash", http.StatusOK)
	data := body["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("GET /ledger/accounts?kind=cash: %d cuentas", len(data))
	}
	acc := data[0].(map[string]any)
	// Saldo tras el capital semilla (+1000) y las tres transferencias (+300).
	if acc["balance"] != "1300" || acc["id"] != cashID.String() {
		t.Fatalf("cuenta HTTP inesperada: %v", acc)
	}

	// Extracto paginado: cursor en meta.next_cursor y amounts como string.
	body = getJSON(t, srv, "/ledger/accounts/"+cashID.String()+"/entries?limit=2", http.StatusOK)
	meta := body["meta"].(map[string]any)
	nextCursor, _ := meta["next_cursor"].(string)
	if nextCursor == "" {
		t.Fatal("meta.next_cursor ausente con más páginas pendientes")
	}
	for _, item := range body["data"].([]any) {
		if _, ok := item.(map[string]any)["amount"].(string); !ok {
			t.Fatalf("amount debe ser string: %v", item)
		}
	}
	body = getJSON(t, srv, "/ledger/accounts/"+cashID.String()+"/entries?limit=2&cursor="+nextCursor, http.StatusOK)
	if got := len(body["data"].([]any)); got != 2 {
		t.Fatalf("página 2 vía HTTP: %d partidas, esperado 2", got)
	}

	// Propiedad: la cuenta emission no es del solicitante → 403.
	body = getJSON(t, srv, "/ledger/accounts/"+emissionID.String()+"/entries", http.StatusForbidden)
	if code := body["error"].(map[string]any)["code"]; code != "NOT_RESOURCE_OWNER" {
		t.Fatalf("código 403: %v, esperado NOT_RESOURCE_OWNER", code)
	}

	// Inexistente → 404 NOT_FOUND.
	body = getJSON(t, srv, "/ledger/accounts/"+uuid.NewString()+"/entries", http.StatusNotFound)
	if code := body["error"].(map[string]any)["code"]; code != "NOT_FOUND" {
		t.Fatalf("código 404: %v, esperado NOT_FOUND", code)
	}
}

// ─── Infraestructura del test ───────────────────────────────────────────────

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella. Todo se destruye al terminar el test.
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("ledgertest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(conn, "../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
		t.Fatalf("aplicando las migraciones: %v", err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("cerrando la conexión de migraciones: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando la URL del pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mustAuthAccount inserta una corporación de prueba en auth.accounts.
func mustAuthAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, name string) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generando UUIDv7: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.accounts (id, kind, name) VALUES ($1, $2, $3)`, id, kind, name); err != nil {
		t.Fatalf("creando la cuenta %q: %v", name, err)
	}
	return id
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// metricSum suma las series de un counter cuyo par etiqueta=valor coincide.
func metricSum(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	var sum float64
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					sum += m.GetCounter().GetValue()
				}
			}
		}
	}
	return sum
}

func getJSON(t *testing.T, srv *httptest.Server, path string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d, esperado %d (cuerpo: %s)", path, resp.StatusCode, wantStatus, raw)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decodificando %s: %v", path, err)
	}
	return body
}

// identStub implementa ledger.Identity con la cuenta del test.
type identStub struct{ id uuid.UUID }

func (s identStub) AccountID(context.Context) (uuid.UUID, bool) { return s.id, true }

// metaStub implementa ledger.MetaSource con un reloj fijo.
type metaStub struct{}

func (metaStub) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(0), ServerTime: time.Now().UTC()}
}
