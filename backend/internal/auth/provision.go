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
