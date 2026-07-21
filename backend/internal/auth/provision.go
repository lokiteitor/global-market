package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Aprovisionamiento mínimo de cuentas y credenciales, pensado para los
// composition roots (cmd/seed y futuros flujos de alta). Vive en el módulo
// auth para que ningún otro paquete escriba SQL contra el esquema auth ni
// duplique la convención de IDs (UUIDv7 generados en la aplicación, ADR-018).

const getAccountByNameSQL = `
SELECT a.id, a.kind::text, a.name, a.status::text, a.created_at,
       COALESCE(bp.archetype::text, '')
FROM auth.accounts a
LEFT JOIN auth.bot_profiles bp ON bp.account_id = a.id AND a.kind = 'bot'
WHERE lower(a.name) = lower($1)
`

// GetAccountByName localiza una cuenta por nombre (case-insensitive, índice
// lower(name)) sin exigir que tenga credencial: a diferencia de
// FindAccountByName, también resuelve cuentas de sistema. ErrNotFound si no
// existe.
func (r *PGRepository) GetAccountByName(ctx context.Context, name string) (Account, error) {
	var acc Account
	err := r.db.QueryRow(ctx, getAccountByNameSQL, name).Scan(
		&acc.ID, &acc.Kind, &acc.Name, &acc.Status, &acc.CreatedAt, &acc.BotArchetype,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("auth: consultando la cuenta %q: %w", name, err)
	}
	return acc, nil
}

const createAccountSQL = `
INSERT INTO auth.accounts (id, kind, name)
VALUES ($1, $2, $3)
RETURNING status::text, created_at
`

// CreateAccount crea una cuenta nueva con un UUIDv7 generado en la aplicación
// (ADR-018). kind debe ser un valor del enum auth.account_kind (human, bot,
// city, system); la BD rechaza cualquier otro. El nombre es único
// (case-insensitive, ux_accounts_name).
func (r *PGRepository) CreateAccount(ctx context.Context, kind, name string) (Account, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Account{}, fmt.Errorf("auth: generando UUIDv7: %w", err)
	}
	acc := Account{ID: id, Kind: kind, Name: name}
	if err := r.db.QueryRow(ctx, createAccountSQL, id, kind, name).
		Scan(&acc.Status, &acc.CreatedAt); err != nil {
		return Account{}, fmt.Errorf("auth: creando la cuenta %q (%s): %w", name, kind, err)
	}
	return acc, nil
}

const ensureCredentialSQL = `
INSERT INTO auth.account_credentials (account_id, secret_hash)
VALUES ($1, $2)
ON CONFLICT (account_id) DO NOTHING
`

const ensureBotProfileSQL = `
INSERT INTO auth.bot_profiles (id, account_id, archetype, behavior)
VALUES ($1, $2, $3::auth.bot_archetype, $4)
ON CONFLICT (account_id) DO NOTHING
`

// EnsureCredential fija la credencial de la cuenta solo si aún no tiene una
// (idempotente: nunca sobrescribe un secreto existente). secretHash debe ser
// una codificación PHC producida por HashSecret. Devuelve si la creó.
func (r *PGRepository) EnsureCredential(ctx context.Context, accountID uuid.UUID, secretHash string) (bool, error) {
	tag, err := r.db.Exec(ctx, ensureCredentialSQL, accountID, secretHash)
	if err != nil {
		return false, fmt.Errorf("auth: creando la credencial de %s: %w", accountID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// EnsureBotProfile crea el perfil de bot de la cuenta (arquetipo + behavior
// JSON con los umbrales) solo si aún no existe (idempotente: nunca pisa un
// perfil vigente). archetype debe ser un valor del enum auth.bot_archetype;
// behavior debe ser JSON válido. Devuelve si lo creó. Lo usa el Bot
// Orchestration Service (ADR-024: lifecycle interno).
func (r *PGRepository) EnsureBotProfile(ctx context.Context, accountID uuid.UUID, archetype string, behavior []byte) (bool, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return false, fmt.Errorf("auth: generando UUIDv7: %w", err)
	}
	if len(behavior) == 0 {
		behavior = []byte("{}")
	}
	tag, err := r.db.Exec(ctx, ensureBotProfileSQL, id, accountID, archetype, behavior)
	if err != nil {
		return false, fmt.Errorf("auth: creando el bot_profile de %s (%s): %w", accountID, archetype, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ─── Retiro de bots (ADR-024: liquidación + absorción) ───────────────────────
//
// El Bot Orchestration Service retira las cuentas de bot insolventes-inactivas
// (GDD 5.9): absorbe su caja al banco central y las marca retiradas. Todas las
// escrituras del esquema auth viven aquí (el resto de módulos no escribe SQL
// contra auth). Los métodos ligados a la tx del retiro se invocan sobre un
// PGRepository construido con NewPGRepository(tx).

const getAccountStatusSQL = `SELECT status::text FROM auth.accounts WHERE id = $1`

// GetAccountStatus devuelve el estado de una cuenta (active/suspended/retired).
// ErrNotFound si no existe. Lo usa el bucle del orquestador para saltar las
// cuentas que ya no juegan (una cuenta retirada no decide).
func (r *PGRepository) GetAccountStatus(ctx context.Context, id uuid.UUID) (string, error) {
	var status string
	err := r.db.QueryRow(ctx, getAccountStatusSQL, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("auth: consultando el estado de %s: %w", id, err)
	}
	return status, nil
}

const lockAccountForUpdateSQL = `
SELECT kind::text, status::text FROM auth.accounts WHERE id = $1 FOR UPDATE
`

// LockAccountForUpdate bloquea la fila de una cuenta (FOR UPDATE) y devuelve su
// kind y status. Serializa el retiro concurrente de la misma cuenta entre
// instancias del orquestador. ErrNotFound si la cuenta no existe. Debe llamarse
// dentro de una transacción (sobre NewPGRepository(tx)).
func (r *PGRepository) LockAccountForUpdate(ctx context.Context, id uuid.UUID) (kind, status string, err error) {
	err = r.db.QueryRow(ctx, lockAccountForUpdateSQL, id).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("auth: bloqueando la cuenta %s: %w", id, err)
	}
	return kind, status, nil
}

const listActiveBotAccountsSQL = `
SELECT a.id
FROM auth.accounts a
JOIN auth.bot_profiles bp ON bp.account_id = a.id
WHERE a.kind = 'bot' AND a.status = 'active' AND bp.active
ORDER BY a.id
LIMIT $1
`

// ListActiveBotAccounts lista las cuentas de bot activas con perfil activo
// (candidatas del barrido de retiro), acotadas por limit. El orden es estable
// (por id) para un barrido reproducible.
func (r *PGRepository) ListActiveBotAccounts(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, listActiveBotAccountsSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("auth: listando cuentas de bot activas: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("auth: leyendo cuenta de bot: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: iterando cuentas de bot: %w", err)
	}
	return ids, nil
}

const getBotProfileMarkSQL = `SELECT (behavior->>$2)::bigint FROM auth.bot_profiles WHERE account_id = $1`

// GetBotProfileMark lee una marca entera del behavior JSON del perfil (p. ej.
// el sim-time desde el que un bot está insolvente). Devuelve nil si la clave no
// existe. ErrNotFound si el perfil no existe.
func (r *PGRepository) GetBotProfileMark(ctx context.Context, accountID uuid.UUID, key string) (*int64, error) {
	var v *int64
	err := r.db.QueryRow(ctx, getBotProfileMarkSQL, accountID, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("auth: leyendo la marca %q del perfil de %s: %w", key, accountID, err)
	}
	return v, nil
}

const setBotProfileMarkSQL = `
UPDATE auth.bot_profiles
SET behavior = jsonb_set(behavior, ARRAY[$2], to_jsonb($3::bigint), true), updated_at = now()
WHERE account_id = $1
`

// SetBotProfileMark fija una marca entera en el behavior JSON del perfil sin
// tocar las demás claves (jsonb_set con create_if_missing).
func (r *PGRepository) SetBotProfileMark(ctx context.Context, accountID uuid.UUID, key string, value int64) error {
	if _, err := r.db.Exec(ctx, setBotProfileMarkSQL, accountID, key, value); err != nil {
		return fmt.Errorf("auth: fijando la marca %q del perfil de %s: %w", key, accountID, err)
	}
	return nil
}

const clearBotProfileMarkSQL = `
UPDATE auth.bot_profiles SET behavior = behavior - $2::text, updated_at = now() WHERE account_id = $1
`

// ClearBotProfileMark elimina una marca del behavior JSON del perfil
// (idempotente: borrar una clave ausente no es un error).
func (r *PGRepository) ClearBotProfileMark(ctx context.Context, accountID uuid.UUID, key string) error {
	if _, err := r.db.Exec(ctx, clearBotProfileMarkSQL, accountID, key); err != nil {
		return fmt.Errorf("auth: limpiando la marca %q del perfil de %s: %w", key, accountID, err)
	}
	return nil
}

const retireBotAccountSQL = `
UPDATE auth.accounts SET status = 'retired', updated_at = now()
WHERE id = $1 AND kind = 'bot' AND status = 'active'
`

const deactivateBotProfileSQL = `
UPDATE auth.bot_profiles SET active = false, updated_at = now() WHERE account_id = $1
`

// RetireBotAccount marca la cuenta de bot como retirada y desactiva su perfil
// (bot_profiles.active = false), de modo que deje de ser candidata y de jugar.
// Devuelve si cambió el estado (false si ya no estaba activa: idempotente). Se
// invoca dentro de la tx del retiro (junto a la absorción de caja y el evento).
func (r *PGRepository) RetireBotAccount(ctx context.Context, accountID uuid.UUID) (bool, error) {
	tag, err := r.db.Exec(ctx, retireBotAccountSQL, accountID)
	if err != nil {
		return false, fmt.Errorf("auth: retirando la cuenta de bot %s: %w", accountID, err)
	}
	retired := tag.RowsAffected() > 0
	if retired {
		if _, err := r.db.Exec(ctx, deactivateBotProfileSQL, accountID); err != nil {
			return false, fmt.Errorf("auth: desactivando el perfil del bot %s: %w", accountID, err)
		}
	}
	return retired, nil
}
