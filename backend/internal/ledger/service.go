package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/ledger/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Códigos SQLSTATE y constraints que este módulo traduce a errores tipados.
// Las invariantes viven en la BD (0004_ledger); aquí solo se reconocen.
const (
	sqlstateUniqueViolation = "23505" // unique_violation
	sqlstateCheckViolation  = "23514" // check_violation
	sqlstateFKViolation     = "23503" // foreign_key_violation
	sqlstateRaiseException  = "P0001" // RAISE EXCEPTION de los triggers plpgsql

	constraintNonNegative = "ck_accounts_non_negative"
	constraintCashUnique  = "ux_accounts_cash"
)

// Resultados de la métrica de asientos.
const (
	outcomeCommitted = "committed" // asentado y confirmado
	outcomeRejected  = "rejected"  // rechazado por una invariante del ledger
	outcomeError     = "error"     // fallo técnico (BD caída, timeout…)
)

// Service es la capa fina del módulo ledger sobre el código generado por
// sqlc: lecturas con autorización por propiedad, primitivas de asiento
// (Poster) y aprovisionamiento idempotente de cuentas.
type Service struct {
	pool     *pgxpool.Pool
	q        *sqlcgen.Queries
	opts     Options
	txPosted *prometheus.CounterVec
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
// reg registra la métrica de asientos (ii_ledger_transactions_posted_total);
// nil la deja sin registrar (tests, herramientas).
func NewService(pool *pgxpool.Pool, opts Options, reg prometheus.Registerer) *Service {
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = DefaultQueryTimeout
	}
	txPosted := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ii_ledger_transactions_posted_total",
		Help: "Total de asientos del ledger intentados, por tipo y resultado.",
	}, []string{"kind", "outcome"})
	if reg != nil {
		reg.MustRegister(txPosted)
	}
	return &Service{
		pool:     pool,
		q:        sqlcgen.New(pool),
		opts:     opts,
		txPosted: txPosted,
	}
}

// withTimeout acota cada operación de BD con el timeout configurado.
func (s *Service) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.opts.QueryTimeout)
}

// ─── Lecturas (contrato /ledger/*) ──────────────────────────────────────────

// ListAccounts lista las cuentas del ledger cuyo titular es owner (la cuenta
// autenticada: la autorización por propiedad es por construcción, la query
// solo ve sus cuentas). Devuelve la página y el cursor de la siguiente
// ("" si no hay más).
func (s *Service) ListAccounts(ctx context.Context, owner uuid.UUID, f AccountFilter) ([]Account, string, error) {
	if f.Kind != "" && !f.Kind.Valid() {
		return nil, "", fmt.Errorf("ledger: kind de cuenta inválido %q", f.Kind)
	}
	limit := normalizeLimit(f.Limit)
	var after *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeAccountCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		after = &id
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListAccountsByOwner(ctx, sqlcgen.ListAccountsByOwnerParams{
		OwnerAccountID: &owner,
		Kind: sqlcgen.NullLedgerAccountKind{
			LedgerAccountKind: sqlcgen.LedgerAccountKind(f.Kind),
			Valid:             f.Kind != "",
		},
		ProductID: f.ProductID,
		AfterID:   after,
		PageLimit: limit + 1, // +1: detección de página siguiente
	})
	if err != nil {
		return nil, "", fmt.Errorf("ledger: listando cuentas: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeAccountCursor(rows[len(rows)-1].ID)
	}
	accounts := make([]Account, len(rows))
	for i, r := range rows {
		accounts[i] = toAccount(r)
	}
	return accounts, next, nil
}

// ListEntries devuelve el extracto de una cuenta del ledger, de más reciente
// a más antigua. requester es la cuenta autenticada: si la cuenta no existe
// devuelve ErrAccountNotFound y si pertenece a otra corporación (o no tiene
// titular) ErrNotOwner — el handler los mapea a 404/403.
func (s *Service) ListEntries(ctx context.Context, requester, ledgerAccountID uuid.UUID, f EntryFilter) ([]Entry, string, error) {
	limit := normalizeLimit(f.Limit)
	params := sqlcgen.ListEntriesByAccountParams{
		AccountID: ledgerAccountID,
		FromSim:   (*int64)(f.FromSim),
		ToSim:     (*int64)(f.ToSim),
		PageLimit: limit + 1,
	}
	if f.Cursor != "" {
		createdAt, id, err := decodeEntryCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		params.BeforeCreatedAt = &createdAt
		params.BeforeID = &id
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	// Autorización por propiedad: solo el titular consulta su extracto.
	account, err := s.q.GetAccount(ctx, ledgerAccountID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, "", fmt.Errorf("%w (%s)", ErrAccountNotFound, ledgerAccountID)
	case err != nil:
		return nil, "", fmt.Errorf("ledger: consultando la cuenta %s: %w", ledgerAccountID, err)
	case account.OwnerAccountID == nil || *account.OwnerAccountID != requester:
		return nil, "", fmt.Errorf("%w (%s)", ErrNotOwner, ledgerAccountID)
	}

	rows, err := s.q.ListEntriesByAccount(ctx, params)
	if err != nil {
		return nil, "", fmt.Errorf("ledger: listando el extracto de %s: %w", ledgerAccountID, err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		next = encodeEntryCursor(last.CreatedAt, last.ID)
	}
	entries := make([]Entry, len(rows))
	for i, r := range rows {
		entries[i] = Entry{
			ID:              r.ID,
			TransactionID:   r.TransactionID,
			AccountID:       r.AccountID,
			Amount:          r.Amount,
			TransactionKind: TransactionKind(r.TransactionKind),
			ReferenceID:     r.ReferenceID,
			Description:     r.Description,
			SimTimeAt:       simtime.SimTime(r.SimTimeAt),
			CreatedAt:       r.CreatedAt,
		}
	}
	return entries, next, nil
}

// ─── Primitivas de asiento (Poster) ─────────────────────────────────────────

// PostTransaction asienta cabecera + partidas EN UNA transacción SERIALIZABLE
// (regla de oro GDD 18.3), generando los UUIDv7 en la aplicación (ADR-018).
// Los triggers de la BD aplican los saldos y garantizan doble entrada
// balanceada por activo (diferido, en el COMMIT), no-negatividad e
// inmutabilidad: si cualquier invariante falla NO queda ninguna partida.
// description vacía se asienta como NULL. Devuelve el id del asiento.
func (s *Service) PostTransaction(ctx context.Context, kind TransactionKind, simTime simtime.SimTime, referenceID *uuid.UUID, description string, entries []EntryInput) (uuid.UUID, error) {
	if len(entries) < 2 {
		return uuid.Nil, ErrTooFewEntries
	}
	for _, e := range entries {
		if e.Amount == 0 {
			return uuid.Nil, fmt.Errorf("%w (cuenta %s)", ErrZeroAmount, e.AccountID)
		}
	}

	txID, err := s.postTransaction(ctx, kind, simTime, referenceID, description, entries)
	s.observePost(kind, err)
	return txID, err
}

// PostTransactionTx asienta cabecera + partidas DENTRO de la transacción tx del
// llamante, para operaciones que deben confirmar de forma atómica JUNTO a otros
// efectos —típicamente un evento del outbox (transactional outbox) y cambios en
// otros esquemas— en una única transacción SERIALIZABLE. El aislamiento, los
// reintentos por conflicto y el COMMIT son del llamante (db.RunSerializable);
// el trigger diferido de doble entrada se evalúa en ESE commit, así que un
// asiento no balanceado revierte toda la transacción del llamante.
//
// Misma validación de forma que PostTransaction (>=2 partidas, importes no
// nulos) y mismo mapeo de errores de invariante (ErrInsufficientBalance…). No
// observa la métrica de asientos: el resultado real solo se conoce en el commit
// del llamante, que este método no controla.
func (s *Service) PostTransactionTx(ctx context.Context, tx pgx.Tx, kind TransactionKind, simTime simtime.SimTime, referenceID *uuid.UUID, description string, entries []EntryInput) (uuid.UUID, error) {
	if tx == nil {
		return uuid.Nil, errors.New("ledger: PostTransactionTx requiere la transacción del llamante (tx nil)")
	}
	if len(entries) < 2 {
		return uuid.Nil, ErrTooFewEntries
	}
	for _, e := range entries {
		if e.Amount == 0 {
			return uuid.Nil, fmt.Errorf("%w (cuenta %s)", ErrZeroAmount, e.AccountID)
		}
	}
	return s.insertTransactionEntries(ctx, s.q.WithTx(tx), kind, simTime, referenceID, description, entries)
}

// postTransaction ejecuta el asiento en su PROPIA transacción serializable;
// separado para observar el resultado en un único punto.
func (s *Service) postTransaction(ctx context.Context, kind TransactionKind, simTime simtime.SimTime, referenceID *uuid.UUID, description string, entries []EntryInput) (uuid.UUID, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: abriendo la transacción: %w", err)
	}
	// Rollback tras Commit devuelve ErrTxClosed: inocuo.
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	txID, err := s.insertTransactionEntries(ctx, s.q.WithTx(tx), kind, simTime, referenceID, description, entries)
	if err != nil {
		return uuid.Nil, err
	}
	// El trigger diferido de doble entrada se evalúa aquí: un asiento no
	// balanceado hace fallar el COMMIT y lo revierte todo.
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, mapPostError(err)
	}
	return txID, nil
}

// insertTransactionEntries inserta la cabecera y sus partidas con el querier
// dado (ligado a la transacción que corresponda), generando los UUIDv7 en la
// aplicación (ADR-018) y mapeando las violaciones de invariante a errores
// tipados. No confirma: el COMMIT lo hace el dueño de la transacción.
func (s *Service) insertTransactionEntries(ctx context.Context, q *sqlcgen.Queries, kind TransactionKind, simTime simtime.SimTime, referenceID *uuid.UUID, description string, entries []EntryInput) (uuid.UUID, error) {
	txID, err := newUUIDv7()
	if err != nil {
		return uuid.Nil, err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	if err := q.InsertTransaction(ctx, sqlcgen.InsertTransactionParams{
		ID:          txID,
		Kind:        sqlcgen.LedgerTransactionKind(kind),
		SimTimeAt:   int64(simTime),
		ReferenceID: referenceID,
		Description: desc,
	}); err != nil {
		return uuid.Nil, mapPostError(err)
	}
	for _, e := range entries {
		entryID, err := newUUIDv7()
		if err != nil {
			return uuid.Nil, err
		}
		if err := q.InsertEntry(ctx, sqlcgen.InsertEntryParams{
			ID:            entryID,
			TransactionID: txID,
			AccountID:     e.AccountID,
			Amount:        e.Amount,
		}); err != nil {
			return uuid.Nil, mapPostError(err)
		}
	}
	return txID, nil
}

// observePost registra el resultado del asiento en la métrica del módulo.
func (s *Service) observePost(kind TransactionKind, err error) {
	outcome := outcomeCommitted
	switch {
	case err == nil:
	case errors.Is(err, ErrUnbalanced), errors.Is(err, ErrInsufficientBalance),
		errors.Is(err, ErrAccountNotFound), errors.Is(err, ErrTooFewEntries),
		errors.Is(err, ErrZeroAmount):
		outcome = outcomeRejected
	default:
		outcome = outcomeError
	}
	s.txPosted.WithLabelValues(string(kind), outcome).Inc()
}

// mapPostError traduce las violaciones de las invariantes del ledger
// (triggers y constraints de 0004_ledger) a errores tipados del módulo.
func mapPostError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative:
			return fmt.Errorf("%w: %s", ErrInsufficientBalance, pgErr.Message)
		case pgErr.Code == sqlstateRaiseException && strings.Contains(pgErr.Message, "no balanceada"):
			return fmt.Errorf("%w: %s", ErrUnbalanced, pgErr.Message)
		case pgErr.Code == sqlstateFKViolation && pgErr.TableName == "entries":
			return fmt.Errorf("%w: una partida referencia una cuenta inexistente", ErrAccountNotFound)
		}
	}
	return fmt.Errorf("ledger: asentando la transacción: %w", err)
}

// ─── Aprovisionamiento de cuentas ───────────────────────────────────────────

// EnsureCashAccount devuelve la cuenta de caja de la corporación, creándola
// si no existe. Idempotente y seguro ante carreras: la unicidad parcial
// ux_accounts_cash garantiza una sola caja por corporación; si otro proceso
// gana la carrera se relee la suya.
func (s *Service) EnsureCashAccount(ctx context.Context, owner uuid.UUID) (Account, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	row, err := s.q.GetCashAccountByOwner(ctx, &owner)
	switch {
	case err == nil:
		return toAccount(row), nil
	case !errors.Is(err, pgx.ErrNoRows):
		return Account{}, fmt.Errorf("ledger: consultando la caja de %s: %w", owner, err)
	}

	id, err := newUUIDv7()
	if err != nil {
		return Account{}, err
	}
	row, err = s.q.CreateLedgerAccount(ctx, sqlcgen.CreateLedgerAccountParams{
		ID:             id,
		Kind:           sqlcgen.LedgerAccountKindCash,
		OwnerAccountID: &owner,
	})
	if err == nil {
		return toAccount(row), nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlstateUniqueViolation && pgErr.ConstraintName == constraintCashUnique {
		row, err = s.q.GetCashAccountByOwner(ctx, &owner)
		if err != nil {
			return Account{}, fmt.Errorf("ledger: releyendo la caja de %s tras la carrera: %w", owner, err)
		}
		return toAccount(row), nil
	}
	return Account{}, fmt.Errorf("ledger: creando la caja de %s: %w", owner, err)
}

// CreateAccount crea una cuenta del ledger de cualquier tipo (primitiva para
// los composition roots y servicios del contexto: cuentas espejo, emission,
// sink, stock…). La forma la validan los constraints de la BD
// (ck_accounts_asset, unicidades parciales).
func (s *Service) CreateAccount(ctx context.Context, kind AccountKind, owner, product, warehouse, reference *uuid.UUID) (Account, error) {
	if !kind.Valid() {
		return Account{}, fmt.Errorf("ledger: kind de cuenta inválido %q", kind)
	}
	id, err := newUUIDv7()
	if err != nil {
		return Account{}, err
	}
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	row, err := s.q.CreateLedgerAccount(ctx, sqlcgen.CreateLedgerAccountParams{
		ID:                  id,
		Kind:                sqlcgen.LedgerAccountKind(kind),
		OwnerAccountID:      owner,
		ProductID:           product,
		WarehouseBuildingID: warehouse,
		ReferenceID:         reference,
	})
	if err != nil {
		return Account{}, fmt.Errorf("ledger: creando la cuenta %s: %w", kind, err)
	}
	return toAccount(row), nil
}

// ─── Helpers ────────────────────────────────────────────────────────────────

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

// toAccount convierte la fila generada por sqlc al tipo de dominio.
func toAccount(r sqlcgen.LedgerAccount) Account {
	return Account{
		ID:                  r.ID,
		Kind:                AccountKind(r.Kind),
		OwnerAccountID:      r.OwnerAccountID,
		ProductID:           r.ProductID,
		WarehouseBuildingID: r.WarehouseBuildingID,
		ReferenceID:         r.ReferenceID,
		Balance:             r.Balance,
		CreatedAt:           r.CreatedAt,
		UpdatedAt:           r.UpdatedAt,
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("ledger: generando UUIDv7: %w", err)
	}
	return id, nil
}
