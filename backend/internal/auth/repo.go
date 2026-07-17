package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound indica que la entidad buscada no existe (o, en sesiones, que
// ya expiró: el repositorio no distingue ambos casos a propósito).
var ErrNotFound = errors.New("auth: no encontrado")

// Account es la proyección de dominio de auth.accounts (schema Account del
// contrato; el saldo no vive aquí — es la cuenta cash del ledger).
type Account struct {
	ID     uuid.UUID
	Kind   string
	Name   string
	Status string
	// BotArchetype solo está poblado cuando Kind == "bot" (auth.bot_profiles).
	BotArchetype string
	CreatedAt    time.Time
}

// Session es la proyección de dominio de auth.sessions. El token en claro
// nunca se persiste: solo su hash (HashToken).
type Session struct {
	ID         uuid.UUID
	AccountID  uuid.UUID
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// Repository es el contrato de persistencia del módulo. Service y middleware
// dependen de esta interfaz; PGRepository es la implementación real y los
// tests unitarios usan un fake.
type Repository interface {
	// FindAccountByName localiza una cuenta por nombre (case-insensitive,
	// índice lower(name)) junto con su hash de credencial PHC.
	FindAccountByName(ctx context.Context, name string) (Account, string, error)
	// CreateSession persiste una sesión nueva; la BD asigna id y timestamps.
	CreateSession(ctx context.Context, accountID uuid.UUID, tokenHash string, clientInfo map[string]any, expiresAt time.Time) (Session, error)
	// FindSessionByTokenHash resuelve una sesión vigente (expires_at > now())
	// por hash de token, junto con su cuenta.
	FindSessionByTokenHash(ctx context.Context, tokenHash string) (Session, Account, error)
	// TouchSessionLastSeen actualiza last_seen_at, solo si el valor actual
	// tiene más de touchInterval de antigüedad (throttling en SQL, sin carrera).
	TouchSessionLastSeen(ctx context.Context, sessionID uuid.UUID) error
	// DeleteSession invalida una sesión. Idempotente: borrar una sesión
	// inexistente no es un error.
	DeleteSession(ctx context.Context, sessionID uuid.UUID) error
}

// touchInterval es la antigüedad mínima de last_seen_at para reescribirlo:
// acota las escrituras a una por sesión y minuto bajo tráfico sostenido.
const touchInterval = 60 * time.Second

// Querier es el subconjunto de pgx que usa el repositorio; lo satisfacen
// *pgxpool.Pool, *pgx.Conn y pgx.Tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PGRepository es el repositorio real contra el esquema auth (migración
// 0002_auth). SQL a mano con pgx: este módulo no usa sqlc (el patrón sqlc lo
// establece el módulo ledger).
type PGRepository struct {
	db Querier
}

// NewPGRepository construye el repositorio sobre un pool/conn/tx de pgx.
func NewPGRepository(db Querier) *PGRepository {
	return &PGRepository{db: db}
}

// compile-time: PGRepository implementa Repository.
var _ Repository = (*PGRepository)(nil)

const findAccountByNameSQL = `
SELECT a.id, a.kind::text, a.name, a.status::text, a.created_at,
       COALESCE(bp.archetype::text, ''),
       c.secret_hash
FROM auth.accounts a
JOIN auth.account_credentials c ON c.account_id = a.id
LEFT JOIN auth.bot_profiles bp ON bp.account_id = a.id AND a.kind = 'bot'
WHERE lower(a.name) = lower($1)
`

func (r *PGRepository) FindAccountByName(ctx context.Context, name string) (Account, string, error) {
	var (
		acc        Account
		secretHash string
	)
	err := r.db.QueryRow(ctx, findAccountByNameSQL, name).Scan(
		&acc.ID, &acc.Kind, &acc.Name, &acc.Status, &acc.CreatedAt,
		&acc.BotArchetype, &secretHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, "", ErrNotFound
	}
	if err != nil {
		return Account{}, "", fmt.Errorf("auth: buscando cuenta por nombre: %w", err)
	}
	return acc, secretHash, nil
}

const createSessionSQL = `
INSERT INTO auth.sessions (account_id, token_hash, client_info, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING id, created_at, last_seen_at
`

func (r *PGRepository) CreateSession(ctx context.Context, accountID uuid.UUID, tokenHash string, clientInfo map[string]any, expiresAt time.Time) (Session, error) {
	info, err := json.Marshal(clientInfo)
	if err != nil {
		return Session{}, fmt.Errorf("auth: serializando client_info: %w", err)
	}
	if clientInfo == nil {
		info = []byte("{}")
	}
	s := Session{AccountID: accountID, ExpiresAt: expiresAt}
	err = r.db.QueryRow(ctx, createSessionSQL, accountID, tokenHash, info, expiresAt).
		Scan(&s.ID, &s.CreatedAt, &s.LastSeenAt)
	if err != nil {
		return Session{}, fmt.Errorf("auth: creando sesión: %w", err)
	}
	return s, nil
}

const findSessionByTokenHashSQL = `
SELECT s.id, s.account_id, s.created_at, s.last_seen_at, s.expires_at,
       a.kind::text, a.name, a.status::text, a.created_at,
       COALESCE(bp.archetype::text, '')
FROM auth.sessions s
JOIN auth.accounts a ON a.id = s.account_id
LEFT JOIN auth.bot_profiles bp ON bp.account_id = a.id AND a.kind = 'bot'
WHERE s.token_hash = $1
  AND s.expires_at > now()
`

func (r *PGRepository) FindSessionByTokenHash(ctx context.Context, tokenHash string) (Session, Account, error) {
	var (
		s   Session
		acc Account
	)
	err := r.db.QueryRow(ctx, findSessionByTokenHashSQL, tokenHash).Scan(
		&s.ID, &s.AccountID, &s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt,
		&acc.Kind, &acc.Name, &acc.Status, &acc.CreatedAt,
		&acc.BotArchetype,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, Account{}, ErrNotFound
	}
	if err != nil {
		return Session{}, Account{}, fmt.Errorf("auth: buscando sesión por token: %w", err)
	}
	acc.ID = s.AccountID
	return s, acc, nil
}

const touchSessionSQL = `
UPDATE auth.sessions
SET last_seen_at = now()
WHERE id = $1
  AND last_seen_at < now() - make_interval(secs => $2)
`

func (r *PGRepository) TouchSessionLastSeen(ctx context.Context, sessionID uuid.UUID) error {
	// El predicado sobre last_seen_at hace el throttling en la propia BD:
	// concurrente-seguro y sin lecturas previas.
	if _, err := r.db.Exec(ctx, touchSessionSQL, sessionID, touchInterval.Seconds()); err != nil {
		return fmt.Errorf("auth: actualizando last_seen_at: %w", err)
	}
	return nil
}

const deleteSessionSQL = `DELETE FROM auth.sessions WHERE id = $1`

func (r *PGRepository) DeleteSession(ctx context.Context, sessionID uuid.UUID) error {
	if _, err := r.db.Exec(ctx, deleteSessionSQL, sessionID); err != nil {
		return fmt.Errorf("auth: eliminando sesión: %w", err)
	}
	return nil
}
